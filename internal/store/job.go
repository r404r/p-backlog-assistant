package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// 一括更新・追加ジョブ(jobs / job_rows)のリポジトリ。
// 実行状態を永続化し、中断・再開を可能にする(設計書 5 節)。
//
// 再開安全性の要は「sending の記録 → API 送信 → 結果の記録」の順序と、
// 「再開時に sending 行を自動再送しない」ことの 2 点。
// 送信前に sending を記録しておけば、送信直後にクラッシュしても
// 「送信済みかもしれない行」を区別でき、二重作成を避けられる。

// ジョブ種別(jobs.kind)。1 ファイルに追加行と更新行が混在しうるため
// mixed を設ける(設計書 2 節のコメントは update / create のみだが、
// テンプレートは issueKey 有無で行ごとに決まる)。
const (
	JobKindCreate = "create"
	JobKindUpdate = "update"
	JobKindMixed  = "mixed"
)

// ジョブ状態(jobs.status)。
const (
	JobStatusPending  = "pending"  // 取り込み済み・未実行
	JobStatusRunning  = "running"  // 実行中
	JobStatusDone     = "done"     // 実行完了(失敗行を含みうる)
	JobStatusCanceled = "canceled" // ユーザ操作で中断
)

// 行状態(job_rows.status)。
// skip は「変更が 1 つも無い行」(dry-run で差分ゼロ)を表し、送信対象にしない。
const (
	RowStatusPending  = "pending"
	RowStatusSending  = "sending"
	RowStatusDone     = "done"
	RowStatusError    = "error"
	RowStatusConflict = "conflict"
	RowStatusSkip     = "skip"
)

// Job は 1 つの一括更新・追加ジョブ。
type Job struct {
	ID         int64  `json:"jobId"`
	Kind       string `json:"kind"`
	ProjectID  int64  `json:"projectId"`
	SourceFile string `json:"sourceFile"` // ファイル名のみ(パスは保存しない)
	SourceHash string `json:"sourceHash"` // 入力ファイルの SHA-256(再開時の同一性確認)
	CreatedAt  string `json:"createdAt"`
	Status     string `json:"status"`
}

// JobRow はジョブの 1 行。
type JobRow struct {
	JobID         int64  `json:"jobId"`
	RowNo         int    `json:"rowNo"`       // Excel の行番号(ヘッダが 1 行目)
	IssueKey      string `json:"issueKey"`    // 新規追加行は空
	Payload       string `json:"payload"`     // 送信内容(JSON)
	BaseUpdated   string `json:"baseUpdated"` // Excel 出力時点の updated(競合検知の基準)
	Status        string `json:"status"`
	ResultIssueID int64  `json:"resultIssueId"`
	Error         string `json:"error"`
}

// JobSummary はジョブ一覧の 1 行(行数集計付き)。
type JobSummary struct {
	Job
	Total    int `json:"total"`
	Done     int `json:"done"`
	Failed   int `json:"failed"`
	Pending  int `json:"pending"`
	Sending  int `json:"sending"`
	Conflict int `json:"conflict"`
	Skipped  int `json:"skipped"`
}

// validJobStatuses / validRowStatuses は許可する状態値。
var validJobStatuses = map[string]bool{
	JobStatusPending: true, JobStatusRunning: true,
	JobStatusDone: true, JobStatusCanceled: true,
}

var validRowStatuses = map[string]bool{
	RowStatusPending: true, RowStatusSending: true, RowStatusDone: true,
	RowStatusError: true, RowStatusConflict: true, RowStatusSkip: true,
}

// allowedRowTransitions は行状態の遷移規則。
// done / skip は終了状態で、そこから sending へ戻すことは許さない
// (完了済みの行を再送して二重作成することを防ぐ)。
var allowedRowTransitions = map[string]map[string]bool{
	RowStatusPending: {
		RowStatusSending: true, RowStatusConflict: true,
		RowStatusError: true, RowStatusSkip: true,
	},
	RowStatusSending: {
		RowStatusDone: true, RowStatusError: true,
		RowStatusConflict: true, RowStatusSending: true, // 明示的な再送指示
	},
	RowStatusError: {
		RowStatusSending: true, RowStatusPending: true, RowStatusSkip: true,
	},
	RowStatusConflict: {
		RowStatusSending: true, RowStatusPending: true, RowStatusSkip: true,
	},
	RowStatusDone: {},
	RowStatusSkip: {},
}

// CreateJob はジョブと行をまとめて作成し、ジョブ ID を返す。
// 行が 1 つも無いジョブは作らない。
//
// sourceFile はファイル名のみを保存する(利用者のディレクトリ構成を
// DB に残さない。設計書 7 節)。行の状態が未指定なら pending にする。
func (s *Store) CreateJob(ctx context.Context, kind string, projectID int64, sourceFile, sourceHash string, rows []JobRow) (int64, error) {
	if len(rows) == 0 {
		return 0, errors.New("取り込む行がありません")
	}
	switch kind {
	case JobKindCreate, JobKindUpdate, JobKindMixed:
	default:
		return 0, fmt.Errorf("不明なジョブ種別です: %s", kind)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	var jobID int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO jobs (kind, project_id, source_file, source_hash, created_at, status)
			VALUES (?, ?, ?, ?, ?, ?)`,
			kind, projectID, filepath.Base(sourceFile), sourceHash, createdAt, JobStatusPending)
		if err != nil {
			return err
		}
		jobID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		for _, r := range rows {
			status := r.Status
			if status == "" {
				status = RowStatusPending
			}
			if !validRowStatuses[status] {
				return fmt.Errorf("不明な行状態です: %s", status)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
				VALUES (?, ?, ?, ?, ?, ?, 0, '')`,
				jobID, r.RowNo, r.IssueKey, r.Payload, r.BaseUpdated, status); err != nil {
				return fmt.Errorf("行 %d の保存に失敗しました: %w", r.RowNo, err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return jobID, nil
}

// GetJob はジョブ 1 件を返す(存在しなければエラー)。
func GetJob(ctx context.Context, q dbtx, jobID int64) (*Job, error) {
	var j Job
	err := q.QueryRowContext(ctx, `
		SELECT id, kind, project_id, source_file, source_hash, created_at, status
		FROM jobs WHERE id = ?`, jobID).
		Scan(&j.ID, &j.Kind, &j.ProjectID, &j.SourceFile, &j.SourceHash, &j.CreatedAt, &j.Status)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("ジョブが見つかりません(ID %d)", jobID)
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// GetJob は Store 直接実行版。
func (s *Store) GetJob(ctx context.Context, jobID int64) (*Job, error) {
	return GetJob(ctx, s.db, jobID)
}

// jobRowColumns は JobRow の SELECT 列。
const jobRowColumns = `job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error`

func scanJobRows(ctx context.Context, q dbtx, query string, args ...any) ([]JobRow, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobRow{}
	for rows.Next() {
		var r JobRow
		if err := rows.Scan(&r.JobID, &r.RowNo, &r.IssueKey, &r.Payload,
			&r.BaseUpdated, &r.Status, &r.ResultIssueID, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListJobRows はジョブの全行を行番号順で返す。
func ListJobRows(ctx context.Context, q dbtx, jobID int64) ([]JobRow, error) {
	return scanJobRows(ctx, q,
		`SELECT `+jobRowColumns+` FROM job_rows WHERE job_id = ? ORDER BY row_no`, jobID)
}

// ListJobRows は Store 直接実行版。
func (s *Store) ListJobRows(ctx context.Context, jobID int64) ([]JobRow, error) {
	return ListJobRows(ctx, s.db, jobID)
}

// ListJobRowsByStatus は指定状態の行を行番号順で返す
// (競合行の強制再実行など、状態を絞った再処理に使う)。
func ListJobRowsByStatus(ctx context.Context, q dbtx, jobID int64, status string) ([]JobRow, error) {
	if !validRowStatuses[status] {
		return nil, fmt.Errorf("不明な行状態です: %s", status)
	}
	return scanJobRows(ctx, q,
		`SELECT `+jobRowColumns+` FROM job_rows WHERE job_id = ? AND status = ? ORDER BY row_no`,
		jobID, status)
}

// ListJobRowsByStatus は Store 直接実行版。
func (s *Store) ListJobRowsByStatus(ctx context.Context, jobID int64, status string) ([]JobRow, error) {
	return ListJobRowsByStatus(ctx, s.db, jobID, status)
}

// ResumeTargets は再開対象を pending 行と sending 行に分けて返す。
//
// sending 行は「送信済みかどうかが不明」な行であり、自動再送してはならない
// (設計書 5 節。二重作成防止)。呼び出し側が明示的な再送指示を受けた場合のみ
// 処理する。
func ResumeTargets(ctx context.Context, q dbtx, jobID int64) (pending, sending []JobRow, err error) {
	pending, err = scanJobRows(ctx, q,
		`SELECT `+jobRowColumns+` FROM job_rows WHERE job_id = ? AND status = ? ORDER BY row_no`,
		jobID, RowStatusPending)
	if err != nil {
		return nil, nil, err
	}
	sending, err = scanJobRows(ctx, q,
		`SELECT `+jobRowColumns+` FROM job_rows WHERE job_id = ? AND status = ? ORDER BY row_no`,
		jobID, RowStatusSending)
	if err != nil {
		return nil, nil, err
	}
	return pending, sending, nil
}

// ResumeTargets は Store 直接実行版(2 クエリを 1 スナップショットで揃える)。
func (s *Store) ResumeTargets(ctx context.Context, jobID int64) (pending, sending []JobRow, err error) {
	err = s.WithReadTx(ctx, func(tx *sql.Tx) error {
		var terr error
		pending, sending, terr = ResumeTargets(ctx, tx, jobID)
		return terr
	})
	if err != nil {
		return nil, nil, err
	}
	return pending, sending, nil
}

// UpdateRowStatus は行の状態を遷移させる。
// status が done のときは resultIssueID(新規追加で作成された課題 ID)、
// error のときは errMsg を併せて記録する。
//
// 遷移規則(allowedRowTransitions)に反する更新はエラーにする。
// 特に done → sending を許すと、完了済みの新規追加行を再送して
// 課題を二重作成しうる。
func UpdateRowStatus(ctx context.Context, q dbtx, jobID int64, rowNo int, status string, resultIssueID int64, errMsg string) error {
	if !validRowStatuses[status] {
		return fmt.Errorf("不明な行状態です: %s", status)
	}
	var current string
	err := q.QueryRowContext(ctx,
		`SELECT status FROM job_rows WHERE job_id = ? AND row_no = ?`, jobID, rowNo).Scan(&current)
	if err == sql.ErrNoRows {
		return fmt.Errorf("ジョブ %d に行 %d がありません", jobID, rowNo)
	}
	if err != nil {
		return err
	}
	if !allowedRowTransitions[current][status] {
		return fmt.Errorf("行 %d の状態遷移が不正です(%s → %s)", rowNo, current, status)
	}
	// result_issue_id は done のときのみ更新し、それ以外では既存値を保つ
	// (再実行で結果が消えないようにする)。
	if status == RowStatusDone && resultIssueID > 0 {
		_, err = q.ExecContext(ctx,
			`UPDATE job_rows SET status = ?, result_issue_id = ?, error = ? WHERE job_id = ? AND row_no = ?`,
			status, resultIssueID, errMsg, jobID, rowNo)
		return err
	}
	_, err = q.ExecContext(ctx,
		`UPDATE job_rows SET status = ?, error = ? WHERE job_id = ? AND row_no = ?`,
		status, errMsg, jobID, rowNo)
	return err
}

// UpdateRowStatus は Store 直接実行版(状態確認と更新を 1 トランザクションで行う)。
func (s *Store) UpdateRowStatus(ctx context.Context, jobID int64, rowNo int, status string, resultIssueID int64, errMsg string) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return UpdateRowStatus(ctx, tx, jobID, rowNo, status, resultIssueID, errMsg)
	})
}

// SetJobStatus はジョブ状態を更新する。
func SetJobStatus(ctx context.Context, q dbtx, jobID int64, status string) error {
	if !validJobStatuses[status] {
		return fmt.Errorf("不明なジョブ状態です: %s", status)
	}
	res, err := q.ExecContext(ctx, `UPDATE jobs SET status = ? WHERE id = ?`, status, jobID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("ジョブが見つかりません(ID %d)", jobID)
	}
	return nil
}

// SetJobStatus は Store 直接実行版。
func (s *Store) SetJobStatus(ctx context.Context, jobID int64, status string) error {
	return SetJobStatus(ctx, s.db, jobID, status)
}

// ListJobs はジョブ一覧を新しい順(ID 降順)で返す(行数集計付き)。
func ListJobs(ctx context.Context, q dbtx) ([]JobSummary, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT j.id, j.kind, j.project_id, j.source_file, j.source_hash, j.created_at, j.status,
			COUNT(r.row_no),
			COALESCE(SUM(CASE WHEN r.status = 'done' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.status = 'error' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.status = 'pending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.status = 'sending' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.status = 'conflict' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN r.status = 'skip' THEN 1 ELSE 0 END), 0)
		FROM jobs j LEFT JOIN job_rows r ON r.job_id = j.id
		GROUP BY j.id ORDER BY j.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobSummary{}
	for rows.Next() {
		var s JobSummary
		if err := rows.Scan(&s.ID, &s.Kind, &s.ProjectID, &s.SourceFile, &s.SourceHash,
			&s.CreatedAt, &s.Status,
			&s.Total, &s.Done, &s.Failed, &s.Pending, &s.Sending, &s.Conflict, &s.Skipped); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListJobs は Store 直接実行版。
func (s *Store) ListJobs(ctx context.Context) ([]JobSummary, error) {
	return ListJobs(ctx, s.db)
}

// GetJobSummary は 1 ジョブの集計を返す。
func (s *Store) GetJobSummary(ctx context.Context, jobID int64) (*JobSummary, error) {
	var summary *JobSummary
	err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		job, err := GetJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		rows, err := ListJobRows(ctx, tx, jobID)
		if err != nil {
			return err
		}
		sum := JobSummary{Job: *job, Total: len(rows)}
		for _, r := range rows {
			switch r.Status {
			case RowStatusDone:
				sum.Done++
			case RowStatusError:
				sum.Failed++
			case RowStatusPending:
				sum.Pending++
			case RowStatusSending:
				sum.Sending++
			case RowStatusConflict:
				sum.Conflict++
			case RowStatusSkip:
				sum.Skipped++
			}
		}
		summary = &sum
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summary, nil
}
