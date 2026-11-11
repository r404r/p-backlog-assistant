package sync

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type wikiFake struct {
	fakeAPI
	pages          []backlogclient.Wiki
	listErr        error
	fail           bool
	lists, details int
}

func (f *wikiFake) GetWikis(context.Context, int64) ([]backlogclient.Wiki, error) {
	f.lists++
	return f.pages, f.listErr
}
func (f *wikiFake) GetWiki(_ context.Context, id int64) (*backlogclient.Wiki, error) {
	f.details++
	if f.fail {
		return nil, errors.New("失敗")
	}
	for _, p := range f.pages {
		if p.ID == id {
			p.Content = "日本語の本文"
			return &p, nil
		}
	}
	return nil, backlogclient.ErrNotFound
}
func TestWikisIncrementalRetryAndDisabled(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertProject(ctx, &store.Project{ID: 1, RawJSON: `{"useWiki":true}`}); err != nil {
		t.Fatal(err)
	}
	f := &wikiFake{pages: []backlogclient.Wiki{{ID: 10, ProjectID: 1, Name: "例", Updated: "T1", TagsJSON: "[]", RawJSON: "{}"}}}
	e := NewEngine(f, st)
	run := func() {
		t.Helper()
		if _, err := e.SyncWikis(ctx, 1, nil); err != nil {
			t.Fatal(err)
		}
	}
	run()
	run()
	if f.lists != 2 || f.details != 1 {
		t.Fatal(f.lists, f.details)
	}
	f.pages[0].Updated = "T2"
	f.fail = true
	run()
	w, _ := store.GetWiki(ctx, st.DB(), 1, 10)
	if w.ContentUpdated != "T1" || w.Updated != "T2" {
		t.Fatal(w)
	}
	f.fail = false
	run()
	if f.details != 3 {
		t.Fatal(f.details)
	}
	before, _ := st.GetSyncState(ctx, store.DataKindWikis, 1)
	f.listErr = errors.New("失敗")
	if _, err := e.SyncWikis(ctx, 1, nil); err == nil {
		t.Fatal("expected error")
	}
	after, _ := st.GetSyncState(ctx, store.DataKindWikis, 1)
	w, _ = store.GetWiki(ctx, st.DB(), 1, 10)
	if w == nil || before.LastSyncedAt != after.LastSyncedAt {
		t.Fatal("failed list modified cache")
	}
	f.listErr = nil
	f.pages = nil
	run()
	rows, _ := store.SearchWikis(ctx, st.DB(), store.WikiFilter{ProjectID: 1})
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	f.pages = []backlogclient.Wiki{{ID: 10, ProjectID: 1, Name: "例", Updated: "T2", TagsJSON: "[]", RawJSON: "{}"}}
	run()
	rows, _ = store.SearchWikis(ctx, st.DB(), store.WikiFilter{ProjectID: 1})
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	f.listErr = backlogclient.ErrPermissionDenied
	r, err := e.SyncWikis(ctx, 1, nil)
	if err != nil || !r.Disabled {
		t.Fatal(r, err)
	}
	st.UpsertProject(ctx, &store.Project{ID: 1, RawJSON: `{"useWiki":false}`})
	n := f.lists
	r, err = e.SyncWikis(ctx, 1, nil)
	if err != nil || !r.Disabled || f.lists != n {
		t.Fatal("disabled called API")
	}
}

func TestWikiPermissionStateSurvivesViewReload(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.UpsertProject(ctx, &store.Project{ID: 1, RawJSON: `{"useWiki":true}`})
	f := &wikiFake{listErr: backlogclient.ErrPermissionDenied}
	e := NewEngine(f, st)
	e.SyncWikis(ctx, 1, nil)
	disabled, err := store.WikiDisabled(ctx, st.DB(), 1)
	if err != nil || !disabled {
		t.Fatal(disabled, err)
	}
	f.listErr = nil
	e.SyncWikis(ctx, 1, nil)
	disabled, err = store.WikiDisabled(ctx, st.DB(), 1)
	if err != nil || disabled {
		t.Fatal(disabled, err)
	}
}
