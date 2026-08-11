package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"backlog-assistant/internal/store"
)

// TestFileExtAttrRecordsOnlyExtension は、動作ログに残すのが拡張子だけであり、
// ユーザが付けたファイル名(顧客名を含みうる)が残らないことを確認する(低 1)。
func TestFileExtAttrRecordsOnlyExtension(t *testing.T) {
	attr := fileExtAttr("/home/someuser/Documents/顧客A/2026年度_顧客A_課題一覧.XLSX")
	if attr.Key != "ext" {
		t.Errorf("キー = %q, want \"ext\"", attr.Key)
	}
	if got := attr.Value.String(); got != ".xlsx" {
		t.Errorf("値 = %q, want \".xlsx\"", got)
	}
	if strings.Contains(attr.Value.String(), "顧客A") {
		t.Errorf("ファイル名が記録されています: %q", attr.Value.String())
	}
}

// TestBulkRowActionAndStatusLabel は結果レポートの表示名解決を確認する(高 5)。
// 処理区分は payload を解析せず、行状態と課題キーの有無だけで決める。
func TestBulkRowActionAndStatusLabel(t *testing.T) {
	cases := []struct {
		row  store.JobRow
		want string
	}{
		{store.JobRow{IssueKey: "", Status: store.RowStatusDone}, "追加"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusDone}, "更新"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusSkip}, "変更なし"},
	}
	for _, c := range cases {
		if got := bulkRowAction(c.row); got != c.want {
			t.Errorf("bulkRowAction(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
	if got := bulkRowStatusLabel(store.RowStatusSending); got != "送信中(結果未確認)" {
		t.Errorf("sending の表示名 = %q", got)
	}
	// 未知の状態はそのまま返す(表示から値が消えないようにする)
	if got := bulkRowStatusLabel("unknown"); got != "unknown" {
		t.Errorf("未知の状態 = %q, want \"unknown\"", got)
	}
}

// TestMaskPathInErrorReplacesFullPathWithPlaceholder は、Excel 出力の失敗メッセージから
// 保存先のフルパスが消え、固定のプレースホルダに置換されることを確認する
// (高 2 / 2 回目 低 1)。ファイル名も顧客名・案件名を含みうるため記録しない。
func TestMaskPathInErrorReplacesFullPathWithPlaceholder(t *testing.T) {
	path := "/home/someuser/Documents/顧客A/backlog-issues.xlsx"

	t.Run("フルパスがプレースホルダに置換される", func(t *testing.T) {
		err := fmt.Errorf("ファイルを保存できません: open %s: permission denied", path)
		got := maskPathInError(err, path)
		if got == nil {
			t.Fatal("maskPathInError = nil, want 非 nil")
		}
		want := "ファイルを保存できません: open " + maskedPathPlaceholder + ": permission denied"
		if got.Error() != want {
			t.Errorf("maskPathInError = %q, want %q", got.Error(), want)
		}
	})

	t.Run("ファイル名も残らない", func(t *testing.T) {
		err := fmt.Errorf("ファイルを保存できません: open %s: permission denied", path)
		got := maskPathInError(err, path)
		if strings.Contains(got.Error(), "backlog-issues") {
			t.Errorf("ファイル名が残っています: %q", got.Error())
		}
	})

	t.Run("複数箇所すべて置換される", func(t *testing.T) {
		err := fmt.Errorf("rename %s %s: cross-device link", path, path)
		got := maskPathInError(err, path)
		if strings.Contains(got.Error(), "顧客A") || strings.Contains(got.Error(), "someuser") {
			t.Errorf("パスが残っています: %q", got.Error())
		}
	})

	t.Run("パスを含まないエラーはそのまま", func(t *testing.T) {
		err := errors.New("列の指定が不正です")
		if got := maskPathInError(err, path); got.Error() != err.Error() {
			t.Errorf("maskPathInError = %q, want %q", got.Error(), err.Error())
		}
	})

	t.Run("nil と空パスは安全", func(t *testing.T) {
		if got := maskPathInError(nil, path); got != nil {
			t.Errorf("maskPathInError(nil) = %v, want nil", got)
		}
		err := errors.New("失敗")
		if got := maskPathInError(err, ""); got != err {
			t.Errorf("空パスでエラーが差し替えられました: %v", got)
		}
	})
}
