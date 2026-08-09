package secret

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// keyring.MockInit によりインメモリ実装へ差し替える(OS キーチェーン不要)。
func TestSaveGetDelete(t *testing.T) {
	keyring.MockInit()

	if err := Save("profile-1", "dummy-api-key"); err != nil {
		t.Fatal(err)
	}
	got, err := Get("profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "dummy-api-key" {
		t.Errorf("Get = %q, want dummy-api-key", got)
	}

	if err := Delete("profile-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("profile-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("削除後の Get のエラー = %v, want ErrNotFound", err)
	}
	// 削除は冪等
	if err := Delete("profile-1"); err != nil {
		t.Errorf("二重削除がエラーになった: %v", err)
	}
}

func TestSave_EmptyProfileID(t *testing.T) {
	keyring.MockInit()
	if err := Save("", "key"); err == nil {
		t.Error("空のプロファイル ID で保存できてしまった")
	}
}
