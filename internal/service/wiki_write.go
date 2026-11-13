package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

type wikiWriteAPI interface {
	syncpkg.WikiAPI
	CreateWiki(context.Context, int64, backlogclient.WikiWrite) (*backlogclient.Wiki, error)
	UpdateWiki(context.Context, int64, backlogclient.WikiWrite) (*backlogclient.Wiki, error)
}
type WikiEdit struct {
	Token           string    `json:"token"`
	BaselineUpdated string    `json:"baselineUpdated"`
	Page            *WikiPage `json:"page"`
}
type WikiSaveInput struct {
	Token      string `json:"token"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	MailNotify bool   `json:"mailNotify"`
	Overwrite  bool   `json:"overwrite"`
}
type WikiCreateInput struct {
	ProjectID  int64  `json:"projectId"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	MailNotify bool   `json:"mailNotify"`
}
type WikiWriteResult struct {
	Status string    `json:"status"`
	Page   *WikiPage `json:"page"`
}
type wikiEditSession struct {
	profile       string
	projectID, id int64
	identity      string
	baseline      string
	conflict      bool
	pending       *backlogclient.WikiWrite
}
type wikiCreateAttempt struct {
	Input  WikiCreateInput `json:"input"`
	UserID int64           `json:"userId"`
}

// 各公開操作は profileMu → syncMu の順でロックする。
// 編集基準は詳細 GET 成功時だけ発行し、一覧時刻は使わない。
func (s *ProfileService) BeginWikiEdit(ctx context.Context, profile string, projectID, id int64) (*WikiEdit, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	api, st, identity, err := s.wikiWriterLocked(ctx, profile, projectID)
	if err != nil {
		return nil, err
	}
	p, err := api.GetWiki(ctx, id)
	if err != nil {
		return nil, err
	}
	if !validWikiTarget(p, projectID, id) {
		return nil, errors.New("Wiki 詳細の対象が一致しません")
	}
	page, err := s.cacheWrittenWiki(ctx, profile, st, p, "")
	if err != nil {
		return nil, err
	}
	token := rand.Text()
	if s.wikiEdits == nil {
		s.wikiEdits = map[string]*wikiEditSession{}
	}
	// 同一画面の破棄済みセッションを保持し続けない。
	for key, edit := range s.wikiEdits {
		if edit.profile == profile && edit.pending == nil {
			delete(s.wikiEdits, key)
		}
	}
	s.wikiEdits[token] = &wikiEditSession{profile: profile, projectID: projectID, id: id, identity: identity, baseline: p.Updated}
	return &WikiEdit{Token: token, BaselineUpdated: p.Updated, Page: page}, nil
}
func (s *ProfileService) wikiWriterLocked(ctx context.Context, profile string, projectID int64) (wikiWriteAPI, *store.Store, string, error) {
	if projectID <= 0 {
		return nil, nil, "", errors.New("プロジェクトが指定されていません")
	}
	st, err := s.storeForProfile(profile)
	if err != nil {
		return nil, nil, "", err
	}
	raw, err := st.GetProjectRawJSON(ctx, projectID)
	if err != nil {
		return nil, nil, "", err
	}
	if !syncpkg.WikiEnabled(raw) {
		return nil, nil, "", errors.New("Wiki 無効")
	}
	c, err := s.clientForProfile(ctx, profile)
	if err != nil {
		return nil, nil, "", err
	}
	api, ok := c.(wikiWriteAPI)
	if !ok {
		return nil, nil, "", errors.New("Wiki 保存が利用できません")
	}
	p, err := s.cfg.Get(profile)
	if err != nil {
		return nil, nil, "", err
	}
	return api, st, fmt.Sprintf("%s/%d", p.SpaceURL, p.LastUserID), nil
}
func validWikiTarget(p *backlogclient.Wiki, projectID, id int64) bool {
	return p != nil && p.ProjectID == projectID && p.ID > 0 && (id == 0 || p.ID == id) && p.Updated != ""
}

func (s *ProfileService) SaveWikiEdit(ctx context.Context, profile string, in WikiSaveInput) (*WikiWriteResult, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	edit := s.wikiEdits[in.Token]
	if edit == nil || edit.profile != profile {
		return nil, errors.New("編集を開始し直してください")
	}
	api, st, identity, err := s.wikiWriterLocked(ctx, profile, edit.projectID)
	if err != nil {
		return nil, err
	}
	if identity != edit.identity {
		return nil, errors.New("接続先が変更されました。編集を開始し直してください")
	}
	if edit.pending != nil {
		if edit.pending.Name != in.Name || edit.pending.Content != in.Content {
			return &WikiWriteResult{Status: "uncertain"}, nil
		}
		return s.resolveWikiEdit(ctx, profile, api, st, edit, in.Token)
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("ページ名が入力されていません")
	}
	if in.Overwrite {
		if !edit.conflict {
			return nil, errors.New("競合内容を確認してください")
		}
	} else {
		latest, err := api.GetWiki(ctx, edit.id)
		if err != nil {
			return nil, err
		}
		if !validWikiTarget(latest, edit.projectID, edit.id) {
			return nil, errors.New("Wiki 詳細の対象が一致しません")
		}
		if latest.Updated != edit.baseline {
			page, err := s.cacheWrittenWiki(ctx, profile, st, latest, "")
			if err != nil {
				return nil, err
			}
			edit.conflict = true
			return &WikiWriteResult{Status: "conflict", Page: page}, nil
		}
	}
	write := backlogclient.WikiWrite{Name: in.Name, Content: in.Content, MailNotify: in.MailNotify}
	edit.pending = &write
	p, err := api.UpdateWiki(ctx, edit.id, write)
	if err != nil && !backlogclient.IsUncertain(err) {
		edit.pending = nil
		return nil, errors.New("Wiki の保存が拒否されました。権限・入力・同期状態を確認してください")
	}
	if err != nil || !validWikiTarget(p, edit.projectID, edit.id) || p.Name != in.Name || p.Content != in.Content {
		return s.resolveWikiEdit(ctx, profile, api, st, edit, in.Token)
	}
	page, err := s.cacheWrittenWiki(ctx, profile, st, p, "")
	if err != nil {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	delete(s.wikiEdits, in.Token)
	return &WikiWriteResult{Status: "saved", Page: page}, nil
}
func (s *ProfileService) resolveWikiEdit(ctx context.Context, profile string, api wikiWriteAPI, st *store.Store, edit *wikiEditSession, token string) (*WikiWriteResult, error) {
	p, err := api.GetWiki(ctx, edit.id)
	if err != nil || !validWikiTarget(p, edit.projectID, edit.id) {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	if p.Name != edit.pending.Name || p.Content != edit.pending.Content {
		edit.pending = nil
		return &WikiWriteResult{Status: "not_saved"}, nil
	}
	page, err := s.cacheWrittenWiki(ctx, profile, st, p, "")
	if err != nil {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	delete(s.wikiEdits, token)
	return &WikiWriteResult{Status: "saved", Page: page}, nil
}
func wikiCreateKey(projectID int64, name string) string {
	return fmt.Sprintf("wiki_create_%d_%x", projectID, sha256.Sum256([]byte(name)))
}

func (s *ProfileService) CreateWiki(ctx context.Context, profile string, in WikiCreateInput) (*WikiWriteResult, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("ページ名が入力されていません")
	}
	api, st, _, err := s.wikiWriterLocked(ctx, profile, in.ProjectID)
	if err != nil {
		return nil, err
	}
	key := wikiCreateKey(in.ProjectID, in.Name)
	marker, err := store.GetMeta(ctx, st.DB(), key)
	if err != nil {
		return nil, err
	}
	if marker != "" {
		var attempt wikiCreateAttempt
		if json.Unmarshal([]byte(marker), &attempt) != nil {
			return nil, errors.New("保存結果の記録を確認できません。再送しません")
		}
		if attempt.Input.ProjectID != in.ProjectID || attempt.Input.Name != in.Name || attempt.Input.Content != in.Content {
			return &WikiWriteResult{Status: "uncertain"}, nil
		}
		return s.resolveWikiCreate(ctx, profile, api, st, attempt, key)
	}
	pages, err := s.wikiListLocked(ctx, profile, store.WikiFilter{ProjectID: in.ProjectID})
	if err != nil {
		return nil, err
	}
	for _, p := range pages.Pages {
		if p.Name == in.Name {
			return &WikiWriteResult{Status: "duplicate"}, nil
		}
	}
	// 実際の認証主体を確定してから送信する。再開時はこの ID で照合する。
	c, err := s.clientForProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	me, err := c.TestConnection(ctx)
	if err != nil || me == nil || me.UserID <= 0 {
		return nil, errors.New("作成者を確認できません")
	}
	configured, err := s.cfg.Get(profile)
	if err != nil {
		return nil, err
	}
	if configured.LastUserID != me.UserID {
		return nil, errors.New("接続ユーザが変更されました")
	}
	attempt := wikiCreateAttempt{Input: in, UserID: int64(me.UserID)}
	b, _ := json.Marshal(attempt)
	// 送信前に保存する。中断・再起動しても同じ名前では再 POST しない。
	if err := store.SetMeta(ctx, st.DB(), key, string(b)); err != nil {
		return nil, err
	}
	p, err := api.CreateWiki(ctx, in.ProjectID, backlogclient.WikiWrite{Name: in.Name, Content: in.Content, MailNotify: in.MailNotify})
	if err != nil && !backlogclient.IsUncertain(err) {
		if clearErr := store.SetMeta(ctx, st.DB(), key, ""); clearErr != nil {
			return &WikiWriteResult{Status: "uncertain"}, nil
		}
		return nil, errors.New("Wiki の作成が拒否されました。権限・入力を確認し、同期してください")
	}
	if err != nil || !validWikiTarget(p, in.ProjectID, 0) || p.Name != in.Name || p.Content != in.Content {
		return s.resolveWikiCreate(ctx, profile, api, st, attempt, key)
	}
	page, err := s.cacheWrittenWiki(ctx, profile, st, p, key)
	if err != nil {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	return &WikiWriteResult{Status: "saved", Page: page}, nil
}
func (s *ProfileService) resolveWikiCreate(ctx context.Context, profile string, api wikiWriteAPI, st *store.Store, attempt wikiCreateAttempt, key string) (*WikiWriteResult, error) {
	in := attempt.Input
	pages, err := api.GetWikis(ctx, in.ProjectID)
	if err != nil {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	var id int64
	count := 0
	for _, p := range pages {
		if p.Name == in.Name {
			count++
			id = p.ID
		}
	}
	if count != 1 || id <= 0 {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	p, err := api.GetWiki(ctx, id)
	if err != nil || !validWikiTarget(p, in.ProjectID, id) || p.Name != in.Name || p.CreatedUserID != attempt.UserID || p.Content != in.Content {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	page, err := s.cacheWrittenWiki(ctx, profile, st, p, key)
	if err != nil {
		return &WikiWriteResult{Status: "uncertain"}, nil
	}
	return &WikiWriteResult{Status: "saved", Page: page}, nil
}

func (s *ProfileService) cacheWrittenWiki(ctx context.Context, profile string, st *store.Store, p *backlogclient.Wiki, clearKey string) (*WikiPage, error) {
	at := s.now().UTC().Format(time.RFC3339)
	attachments := make([]store.WikiAttachment, 0, len(p.Attachments))
	for _, a := range p.Attachments {
		attachments = append(attachments, store.WikiAttachment{WikiID: p.ID, ID: a.ID, Name: a.Name, Size: a.Size, Created: a.Created, RawJSON: a.RawJSON})
	}
	err := st.WithTx(ctx, func(tx *sql.Tx) error {
		row := &store.Wiki{ID: p.ID, ProjectID: p.ProjectID, Name: p.Name, Updated: p.Updated, Created: p.Created, TagsJSON: p.TagsJSON, RawJSON: p.RawJSON}
		if row.TagsJSON == "" {
			row.TagsJSON = "[]"
		}
		if err := store.UpsertWikiList(ctx, tx, row, at); err != nil {
			return err
		}
		if err := store.SaveWikiContent(ctx, tx, p.ID, p.Content, p.Updated, at, attachments); err != nil {
			return err
		}
		if clearKey != "" {
			return store.SetMeta(ctx, tx, clearKey, "")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	list, err := s.wikiListLocked(ctx, profile, store.WikiFilter{ProjectID: p.ProjectID})
	if err != nil {
		return nil, err
	}
	for _, page := range list.Pages {
		if page.ID == p.ID {
			return &page, nil
		}
	}
	return nil, errors.New("Wiki キャッシュを確認できません")
}
