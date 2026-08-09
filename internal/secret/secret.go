// Package secret は OS キーチェーン(Windows Credential Manager / macOS Keychain /
// Linux Secret Service)への API キー保存の薄いラッパー。
//
// セキュリティ方針: キーチェーンが使えない環境ではエラーを返す。
// 設定ファイル等への平文フォールバックは絶対に行わない。
package secret

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// serviceName はキーチェーン上のサービス識別子。
const serviceName = "backlog-assistant"

// ErrNotFound は API キーが保存されていない場合のエラー。
var ErrNotFound = errors.New("API キーがキーチェーンに保存されていません")

// Save はプロファイル ID をキーとして API キーを OS キーチェーンへ保存する。
func Save(profileID, apiKey string) error {
	if profileID == "" {
		return errors.New("プロファイル ID が空です")
	}
	if err := keyring.Set(serviceName, profileID, apiKey); err != nil {
		// 注意: err に apiKey が含まれることは無い(go-keyring はキー値をエラーに含めない)。
		return fmt.Errorf("OS キーチェーンへの保存に失敗しました(平文保存へのフォールバックは行いません): %w", err)
	}
	return nil
}

// Get はプロファイル ID に対応する API キーを取得する。
func Get(profileID string) (string, error) {
	v, err := keyring.Get(serviceName, profileID)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("OS キーチェーンからの取得に失敗しました: %w", err)
	}
	return v, nil
}

// Delete はプロファイル ID に対応する API キーを削除する。
// 既に存在しない場合はエラーにしない(冪等)。
func Delete(profileID string) error {
	if err := keyring.Delete(serviceName, profileID); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("OS キーチェーンからの削除に失敗しました: %w", err)
	}
	return nil
}
