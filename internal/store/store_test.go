package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func openTempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "example.backlog.jp_12345.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrate_FreshDatabase(t *testing.T) {
	s := openTempStore(t)

	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestSchemaVersion() {
		t.Errorf("schema_version = %d, want %d", v, LatestSchemaVersion())
	}

	// 設計書 2 節の主要テーブルが存在すること
	for _, table := range []string{
		"meta", "projects", "issues", "users", "teams",
		"team_members", "project_users", "sync_state", "jobs", "job_rows",
	} {
		var name string
		err := s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("テーブル %s が存在しない: %v", table, err)
		}
	}
	// インデックス
	var idx string
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_issues_project_updated'`,
	).Scan(&idx); err != nil {
		t.Errorf("idx_issues_project_updated が存在しない: %v", err)
	}
}

func TestMigrate_ReopenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_1.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SetMeta("marker", "keep"); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// 再オープンしてもマイグレーションは再適用されず、データは保持される
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v, err := s2.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestSchemaVersion() {
		t.Errorf("再オープン後の schema_version = %d, want %d", v, LatestSchemaVersion())
	}
	marker, err := s2.GetMeta("marker")
	if err != nil {
		t.Fatal(err)
	}
	if marker != "keep" {
		t.Errorf("再オープンで meta の値が失われた: %q", marker)
	}
}

// TestMigrate_V1ToV2AddsCompletedAt は v1 で作られた既存 DB を開いたときに
// v2(jobs.completed_at の追加)が適用され、既存データが保持されることを
// 確認する(R2)。
func TestMigrate_V1ToV2AddsCompletedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_1.db")

	// v1 のスキーマだけを持つ DB を作る(旧バージョンで作られた DB の再現)
	db := createLegacyDB(t, path, 1)
	// 実行中(pending)のジョブ。マイグレーションでも整理でも消えてはならない。
	mustExec(t, db, `INSERT INTO jobs (id, kind, project_id, source_file, source_hash, created_at, status)
		VALUES (1, 'update', 1, 'bulk.xlsx', 'h', '2020-01-01T00:00:00Z', 'pending')`)
	mustExec(t, db, `INSERT INTO job_rows (job_id, row_no, issue_key, payload, base_updated, status, result_issue_id, error)
		VALUES (1, 2, 'EXA-1', '{"summary":"件名"}', '', 'pending', 0, '')`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v != LatestSchemaVersion() {
		t.Errorf("移行後の schema_version = %d, want %d", v, LatestSchemaVersion())
	}
	if got := jobCompletedAt(t, s, 1); got != "" {
		t.Errorf("既存ジョブの completed_at = %q, want 空(NULL)", got)
	}
	if !jobExists(t, s, 1) || jobRowCount(t, s, 1) != 1 {
		t.Error("移行で既存ジョブが失われた")
	}
}

func TestMigrate_RejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_1.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta("schema_version", "999"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(path); err == nil {
		t.Error("アプリより新しい schema_version の DB を開けてしまった")
	}
}

func TestDBPathIn(t *testing.T) {
	got := DBPathIn("/base", "example.backlog.jp", 12345)
	want := filepath.Join("/base", "example.backlog.jp_12345.db")
	if got != want {
		t.Errorf("DBPathIn = %q, want %q", got, want)
	}
	// ファイル名に使えない文字はサニタイズされる
	got = DBPathIn("/base", "bad/host\\name", 1)
	if filepath.Base(got) != "bad_host_name_1.db" {
		t.Errorf("サニタイズ結果 = %q", filepath.Base(got))
	}
}

func TestOpen_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では POSIX パーミッションを検証しない")
	}
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "example.backlog.jp_1.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("データディレクトリの権限 = %o, want 700", perm)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("DB ファイルの権限 = %o, want 600", perm)
	}
}

func TestOpen_FixesExistingFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では POSIX パーミッションを検証しない")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_1.db")
	// いったん作成して緩い権限に変更する(過去バージョンの状態を再現)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Chmod(path, 0o664); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("再オープン後の DB ファイル権限 = %o, want 600", perm)
	}
}

func TestRemoveDatabaseIn(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("example.backlog.jp_1.db")
	mk("example.backlog.jp_1.db-wal")
	mk("example.backlog.jp_1.db-shm")
	mk("example.backlog.jp_2.db") // 同一ホスト・別ユーザは残す
	mk("other.backlog.jp_1.db")   // 別ホストは残す

	if err := RemoveDatabaseIn(dir, "example.backlog.jp", 1); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var remaining []string
	for _, e := range entries {
		remaining = append(remaining, e.Name())
	}
	want := []string{"example.backlog.jp_2.db", "other.backlog.jp_1.db"}
	if len(remaining) != len(want) || remaining[0] != want[0] || remaining[1] != want[1] {
		t.Errorf("削除後の残ファイル = %v, want %v", remaining, want)
	}

	// 存在しない DB の削除は冪等(エラーにしない)
	if err := RemoveDatabaseIn(dir, "example.backlog.jp", 1); err != nil {
		t.Errorf("冪等な再削除でエラー: %v", err)
	}
}

func TestRemoveDatabasesForHostIn(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("example.backlog.jp_1.db")
	mk("example.backlog.jp_1.db-wal")
	mk("example.backlog.jp_2.db")
	mk("other.backlog.jp_1.db") // 別ホストは残す

	if err := RemoveDatabasesForHostIn(dir, "example.backlog.jp"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var remaining []string
	for _, e := range entries {
		remaining = append(remaining, e.Name())
	}
	if len(remaining) != 1 || remaining[0] != "other.backlog.jp_1.db" {
		t.Errorf("削除後の残ファイル = %v, want [other.backlog.jp_1.db]", remaining)
	}
}
