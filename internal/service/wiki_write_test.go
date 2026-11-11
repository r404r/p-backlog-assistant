package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type wikiWriter struct {
	*fakeConnector
	remote                      backlogclient.Wiki
	getErr                      error
	listErr                     error
	writeErr                    error
	apply                       bool
	gets, patches, posts, lists int
	candidates                  []backlogclient.Wiki
}

func (f *wikiWriter) GetWiki(context.Context, int64) (*backlogclient.Wiki, error) {
	f.gets++
	p := f.remote
	return &p, f.getErr
}
func (f *wikiWriter) GetWikis(context.Context, int64) ([]backlogclient.Wiki, error) {
	f.lists++
	return f.candidates, f.listErr
}
func (f *wikiWriter) UpdateWiki(_ context.Context, _ int64, in backlogclient.WikiWrite) (*backlogclient.Wiki, error) {
	f.patches++
	return f.write(in)
}
func (f *wikiWriter) CreateWiki(_ context.Context, _ int64, in backlogclient.WikiWrite) (*backlogclient.Wiki, error) {
	f.posts++
	return f.write(in)
}
func (f *wikiWriter) write(in backlogclient.WikiWrite) (*backlogclient.Wiki, error) {
	if in.MailNotify {
		return nil, errors.New("unexpected notification")
	}
	if f.apply {
		f.remote.Name = in.Name
		f.remote.Content = in.Content
		f.remote.Updated = "T3"
		f.candidates = []backlogclient.Wiki{f.remote}
	}
	p := f.remote
	return &p, f.writeErr
}
func wikiWriteSetup(t *testing.T) (*ProfileService, string, *wikiWriter) {
	t.Helper()
	f := &wikiWriter{fakeConnector: &fakeConnector{info: testInfo()}, remote: backlogclient.Wiki{ID: 10, ProjectID: 1, Name: "Guide/Start", Content: "old body", Updated: "T1", TagsJSON: "[]", RawJSON: `{}`, CreatedUserID: 42}, apply: true}
	s, _, _ := newTestService(t, f.fakeConnector)
	s.newClient = func(string, string) (connector, error) { return f, nil }
	p, err := s.SaveProfile(context.Background(), "", "Sample", "https://example.backlog.jp", "DUMMY-KEY")
	if err != nil {
		t.Fatal(err)
	}
	id := p.Profile.ID
	st, _ := s.storeForProfile(id)
	if err := st.UpsertProject(context.Background(), &store.Project{ID: 1, ProjectKey: "SAMPLE", RawJSON: `{"useWiki":true,"textFormattingRule":"markdown"}`}); err != nil {
		t.Fatal(err)
	}
	return s, id, f
}
func TestWikiEditBaselineConflictAndExplicitOverwrite(t *testing.T) {
	ctx := context.Background()
	s, id, f := wikiWriteSetup(t)
	edit, err := s.BeginWikiEdit(ctx, id, 1, 10)
	if err != nil || edit.BaselineUpdated != "T1" || edit.Page.Content != "old body" {
		t.Fatal(edit, err)
	}
	f.remote.Updated = "T2"
	r, err := s.SaveWikiEdit(ctx, id, WikiSaveInput{Token: edit.Token, Name: "Guide/New", Content: "my body"})
	if err != nil || r.Status != "conflict" || f.patches != 0 || r.Page.Updated != "T2" {
		t.Fatal(r, err)
	}
	before := f.gets
	// PATCH に楽観ロックはなく、GET と PATCH 間の競合窓は残る。
	r, err = s.SaveWikiEdit(ctx, id, WikiSaveInput{Token: edit.Token, Name: "Guide/New", Content: "my body", Overwrite: true})
	if err != nil || r.Status != "saved" || f.gets != before || f.patches != 1 {
		t.Fatal(r, err, f.gets)
	}
	list, err := s.ListWikis(ctx, id, store.WikiFilter{ProjectID: 1})
	if err != nil || len(list.Pages) != 1 || list.Pages[0].Content != "my body" || list.Pages[0].Name != "Guide/New" {
		t.Fatal(list, err)
	}
}
func TestWikiEditRequiresFreshSuccessfulDetail(t *testing.T) {
	s, id, f := wikiWriteSetup(t)
	ctx := context.Background()
	st, _ := s.storeForProfile(id)
	err := st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertWikiList(ctx, tx, &store.Wiki{ID: 10, ProjectID: 1, Name: "Guide/Start", Updated: "T2", TagsJSON: "[]", RawJSON: `{}`}, "now"); err != nil {
			return err
		}
		return store.SaveWikiContent(ctx, tx, 10, "stale body", "T1", "old", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	f.getErr = errors.New("offline")
	if _, err := s.BeginWikiEdit(ctx, id, 1, 10); err == nil {
		t.Fatal("failed detail accepted")
	}
	if _, err := s.SaveWikiEdit(ctx, id, WikiSaveInput{Name: "x", Content: "stale body"}); err == nil || f.patches != 0 {
		t.Fatal("unbound edit sent")
	}
}
func TestWikiEditUncertainNeverResends(t *testing.T) {
	for _, applied := range []bool{true, false} {
		t.Run(map[bool]string{true: "applied", false: "not applied"}[applied], func(t *testing.T) {
			s, id, f := wikiWriteSetup(t)
			ctx := context.Background()
			edit, err := s.BeginWikiEdit(ctx, id, 1, 10)
			if err != nil {
				t.Fatal(err)
			}
			f.apply = applied
			f.writeErr = &backlogclient.UncertainError{Op: "update", Err: context.DeadlineExceeded}
			r, err := s.SaveWikiEdit(ctx, id, WikiSaveInput{Token: edit.Token, Name: "Guide/Start", Content: "new"})
			want := "not_saved"
			if applied {
				want = "saved"
			}
			if err != nil || r.Status != want || f.patches != 1 || f.gets != 3 {
				t.Fatal(r, err, f.patches, f.gets)
			}
		})
	}
}
func TestWikiCreateUncertainMatchesEveryFieldAndNeverResends(t *testing.T) {
	for _, mode := range []string{"match", "none", "ambiguous", "creator", "content", "project", "name", "detail failure", "list failure"} {
		t.Run(mode, func(t *testing.T) {
			s, id, f := wikiWriteSetup(t)
			ctx := context.Background()
			f.apply = false
			f.writeErr = &backlogclient.UncertainError{Op: "create", Err: context.DeadlineExceeded}
			f.remote.Name = "Guide/New"
			f.remote.Content = "new"
			f.candidates = []backlogclient.Wiki{f.remote}
			switch mode {
			case "none":
				f.candidates = nil
			case "ambiguous":
				f.candidates = append(f.candidates, f.remote)
			case "creator":
				f.remote.CreatedUserID = 99
			case "content":
				f.remote.Content = "different"
			case "project":
				f.remote.ProjectID = 2
			case "name":
				f.remote.Name = "Other"
			case "detail failure":
				f.getErr = errors.New("offline")
			case "list failure":
				f.listErr = errors.New("offline")
			}
			in := WikiCreateInput{ProjectID: 1, Name: "Guide/New", Content: "new"}
			r, err := s.CreateWiki(ctx, id, in)
			want := "uncertain"
			if mode == "match" {
				want = "saved"
			}
			if err != nil || r.Status != want || f.posts != 1 || f.lists != 1 {
				t.Fatal(r, err, f.posts, f.lists)
			}
			if mode != "match" {
				// セッションを捨てても送信前マーカーはローカル DB に残る。
				s.wikiEdits = nil
				s.closeStore(id)
				r, err = s.CreateWiki(ctx, id, in)
				if err != nil || r.Status != "uncertain" || f.posts != 1 {
					t.Fatal("resent", r, err, f.posts)
				}
			}
		})
	}
}

func TestWikiCreateDoesNotClaimChangedDraftWasSaved(t *testing.T) {
	s, id, f := wikiWriteSetup(t)
	ctx := context.Background()
	f.apply = false
	f.writeErr = &backlogclient.UncertainError{Op: "create", Err: context.DeadlineExceeded}
	in := WikiCreateInput{ProjectID: 1, Name: "Guide/New", Content: "first draft"}
	r, err := s.CreateWiki(ctx, id, in)
	if err != nil || r.Status != "uncertain" {
		t.Fatal(r, err)
	}
	f.remote.Name = in.Name
	f.remote.Content = in.Content
	f.candidates = []backlogclient.Wiki{f.remote}
	in.Content = "different draft"
	r, err = s.CreateWiki(ctx, id, in)
	if err != nil || r.Status != "uncertain" || f.posts != 1 {
		t.Fatal("changed draft lost or resent", r, err, f.posts)
	}
}
func TestWikiCreateSuccessAndLocalDuplicate(t *testing.T) {
	s, id, f := wikiWriteSetup(t)
	ctx := context.Background()
	in := WikiCreateInput{ProjectID: 1, Name: "Guide/New", Content: "new"}
	r, err := s.CreateWiki(ctx, id, in)
	if err != nil || r.Status != "saved" || r.Page.Content != "new" {
		t.Fatal(r, err)
	}
	r, err = s.CreateWiki(ctx, id, in)
	if err != nil || r.Status != "duplicate" || f.posts != 1 {
		t.Fatal(r, err, f.posts)
	}
}
func TestWikiSavePreflightFailureAndUnconfirmedOverwrite(t *testing.T) {
	s, id, f := wikiWriteSetup(t)
	ctx := context.Background()
	edit, err := s.BeginWikiEdit(ctx, id, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	input := WikiSaveInput{Token: edit.Token, Name: "Guide/New", Content: "new", Overwrite: true}
	if _, err = s.SaveWikiEdit(ctx, id, input); err == nil || f.patches != 0 {
		t.Fatal("unconfirmed overwrite sent")
	}
	input.Overwrite = false
	f.getErr = errors.New("offline")
	if _, err = s.SaveWikiEdit(ctx, id, input); err == nil || f.patches != 0 {
		t.Fatal("preflight failure ignored")
	}
}
