package store

import (
	"context"
	"testing"
)

func TestGetIssueByKey(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssue(ctx, &Issue{
		ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "件名", Updated: "2026-08-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetIssueByKey(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Summary != "件名" || got.Updated != "2026-08-01T00:00:00Z" {
		t.Fatalf("issue = %+v", got)
	}

	// 未登録・別プロジェクトは nil(エラーではない)
	missing, err := s.GetIssueByKey(ctx, 1, "EXA-9")
	if err != nil || missing != nil {
		t.Errorf("未登録キー = %+v, %v", missing, err)
	}
	other, err := s.GetIssueByKey(ctx, 2, "EXA-1")
	if err != nil || other != nil {
		t.Errorf("別プロジェクト = %+v, %v", other, err)
	}
}

// TestGetIssueByKey_ExcludesDeleted は論理削除済みの課題を返さないことを確認する。
func TestGetIssueByKey_ExcludesDeleted(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssue(ctx, &Issue{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "件名"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkIssueDeletedByKey(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueByKey(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("削除済み課題が返った: %+v", got)
	}
}

// TestGetIssueKeyByID は課題 ID から課題キーを 1 件だけ引けることを確認する
// (課題詳細ポップアップの親課題キー解決)。
func TestGetIssueKeyByID(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssue(ctx, &Issue{ID: 100, IssueKey: "EXA-1", ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssue(ctx, &Issue{ID: 200, IssueKey: "SUB-1", ProjectID: 2}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetIssueKeyByID(ctx, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != "EXA-1" {
		t.Errorf("課題キー = %q, want EXA-1", got)
	}

	// 未登録・別プロジェクトは空文字(エラーにしない。呼び出し側は ID:<数値> へ縮退する)
	missing, err := s.GetIssueKeyByID(ctx, 1, 999)
	if err != nil || missing != "" {
		t.Errorf("未登録 ID = %q, %v, want \"\", nil", missing, err)
	}
	other, err := s.GetIssueKeyByID(ctx, 1, 200)
	if err != nil || other != "" {
		t.Errorf("別プロジェクト = %q, %v, want \"\", nil", other, err)
	}
}

// TestGetIssueKeyByID_ExcludesDeleted は論理削除済みの課題を引き当てないことを確認する。
func TestGetIssueKeyByID_ExcludesDeleted(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssue(ctx, &Issue{ID: 100, IssueKey: "EXA-1", ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkIssueDeletedByKey(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIssueKeyByID(ctx, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("削除済み課題が引き当てられた: %q", got)
	}
}

// TestListProjectUserRefs はプロジェクト参加者のみを返すことを確認する(中 1)。
func TestListProjectUserRefs(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	seedProjectRows(t, s, 1, 2)

	if err := s.ReplaceUsers(ctx, []*User{
		{ID: 1, UserCode: "a", Name: "山田 太郎"},
		{ID: 2, UserCode: "b", Name: "山田 花子"},
		{ID: 3, UserCode: "c", Name: "別プロジェクトの人"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 1, []ProjectUser{{UserID: 2}, {UserID: 1, IsAdmin: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 2, []ProjectUser{{UserID: 3}}); err != nil {
		t.Fatal(err)
	}

	refs, err := s.ListProjectUserRefs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("件数 = %d, want 2", len(refs))
	}
	if refs[0].ID != 1 || refs[1].ID != 2 {
		t.Errorf("refs = %+v(ID 昇順を期待)", refs)
	}
	// 参加者のいないプロジェクトは 0 件(呼び出し側でフォールバックする)
	none, err := s.ListProjectUserRefs(ctx, 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("未参加プロジェクト = %+v, want 0 件", none)
	}
}

func TestListUserRefs(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.ReplaceUsers(ctx, []*User{
		{ID: 2, UserCode: "b", Name: "山田 花子"},
		{ID: 1, UserCode: "a", Name: "山田 太郎"},
	}); err != nil {
		t.Fatal(err)
	}
	refs, err := s.ListUserRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("件数 = %d, want 2", len(refs))
	}
	if refs[0].ID != 1 || refs[0].Name != "山田 太郎" || refs[0].UserCode != "a" {
		t.Errorf("refs[0] = %+v", refs[0])
	}
}
