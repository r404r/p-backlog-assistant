package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

type WikiPage struct {
	store.Wiki
	Tags               []string               `json:"tags"`
	CreatedUser        string                 `json:"createdUser"`
	UpdatedUser        string                 `json:"updatedUser"`
	TextFormattingRule string                 `json:"textFormattingRule"`
	Attachments        []store.WikiAttachment `json:"attachments"`
}
type WikiList struct {
	Pages              []WikiPage `json:"pages"`
	Disabled           bool       `json:"disabled"`
	LastSyncedAt       string     `json:"lastSyncedAt"`
	TextFormattingRule string     `json:"textFormattingRule"`
}
type WikiExportEstimate struct {
	Pages            int      `json:"pages"`
	Attachments      int      `json:"attachments"`
	Bytes            int64    `json:"bytes"`
	EstimatedSeconds int64    `json:"estimatedSeconds"`
	MissingContent   int      `json:"missingContent"`
	LastSyncedAt     string   `json:"lastSyncedAt"`
	Warnings         []string `json:"warnings"`
}
type wikiExportRun struct {
	ctx     context.Context
	cancel  context.CancelFunc
	phase   string
	options export.WikiExportOptions
}

func wikiRunKey(profile, run string) string { return profile + "\x00" + run }

func (s *ProfileService) SyncWikis(ctx context.Context, profile string, projectID int64, runID string) (*syncpkg.WikiResult, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	e, err := s.engineForProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	var progress syncpkg.ProgressFunc
	if fn := s.progressHandler(); fn != nil {
		progress = func(p syncpkg.Progress) {
			fn(SyncProgressEvent{ProfileID: profile, ProjectID: projectID, RunID: runID, Progress: p})
		}
	}
	return e.SyncWikis(ctx, projectID, progress)
}
func (s *ProfileService) wikiListLocked(ctx context.Context, profile string, f store.WikiFilter) (*WikiList, error) {
	st, err := s.storeForProfile(profile)
	if err != nil {
		return nil, err
	}
	out := &WikiList{Pages: []WikiPage{}}
	err = st.WithReadTx(ctx, func(tx *sql.Tx) error {
		raw, err := store.GetProjectRawJSON(ctx, tx, f.ProjectID)
		if err != nil {
			return err
		}
		apiDisabled, err := store.WikiDisabled(ctx, tx, f.ProjectID)
		if err != nil {
			return err
		}
		out.Disabled = !syncpkg.WikiEnabled(raw) || apiDisabled
		var project struct {
			TextFormattingRule string `json:"textFormattingRule"`
		}
		_ = json.Unmarshal([]byte(raw), &project)
		out.TextFormattingRule = project.TextFormattingRule
		state, err := store.GetSyncState(ctx, tx, store.DataKindWikis, f.ProjectID)
		if err != nil {
			return err
		}
		if state != nil {
			out.LastSyncedAt = state.LastSyncedAt
		}
		pages, err := store.SearchWikis(ctx, tx, f)
		if err != nil {
			return err
		}
		for _, p := range pages {
			a, err := store.ListWikiAttachments(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			page := WikiPage{Wiki: p, Tags: []string{}, TextFormattingRule: project.TextFormattingRule, Attachments: a}
			var tags []struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal([]byte(p.TagsJSON), &tags)
			for _, tag := range tags {
				page.Tags = append(page.Tags, tag.Name)
			}
			var meta struct {
				CreatedUser struct {
					Name string `json:"name"`
				} `json:"createdUser"`
				UpdatedUser struct {
					Name string `json:"name"`
				} `json:"updatedUser"`
			}
			_ = json.Unmarshal([]byte(p.RawJSON), &meta)
			page.CreatedUser = meta.CreatedUser.Name
			page.UpdatedUser = meta.UpdatedUser.Name
			out.Pages = append(out.Pages, page)
		}
		return nil
	})
	return out, err
}
func (s *ProfileService) ListWikis(ctx context.Context, profile string, f store.WikiFilter) (*WikiList, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	return s.wikiListLocked(ctx, profile, f)
}
func (s *ProfileService) RefreshWiki(ctx context.Context, profile string, projectID, id int64) (*WikiPage, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	e, err := s.engineForProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	if err := e.RefreshWiki(ctx, projectID, id); err != nil {
		return nil, err
	}
	list, err := s.wikiListLocked(ctx, profile, store.WikiFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	for _, p := range list.Pages {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, errors.New("Wiki が見つかりません")
}

// PrepareWikiExport は確認画面に示す対象を固定する。確認後に再同期しない。
func (s *ProfileService) PrepareWikiExport(ctx context.Context, profile string, projectID int64, subtree string, syncFirst bool, runID string) (estimate *WikiExportEstimate, err error) {
	if strings.TrimSpace(runID) == "" {
		return nil, errors.New("実行 ID が必要です")
	}
	ctx, cancel := context.WithCancel(ctx)
	run := &wikiExportRun{ctx: ctx, cancel: cancel, phase: "preparing"}
	key := wikiRunKey(profile, runID)
	s.wikiMu.Lock()
	if s.wikiExports == nil {
		s.wikiExports = map[string]*wikiExportRun{}
	}
	if _, ok := s.wikiExports[key]; ok {
		s.wikiMu.Unlock()
		cancel()
		return nil, errors.New("実行 ID が重複しています")
	}
	s.wikiExports[key] = run
	s.wikiMu.Unlock()
	defer func() {
		if err != nil {
			s.finishWikiExport(key, run)
		}
	}()
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return nil, errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	warnings := []string{}
	if syncFirst {
		e, eerr := s.engineForProfile(ctx, profile)
		if eerr != nil {
			return nil, eerr
		}
		res, eerr := e.SyncWikis(ctx, projectID, nil)
		if eerr != nil {
			return nil, eerr
		}
		if res.Disabled {
			return nil, errors.New("Wiki 無効")
		}
		warnings = res.Warnings
	}
	list, err := s.wikiListLocked(ctx, profile, store.WikiFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	if list.Disabled {
		return nil, errors.New("Wiki 無効")
	}
	st, err := s.storeForProfile(profile)
	if err != nil {
		return nil, err
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projectKey := ""
	for _, p := range projects {
		if p.ID == projectID {
			projectKey = p.ProjectKey
		}
	}
	if projectKey == "" {
		return nil, errors.New("プロジェクトが見つかりません")
	}
	estimate = &WikiExportEstimate{LastSyncedAt: list.LastSyncedAt, Warnings: warnings}
	options := export.WikiExportOptions{ProjectKey: projectKey, Pages: []export.WikiExportPage{}}
	for _, p := range list.Pages {
		options.TextFormattingRule = p.TextFormattingRule
		if !export.WikiSubtree(p.Name, subtree) {
			continue
		}
		options.Pages = append(options.Pages, export.WikiExportPage{Wiki: p.Wiki, Attachments: p.Attachments})
		estimate.Pages++
		if p.ContentFetchedAt == "" {
			estimate.MissingContent++
		}
		for _, a := range p.Attachments {
			estimate.Attachments++
			estimate.Bytes += a.Size
		}
	}
	// 1 MiB/s + 1 リクエスト/秒の保守的な目安。実回線・制限値で変わる。
	estimate.EstimatedSeconds = (estimate.Bytes+(1<<20)-1)/(1<<20) + int64(estimate.Attachments)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.wikiMu.Lock()
	run.options = options
	run.phase = "ready"
	s.wikiMu.Unlock()
	return estimate, nil
}
func (s *ProfileService) finishWikiExport(key string, run *wikiExportRun) {
	s.wikiMu.Lock()
	defer s.wikiMu.Unlock()
	run.cancel()
	if s.wikiExports[key] == run {
		delete(s.wikiExports, key)
	}
}
func (s *ProfileService) CancelWikiExport(profile, runID string) {
	key := wikiRunKey(profile, runID)
	s.wikiMu.Lock()
	defer s.wikiMu.Unlock()
	if run := s.wikiExports[key]; run != nil {
		run.cancel()
		if run.phase == "ready" {
			delete(s.wikiExports, key)
		}
	}
}
func (s *ProfileService) ExportWiki(profile, runID, parent, version string) (string, error) {
	key := wikiRunKey(profile, runID)
	s.wikiMu.Lock()
	run := s.wikiExports[key]
	if run == nil || run.phase != "ready" {
		s.wikiMu.Unlock()
		return "", errors.New("出力対象を確認してください")
	}
	run.phase = "running"
	options := run.options
	s.wikiMu.Unlock()
	defer s.finishWikiExport(key, run)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.lockWikiOperation() {
		return "", errors.New("別の処理が実行中です")
	}
	defer s.syncMu.Unlock()
	c, err := s.clientForProfile(run.ctx, profile)
	if err != nil {
		return "", err
	}
	downloader, ok := c.(export.WikiDownloader)
	if !ok {
		return "", errors.New("添付取得が利用できません")
	}
	options.Parent = parent
	options.AppVersion = version
	if fn := s.progressHandler(); fn != nil {
		options.OnProgress = func(done, total int) {
			fn(SyncProgressEvent{ProfileID: profile, RunID: runID, Progress: syncpkg.Progress{Phase: syncpkg.PhaseFetch, Fetched: done, Total: total}})
		}
	}
	return export.ExportWiki(run.ctx, options, downloader)
}
