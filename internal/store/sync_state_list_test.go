package store

import (
	"context"
	"path/filepath"
	"testing"
)

// ListSyncStates が全件を data_kind, project_id 順で返すこと(同期状態画面用)。
func TestListSyncStates(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "example.backlog.jp_1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	states := []SyncState{
		{DataKind: DataKindIssues, ProjectID: 200, LastSyncedAt: "2026-08-09T01:00:00Z"},
		{DataKind: DataKindIssues, ProjectID: 100, LastSyncedAt: "2026-08-09T02:00:00Z"},
		{DataKind: DataKindProjects, ProjectID: 0, LastSyncedAt: "2026-08-09T03:00:00Z"},
	}
	for i := range states {
		if err := UpsertSyncState(ctx, s.db, &states[i]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListSyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("件数 = %d, want 3", len(got))
	}
	// data_kind, project_id の昇順
	if got[0].DataKind != DataKindIssues || got[0].ProjectID != 100 {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].ProjectID != 200 {
		t.Errorf("got[1] = %+v", got[1])
	}
	if got[2].DataKind != DataKindProjects || got[2].LastSyncedAt != "2026-08-09T03:00:00Z" {
		t.Errorf("got[2] = %+v", got[2])
	}
}

// 空 DB では空スライスを返す(nil でも要素 0 なら可)。
func TestListSyncStatesEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "example.backlog.jp_1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.ListSyncStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("件数 = %d, want 0", len(got))
	}
}
