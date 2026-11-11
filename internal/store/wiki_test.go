package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWikiV6UpgradeSearchAndPrune(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	old := createLegacyDB(t, path, 6)
	mustExec(t, old, `INSERT INTO projects(id,project_key,name) VALUES(1,'SAMPLE','例')`)
	old.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v, _ := st.SchemaVersion(); v != 7 {
		t.Fatalf("version=%d", v)
	}
	w := &Wiki{ID: 10, ProjectID: 1, Name: "手順/日本語検索", Updated: "T1", RawJSON: "{}", TagsJSON: "[]"}
	if err := UpsertWikiList(ctx, st.DB(), w, "now"); err != nil {
		t.Fatal(err)
	}
	if err := st.WithTx(ctx, func(tx *sql.Tx) error {
		return SaveWikiContent(ctx, tx, 10, "添付を検索する本文", "T1", "now", []WikiAttachment{{ID: 2, Name: "sample.txt", Size: 4}})
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []WikiFilter{{ProjectID: 1, Keyword: "日本語検索 本文"}, {ProjectID: 1, Keyword: "日本"}, {ProjectID: 1, Keyword: "無一致 本文", KeywordMode: "or"}} {
		got, err := SearchWikis(ctx, st.DB(), q)
		if err != nil || len(got) != 1 {
			t.Fatalf("query=%+v result=%d err=%v", q, len(got), err)
		}
	}
	w.Name = "変更後"
	w.Updated = "T2"
	if err := UpsertWikiList(ctx, st.DB(), w, "later"); err != nil {
		t.Fatal(err)
	}
	got, err := GetWiki(ctx, st.DB(), 1, 10)
	if err != nil || got.ContentUpdated != "T1" || got.ContentFetchedAt != "now" || got.Updated != "T2" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := st.DB().Exec(`UPDATE wikis SET deleted=2`); err == nil {
		t.Fatal("CHECK missing")
	}
	if _, err := st.DB().Exec(`INSERT INTO wiki_attachments(wiki_id,id,name) VALUES(999,1,'x')`); err == nil {
		t.Fatal("FK missing")
	}
	if err := MarkWikisMissing(ctx, st.DB(), 1, nil); err != nil {
		t.Fatal(err)
	}
	gotRows, err := SearchWikis(ctx, st.DB(), WikiFilter{ProjectID: 1})
	if err != nil || len(gotRows) != 0 {
		t.Fatalf("deleted search=%v %v", gotRows, err)
	}
	// FK 無効でも子の添付と FTS を取り残さない。
	mustExec(t, st.DB(), `PRAGMA foreign_keys=OFF`)
	if _, err := DeleteProjectsNotIn(ctx, st.DB(), nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"wikis", "wiki_attachments"} {
		var n int
		st.DB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		if n != 0 {
			t.Fatal(table, n)
		}
	}
}
