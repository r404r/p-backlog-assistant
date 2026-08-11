package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/store"
)

// bulkHeaders は一括更新テンプレートの列(設計書 5 節)。
var bulkHeaders = []string{
	"issueKey", "件名", "種別ID", "種別名", "状態ID", "状態名",
	"優先度ID", "優先度名", "担当者ID", "担当者名", "期限", "詳細", "base_updated",
}

// newBulkTestService は課題を同期済みのプロファイルを用意する。
func newBulkTestService(t *testing.T) (*ProfileService, string, *fakeConnector) {
	t.Helper()
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "ログイン不具合", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		issueTypes: []backlogclient.IssueType{{ID: 11, Name: "タスク", ProjectID: 1}},
		priorities: []backlogclient.Priority{{ID: 2, Name: "高"}, {ID: 3, Name: "中"}},
		statuses:   []backlogclient.Status{{ID: 1, Name: "未対応"}, {ID: 2, Name: "処理中"}},
	}
	s, id := newSyncTestService(t, fake)
	if _, err := s.SyncIssues(context.Background(), id, 1, "full"); err != nil {
		t.Fatal(err)
	}
	return s, id, fake
}

// writeBulkXLSX は取り込み用の xlsx を作り、そのパスを返す。
func writeBulkXLSX(t *testing.T, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for i, h := range bulkHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellStr(sheet, cell, h); err != nil {
			t.Fatal(err)
		}
	}
	for r, row := range rows {
		for i, v := range row {
			cell, _ := excelize.CoordinatesToCellName(i+1, r+2)
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGetMasterData(t *testing.T) {
	s, id, _ := newBulkTestService(t)

	master, err := s.GetMasterData(context.Background(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(master.IssueTypes) != 1 || master.IssueTypes[0].Name != "タスク" {
		t.Errorf("種別 = %+v", master.IssueTypes)
	}
	if len(master.Priorities) != 2 || len(master.Statuses) != 2 {
		t.Errorf("master = %+v", master)
	}
}

// TestListAssigneeCandidates はテンプレートの担当者候補が
// プロジェクト参加者(未同期ならスペース全体)になることを確認する。
func TestListAssigneeCandidates(t *testing.T) {
	s, id, _ := newBulkTestService(t)
	ctx := context.Background()
	st, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceUsers(ctx, []*store.User{
		{ID: 501, UserCode: "taro", Name: "山田 太郎"},
		{ID: 502, UserCode: "hanako", Name: "山田 花子"},
	}); err != nil {
		t.Fatal(err)
	}

	// 参加者が未同期のうちはスペース全体へ縮退する
	all, err := s.ListAssigneeCandidates(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("候補 = %+v, want 2 件", all)
	}

	// 参加者が同期済みならプロジェクト参加者に限定する
	if err := st.ReplaceProjectUsers(ctx, 1, []store.ProjectUser{{UserID: 501}}); err != nil {
		t.Fatal(err)
	}
	members, err := s.ListAssigneeCandidates(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != 501 {
		t.Errorf("候補 = %+v, want [501]", members)
	}
}

func TestImportAndRunBulkJob(t *testing.T) {
	s, id, fake := newBulkTestService(t)
	ctx := context.Background()
	path := writeBulkXLSX(t, [][]string{
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})

	res, err := s.ImportBulkFile(ctx, id, 1, path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.JobID == 0 {
		t.Fatalf("取り込み結果 = %+v", res)
	}
	if res.Creates != 1 || res.Updates != 1 || res.TotalRows != 2 {
		t.Errorf("集計 = %+v", res)
	}
	if len(res.Previews) != 2 {
		t.Errorf("プレビュー = %+v", res.Previews)
	}

	var last bulk.Progress
	runRes, err := s.RunBulkJob(ctx, id, res.JobID, bulk.RunOptions{}, func(p bulk.Progress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if runRes.Done != 2 || runRes.Failed != 0 {
		t.Errorf("実行結果 = %+v", runRes)
	}
	if last.Processed != 2 || last.Total != 2 {
		t.Errorf("進捗 = %+v", last)
	}
	if len(fake.created) != 1 || len(fake.updated) != 1 || fake.updated[0] != "EXA-1" {
		t.Errorf("API 呼び出し = %+v / %v", fake.created, fake.updated)
	}

	jobs, err := s.ListBulkJobs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ジョブ数 = %d, want 1", len(jobs))
	}
	if jobs[0].Total != 2 || jobs[0].Done != 2 || jobs[0].Status != store.JobStatusDone {
		t.Errorf("ジョブ = %+v", jobs[0])
	}
	if jobs[0].ProjectID != 1 || jobs[0].CreatedAt == "" {
		t.Errorf("ジョブ = %+v", jobs[0])
	}
}

// TestCancelBulkRun は実行前のキャンセル指示が反映されることを確認する。
func TestCancelBulkRun(t *testing.T) {
	s, id, fake := newBulkTestService(t)
	ctx := context.Background()
	path := writeBulkXLSX(t, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})

	res, err := s.ImportBulkFile(ctx, id, 1, path, 3)
	if err != nil {
		t.Fatal(err)
	}
	s.CancelBulkRun(id, res.JobID)

	runRes, err := s.RunBulkJob(ctx, id, res.JobID, bulk.RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runRes.Done != 0 {
		t.Errorf("キャンセル指示後に実行された: %+v", runRes)
	}
	if len(fake.created) != 0 {
		t.Errorf("キャンセル指示後に送信された: %+v", fake.created)
	}

	// キャンセル指示は 1 回の実行で消費される(次の実行は通常どおり動く)
	runRes2, err := s.RunBulkJob(ctx, id, res.JobID, bulk.RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runRes2.Done != 1 {
		t.Errorf("再実行結果 = %+v", runRes2)
	}
}

// TestCancelBulkRun_OtherProfileIsNotAffected は別プロファイルへのキャンセル指示で
// 実行が中断されないことを確認する(中 2。ジョブ ID はプロファイルごとの採番)。
func TestCancelBulkRun_OtherProfileIsNotAffected(t *testing.T) {
	s, id, fake := newBulkTestService(t)
	ctx := context.Background()
	path := writeBulkXLSX(t, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})

	res, err := s.ImportBulkFile(ctx, id, 1, path, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 同じジョブ ID を別プロファイル ID で中断指示しても影響しない
	s.CancelBulkRun("other-profile", res.JobID)

	runRes, err := s.RunBulkJob(ctx, id, res.JobID, bulk.RunOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runRes.Done != 1 {
		t.Errorf("別プロファイルの中断指示で実行が止まった: %+v", runRes)
	}
	if len(fake.created) != 1 {
		t.Errorf("送信されなかった: %+v", fake.created)
	}
}

// TestImportBulkFile_InvalidRowsReturnErrors は検証エラーをそのまま返し、
// ジョブを作らないことを確認する。
func TestImportBulkFile_InvalidRowsReturnErrors(t *testing.T) {
	s, id, _ := newBulkTestService(t)
	path := writeBulkXLSX(t, [][]string{
		{"EXA-99", "件名", "", "", "", "", "", "", "", "", "", "", ""},
	})

	res, err := s.ImportBulkFile(context.Background(), id, 1, path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Valid || len(res.Errors) != 1 || res.JobID != 0 {
		t.Errorf("結果 = %+v", res)
	}
	if res.Errors[0].RowNo != 2 {
		t.Errorf("エラー = %+v", res.Errors[0])
	}

	jobs, err := s.ListBulkJobs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("エラー入力でジョブが作られた: %+v", jobs)
	}
}

// TestRunBulkJob_UnknownProfile は未知プロファイルでエラーになることを確認する。
func TestRunBulkJob_UnknownProfile(t *testing.T) {
	s, _, _ := newBulkTestService(t)
	if _, err := s.RunBulkJob(context.Background(), "unknown", 1, bulk.RunOptions{}, nil); err == nil {
		t.Fatal("未知プロファイルでエラーにならなかった")
	}
}
