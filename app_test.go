package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestMaskPathInErrorReplacesFullPathWithFileName は、Excel 出力の失敗メッセージから
// 保存先のフルパス(顧客名を含みうるディレクトリ)が消え、ファイル名だけが残ることを確認する(高 2)。
func TestMaskPathInErrorReplacesFullPathWithFileName(t *testing.T) {
	path := "/home/someuser/Documents/顧客A/backlog-issues.xlsx"

	t.Run("フルパスがファイル名に置換される", func(t *testing.T) {
		err := fmt.Errorf("ファイルを保存できません: open %s: permission denied", path)
		got := maskPathInError(err, path)
		if got == nil {
			t.Fatal("maskPathInError = nil, want 非 nil")
		}
		want := "ファイルを保存できません: open backlog-issues.xlsx: permission denied"
		if got.Error() != want {
			t.Errorf("maskPathInError = %q, want %q", got.Error(), want)
		}
	})

	t.Run("複数箇所すべて置換される", func(t *testing.T) {
		err := fmt.Errorf("rename %s %s: cross-device link", path, path)
		got := maskPathInError(err, path)
		if strings.Contains(got.Error(), "顧客A") {
			t.Errorf("ディレクトリが残っています: %q", got.Error())
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
