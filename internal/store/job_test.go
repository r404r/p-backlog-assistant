package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
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

// --- 完了ジョブの保持期限(R2)---------------------------------------------

// jobCompletedAt はジョブの完了時刻(未設定なら空文字)を返す(検証用)。
func jobCompletedAt(t *testing.T, s *Store, jobID int64) string {
	t.Helper()
	var v sql.NullString
	if err := s.DB().QueryRow(`SELECT completed_at FROM jobs WHERE id = ?`, jobID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v.String
}

// setJobTimes は保持期限の検証用に、ジョブの状態と時刻を直接書き換える。
// completedAt に空文字を渡すと NULL(旧バージョンで作られたジョブ)にする。
func setJobTimes(t *testing.T, s *Store, jobID int64, status, createdAt, completedAt string) {
	t.Helper()
	var completed any
	if completedAt != "" {
		completed = completedAt
	}
	if _, err := s.DB().Exec(
		`UPDATE jobs SET status = ?, created_at = ?, completed_at = ? WHERE id = ?`,
		status, createdAt, completed, jobID); err != nil {
		t.Fatal(err)
	}
}

// setRowStatuses は保持期限の検証用に、ジョブの全行の状態を直接書き換える
// (遷移規則を経由せずに任意の状態を作る)。
func setRowStatuses(t *testing.T, s *Store, jobID int64, status string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE job_rows SET status = ? WHERE job_id = ?`, status, jobID); err != nil {
		t.Fatal(err)
	}
}

func jobExists(t *testing.T, s *Store, jobID int64) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func jobRowCount(t *testing.T, s *Store, jobID int64) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM job_rows WHERE job_id = ?`, jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestSetJobStatus_RecordsCompletedAt は終端状態(done / canceled)への遷移で
// 完了時刻を記録し、再実行(running / pending)で解除することを確認する(R2)。
// 完了時刻は保持期限の起点になるため、再実行したジョブが古い完了時刻のまま
// 消えないようにする。
func TestSetJobStatus_RecordsCompletedAt(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	id := newTestJob(t, s)

	if got := jobCompletedAt(t, s, id); got != "" {
		t.Errorf("作成直後の completed_at = %q, want 空", got)
	}
	if err := s.SetJobStatus(ctx, id, JobStatusDone); err != nil {
		t.Fatal(err)
	}
	done := jobCompletedAt(t, s, id)
	if done == "" {
		t.Fatal("done への遷移で completed_at が記録されない")
	}
	if _, err := time.Parse(time.RFC3339, done); err != nil {
		t.Errorf("completed_at = %q, want RFC3339: %v", done, err)
	}
	// 再実行したら完了時刻は解除する
	if err := s.SetJobStatus(ctx, id, JobStatusRunning); err != nil {
		t.Fatal(err)
	}
	if got := jobCompletedAt(t, s, id); got != "" {
		t.Errorf("再実行後の completed_at = %q, want 空", got)
	}
	// 中断も終端状態として記録する
	if err := s.SetJobStatus(ctx, id, JobStatusCanceled); err != nil {
		t.Fatal(err)
	}
	if jobCompletedAt(t, s, id) == "" {
		t.Error("canceled への遷移で completed_at が記録されない")
	}
}

// TestPurgeExpiredJobs は保持期限(完了から 90 日)を過ぎた完了ジョブだけを
// 削除することを確認する(R2)。
func TestPurgeExpiredJobs(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	at := func(daysAgo int) string {
		return now.AddDate(0, 0, -daysAgo).Format(time.RFC3339)
	}

	// 期限切れの完了ジョブ(削除対象)
	expired := newTestJob(t, s)
	setRowStatuses(t, s, expired, RowStatusDone)
	setJobTimes(t, s, expired, JobStatusDone, at(120), at(91))

	// 境界(完了からちょうど 90 日)は保持する
	boundary := newTestJob(t, s)
	setRowStatuses(t, s, boundary, RowStatusDone)
	setJobTimes(t, s, boundary, JobStatusDone, at(120), at(90))

	// 未完了(pending)のジョブは、どれだけ古くても消さない(再開データ)
	pending := newTestJob(t, s)
	setJobTimes(t, s, pending, JobStatusPending, at(400), "")

	// 中断済みだが未送信の行が残るジョブも消さない(再開できる)
	canceled := newTestJob(t, s)
	setRowStatuses(t, s, canceled, RowStatusPending)
	setJobTimes(t, s, canceled, JobStatusCanceled, at(400), at(300))

	// 送信済みか不明な行(sending)が残るジョブも消さない(突合に使う)
	sending := newTestJob(t, s)
	setRowStatuses(t, s, sending, RowStatusSending)
	setJobTimes(t, s, sending, JobStatusDone, at(400), at(300))

	// 完了時刻を持たない旧バージョンのジョブは作成日時で判定する
	legacy := newTestJob(t, s)
	setRowStatuses(t, s, legacy, RowStatusError)
	setJobTimes(t, s, legacy, JobStatusDone, at(200), "")

	n, err := s.PurgeExpiredJobs(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("削除件数 = %d, want 2", n)
	}
	for _, tc := range []struct {
		name string
		id   int64
		want bool
	}{
		{"期限切れの完了ジョブ", expired, false},
		{"境界(ちょうど 90 日)", boundary, true},
		{"未完了ジョブ", pending, true},
		{"未送信行が残る中断ジョブ", canceled, true},
		{"sending 行が残るジョブ", sending, true},
		{"完了時刻の無い旧ジョブ", legacy, false},
	} {
		if got := jobExists(t, s, tc.id); got != tc.want {
			t.Errorf("%s の残存 = %v, want %v", tc.name, got, tc.want)
		}
		if !tc.want && jobRowCount(t, s, tc.id) != 0 {
			t.Errorf("%s の job_rows が残っている", tc.name)
		}
	}
}

// TestOpen_PurgesExpiredJobs は DB オープン時(アプリ起動時)に
// 期限切れの完了ジョブが整理されることを確認する(R2)。
func TestOpen_PurgesExpiredJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_1.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := newTestJob(t, s)
	setRowStatuses(t, s, id, RowStatusDone)
	old := time.Now().UTC().AddDate(0, 0, -200).Format(time.RFC3339)
	setJobTimes(t, s, id, JobStatusDone, old, old)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if jobExists(t, s2, id) {
		t.Error("オープン時に期限切れの完了ジョブが整理されていない")
	}
}
