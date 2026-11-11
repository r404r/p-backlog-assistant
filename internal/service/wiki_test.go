package service

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type wikiConnector struct {
	*fakeConnector
	lists int
}

func (c *wikiConnector) GetWikis(context.Context, int64) ([]backlogclient.Wiki, error) {
	c.lists++
	return []backlogclient.Wiki{}, nil
}
func (c *wikiConnector) GetWiki(context.Context, int64) (*backlogclient.Wiki, error) {
	return nil, backlogclient.ErrNotFound
}
func (c *wikiConnector) DownloadWikiAttachment(_ context.Context, _, _ int64, w io.Writer) error {
	_, err := io.WriteString(w, "data")
	return err
}
func TestWikiExportFreezesConfirmedSnapshot(t *testing.T) {
	ctx := context.Background()
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	c := &wikiConnector{fakeConnector: fake}
	s.newClient = func(string, string) (connector, error) { return c, nil }
	saved, err := s.SaveProfile(ctx, "", "例", "https://example.backlog.jp", "DUMMY-KEY")
	if err != nil {
		t.Fatal(err)
	}
	id := saved.Profile.ID
	st, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "SAMPLE", RawJSON: `{"useWiki":true,"textFormattingRule":"markdown"}`}); err != nil {
		t.Fatal(err)
	}
	if err := st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertWikiList(ctx, tx, &store.Wiki{ID: 1, ProjectID: 1, Name: "Guide/Start", Updated: "T1", RawJSON: `{"createdUser":{"name":"Writer"},"updatedUser":{"name":"Editor"}}`, TagsJSON: `[{"name":"tag"}]`}, "now"); err != nil {
			return err
		}
		return store.SaveWikiContent(ctx, tx, 1, "before", "T1", "now", nil)
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListWikis(ctx, id, store.WikiFilter{ProjectID: 1})
	if err != nil || len(list.Pages) != 1 || list.Pages[0].CreatedUser != "Writer" || list.Pages[0].TextFormattingRule != "markdown" {
		t.Fatal(list, err)
	}
	estimate, err := s.PrepareWikiExport(ctx, id, 1, "Guide", false, "run")
	if err != nil || estimate.Pages != 1 || estimate.LastSyncedAt != "" || c.lists != 0 {
		t.Fatal(estimate, err)
	}
	if _, err := s.PrepareWikiExport(ctx, id, 1, "", false, "run"); err == nil {
		t.Fatal("duplicate run accepted")
	}
	if err := st.WithTx(ctx, func(tx *sql.Tx) error { return store.SaveWikiContent(ctx, tx, 1, "after", "T2", "later", nil) }); err != nil {
		t.Fatal(err)
	}
	path, err := s.ExportWiki(id, "run", t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(path, "pages", "Guide", "Start.md"))
	if err != nil || string(body) != "before" {
		t.Fatal(string(body), err)
	}
	if _, err := s.ExportWiki(id, "run", t.TempDir(), "test"); err == nil {
		t.Fatal("consumed run reused")
	}
	if _, err := s.PrepareWikiExport(ctx, id, 1, "", false, "cancel"); err != nil {
		t.Fatal(err)
	}
	s.CancelWikiExport(id, "cancel")
	if _, err := s.ExportWiki(id, "cancel", t.TempDir(), "test"); err == nil {
		t.Fatal("cancelled run exported")
	}
	if err := store.SetWikiDisabled(ctx, st.DB(), 1, true); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListWikis(ctx, id, store.WikiFilter{ProjectID: 1})
	if err != nil || !list.Disabled {
		t.Fatal(list, err)
	}
}
