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
