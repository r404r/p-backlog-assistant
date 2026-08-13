package store

import (
	"context"
	"testing"
)

// seedCommentFixtures はコメント保存テスト用に、プロジェクト 1 件と課題 2 件を作る。
// issue_comments.issue_id は issues(id) への FK なので、課題を先に作っておく。
func seedCommentFixtures(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 101, IssueKey: "EXA-1", ProjectID: 1, Summary: "件名 1"},
		{ID: 102, IssueKey: "EXA-2", ProjectID: 1, Summary: "件名 2"},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReplaceIssueComments_SavesAndReplaces は初回保存と再実行時の全入れ替え
// (古い行が残らないこと)を確認する。
func TestReplaceIssueComments_SavesAndReplaces(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	first := []*IssueComment{
		{ID: 1001, AuthorName: "山田 太郎", Content: "最初のコメント", Created: "2026-08-01T00:00:00Z", Updated: "2026-08-01T00:00:00Z"},
		{ID: 1002, AuthorName: "鈴木 花子", Content: "2 番目", Created: "2026-08-02T00:00:00Z", Updated: "2026-08-02T00:00:00Z"},
	}
	if err := s.ReplaceIssueComments(ctx, 101, 1, first,
		CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("保存後の件数 = %d, want 2", len(got))
	}
	// 新しい順(created 降順)
	if got[0].ID != 1002 || got[1].ID != 1001 {
		t.Errorf("並び順 = [%d %d], want [1002 1001]", got[0].ID, got[1].ID)
	}
	if got[0].IssueID != 101 || got[0].ProjectID != 1 ||
		got[0].AuthorName != "鈴木 花子" || got[0].Content != "2 番目" ||
		got[0].Created != "2026-08-02T00:00:00Z" || got[0].Updated != "2026-08-02T00:00:00Z" {
		t.Errorf("保存内容 = %+v", got[0])
	}

	// 再実行で全入れ替え(古い 2 件は消え、新しい 1 件だけになる)
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1003, AuthorName: "佐藤", Content: "入れ替え後", Created: "2026-08-03T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-11T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1003 {
		t.Fatalf("入れ替え後 = %+v, want [1003] のみ", got)
	}
	if n := countRows(t, s, "issue_comments"); n != 1 {
		t.Errorf("issue_comments 全体の件数 = %d, want 1(古い行が残っている)", n)
	}
}

// TestReplaceIssueComments_EmptyClears は 0 件での入れ替え(コメントが
// すべて削除された課題)でも既存行が消え、取得結果は記録されることを確認する。
func TestReplaceIssueComments_EmptyClears(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "消える予定", Created: "2026-08-01T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	for _, comments := range [][]*IssueComment{{}, nil} {
		if err := s.ReplaceIssueComments(ctx, 101, 1, comments,
			CommentFetchStatus{FetchedAt: "2026-08-12T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ListIssueComments(ctx, 101)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Error("0 件のとき nil が返った(空スライスを返すこと)")
		}
		if len(got) != 0 {
			t.Errorf("0 件で入れ替えた後 = %+v, want 空", got)
		}
		st, err := s.GetIssueCommentStatus(ctx, 101)
		if err != nil {
			t.Fatal(err)
		}
		if st.FetchedAt != "2026-08-12T00:00:00Z" {
			t.Errorf("0 件でも取得時刻を記録すること: %+v", st)
		}
	}
}

// TestReplaceIssueComments_RecordsStatus は取得結果(取得時刻・打ち切り・
// 変更履歴のみの件数)が読み戻せることを確認する。
func TestReplaceIssueComments_RecordsStatus(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	want := CommentFetchStatus{FetchedAt: "2026-08-10T12:34:56Z", Truncated: true, HistoryOnly: 7}
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "本文", Created: "2026-08-01T00:00:00Z"},
	}, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueCommentStatus(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("取得結果 = %+v, want %+v", got, want)
	}

	// 再取得で打ち切り状態が解消された場合は false / 0 に戻ること
	want2 := CommentFetchStatus{FetchedAt: "2026-08-11T00:00:00Z"}
	if err := s.ReplaceIssueComments(ctx, 101, 1, nil, want2); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetIssueCommentStatus(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if got != want2 {
		t.Errorf("再取得後の取得結果 = %+v, want %+v", got, want2)
	}
}

// TestListIssueComments_ScopedToIssue は他の課題のコメントが混ざらないこと、
// 0 件なら空スライスを返すことを確認する。
func TestListIssueComments_ScopedToIssue(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "課題 101 のコメント", Created: "2026-08-01T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssueComments(ctx, 102, 1, []*IssueComment{
		{ID: 2001, Content: "課題 102 のコメント", Created: "2026-08-02T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 1001 {
		t.Errorf("課題 101 のコメント = %+v, want 1001 のみ", got)
	}

	// 未取得の課題は空スライス(nil ではない)
	if err := s.UpsertIssue(ctx, &Issue{ID: 103, IssueKey: "EXA-3", ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	empty, err := s.ListIssueComments(ctx, 103)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil {
		t.Error("コメントが無い課題で nil が返った(空スライスを返すこと)")
	}
	if len(empty) != 0 {
		t.Errorf("コメントが無い課題 = %+v, want 空", empty)
	}
}

// TestListIssueComments_OrdersByCreatedThenID は created が同着の場合に
// id 降順で安定して並ぶことを確認する(Backlog の created は秒精度のため
// 同一秒のコメントがありうる)。
func TestListIssueComments_OrdersByCreatedThenID(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1, Content: "a", Created: "2026-08-01T00:00:00Z"},
		{ID: 3, Content: "c", Created: "2026-08-01T00:00:00Z"},
		{ID: 2, Content: "b", Created: "2026-08-01T00:00:00Z"},
		{ID: 4, Content: "d", Created: "2026-07-31T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	want := []int64{3, 2, 1, 4}
	if len(ids) != len(want) {
		t.Fatalf("件数 = %d, want %d", len(ids), len(want))
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("並び = %v, want %v", ids, want)
		}
	}
}

// TestGetIssueCommentStatus_ZeroValueWhenUnfetched は未取得の課題と
// 存在しない課題 ID でゼロ値が返り、エラーにならないことを確認する。
func TestGetIssueCommentStatus_ZeroValueWhenUnfetched(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	for _, id := range []int64{101, 999999} {
		st, err := s.GetIssueCommentStatus(ctx, id)
		if err != nil {
			t.Fatalf("課題 %d: %v", id, err)
		}
		if st != (CommentFetchStatus{}) {
			t.Errorf("課題 %d の取得結果 = %+v, want ゼロ値", id, st)
		}
	}
}

// TestDeleteProjectsNotIn_RemovesComments は、閲覧できなくなった
// プロジェクトのコメントが破棄され、残すプロジェクトのコメントは残ることを
// 確認する(課題本文と同じ扱い。設計書 2 節)。
func TestDeleteProjectsNotIn_RemovesComments(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	for _, p := range []*Project{
		{ID: 1, ProjectKey: "EXA", Name: "残す"},
		{ID: 2, ProjectKey: "EXB", Name: "消える"},
	} {
		if err := s.UpsertProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 101, IssueKey: "EXA-1", ProjectID: 1},
		{ID: 201, IssueKey: "EXB-1", ProjectID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ issueID, projectID, commentID int64 }{
		{101, 1, 1001}, {201, 2, 2001},
	} {
		if err := s.ReplaceIssueComments(ctx, tc.issueID, tc.projectID, []*IssueComment{
			{ID: tc.commentID, Content: "コメント", Created: "2026-08-01T00:00:00Z"},
		}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.DeleteProjectsNotIn(ctx, []int64{1}); err != nil {
		t.Fatal(err)
	}
	kept, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("残すプロジェクトのコメント = %d 件, want 1", len(kept))
	}
	if n := countRows(t, s, "issue_comments"); n != 1 {
		t.Errorf("削除後の issue_comments 全体 = %d 件, want 1", n)
	}
}

// TestDeleteProjectsNotIn_RemovesAllCommentsWhenNoKeep は keepIDs が空
// (参加プロジェクト 0 件)のときに全コメントが消えることを確認する。
func TestDeleteProjectsNotIn_RemovesAllCommentsWhenNoKeep(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "コメント", Created: "2026-08-01T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteProjectsNotIn(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "issue_comments"); n != 0 {
		t.Errorf("全プロジェクト削除後の issue_comments = %d 件, want 0", n)
	}
}

// TestReplaceIssueComments_KeepsFTSIndexConsistent は、コメント保存に伴う
// issues の UPDATE(取得結果 3 列)が FTS5 索引を壊さないことを確認する。
// issues の UPDATE は issues_fts_au トリガー(旧テキストの delete → 新テキストの
// insert)を発火させるため、検索が継続して当たることを固定しておく。
func TestReplaceIssueComments_KeepsFTSIndexConsistent(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssue(ctx, &Issue{
		ID: 101, IssueKey: "EXA-1", ProjectID: 1,
		Summary: "ログイン画面の不具合", Description: "再現手順を記載",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "コメント", Created: "2026-08-01T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: "再現手順"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].ID != 101 {
		t.Errorf("コメント保存後の検索結果 = %+v, want 課題 101 が 1 件", res.Issues)
	}
}

// TestUpsertIssues_KeepsComments は、コメント保存後に同期経路(UpsertIssues)で
// 同じ課題を上書きしても、コメント本体と取得結果の 3 列が失われないことを
// 確認する。通常の同期はコメントに一切触れないという前提を固定するもの
// (UpsertIssue の ON CONFLICT DO UPDATE に新列を足さないことで成立する)。
func TestUpsertIssues_KeepsComments(t *testing.T) {
	s := openTempStore(t)
	seedCommentFixtures(t, s)
	ctx := context.Background()

	want := CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z", Truncated: true, HistoryOnly: 3}
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, AuthorName: "山田", Content: "残るべきコメント", Created: "2026-08-01T00:00:00Z"},
	}, want); err != nil {
		t.Fatal(err)
	}

	// 同期による再取得(件名等が更新される)
	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 101, IssueKey: "EXA-1", ProjectID: 1, Summary: "更新後の件名", Updated: "2026-08-11T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "残るべきコメント" {
		t.Errorf("同期後のコメント = %+v, want 保持", got)
	}
	st, err := s.GetIssueCommentStatus(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if st != want {
		t.Errorf("同期後の取得結果 = %+v, want %+v", st, want)
	}
}
