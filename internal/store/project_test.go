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

// TestGetProjectRawJSON は課題詳細で使うプロジェクトの生 JSON を
// 1 件だけ引けることを確認する。
//
// 記法設定(textFormattingRule)の判定に使うため、全件を走査する ListProjects
// ではなく 1 件だけ引く経路を分けている(GetIssueKeyByID と同じ流儀)。
// 見つからない場合はエラーにせず空文字を返し、詳細表示全体を止めない。
func TestGetProjectRawJSON(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA", Name: "検証用",
		RawJSON: `{"id":1,"textFormattingRule":"markdown"}`}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetProjectRawJSON(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"id":1,"textFormattingRule":"markdown"}` {
		t.Errorf("生 JSON = %q", got)
	}

	missing, err := s.GetProjectRawJSON(ctx, 99)
	if err != nil {
		t.Fatalf("未登録のプロジェクトでエラーになった: %v", err)
	}
	if missing != "" {
		t.Errorf("未登録のプロジェクト = %q, want 空文字", missing)
	}

	// raw_json が NULL の行(生 JSON を保存していなかった頃の既存 DB)でも
	// 走査に失敗しない。ここでエラーを返すと課題詳細の表示自体が落ちるため、
	// 記法設定だけを諦めて空文字へ縮退させる
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO projects (id, project_key, name, archived, raw_json, fetched_at)
		 VALUES (7, 'EXN', 'NULL 行', 0, NULL, '')`); err != nil {
		t.Fatal(err)
	}
	nullRow, err := s.GetProjectRawJSON(ctx, 7)
	if err != nil {
		t.Fatalf("raw_json が NULL の行でエラーになった: %v", err)
	}
	if nullRow != "" {
		t.Errorf("raw_json が NULL の行 = %q, want 空文字", nullRow)
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

// TestDeleteProjectsNotIn_DeletesJobsOfRemovedProjects はプロジェクトの
// キャッシュ破棄と同時に、そのプロジェクトの一括更新ジョブ(jobs / job_rows)も
// 削除することを確認する(R2)。
// job_rows の payload には件名・詳細・カスタム属性が入るため、閲覧できなくなった
// プロジェクトのデータをローカルに残さない(設計書 2 節・7 節)。
func TestDeleteProjectsNotIn_DeletesJobsOfRemovedProjects(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := s.UpsertProject(ctx, &Project{ID: id, ProjectKey: "EX", Name: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	keep, err := s.CreateJob(ctx, JobKindUpdate, 1, "keep.xlsx", "h1",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: `{"summary":"残す"}`}})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := s.CreateJob(ctx, JobKindUpdate, 2, "removed.xlsx", "h2",
		[]JobRow{{RowNo: 2, IssueKey: "EXB-1", Payload: `{"summary":"消す"}`}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DeleteProjectsNotIn(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	if jobExists(t, s, removed) {
		t.Error("削除したプロジェクトのジョブが残っている")
	}
	if jobRowCount(t, s, removed) != 0 {
		t.Error("削除したプロジェクトの job_rows(payload)が残っている")
	}
	if !jobExists(t, s, keep) || jobRowCount(t, s, keep) != 1 {
		t.Error("参加中プロジェクトのジョブまで削除された")
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
	// 一括更新ジョブ(payload に件名・詳細が入る)も破棄対象(R2)
	job, err := s.CreateJob(ctx, JobKindUpdate, 1, "bulk.xlsx", "h",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: `{"summary":"件名"}`}})
	if err != nil {
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
	if jobExists(t, s, job) || jobRowCount(t, s, job) != 0 {
		t.Error("全削除でジョブ(payload)が残っている")
	}
}
