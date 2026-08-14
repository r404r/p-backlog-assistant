package service

import (
	"os"
	"path/filepath"
	"testing"

	"backlog-assistant/internal/config"
	"backlog-assistant/internal/store"
)

// writeSizedFile は size バイトのダミーファイルを作る(サイズ集計の検証用)。
func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newStorageTestService は一時ディレクトリの config とデータディレクトリを使う
// ProfileService を返す(キーチェーン・API には触れない)。
func newStorageTestService(t *testing.T, profiles ...config.Profile) (*ProfileService, string, string) {
	t.Helper()
	cfgDir := t.TempDir()
	dataDir := t.TempDir()
	mgr := config.NewManagerAt(cfgDir)
	for _, p := range profiles {
		if err := mgr.Upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	s := NewProfileService(mgr)
	s.dbPathFor = func(host string, userID int) (string, error) {
		return store.DBPathIn(dataDir, host, userID), nil
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, cfgDir, dataDir
}

// TestGetStorageInfo_ReportsStorageMode は、保存先の決定方法
// (default / env / portable)を返すことを確認する(アプリ情報画面の表示用)。
func TestGetStorageInfo_ReportsStorageMode(t *testing.T) {
	s, _, _ := newStorageTestService(t)
	s.storageMode = func() string { return "portable" }

	info, err := s.GetStorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.StorageMode != "portable" {
		t.Errorf("StorageMode = %q, want %q", info.StorageMode, "portable")
	}
}

// TestNewProfileService_DefaultStorageMode は、既定の構成でも保存先モードが
// 3 値のいずれかで得られる(空にならない)ことを確認する。
func TestNewProfileService_DefaultStorageMode(t *testing.T) {
	s := NewProfileService(config.NewManagerAt(t.TempDir()))
	t.Cleanup(func() { _ = s.Close() })

	switch got := s.storageMode(); got {
	case "default", "env", "portable":
	default:
		t.Errorf("既定の保存先モード = %q, want default / env / portable のいずれか", got)
	}
}

// TestGetStorageInfo_SumsDatabaseFileSizes は、DB 本体に WAL / SHM を加えた
// 合計サイズを返すこと、設定ディレクトリと DB パスを返すことを確認する。
func TestGetStorageInfo_SumsDatabaseFileSizes(t *testing.T) {
	s, cfgDir, dataDir := newStorageTestService(t, config.Profile{
		ID: "p1", Name: "検証用", SpaceURL: "https://example.backlog.jp", LastUserID: 42,
	})
	dbPath := store.DBPathIn(dataDir, "example.backlog.jp", 42)
	writeSizedFile(t, dbPath, 1000)
	writeSizedFile(t, dbPath+"-wal", 2000)
	writeSizedFile(t, dbPath+"-shm", 30)

	info, err := s.GetStorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.ConfigDir != cfgDir {
		t.Errorf("ConfigDir = %q, want %q", info.ConfigDir, cfgDir)
	}
	if len(info.Databases) != 1 {
		t.Fatalf("Databases = %+v", info.Databases)
	}
	db := info.Databases[0]
	if db.ProfileID != "p1" || db.ProfileName != "検証用" {
		t.Errorf("プロファイル = %+v", db)
	}
	if db.Path != dbPath {
		t.Errorf("Path = %q, want %q", db.Path, dbPath)
	}
	if !db.Exists {
		t.Error("Exists = false, want true")
	}
	if db.SizeBytes != 3030 {
		t.Errorf("SizeBytes = %d, want 3030(本体 + WAL + SHM)", db.SizeBytes)
	}
}

// TestGetStorageInfo_MissingAndUnconnected は、DB ファイルが未作成の場合と
// 接続実績が無い(LastUserID = 0)場合の扱いを確認する。
func TestGetStorageInfo_MissingAndUnconnected(t *testing.T) {
	s, _, dataDir := newStorageTestService(t,
		config.Profile{ID: "p1", Name: "未作成", SpaceURL: "https://example.backlog.jp", LastUserID: 42},
		config.Profile{ID: "p2", Name: "未接続", SpaceURL: "https://another.backlog.jp"},
		config.Profile{ID: "p3", Name: "URL 不正", SpaceURL: "https://example.invalid", LastUserID: 7},
	)

	info, err := s.GetStorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Databases) != 3 {
		t.Fatalf("Databases = %+v", info.Databases)
	}

	// 接続実績はあるがファイル未作成: パスは示しつつ未作成扱い
	missing := info.Databases[0]
	if missing.Path != store.DBPathIn(dataDir, "example.backlog.jp", 42) {
		t.Errorf("Path = %q", missing.Path)
	}
	if missing.Exists || missing.SizeBytes != 0 {
		t.Errorf("未作成の DB = %+v", missing)
	}

	if missing.Error != "" {
		t.Errorf("未作成の DB にエラーが付いています: %q", missing.Error)
	}

	// 接続実績が無いプロファイルは DB ファイルを特定できない(異常ではない)
	unconnected := info.Databases[1]
	if unconnected.Path != "" || unconnected.Exists || unconnected.SizeBytes != 0 {
		t.Errorf("未接続プロファイル = %+v", unconnected)
	}
	if unconnected.Error != "" {
		t.Errorf("未接続プロファイルにエラーが付いています: %q", unconnected.Error)
	}

	// スペース URL が不正なプロファイルはパスを特定できない。
	// 「未作成」と混同されないよう、理由をエラーとして返す(1 回目 中 1)
	invalid := info.Databases[2]
	if invalid.Path != "" || invalid.Exists {
		t.Errorf("URL 不正プロファイル = %+v", invalid)
	}
	if invalid.Error == "" {
		t.Error("URL 不正プロファイルの Error が空(未作成と区別できない)")
	}
}

// TestGetStorageInfo_ReportsStatError は、未作成(IsNotExist)ではない
// 参照エラー(権限不足等)を「未作成」と同一視せずエラーとして返すことを
// 確認する(1 回目 中 1)。
func TestGetStorageInfo_ReportsStatError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root では権限による参照失敗を再現できない")
	}
	s, _, dataDir := newStorageTestService(t, config.Profile{
		ID: "p1", Name: "参照不可", SpaceURL: "https://example.backlog.jp", LastUserID: 42,
	})
	dbPath := store.DBPathIn(dataDir, "example.backlog.jp", 42)
	writeSizedFile(t, dbPath, 100)
	// 親ディレクトリを参照不可にすると os.Stat は権限エラーになる(未作成ではない)
	if err := os.Chmod(dataDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })

	info, err := s.GetStorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	db := info.Databases[0]
	if db.Error == "" {
		t.Error("参照エラーが Error に出ていない(未作成と区別できない)")
	}
	if db.Exists {
		t.Errorf("Exists = true, want false: %+v", db)
	}
	if db.Path != dbPath {
		t.Errorf("Path = %q, want %q", db.Path, dbPath)
	}
}

// TestDatabaseTotalSize_SidecarOnly は、本体が無く WAL / SHM だけが残っている
// 場合でも残存分を合算し、本体の有無(Exists)とは独立に扱うことを確認する
// (1 回目 中 2)。
func TestDatabaseTotalSize_SidecarOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "example.backlog.jp_42.db")
	writeSizedFile(t, path+"-wal", 500)
	writeSizedFile(t, path+"-shm", 20)

	total, exists, err := databaseTotalSize(path)
	if err != nil {
		t.Fatalf("err = %v, want nil(本体の未作成はエラーではない)", err)
	}
	if exists {
		t.Error("Exists = true, want false(本体は存在しない)")
	}
	if total != 520 {
		t.Errorf("合計 = %d, want 520(WAL + SHM の残存分)", total)
	}
}

// TestGetStorageInfo_NoProfiles はプロファイルが 1 件も無い場合に
// nil ではなく空スライスを返すこと(フロント契約: null を返さない)を確認する。
func TestGetStorageInfo_NoProfiles(t *testing.T) {
	s, cfgDir, _ := newStorageTestService(t)

	info, err := s.GetStorageInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.ConfigDir != cfgDir {
		t.Errorf("ConfigDir = %q, want %q", info.ConfigDir, cfgDir)
	}
	if info.Databases == nil {
		t.Fatal("Databases = nil, want 空スライス")
	}
	if len(info.Databases) != 0 {
		t.Errorf("Databases = %+v", info.Databases)
	}
}
