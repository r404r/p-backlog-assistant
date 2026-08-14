package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"backlog-assistant/internal/storagepath"
)

// TestWindowShower_ShowsOnce は、OnDomReady とフォールバックタイマーの
// どちらから呼ばれてもウィンドウ表示が 1 回だけになることを確認する。
//
// なお wails.Run のオプション(StartHidden・OnDomReady の結線)自体は
// Wails ランタイムとの結合が必要で自動テストが難しいため、TDD 例外(手動確認)。
func TestWindowShower_ShowsOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	s := &windowShower{show: func(_ context.Context) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	}}

	ctx := context.Background()
	// 2 経路(DomReady・フォールバック)からの呼び出しを同時に再現する
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Show(ctx)
		}()
	}
	wg.Wait()

	if calls != 1 {
		t.Errorf("表示回数 = %d, want 1", calls)
	}
}

// TestNewWindowShower_HasShowFunc は、既定の生成関数が表示処理を持つ
// (nil のまま Show を呼んで panic しない)ことを確認する。
func TestNewWindowShower_HasShowFunc(t *testing.T) {
	if newWindowShower().show == nil {
		t.Error("既定の表示処理が設定されていない")
	}
}

// TestStartupWithWindowShow_RegistersFallbackBeforeStartup は、表示のフォールバック
// タイマーが app.startup より **先に** 登録されることを確認する。
//
// startup は設定の読み込み・キーチェーンの初期化などを行うため、環境によっては
// 長時間ブロックし得る。タイマーの登録が startup の後だと、その間ウィンドウが
// 隠れたまま(StartHidden)になり「起動したのに何も出ない」状態が続いてしまう。
func TestStartupWithWindowShow_RegistersFallbackBeforeStartup(t *testing.T) {
	shown := make(chan struct{})
	shower := &windowShower{show: func(_ context.Context) { close(shown) }}

	startupCalled := false
	handler := startupWithWindowShow(shower, func(_ context.Context) {
		startupCalled = true
		// startup が終わらないうちに、フォールバックで表示されることを確認する
		select {
		case <-shown:
		case <-time.After(3 * time.Second):
			t.Error("startup の完了前にウィンドウが表示されなかった")
		}
	}, time.Millisecond)

	handler(context.Background())

	if !startupCalled {
		t.Error("startup が呼ばれていない")
	}
}

// TestStartupWithWindowShow_DomReadyWinsOverFallback は、フロントエンドが
// 正常に読み込めた場合(OnDomReady が先)にフォールバックが二重に表示しないことを
// 確認する。
func TestStartupWithWindowShow_DomReadyWinsOverFallback(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	shower := &windowShower{show: func(_ context.Context) {
		mu.Lock()
		defer mu.Unlock()
		calls++
	}}

	ctx := context.Background()
	handler := startupWithWindowShow(shower, func(_ context.Context) {}, 10*time.Millisecond)
	handler(ctx)
	// OnDomReady 相当(フォールバックより前に届く)
	shower.Show(ctx)
	// フォールバックタイマーが発火する時間まで待つ
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("表示回数 = %d, want 1", calls)
	}
}

// TestStartupFailureText は、起動失敗の記録本文に日時と原因が入り、
// 機密(API キー)・スペース URL・ホームディレクトリのパスが残らないことを確認する。
// この本文は利用者が開発者へ共有する想定のため、動作ログと同じ基準でマスクする。
func TestStartupFailureText(t *testing.T) {
	now := time.Date(2026, 8, 12, 21, 30, 45, 0, time.UTC)
	err := errors.New("webview2 が見つかりません: https://example.backlog.jp/api/v2/space?apiKey=SECRET")

	got := startupFailureText(err, now)

	if !strings.Contains(got, "2026-08-12") || !strings.Contains(got, "21:30:45") {
		t.Errorf("日時が含まれていない: %q", got)
	}
	if !strings.Contains(got, "webview2 が見つかりません") {
		t.Errorf("原因が含まれていない: %q", got)
	}
	if strings.Contains(got, "SECRET") {
		t.Errorf("API キーが残っている: %q", got)
	}
	if strings.Contains(got, "example.backlog.jp") {
		t.Errorf("スペースのホスト名が残っている: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("行末が改行で終わっていない: %q", got)
	}
}

// TestWriteCrashFile_CreatesAndAppends は、crash.txt が作成され、
// 2 回目以降は追記される(前回の記録を消さない)ことを確認する。
func TestWriteCrashFile_CreatesAndAppends(t *testing.T) {
	dir := t.TempDir()

	path, err := writeCrashFile(dir, "1 回目\n")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, crashFileName); path != want {
		t.Errorf("パス = %q, want %q", path, want)
	}
	if _, err := writeCrashFile(dir, "2 回目\n"); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != "1 回目\n2 回目\n" {
		t.Errorf("内容 = %q, want \"1 回目\\n2 回目\\n\"", got)
	}
}

// TestWriteCrashFile_RejectsEmptyDir は、出力先が特定できない場合に
// カレントディレクトリ等へ勝手に書かず、エラーを返すことを確認する。
func TestWriteCrashFile_RejectsEmptyDir(t *testing.T) {
	if _, err := writeCrashFile("", "内容\n"); err == nil {
		t.Error("出力先が空でもエラーにならなかった")
	}
}

// TestCrashFileDirs_IgnoresCustomStorageBase は、crash.txt の保存先が
// データ保存先のカスタマイズ(BACKLOG_ASSISTANT_HOME / portable.txt)に
// **追従しない**ことを確認する(設計 §3.2)。
//
// 起動エラーの主な原因はカスタム保存先が使えないことなので、記録先まで
// そこへ寄せると「なぜ起動できないのか」を残せなくなる。
// 依存は注入する。XDG_CONFIG_HOME は macOS / Windows では効かないため、
// 実際の os.UserConfigDir を使うと利用者の実フォルダへ MkdirAll してしまう。
func TestCrashFileDirs_IgnoresCustomStorageBase(t *testing.T) {
	custom := t.TempDir()
	t.Setenv(storagepath.EnvVar, custom)
	exeDir := t.TempDir()
	configBase := t.TempDir()
	var created []string

	dirs := crashFileDirsWith(
		func() (string, error) { return filepath.Join(exeDir, "backlog-assistant"), nil },
		func() (string, error) { return configBase, nil },
		func(path string, perm os.FileMode) error {
			created = append(created, path)
			return os.MkdirAll(path, perm)
		},
	)

	want := []string{exeDir, filepath.Join(configBase, crashAppDirName)}
	if len(dirs) != len(want) || dirs[0] != want[0] || dirs[1] != want[1] {
		t.Errorf("候補 = %v, want %v", dirs, want)
	}
	for _, d := range dirs {
		if d == custom {
			t.Errorf("カスタム保存先が候補に含まれている: %v", dirs)
		}
	}
	if len(created) != 1 || created[0] != want[1] {
		t.Errorf("作成したフォルダ = %v, want %v のみ", created, want[1:])
	}
}

// TestCrashFileDirs_SkipsUnavailableCandidates は、位置を特定できない
// (または作成できない)候補を黙って飛ばすことを確認する。
func TestCrashFileDirs_SkipsUnavailableCandidates(t *testing.T) {
	configBase := t.TempDir()
	noExe := func() (string, error) { return "", errors.New("特定できません") }
	okConfig := func() (string, error) { return configBase, nil }
	mkdirOK := func(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

	if got := crashFileDirsWith(noExe, okConfig, mkdirOK); len(got) != 1 ||
		got[0] != filepath.Join(configBase, crashAppDirName) {
		t.Errorf("実行ファイル不明時の候補 = %v", got)
	}

	noConfig := func() (string, error) { return "", errors.New("取得できません") }
	if got := crashFileDirsWith(noExe, noConfig, mkdirOK); len(got) != 0 {
		t.Errorf("候補なしのはずが %v", got)
	}

	mkdirNG := func(string, os.FileMode) error { return errors.New("作成できません") }
	if got := crashFileDirsWith(noExe, okConfig, mkdirNG); len(got) != 0 {
		t.Errorf("作成失敗時に候補へ加えている: %v", got)
	}
}
