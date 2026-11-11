package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type WikiAPI interface {
	GetWikis(context.Context, int64) ([]backlogclient.Wiki, error)
	GetWiki(context.Context, int64) (*backlogclient.Wiki, error)
}
type WikiResult struct {
	Result
	Disabled bool `json:"disabled"`
}

func WikiEnabled(raw string) bool {
	var p struct {
		UseWiki bool `json:"useWiki"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	return p.UseWiki
}
func wikiStore(p backlogclient.Wiki) *store.Wiki {
	return &store.Wiki{ID: p.ID, ProjectID: p.ProjectID, Name: p.Name, TagsJSON: p.TagsJSON, Created: p.Created, Updated: p.Updated, RawJSON: p.RawJSON}
}
func wikiAttachments(p *backlogclient.Wiki) []store.WikiAttachment {
	out := make([]store.WikiAttachment, 0, len(p.Attachments))
	for _, a := range p.Attachments {
		out = append(out, store.WikiAttachment{ID: a.ID, WikiID: p.ID, Name: a.Name, Size: a.Size, Created: a.Created, RawJSON: a.RawJSON})
	}
	return out
}
func (e *Engine) SyncWikis(ctx context.Context, projectID int64, onProgress ProgressFunc) (*WikiResult, error) {
	if projectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	start := e.now()
	r := &WikiResult{Result: Result{Mode: ModeIncremental, Warnings: []string{}}}
	raw, err := e.st.GetProjectRawJSON(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !WikiEnabled(raw) {
		r.Disabled = true
		if err := store.SetWikiDisabled(ctx, e.st.DB(), projectID, true); err != nil {
			return nil, err
		}
		return r, nil
	}
	api, ok := e.api.(WikiAPI)
	if !ok {
		return nil, errors.New("Wiki API が利用できません")
	}
	pages, err := api.GetWikis(ctx, projectID)
	if errors.Is(err, backlogclient.ErrPermissionDenied) {
		r.Disabled = true
		if err := store.SetWikiDisabled(ctx, e.st.DB(), projectID, true); err != nil {
			return nil, err
		}
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	// フェイクを含め、異常リストで削除確定しない。
	seen := map[int64]bool{}
	ids := []int64{}
	for _, p := range pages {
		if p.ID <= 0 || p.ProjectID != projectID || seen[p.ID] {
			return nil, errors.New("Wiki 一覧が不正です")
		}
		seen[p.ID] = true
		ids = append(ids, p.ID)
	}
	report(onProgress, Progress{Phase: PhaseFetch, Total: len(pages)})
	for _, p := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prev, err := store.GetWikiIncludingDeleted(ctx, e.st.DB(), projectID, p.ID)
		if err != nil {
			return nil, err
		}
		if err := e.st.WithTx(ctx, func(tx *sql.Tx) error { return store.UpsertWikiList(ctx, tx, wikiStore(p), e.nowString()) }); err != nil {
			return nil, err
		}
		if prev == nil || prev.ContentFetchedAt == "" || prev.ContentUpdated != p.Updated {
			detail, err := api.GetWiki(ctx, p.ID)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, backlogclient.ErrPermissionDenied) {
				r.Disabled = true
				if err := store.SetWikiDisabled(ctx, e.st.DB(), projectID, true); err != nil {
					return nil, err
				}
				return r, nil
			}
			if err != nil {
				r.warn("Wiki %d の本文取得に失敗しました", p.ID)
			} else {
				if detail == nil || detail.ID != p.ID || detail.ProjectID != projectID {
					return nil, errors.New("Wiki 詳細の対象が一致しません")
				}
				if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
					return store.SaveWikiContent(ctx, tx, p.ID, detail.Content, p.Updated, e.nowString(), wikiAttachments(detail))
				}); err != nil {
					return nil, err
				}
				r.Upserted++
			}
		}
		r.Fetched++
		report(onProgress, Progress{Phase: PhaseFetch, Fetched: r.Fetched, Total: len(pages)})
	}
	if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.MarkWikisMissing(ctx, tx, projectID, ids); err != nil {
			return err
		}
		if err := store.SetWikiDisabled(ctx, tx, projectID, false); err != nil {
			return err
		}
		return store.SetSyncCompleted(ctx, tx, store.DataKindWikis, projectID, e.nowString(), "")
	}); err != nil {
		return nil, err
	}
	r.DurationMs = e.now().Sub(start).Milliseconds()
	report(onProgress, Progress{Phase: PhaseDone, Fetched: r.Fetched, Total: len(pages)})
	return r, nil
}
func (e *Engine) RefreshWiki(ctx context.Context, projectID, id int64) error {
	raw, err := e.st.GetProjectRawJSON(ctx, projectID)
	if err != nil {
		return err
	}
	if !WikiEnabled(raw) {
		return errors.New("Wiki 無効")
	}
	api, ok := e.api.(WikiAPI)
	if !ok {
		return errors.New("Wiki API が利用できません")
	}
	p, err := api.GetWiki(ctx, id)
	if err != nil {
		return err
	}
	if p == nil || p.ID != id || p.ProjectID != projectID {
		return fmt.Errorf("Wiki の対象が一致しません")
	}
	return e.st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertWikiList(ctx, tx, wikiStore(*p), e.nowString()); err != nil {
			return err
		}
		return store.SaveWikiContent(ctx, tx, id, p.Content, p.Updated, e.nowString(), wikiAttachments(p))
	})
}
