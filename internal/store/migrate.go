package store

import (
	"fmt"
	"strconv"
)

// migrations[i] はスキーマバージョン i+1 へ上げる SQL 文の列。
// 変更時は末尾に追記する(既存要素は変更しない)。
var migrations = [][]string{
	// v1: 初期スキーマ(設計書 2 節)
	{
		`CREATE TABLE IF NOT EXISTS projects (
			id INTEGER PRIMARY KEY, project_key TEXT, name TEXT,
			archived INTEGER, raw_json TEXT, fetched_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS issues (
			id INTEGER PRIMARY KEY, issue_key TEXT UNIQUE, project_id INTEGER,
			summary TEXT, description TEXT,
			status_id INTEGER, status_name TEXT,
			assignee_id INTEGER, assignee_name TEXT, issue_type_name TEXT,
			priority_name TEXT, created TEXT, updated TEXT, due_date TEXT,
			raw_json TEXT,
			search_text TEXT,
			fetched_at TEXT, deleted INTEGER DEFAULT 0)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_project_updated ON issues(project_id, updated)`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY, user_code TEXT, name TEXT, mail TEXT,
			role_type INTEGER, raw_json TEXT, fetched_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS teams (
			id INTEGER PRIMARY KEY, name TEXT, raw_json TEXT, fetched_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS team_members (
			team_id INTEGER, user_id INTEGER, PRIMARY KEY(team_id, user_id))`,
		`CREATE TABLE IF NOT EXISTS project_users (
			project_id INTEGER, user_id INTEGER, is_admin INTEGER DEFAULT 0,
			PRIMARY KEY(project_id, user_id))`,
		`CREATE TABLE IF NOT EXISTS sync_state (
			data_kind TEXT, project_id INTEGER DEFAULT 0,
			last_synced_at TEXT,
			last_sync_date TEXT,
			activity_cursor INTEGER,
			activity_start_pending INTEGER,
			PRIMARY KEY(data_kind, project_id))`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT,
			project_id INTEGER,
			source_file TEXT, source_hash TEXT,
			created_at TEXT, status TEXT)`,
		`CREATE TABLE IF NOT EXISTS job_rows (
			job_id INTEGER, row_no INTEGER,
			issue_key TEXT,
			payload TEXT,
			base_updated TEXT,
			status TEXT,
			result_issue_id INTEGER, error TEXT,
			PRIMARY KEY(job_id, row_no))`,
	},
	// v2: 完了ジョブの保持期限(R2)のために完了時刻を持たせる。
	// 既存行は NULL のままで、保持期限の判定は created_at で代用する
	// (PurgeExpiredJobs のコメント参照)。
	{
		`ALTER TABLE jobs ADD COLUMN completed_at TEXT`,
	},
	// v3: キーワード検索の FTS5 化(R3)。search_text の LIKE '%語%' は
	// 索引が使えず全走査になるため、trigram トークナイザの全文検索索引を持つ。
	//
	// external content 方式(content='issues')にして本文の二重保存を避け、
	// issues の INSERT / UPDATE / DELETE をトリガーで索引へ反映する。
	// content_rowid='id' により issues_fts.rowid = issues.id となる。
	// トークナイザの選定理由は issue_fts.go の冒頭コメントを参照。
	{
		`CREATE VIRTUAL TABLE IF NOT EXISTS issues_fts USING fts5(
			search_text,
			content='issues',
			content_rowid='id',
			tokenize='trigram')`,
		`CREATE TRIGGER IF NOT EXISTS issues_fts_ai AFTER INSERT ON issues BEGIN
			INSERT INTO issues_fts(rowid, search_text) VALUES (new.id, new.search_text);
		END`,
		`CREATE TRIGGER IF NOT EXISTS issues_fts_ad AFTER DELETE ON issues BEGIN
			INSERT INTO issues_fts(issues_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
		END`,
		// external content 方式では、更新前の値で 'delete' を発行してから
		// 新しい値を入れる(旧テキストの索引語が残らないようにするため)。
		`CREATE TRIGGER IF NOT EXISTS issues_fts_au AFTER UPDATE ON issues BEGIN
			INSERT INTO issues_fts(issues_fts, rowid, search_text) VALUES ('delete', old.id, old.search_text);
			INSERT INTO issues_fts(rowid, search_text) VALUES (new.id, new.search_text);
		END`,
		// 既存 DB(v2 まで)に溜まっている課題を索引へ一括投入する。
		`INSERT INTO issues_fts(issues_fts) VALUES ('rebuild')`,
	},
}

// LatestSchemaVersion は最新スキーマバージョン。
func LatestSchemaVersion() int { return len(migrations) }

// SchemaVersion は現在の schema_version を返す(meta 未設定なら 0)。
func (s *Store) SchemaVersion() (int, error) {
	v, err := s.GetMeta("schema_version")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("schema_version が不正です(%q): %w", v, err)
	}
	return n, nil
}

// migrate は meta テーブルを保証し、schema_version から最新まで
// マイグレーションを順に適用する。各バージョンの適用と schema_version の
// 更新は同一トランザクションで行う。
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		return fmt.Errorf("meta テーブルの作成に失敗しました: %w", err)
	}
	cur, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	if cur > len(migrations) {
		return fmt.Errorf("DB のスキーマバージョン(%d)がアプリの対応バージョン(%d)より新しいため開けません", cur, len(migrations))
	}
	for v := cur; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range migrations[v] {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("マイグレーション v%d の適用に失敗しました: %w", v+1, err)
			}
		}
		if _, err := tx.Exec(
			`INSERT INTO meta(key, value) VALUES('schema_version', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.Itoa(v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
