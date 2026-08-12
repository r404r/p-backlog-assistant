package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
