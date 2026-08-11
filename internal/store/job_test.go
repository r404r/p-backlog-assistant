package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestJob は検証用のジョブ(2 行)を作成し、ジョブ ID を返す。
func newTestJob(t *testing.T, s *Store) int64 {
	t.Helper()
	rows := []JobRow{
		{RowNo: 2, IssueKey: "EXA-1", Payload: `{"action":"update"}`, BaseUpdated: "2026-08-01T00:00:00Z"},
		{RowNo: 3, Payload: `{"action":"create"}`},
	}
	id, err := s.CreateJob(context.Background(), JobKindMixed, 1,
		filepath.Join("home", "user", "bulk.xlsx"), "hash-1", rows)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateJob_StoresRowsAndFileNameOnly(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	job, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// 入力ファイルはファイル名のみ保存する(利用者の環境情報を DB に残さない)
	if job.SourceFile != "bulk.xlsx" {
		t.Errorf("SourceFile = %q, want bulk.xlsx", job.SourceFile)
	}
	if job.Kind != JobKindMixed || job.ProjectID != 1 || job.SourceHash != "hash-1" {
		t.Errorf("job = %+v", job)
	}
	if job.Status != JobStatusPending || job.CreatedAt == "" {
		t.Errorf("job = %+v", job)
	}

	rows, err := s.ListJobRows(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("行数 = %d, want 2", len(rows))
	}
	if rows[0].RowNo != 2 || rows[0].IssueKey != "EXA-1" || rows[0].Status != RowStatusPending {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if rows[0].BaseUpdated != "2026-08-01T00:00:00Z" || rows[0].Payload != `{"action":"update"}` {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if rows[1].IssueKey != "" {
		t.Errorf("新規追加行の issueKey = %q, want 空", rows[1].IssueKey)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	s := openTempStore(t)
	if _, err := s.GetJob(context.Background(), 999); err == nil {
		t.Fatal("存在しないジョブでエラーにならなかった")
	}
}

func TestUpdateRowStatus_RecordsResultAndError(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	// pending → sending → done(新規追加は作成された課題 ID を保存する)
	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusDone, 777, ""); err != nil {
		t.Fatal(err)
	}
	// pending → sending → error
	if err := s.UpdateRowStatus(ctx, id, 2, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, id, 2, RowStatusError, 0, "API エラー"); err != nil {
		t.Fatal(err)
	}

	rows, err := s.ListJobRows(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Status != RowStatusError || rows[0].Error != "API エラー" {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if rows[1].Status != RowStatusDone || rows[1].ResultIssueID != 777 {
		t.Errorf("rows[1] = %+v", rows[1])
	}
}

// TestUpdateRowStatus_RejectsInvalidTransition は終了済みの行を
// 再送状態へ戻さないことを確認する(二重作成防止)。
func TestUpdateRowStatus_RejectsInvalidTransition(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusDone, 777, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusSending, 0, ""); err == nil {
		t.Fatal("done → sending が許可された")
	}
	if err := s.UpdateRowStatus(ctx, id, 3, "unknown", 0, ""); err == nil {
		t.Fatal("未知の状態が許可された")
	}
	if err := s.UpdateRowStatus(ctx, id, 99, RowStatusSending, 0, ""); err == nil {
		t.Fatal("存在しない行が許可された")
	}
}

// TestResumeTargets は pending 行と sending 行を区別して返すことを確認する。
// sending 行は再開時に自動再送してはならない(設計書 5 節)。
func TestResumeTargets(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}

	pending, sending, err := s.ResumeTargets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].RowNo != 2 {
		t.Errorf("pending = %+v", pending)
	}
	if len(sending) != 1 || sending[0].RowNo != 3 {
		t.Errorf("sending = %+v", sending)
	}

	// 完了した行はどちらにも含まれない
	if err := s.UpdateRowStatus(ctx, id, 3, RowStatusDone, 1, ""); err != nil {
		t.Fatal(err)
	}
	pending, sending, err = s.ResumeTargets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || len(sending) != 0 {
		t.Errorf("pending = %+v / sending = %+v", pending, sending)
	}
}

func TestListJobs_NewestFirstWithCounts(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	first := newTestJob(t, s)
	second, err := s.CreateJob(ctx, JobKindCreate, 2, "second.xlsx", "hash-2", []JobRow{
		{RowNo: 2, Payload: `{"action":"create"}`},
		{RowNo: 3, Payload: `{"action":"create"}`},
		{RowNo: 4, Payload: `{"action":"create"}`, Status: RowStatusSkip},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, second, 2, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, second, 2, RowStatusDone, 10, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRowStatus(ctx, second, 3, RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}

	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("件数 = %d, want 2", len(jobs))
	}
	// 新しい順
	if jobs[0].ID != second || jobs[1].ID != first {
		t.Errorf("並び順 = %d, %d(want %d, %d)", jobs[0].ID, jobs[1].ID, second, first)
	}
	got := jobs[0]
	if got.Total != 3 || got.Done != 1 || got.Sending != 1 || got.Pending != 0 || got.Skipped != 1 {
		t.Errorf("集計 = %+v", got)
	}
	if jobs[1].Pending != 2 || jobs[1].Total != 2 {
		t.Errorf("集計 = %+v", jobs[1])
	}
}

func TestSetJobStatus(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	if err := s.SetJobStatus(ctx, id, JobStatusRunning); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusRunning {
		t.Errorf("Status = %q", job.Status)
	}
	if err := s.SetJobStatus(ctx, id, "unknown"); err == nil {
		t.Fatal("未知のジョブ状態が許可された")
	}
}

// TestCreateJob_RejectsEmptyRows は行が 1 つも無いジョブを作らないことを確認する。
func TestCreateJob_RejectsEmptyRows(t *testing.T) {
	s := openTempStore(t)
	if _, err := s.CreateJob(context.Background(), JobKindUpdate, 1, "a.xlsx", "h", nil); err == nil {
		t.Fatal("空のジョブが作成された")
	}
}
