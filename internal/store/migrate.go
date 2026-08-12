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
	// v4: 参照整合性(FK)・NOT NULL・CHECK 制約の導入(R8)。
	//
	// これまで接続では PRAGMA foreign_keys = ON にしていたが、スキーマ側に
	// FK 定義が無かったため孤児行(親の消えた所属関係・ジョブ行)を作れた。
	// SQLite は ALTER TABLE で制約を追加できないため、
	// 「新テーブル作成 → データ移行 → 旧テーブル削除 → リネーム」で作り直す。
	//
	// 対象テーブルの選定(コスト対効果):
	//
	//	含める: team_members / project_users / jobs / job_rows
	//	  親が明確で、既存コードが必ず親を先に作る関係だけを対象にした。
	//	  いずれも「親が消えたのに子だけ残る」ことに実害がある
	//	  (job_rows.payload には件名・詳細・カスタム属性が入る。R2)。
	//
	//	除外: issues
	//	  issues は FTS5 の external content(issues_fts)とトリガー 3 本に
	//	  結び付いており、再作成するにはトリガーの張り直しと索引の全件
	//	  rebuild が要る。10 万件規模では移行時間が跳ね上がり、
	//	  索引と本体が食い違うリスクも増える。一方 issues.project_id の
	//	  孤児は DeleteProjectsNotIn が同一トランザクションで消しており
	//	  (project.go)、実害が小さい。よって今回は見送る。
	//
	//	除外: team_members.user_id / project_users.user_id → users(id)
	//	  users はスペース単位の全置換(DELETE → INSERT)で更新する。
	//	  ここに ON DELETE CASCADE を付けると、全置換のたびに
	//	  team_members / project_users が一度すべて消えてしまい、
	//	  「取得できなかったプロジェクトの参加情報は据え置く」という
	//	  縮退時の不変条件(R1)を壊す。RESTRICT では全置換自体が失敗する。
	//	  ユーザ ID の孤児行は users を起点に JOIN する参照経路
	//	  (ListUserRows)からは見えないため、実害も小さい。
	//
	//	除外: jobs.project_id → projects(id)
	//	  一括取り込みは利用者操作であり、同期の狭間でプロジェクト行が
	//	  消えていると取り込みそのものが失敗する(ハードエラー)。
	//	  プロジェクト削除時のジョブ破棄は DeleteProjectsNotIn が
	//	  明示的に行っている(R2)ため、FK に頼らない。
	//
	//	除外: sync_state.project_id
	//	  project_id = 0 をスペース共通スコープの番兵に使っており、
	//	  projects への FK にできない。
	//
	// 破損データの扱い: 孤児行は移行時に削除する(親を復元する手段が無く、
	// UI からも辿れない死にデータのため)。想定外の状態値・NULL は既知の値へ
	// 丸める。ここで移行を失敗させると DB ごと開けなくなり、実行中のジョブや
	// 課題キャッシュまで巻き添えにしてしまうため。
	//
	// CHECK の許容値は job.go の定数と一致させること。マイグレーションは
	// 追記式で過去分を変更しないため値は直書きし、食い違いは
	// TestV4_CheckConstraintsMatchGoConstants で検出する。
	{
		// --- jobs(job_rows の親なので先に作り直す)---
		//
		// jobs.id は AUTOINCREMENT で「一度使った ID を再利用しない」ことを
		// 保証している。採番上限は sqlite_sequence に入っており、DROP TABLE で
		// 一緒に消える。新テーブルへコピーしただけでは採番が MAX(id) までしか
		// 進まないため、最大 ID のジョブが削除済みだと過去の ID を再利用して
		// しまう(履歴・結果 Excel の参照先が過去のジョブと重なる)。
		// そこで旧 seq を meta へ退避し、作り直したあとに引き継ぐ。
		`INSERT OR REPLACE INTO meta (key, value)
			SELECT '_v4_jobs_seq', CAST(seq AS TEXT) FROM sqlite_sequence WHERE name = 'jobs'`,
		`CREATE TABLE jobs_v4 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL CHECK (kind IN ('create', 'update', 'mixed')),
			project_id INTEGER NOT NULL,
			source_file TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'canceled')),
			completed_at TEXT)`,
		// 不明な kind は mixed(行ごとに追加・更新が決まる汎用種別)へ、
		// 不明な status は canceled(終端。再開対象にならない)へ丸める。
		`INSERT INTO jobs_v4 (id, kind, project_id, source_file, source_hash, created_at, status, completed_at)
			SELECT id,
				CASE WHEN kind IN ('create', 'update', 'mixed') THEN kind ELSE 'mixed' END,
				COALESCE(project_id, 0),
				COALESCE(source_file, ''), COALESCE(source_hash, ''), COALESCE(created_at, ''),
				CASE WHEN status IN ('pending', 'running', 'done', 'canceled') THEN status ELSE 'canceled' END,
				completed_at
			FROM jobs`,
		`DROP TABLE jobs`,
		`ALTER TABLE jobs_v4 RENAME TO jobs`,
		// 採番上限の引き継ぎ。コピーした行が 0 件だと sqlite_sequence に
		// 'jobs' の行自体が無いため、先に 0 で作ってから大きい方を採る。
		// (sqlite_sequence は AUTOINCREMENT 表の作成時に用意されるので存在する。)
		`INSERT INTO sqlite_sequence (name, seq)
			SELECT 'jobs', 0
			WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'jobs')`,
		`UPDATE sqlite_sequence
			SET seq = MAX(seq, COALESCE(
				(SELECT CAST(value AS INTEGER) FROM meta WHERE key = '_v4_jobs_seq'), 0))
			WHERE name = 'jobs'`,
		`DELETE FROM meta WHERE key = '_v4_jobs_seq'`,

		// --- job_rows ---
		`CREATE TABLE job_rows_v4 (
			job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
			row_no INTEGER NOT NULL,
			issue_key TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL DEFAULT '',
			base_updated TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (
				status IN ('pending', 'sending', 'done', 'error', 'conflict', 'skip')),
			result_issue_id INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (job_id, row_no))`,
		// 不明な status は error(終端だが再送指示は出せる)へ丸める。
		`INSERT INTO job_rows_v4 (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
			SELECT job_id, row_no,
				COALESCE(issue_key, ''), COALESCE(payload, ''), COALESCE(base_updated, ''),
				CASE WHEN status IN ('pending', 'sending', 'done', 'error', 'conflict', 'skip')
					THEN status ELSE 'error' END,
				COALESCE(result_issue_id, 0), COALESCE(error, '')
			FROM job_rows
			WHERE job_id IS NOT NULL AND row_no IS NOT NULL
			  AND job_id IN (SELECT id FROM jobs)`,
		`DROP TABLE job_rows`,
		`ALTER TABLE job_rows_v4 RENAME TO job_rows`,

		// --- team_members ---
		`CREATE TABLE team_members_v4 (
			team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
			user_id INTEGER NOT NULL,
			PRIMARY KEY (team_id, user_id))`,
		`INSERT INTO team_members_v4 (team_id, user_id)
			SELECT team_id, user_id FROM team_members
			WHERE team_id IS NOT NULL AND user_id IS NOT NULL
			  AND team_id IN (SELECT id FROM teams)`,
		`DROP TABLE team_members`,
		`ALTER TABLE team_members_v4 RENAME TO team_members`,

		// --- project_users ---
		`CREATE TABLE project_users_v4 (
			project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
			user_id INTEGER NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
			PRIMARY KEY (project_id, user_id))`,
		`INSERT INTO project_users_v4 (project_id, user_id, is_admin)
			SELECT project_id, user_id,
				CASE WHEN COALESCE(is_admin, 0) <> 0 THEN 1 ELSE 0 END
			FROM project_users
			WHERE project_id IS NOT NULL AND user_id IS NOT NULL
			  AND project_id IN (SELECT id FROM projects)`,
		`DROP TABLE project_users`,
		`ALTER TABLE project_users_v4 RENAME TO project_users`,
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
