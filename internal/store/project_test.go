package store

import (
	"context"
	"testing"
)

func TestUpsertProject_AndList(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	p := &Project{ID: 1, ProjectKey: "EXA", Name: "検証用", Archived: false,
		RawJSON: `{"id":1}`, FetchedAt: "2026-08-09T00:00:00Z"}
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	// 同じ ID で更新しても 1 行のまま、内容は上書きされる
	p.Name = "検証用(改称)"
	p.Archived = true
	if err := s.UpsertProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, &Project{ID: 2, ProjectKey: "EXB", Name: "別件"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("件数 = %d, want 2", len(got))
	}
	if got[0].ID != 1 || got[0].Name != "検証用(改称)" || !got[0].Archived {
		t.Errorf("projects[0] = %+v", got[0])
	}
	if got[1].ProjectKey != "EXB" {
		t.Errorf("projects[1] = %+v", got[1])
	}
}

func TestDeleteProjectsNotIn(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	for _, id := range []int64{1, 2, 3} {
		if err := s.UpsertProject(ctx, &Project{ID: id, ProjectKey: "EX", Name: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	// アクセス不能になったプロジェクト(2)のキャッシュを破棄する
	n, err := s.DeleteProjectsNotIn(ctx, []int64{1, 3})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("削除件数 = %d, want 1", n)
	}
	got, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Errorf("残ったプロジェクト = %+v", got)
	}
}

// TestDeleteProjectsNotIn_EmptyDeletesAll は keepIDs が空(= 参加プロジェクトが
// 0 件という正常応答)のときに全キャッシュを破棄することを確認する。
//
// 期待値変更の理由(高 1): 旧仕様は「取得失敗で全消しになる」ことを恐れて
// 空なら何もしなかったが、呼び出し元(internal/sync の SyncProjects)は
// GET /projects が失敗した時点で return しており、ここへは正常応答でしか
// 到達しない。全プロジェクトから除外されたユーザの旧キャッシュが残ると
// 設計書 2 節の情報漏えい防止要件に反するため、空応答では全削除する。
func TestDeleteProjectsNotIn_EmptyDeletesAll(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssues(ctx, []*Issue{{ID: 10, IssueKey: "EXA-1", ProjectID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO project_users (project_id, user_id, is_admin) VALUES (1, 42, 0)`); err != nil {
		t.Fatal(err)
	}
	// プロジェクト別の同期状態(project_id <> 0)も破棄対象
	if err := s.UpsertSyncState(ctx, &SyncState{
		DataKind: DataKindIssues, ProjectID: 1, LastSyncedAt: "2026-08-09T00:00:00Z", ActivityCursor: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// スペース共通の同期状態(project_id = 0)は残す
	if err := s.UpsertSyncState(ctx, &SyncState{
		DataKind: DataKindProjects, ProjectID: ProjectScopeAll, LastSyncedAt: "2026-08-09T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteProjectsNotIn(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("削除件数 = %d, want 1", n)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("残ったプロジェクト = %+v, want 0 件", projects)
	}
	ids, err := s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("残った課題 = %v, want 0 件", ids)
	}
	var users int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM project_users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Errorf("残った project_users = %d 件, want 0", users)
	}
	st, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if st != nil {
		t.Errorf("プロジェクト別 sync_state が残っている: %+v", st)
	}
	all, err := s.GetSyncState(ctx, DataKindProjects, ProjectScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if all == nil {
		t.Error("スペース共通の sync_state(project_id = 0)まで削除された")
	}
}
