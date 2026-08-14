// Package config は接続プロファイル(スペース URL 等の非機密設定)を管理する。
// API キーは絶対にここへ保存しない(OS キーチェーン = internal/secret を使う)。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"backlog-assistant/internal/storagepath"
)

// ErrProfileNotFound は指定 ID のプロファイルが存在しない場合のエラー。
var ErrProfileNotFound = errors.New("プロファイルが見つかりません")

// Profile は 1 つの接続先設定。API キーは含めない(OS キーチェーンに保存)。
type Profile struct {
	ID           string `json:"id"`           // キーチェーンのキーにも使う内部 ID
	Name         string `json:"name"`         // 表示名
	SpaceURL     string `json:"spaceUrl"`     // 例: https://example.backlog.jp
	LastUserName string `json:"lastUserName"` // 直近の接続テストで確認したユーザ表示名(UI 表示用)
	LastUserID   int    `json:"lastUserId"`   // 直近の接続テストで確認したユーザ ID(DB ファイル名の特定に使用)
	// KeyFingerprint はキーチェーンに保存した API キーの SHA-256 hex 先頭 16 文字。
	// キーチェーン保存後・config 保存前にクラッシュした場合の
	// 「新キー + 旧設定」の不整合を検知するために使う(キー本体は保存しない)。
	// 空文字は旧バージョンで保存されたプロファイル(照合スキップ = 後方互換)。
	KeyFingerprint string `json:"keyFingerprint"`
}

// Settings はプロファイル以外のアプリ設定。
type Settings struct {
	ActiveProfileID string `json:"activeProfileId"` // 現在の接続先プロファイル ID(空 = 未選択)
}

// Config は config.json 全体。
type Config struct {
	Profiles []Profile `json:"profiles"`
	Settings Settings  `json:"settings"`
}

// Manager は config.json の読み書きを担う。
// load-modify-save を Mutex で直列化し、並行呼び出しでの更新消失を防ぐ。
type Manager struct {
	mu   sync.Mutex
	path string
}

// DefaultDir はアプリの設定ディレクトリ(データ保存先の基点)を返す。
//
// 既定は os.UserConfigDir()/backlog-assistant だが、portable.txt または
// 環境変数 BACKLOG_ASSISTANT_HOME でカスタマイズできる(internal/storagepath)。
// 解決は起動時に 1 回だけ行われ、以降は同じ値を返す。
func DefaultDir() (string, error) {
	return storagepath.BaseDir()
}

// NewManager は既定の場所(データ保存先の基点/config.json)を使う Manager を返す。
func NewManager() (*Manager, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return NewManagerAt(dir), nil
}

// NewManagerAt は指定ディレクトリ配下の config.json を使う Manager を返す(テスト用にも使用)。
func NewManagerAt(dir string) *Manager {
	return &Manager{path: filepath.Join(dir, "config.json")}
}

// Path は config.json の絶対パスを返す。
func (m *Manager) Path() string { return m.path }

// Load は設定を読み込む。ファイルが無い場合は空の Config を返す(エラーにしない)。
func (m *Manager) Load() (*Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

// loadLocked は mu 保持前提の読み込み実装。
func (m *Manager) loadLocked() (*Config, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("設定ファイルの読み込みに失敗しました: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("設定ファイルの解析に失敗しました: %w", err)
	}
	return &cfg, nil
}

// Save は設定を書き込む。一時ファイル + fsync + rename で原子的かつ耐障害に更新し、
// 権限は 0600 とする。
func (m *Manager) Save(cfg *Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked(cfg)
}

// saveLocked は mu 保持前提の保存実装。
func (m *Manager) saveLocked(cfg *Config) error {
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("設定ディレクトリの作成に失敗しました: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("設定のシリアライズに失敗しました: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("一時ファイルの作成に失敗しました: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功後は no-op
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("設定の書き込みに失敗しました: %w", err)
	}
	// rename 前に必ず fsync する(電源断で空ファイル・破損 rename が残るのを防ぐ)
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("設定ファイルの同期に失敗しました: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		return fmt.Errorf("設定ファイルの更新に失敗しました: %w", err)
	}
	// 親ディレクトリの fsync はベストエフォート(rename の永続化。
	// Windows 等ディレクトリを開けない環境ではスキップされる)
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// List は全プロファイルを返す。
func (m *Manager) List() ([]Profile, error) {
	cfg, err := m.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Profiles == nil {
		// nil のまま返すと Wails バインディング経由で JSON null になり、
		// フロントエンドが配列前提でクラッシュする(初回起動の白画面バグ)
		return []Profile{}, nil
	}
	return cfg.Profiles, nil
}

// Get は ID でプロファイルを取得する。
func (m *Manager) Get(id string) (*Profile, error) {
	cfg, err := m.Load()
	if err != nil {
		return nil, err
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			p := cfg.Profiles[i]
			return &p, nil
		}
	}
	return nil, ErrProfileNotFound
}

// Upsert は同一 ID のプロファイルを置き換え、無ければ末尾に追加して保存する。
func (m *Manager) Upsert(p Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == p.ID {
			cfg.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Profiles = append(cfg.Profiles, p)
	}
	return m.saveLocked(cfg)
}

// Delete は ID のプロファイルを削除して保存する。存在しない場合は ErrProfileNotFound。
// 削除対象が接続先(activeProfileId)だった場合は選択を解除する。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.loadLocked()
	if err != nil {
		return err
	}
	next := cfg.Profiles[:0]
	found := false
	for _, p := range cfg.Profiles {
		if p.ID == id {
			found = true
			continue
		}
		next = append(next, p)
	}
	if !found {
		return ErrProfileNotFound
	}
	cfg.Profiles = next
	if cfg.Settings.ActiveProfileID == id {
		cfg.Settings.ActiveProfileID = ""
	}
	return m.saveLocked(cfg)
}

// GetActiveProfileID は現在の接続先プロファイル ID を返す(未選択なら空文字)。
// 保存済みプロファイルに存在しない ID が残っていた場合も空文字を返す。
func (m *Manager) GetActiveProfileID() (string, error) {
	cfg, err := m.Load()
	if err != nil {
		return "", err
	}
	id := cfg.Settings.ActiveProfileID
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == id {
			return id, nil
		}
	}
	return "", nil
}

// SetActiveProfileID は接続先プロファイル ID を保存する。
// 空文字は「未選択」として許可し、それ以外は存在するプロファイルのみ許可する。
func (m *Manager) SetActiveProfileID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.loadLocked()
	if err != nil {
		return err
	}
	if id != "" {
		found := false
		for i := range cfg.Profiles {
			if cfg.Profiles[i].ID == id {
				found = true
				break
			}
		}
		if !found {
			return ErrProfileNotFound
		}
	}
	cfg.Settings.ActiveProfileID = id
	return m.saveLocked(cfg)
}
