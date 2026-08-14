package storagepath_test

// カスタム基点での結合テスト(設計 §4)。
//
// config(config.json)と store(data/*.db)が **同じ基点** を見ること、
// カスタム基点でも実際に読み書き・削除ができること、基点を切り替えた前後で
// データが混ざらないことを、実ファイルシステムに対して確認する。
//
// 本番の解決結果はプロセス内で 1 回だけ確定する(sync.OnceValues)ため、
// テストバイナリ全体の環境変数を TestMain で先に設定してから解決させる。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"backlog-assistant/internal/config"
	"backlog-assistant/internal/service"
	"backlog-assistant/internal/storagepath"
	"backlog-assistant/internal/store"
)

// customBase はテストバイナリ全体で使うカスタム基点(TestMain で設定)。
var customBase string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "storagepath-integration-*")
	if err != nil {
		panic(err)
	}
	customBase = filepath.Join(dir, "backlog data")
	// 解決は最初の参照時に 1 回だけ行われるため、テスト実行前に設定する
	if err := os.Setenv(storagepath.EnvVar, customBase); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestCustomBase_SharedByConfigAndStore は、config と store が同じカスタム基点を
// 参照すること(= プロファイル削除時の DB 削除も同じ基点を対象にすること)を確認する。
func TestCustomBase_SharedByConfigAndStore(t *testing.T) {
	cfgDir, err := config.DefaultDir()
	if err != nil {
		t.Fatalf("設定ディレクトリを解決できない: %v", err)
	}
	if cfgDir != customBase {
		t.Errorf("設定ディレクトリ = %q, want %q", cfgDir, customBase)
	}

	dataDir, err := store.DefaultDataDir()
	if err != nil {
		t.Fatalf("データディレクトリを解決できない: %v", err)
	}
	if want := filepath.Join(customBase, "data"); dataDir != want {
		t.Errorf("データディレクトリ = %q, want %q", dataDir, want)
	}

	// DBPath(表示・オープン用)と RemoveDatabase(削除用)が同じ基点を見ていること
	dbPath, err := store.DBPath("example.backlog.jp", 42)
	if err != nil {
		t.Fatalf("DB パスを解決できない: %v", err)
	}
	if want := store.DBPathIn(dataDir, "example.backlog.jp", 42); dbPath != want {
		t.Errorf("DB パス = %q, want %q", dbPath, want)
	}

	if mode := storagepath.CurrentMode(); mode != storagepath.ModeEnv {
		t.Errorf("保存先モード = %q, want %q", mode, storagepath.ModeEnv)
	}
}

// TestCustomBase_ConfigSaveLoad は、カスタム基点で config.json を
// 実際に保存・読み込みできることを確認する。
func TestCustomBase_ConfigSaveLoad(t *testing.T) {
	dir := mustCustomBase(t)
	mgr := config.NewManagerAt(dir)
	if err := mgr.Upsert(config.Profile{ID: "p1", Name: "本番", SpaceURL: "https://example.backlog.jp"}); err != nil {
		t.Fatalf("保存に失敗した: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(mgr.Path()) })

	if _, serr := os.Stat(filepath.Join(customBase, "config.json")); serr != nil {
		t.Errorf("カスタム基点に config.json が作成されていない: %v", serr)
	}
	got, err := config.NewManagerAt(dir).List()
	if err != nil {
		t.Fatalf("読み込みに失敗した: %v", err)
	}
	if len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("読み込み結果 = %+v, want p1 の 1 件", got)
	}
}

// TestCustomBase_DatabaseOpenAndRemove は、カスタム基点の data/ で
// DB を作成でき(WAL も同じ場所に作られる)、RemoveDatabase が
// 本体 + WAL/SHM を削除することを確認する。
func TestCustomBase_DatabaseOpenAndRemove(t *testing.T) {
	mustCustomBase(t) // 解決が効いていない状態で実環境の既定フォルダを触らない
	path, err := store.DBPath("example.backlog.jp", 7)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("DB を開けない: %v", err)
	}
	if !strings.HasPrefix(path, customBase+string(filepath.Separator)) {
		t.Errorf("DB がカスタム基点の外にある: %q", path)
	}
	// WAL モードのため、開いている間は -wal が同じフォルダに存在する
	if _, serr := os.Stat(path + "-wal"); serr != nil {
		st.Close()
		t.Fatalf("WAL ファイルが作成されていない: %v", serr)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// WAL/SHM は Close 時に片付くこともあるため、削除の検証用に必ず用意する
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(path+suffix, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.RemoveDatabase("example.backlog.jp", 7); err != nil {
		t.Fatalf("削除に失敗した: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, serr := os.Stat(path + suffix); !os.IsNotExist(serr) {
			t.Errorf("%q が残っている(err=%v)", path+suffix, serr)
		}
	}
}

// TestCustomBase_DeleteProfileRemovesDatabase は、プロファイル削除
// (DeleteProfile の deleteDB = true)の **実経路** がカスタム基点の DB を
// 対象にすることを確認する。
//
// service は DB のパス解決(store.DBPath)と削除(store.RemoveDatabase)を
// 別経路で呼ぶため、片方だけカスタム基点に追従していると「表示は消えたのに
// ファイルが残る」状態になる。既定の構成(NewProfileService)のまま検証する。
func TestCustomBase_DeleteProfileRemovesDatabase(t *testing.T) {
	base := mustCustomBase(t)
	keyring.MockInit() // OS キーチェーンには触れない(インメモリ実装へ差し替え)

	mgr := config.NewManagerAt(base)
	profile := config.Profile{
		ID: "delete-me", Name: "削除検証", SpaceURL: "https://example.backlog.jp", LastUserID: 99,
	}
	if err := mgr.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(mgr.Path()) })

	dbPath, err := store.DBPath("example.backlog.jp", 99)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(dbPath+suffix, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	svc := service.NewProfileService(mgr)
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.DeleteProfile(profile.ID, true); err != nil {
		t.Fatalf("プロファイル削除に失敗した: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, serr := os.Stat(dbPath + suffix); !os.IsNotExist(serr) {
			t.Errorf("カスタム基点の %q が残っている(err=%v)", dbPath+suffix, serr)
		}
	}
	left, err := mgr.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("プロファイルが残っている: %+v", left)
	}
}

// TestSwitchBase_DataNotMixed は、基点を切り替えると別のデータになり
// (自動移行はしない)、戻せば元のデータが見えることを確認する。
func TestSwitchBase_DataNotMixed(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")

	baseA := resolveEnvBase(t, first)
	if err := config.NewManagerAt(baseA).Upsert(config.Profile{ID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}

	baseB := resolveEnvBase(t, second)
	got, err := config.NewManagerAt(baseB).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("切替先に前の基点のデータが見えている: %+v", got)
	}
	if err := config.NewManagerAt(baseB).Upsert(config.Profile{ID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}

	// 元の基点へ戻すと元のデータが再び見える
	back, err := config.NewManagerAt(resolveEnvBase(t, first)).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 1 || back[0].ID != "a" {
		t.Errorf("元の基点のデータ = %+v, want a の 1 件", back)
	}
}

// mustCustomBase は解決済みの基点がカスタム基点であることを確認して返す。
// 解決が効いていない場合に実環境の既定フォルダ(利用者の本物の config.json)を
// 書き換えてしまわないための安全弁。
func mustCustomBase(t *testing.T) string {
	t.Helper()
	dir, err := config.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != customBase {
		t.Fatalf("カスタム基点が使われていない: %q, want %q", dir, customBase)
	}
	return dir
}

// resolveEnvBase は環境変数指定を注入した Resolver で基点を解決する
// (本番のグローバル解決はプロセス内 1 回のため、切替の検証には使えない)。
func resolveEnvBase(t *testing.T, dir string) string {
	t.Helper()
	res, err := storagepath.New(storagepath.Deps{
		Getenv: func(string) string { return dir },
		// マーカーの無いフォルダを実行ファイルの位置として渡す
		// (位置不明はポータブル判定ができずエラーになるため)
		Executable: func() (string, error) { return filepath.Join(t.TempDir(), "app"), nil },
	}).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if res.Mode != storagepath.ModeEnv {
		t.Fatalf("モード = %q, want env", res.Mode)
	}
	return res.BaseDir
}
