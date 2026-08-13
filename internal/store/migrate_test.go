package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// createLegacyDB は指定バージョンまでのマイグレーションだけを適用した DB を作る
// (旧バージョンのアプリが作った DB の再現)。
// 呼び出し側は返した *sql.DB を Close してから store.Open で開き直すこと
// (接続を残したままだと WAL のロックで開けない場合がある)。
//
// 生の接続なので PRAGMA foreign_keys は既定の OFF。v4 で導入する制約に
// 違反する行(孤児行)を意図的に仕込める。
func createLegacyDB(t *testing.T, path string, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, stmts := range migrations[:version] {
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.Itoa(version)); err != nil {
		t.Fatal(err)
	}
	return db
}

// mustExec はテスト用の直接 SQL 実行。
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// seedV3Reference は v3 DB に親行(projects / teams / users)を入れる。
func seedV3Reference(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO projects (id, project_key, name, archived, raw_json, fetched_at)
		VALUES (1, 'EXA', '検証用 A', 0, '{}', '2026-08-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO teams (id, name, raw_json, fetched_at)
		VALUES (10, '開発チーム', '{}', '2026-08-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO users (id, user_code, name, mail, role_type, raw_json, fetched_at)
		VALUES (100, 'taro', '山田 太郎', '', 2, '{}', '2026-08-01T00:00:00Z')`)
}

// TestMigrate_V3ToV4_RemovesOrphansAndKeepsValidRows は v3 の DB に残っている
// 孤児行(親が存在しない team_members / project_users / job_rows)が v4 移行で
// 取り除かれ、正常な行は完全に保持されることを確認する(R8)。
//
// 孤児行を移行時に「削除」するのは、FK 制約を後付けする以上どこかで捨てるしかなく、
// 参照先が失われた所属関係・ジョブ行は UI からも辿れない死にデータであるため。
func TestMigrate_V3ToV4_RemovesOrphansAndKeepsValidRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")
	db := createLegacyDB(t, path, 3)
	seedV3Reference(t, db)

	// 正常な行(移行後も 1 件ずつ残る)
	mustExec(t, db, `INSERT INTO team_members (team_id, user_id) VALUES (10, 100)`)
	mustExec(t, db, `INSERT INTO project_users (project_id, user_id, is_admin) VALUES (1, 100, 1)`)
	mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
		VALUES (5, 'update', 1, 'bulk.xlsx', 'hash-1', '2026-08-01T00:00:00Z', 'pending')`)
	mustExec(t, db, `INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
		VALUES (5, 2, 'EXA-1', '{"summary":"件名"}', '2026-08-01T00:00:00Z', 'pending', 0, '')`)

	// 孤児行(親が存在しない)
	mustExec(t, db, `INSERT INTO team_members (team_id, user_id) VALUES (999, 100)`)
	mustExec(t, db, `INSERT INTO project_users (project_id, user_id, is_admin) VALUES (999, 100, 0)`)
	mustExec(t, db, `INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
		VALUES (999, 1, 'EXZ-1', '{}', '', 'pending', 0, '')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("v3 → v4 の移行に失敗: %v", err)
	}
	defer s.Close()

	if v, err := s.SchemaVersion(); err != nil || v != LatestSchemaVersion() {
		t.Fatalf("移行後の schema_version = %d(err %v), want %d", v, err, LatestSchemaVersion())
	}
	for _, tc := range []struct{ table string }{
		{"team_members"}, {"project_users"}, {"job_rows"},
	} {
		if n := countRows(t, s, tc.table); n != 1 {
			t.Errorf("%s の件数 = %d, want 1(孤児行のみ除去)", tc.table, n)
		}
	}
	// 正常な行の内容が保持されていること
	var teamID, userID int64
	if err := s.DB().QueryRow(`SELECT team_id, user_id FROM team_members`).Scan(&teamID, &userID); err != nil {
		t.Fatal(err)
	}
	if teamID != 10 || userID != 100 {
		t.Errorf("team_members = (%d, %d), want (10, 100)", teamID, userID)
	}
	pus, err := listProjectUsers(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pus) != 1 || pus[0].UserID != 100 || !pus[0].IsAdmin {
		t.Errorf("project_users = %+v, want [{100 true}]", pus)
	}
	rows, err := s.ListJobRows(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RowNo != 2 || rows[0].IssueKey != "EXA-1" ||
		rows[0].Payload != `{"summary":"件名"}` || rows[0].BaseUpdated != "2026-08-01T00:00:00Z" ||
		rows[0].Status != RowStatusPending {
		t.Errorf("job_rows = %+v", rows)
	}
	job, err := s.GetJob(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != JobKindUpdate || job.ProjectID != 1 || job.SourceFile != "bulk.xlsx" ||
		job.SourceHash != "hash-1" || job.CreatedAt != "2026-08-01T00:00:00Z" || job.Status != JobStatusPending {
		t.Errorf("jobs = %+v", job)
	}
}

// TestMigrate_V3ToV4_KeepsJobIDSequence は jobs テーブルの再作成後も
// 採番が続き、削除済みジョブの ID を再利用しないことを確認する
// (AUTOINCREMENT の意味を移行で失わない)。
func TestMigrate_V3ToV4_KeepsJobIDSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")
	db := createLegacyDB(t, path, 3)
	seedV3Reference(t, db)
	mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
		VALUES (7, 'update', 1, 'bulk.xlsx', 'hash-1', '2026-08-01T00:00:00Z', 'done')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.CreateJob(context.Background(), JobKindUpdate, 1, "next.xlsx", "hash-2",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 7 {
		t.Errorf("移行後に採番されたジョブ ID = %d, want > 7", id)
	}
}

// TestMigrate_V3ToV4_CoercesUnknownStatus は、旧 DB に想定外の状態値が
// 入っていても移行が失敗しないこと(既知の値へ丸めること)を確認する。
// 移行に失敗すると DB ごと開けなくなるため、CHECK 制約の導入で
// アプリが起動不能になることを避ける。
func TestMigrate_V3ToV4_CoercesUnknownStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")
	db := createLegacyDB(t, path, 3)
	seedV3Reference(t, db)
	mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
		VALUES (1, 'bogus-kind', 1, 'bulk.xlsx', 'h', '2026-08-01T00:00:00Z', 'bogus-status')`)
	mustExec(t, db, `INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
		VALUES (1, 2, NULL, NULL, NULL, 'bogus-row-status', NULL, NULL)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("想定外の状態値がある DB を開けなくなった: %v", err)
	}
	defer s.Close()

	job, err := s.GetJob(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !validJobStatuses[job.Status] || !validJobKinds[job.Kind] {
		t.Errorf("ジョブ = %+v, want 既知の kind / status へ丸め", job)
	}
	rows, err := s.ListJobRows(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !validRowStatuses[rows[0].Status] {
		t.Errorf("行 = %+v, want 既知の status へ丸め", rows)
	}
	// NOT NULL 化した列は空文字・0 で埋まる
	if rows[0].IssueKey != "" || rows[0].Payload != "" || rows[0].BaseUpdated != "" ||
		rows[0].ResultIssueID != 0 || rows[0].Error != "" {
		t.Errorf("NULL 列の補完結果 = %+v", rows[0])
	}
}

// TestV4_RejectsOrphanRows は v4 の FK 制約が孤児行の作成を拒むことを確認する。
func TestV4_RejectsOrphanRows(t *testing.T) {
	s := openTempStore(t)
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"team_members", `INSERT INTO team_members (team_id, user_id) VALUES (999, 1)`},
		{"project_users", `INSERT INTO project_users (project_id, user_id, is_admin) VALUES (999, 1, 0)`},
		{"job_rows", `INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
			VALUES (999, 1, '', '', '', 'pending', 0, '')`},
	} {
		if _, err := s.DB().Exec(tc.query); err == nil {
			t.Errorf("%s に孤児行を挿入できてしまった", tc.name)
		}
	}
}

// TestV4_CascadeDeletesChildren は親行の削除で子行が消えることを確認する。
func TestV4_CascadeDeletesChildren(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*Team{{ID: 10, Name: "開発チーム", MemberIDs: []int64{100}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 1, []ProjectUser{{UserID: 100}}); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateJob(ctx, JobKindUpdate, 1, "bulk.xlsx", "h",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: "{}"}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB().Exec(`DELETE FROM teams WHERE id = 10`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "team_members"); n != 0 {
		t.Errorf("チーム削除後の team_members = %d, want 0", n)
	}
	if _, err := s.DB().Exec(`DELETE FROM jobs WHERE id = ?`, jobID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "job_rows"); n != 0 {
		t.Errorf("ジョブ削除後の job_rows = %d, want 0", n)
	}
	if _, err := s.DB().Exec(`DELETE FROM projects WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "project_users"); n != 0 {
		t.Errorf("プロジェクト削除後の project_users = %d, want 0", n)
	}
}

// TestV4_CheckConstraintsMatchGoConstants は CHECK 制約の許容値が Go 側の
// 定数と一致していることを確認する。マイグレーション SQL には値を直書きする
// (過去のマイグレーションは変更しない規約)ため、定数を増やしたときに
// ここで食い違いを検出する。
func TestV4_CheckConstraintsMatchGoConstants(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA"}); err != nil {
		t.Fatal(err)
	}

	for kind := range validJobKinds {
		for status := range validJobStatuses {
			if _, err := s.DB().Exec(`INSERT INTO jobs (kind, project_id, source_file, source_hash, created_at, status)
				VALUES (?, 1, 'bulk.xlsx', 'h', '2026-08-01T00:00:00Z', ?)`, kind, status); err != nil {
				t.Errorf("kind=%s status=%s のジョブを保存できない: %v", kind, status, err)
			}
		}
	}
	var jobID int64
	if err := s.DB().QueryRow(`SELECT MIN(id) FROM jobs`).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	rowNo := 0
	for status := range validRowStatuses {
		rowNo++
		if _, err := s.DB().Exec(`INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
			VALUES (?, ?, '', '', '', ?, 0, '')`, jobID, rowNo, status); err != nil {
			t.Errorf("status=%s の行を保存できない: %v", status, err)
		}
	}

	// 未知の値は拒否する
	if _, err := s.DB().Exec(`INSERT INTO jobs (kind, project_id, source_file, source_hash, created_at, status)
		VALUES ('bogus', 1, '', '', '', 'pending')`); err == nil {
		t.Error("不明な kind のジョブを保存できてしまった")
	}
	if _, err := s.DB().Exec(`INSERT INTO jobs (kind, project_id, source_file, source_hash, created_at, status)
		VALUES ('update', 1, '', '', '', 'bogus')`); err == nil {
		t.Error("不明な status のジョブを保存できてしまった")
	}
	if _, err := s.DB().Exec(`INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
		VALUES (?, 999, '', '', '', 'bogus', 0, '')`, jobID); err == nil {
		t.Error("不明な status の行を保存できてしまった")
	}
}

// TestV4_RejectsNullColumns は NOT NULL 化した列が NULL を拒むことを確認する。
func TestV4_RejectsNullColumns(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA"}); err != nil {
		t.Fatal(err)
	}
	jobID, err := s.CreateJob(ctx, JobKindUpdate, 1, "bulk.xlsx", "h",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE job_rows SET payload = NULL WHERE job_id = ?`, jobID); err == nil {
		t.Error("job_rows.payload を NULL にできてしまった")
	}
	if _, err := s.DB().Exec(`UPDATE jobs SET status = NULL WHERE id = ?`, jobID); err == nil {
		t.Error("jobs.status を NULL にできてしまった")
	}
	if _, err := s.DB().Exec(`INSERT INTO project_users (project_id, user_id, is_admin) VALUES (1, 1, NULL)`); err == nil {
		t.Error("project_users.is_admin を NULL にできてしまった")
	}
	if _, err := s.DB().Exec(`INSERT INTO project_users (project_id, user_id, is_admin) VALUES (1, 2, 2)`); err == nil {
		t.Error("project_users.is_admin に 0/1 以外を入れられてしまった")
	}
}

// TestV4_ForeignKeysStayEnabledOnNewConnections は、接続プールが接続を
// 張り直しても FK 制約が有効なままであることを確認する(R8)。
// foreign_keys は接続単位の設定なので、PRAGMA を 1 度実行しただけでは
// 新しい接続で無効に戻ってしまう。
func TestV4_ForeignKeysStayEnabledOnNewConnections(t *testing.T) {
	s := openTempStore(t)
	// アイドル接続を保持させないことで、次のクエリを新しい接続で実行させる。
	s.DB().SetMaxIdleConns(0)
	if _, err := s.DB().Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}

	var on int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Errorf("新しい接続の foreign_keys = %d, want 1", on)
	}
	if _, err := s.DB().Exec(`INSERT INTO team_members (team_id, user_id) VALUES (999, 1)`); err == nil {
		t.Error("新しい接続で孤児行を挿入できてしまった")
	}
}

// TestMigrate_V3ToV4_KeepsJobIDSequenceAfterDelete は、旧 DB で最大 ID の
// ジョブが削除済みでも採番上限(sqlite_sequence)が引き継がれることを確認する。
// jobs を作り直すとコピーした行の MAX(id) までしか採番が進まないため、
// 退避しないと削除済みジョブの ID を再利用してしまう
// (履歴・結果 Excel の参照先が過去のジョブと重なる)。
func TestMigrate_V3ToV4_KeepsJobIDSequenceAfterDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")
	db := createLegacyDB(t, path, 3)
	seedV3Reference(t, db)
	for _, id := range []int64{7, 9} {
		mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
			VALUES (?, 'update', 1, 'bulk.xlsx', 'hash-1', '2026-08-01T00:00:00Z', 'done')`, id)
	}
	// 最大 ID のジョブを削除する(採番上限だけが sqlite_sequence に残る)
	mustExec(t, db, `DELETE FROM jobs WHERE id = 9`)
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'jobs'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 9 {
		t.Fatalf("前提が崩れている: 移行前の sqlite_sequence(jobs) = %d, want 9", seq)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.CreateJob(context.Background(), JobKindUpdate, 1, "next.xlsx", "hash-2",
		[]JobRow{{RowNo: 2, IssueKey: "EXA-1", Payload: "{}"}})
	if err != nil {
		t.Fatal(err)
	}
	if id <= 9 {
		t.Errorf("移行後に採番されたジョブ ID = %d, want > 9(削除済み ID の再利用)", id)
	}
}

// TestMigrate_V4ToV5_AddsCommentStorageAndKeepsData は、v4 の DB に
// コメント保存用のテーブル・列が追加され、既存データが失われないことを
// 確認する。issues は再作成せず ALTER TABLE ADD COLUMN だけで拡張するため、
// 課題行と FTS 索引はそのまま残る。
func TestMigrate_V4ToV5_AddsCommentStorageAndKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")
	db := createLegacyDB(t, path, 4)
	seedV3Reference(t, db)
	mustExec(t, db, `INSERT INTO issues (
			id, issue_key, project_id, summary, description,
			status_id, status_name, assignee_id, assignee_name,
			issue_type_name, priority_name, created, updated, due_date,
			raw_json, search_text, fetched_at, deleted)
		VALUES (101, 'EXA-1', 1, '既存の件名', '既存の詳細',
			1, '未対応', 0, '', 'タスク', '中',
			'2026-08-01T00:00:00Z', '2026-08-02T00:00:00Z', '',
			'{}', '既存の件名\n既存の詳細', '2026-08-02T00:00:00Z', 0)`)
	mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
		VALUES (5, 'update', 1, 'bulk.xlsx', 'hash-1', '2026-08-01T00:00:00Z', 'pending')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("v4 → v5 の移行に失敗: %v", err)
	}
	defer s.Close()

	if v, err := s.SchemaVersion(); err != nil || v != LatestSchemaVersion() {
		t.Fatalf("移行後の schema_version = %d(err %v), want %d", v, err, LatestSchemaVersion())
	}

	// (a) 既存データが保持されていること
	ctx := context.Background()
	issue, err := s.GetIssueByKey(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if issue == nil {
		t.Fatal("移行で既存課題が失われた")
	}
	if issue.Summary != "既存の件名" || issue.Description != "既存の詳細" ||
		issue.StatusName != "未対応" || issue.Updated != "2026-08-02T00:00:00Z" {
		t.Errorf("移行後の課題 = %+v", issue)
	}
	if job, err := s.GetJob(ctx, 5); err != nil || job == nil || job.SourceFile != "bulk.xlsx" {
		t.Errorf("移行後のジョブ = %+v(err %v)", job, err)
	}

	// (b) issue_comments テーブルと索引が存在すること
	var name string
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'issue_comments'`,
	).Scan(&name); err != nil {
		t.Errorf("issue_comments が存在しない: %v", err)
	}
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_issue_comments_issue'`,
	).Scan(&name); err != nil {
		t.Errorf("idx_issue_comments_issue が存在しない: %v", err)
	}

	// (c) issues の新 3 列が存在すること
	cols := issueColumnSet(t, s)
	for _, col := range []string{"comments_fetched_at", "comments_truncated", "comments_history_only"} {
		if !cols[col] {
			t.Errorf("issues に列 %s が無い", col)
		}
	}

	// (d) 既存課題は「未取得」扱いであること
	st, err := s.GetIssueCommentStatus(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if st != (CommentFetchStatus{}) {
		t.Errorf("移行直後の取得結果 = %+v, want ゼロ値(未取得)", st)
	}

	// 移行後の DB でもコメントを保存・参照できること
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, AuthorName: "山田", Content: "移行後のコメント", Created: "2026-08-03T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-12T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIssueComments(ctx, 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "移行後のコメント" {
		t.Errorf("移行後に保存したコメント = %+v", got)
	}
}

// TestV5_CascadeDeletesComments は課題行の削除でコメントが消えることを
// 確認する(issue_comments.issue_id の ON DELETE CASCADE)。
func TestV5_CascadeDeletesComments(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssue(ctx, &Issue{ID: 101, IssueKey: "EXA-1", ProjectID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceIssueComments(ctx, 101, 1, []*IssueComment{
		{ID: 1001, Content: "コメント", Created: "2026-08-01T00:00:00Z"},
	}, CommentFetchStatus{FetchedAt: "2026-08-10T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM issues WHERE id = 101`); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s, "issue_comments"); n != 0 {
		t.Errorf("課題削除後の issue_comments = %d 件, want 0", n)
	}
	// 存在しない課題のコメントは作れないこと
	if _, err := s.DB().Exec(`INSERT INTO issue_comments
		(id, issue_id, project_id, author_name, content, created, updated)
		VALUES (1, 999, 1, '', '', '', '')`); err == nil {
		t.Error("存在しない課題のコメントを挿入できてしまった")
	}
}

// issueColumnSet は issues テーブルの列名の集合を返す。
func issueColumnSet(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	rows, err := s.DB().Query(`SELECT name FROM pragma_table_info('issues')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

// TestOpen_RejectsPathWithQuestionMark は '?' を含む DB パスを明確なエラーで
// 弾くことを確認する。modernc.org/sqlite は "file:" で始まらない DSN の
// '?' 以降をクエリとして解釈するため、素通しするとパスが切り詰められ
// 別の場所に DB を作ってしまう(かつ FK の指定も壊れる)。
func TestOpen_RejectsPathWithQuestionMark(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exa?mple.backlog.jp_1.db")
	if _, err := Open(path); err == nil {
		t.Fatal("'?' を含むパスで DB を開けてしまった")
	}
	// 検証前に副作用(ファイル作成)を起こさないこと
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("エラーにも関わらずファイルが作られた: %d 件", len(entries))
	}
}
