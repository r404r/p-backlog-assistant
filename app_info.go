package main

// app_info.go はアプリ情報画面向けのバインディング
// (バージョン・保存データの所在・動作ログの状態)。

import (
	"log/slog"

	"backlog-assistant/internal/service"
)

// LogInfo は動作ログの状態(frontend/src/lib/backend.ts の LogInfo と対)。
type LogInfo struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// GetLogInfo は動作ログの出力先と有効・無効を返す(画面の案内表示用)。
func (a *App) GetLogInfo() (*LogInfo, error) {
	return &LogInfo{Path: a.log.Path(), Enabled: a.log.Enabled()}, nil
}

// StorageInfo は保存データの所在(frontend/src/lib/backend.ts の StorageInfo と対)。
// 設定・ローカル DB(service 層が解決)と動作ログ(applog)を 1 つにまとめ、
// 画面側の呼び出しを 1 回で済ませる。
type StorageInfo struct {
	ConfigDir string                 `json:"configDir"`
	Databases []service.DatabaseInfo `json:"databases"`
	// LogPath / LogEnabled は GetLogInfo と同じ情報(アプリ情報画面での再取得を避ける)
	LogPath    string `json:"logPath"`
	LogEnabled bool   `json:"logEnabled"`
}

// GetStorageInfo は設定ディレクトリ・プロファイルごとのローカル DB・動作ログの
// 所在を返す(アプリ情報画面の「保存データ」表示用)。
//
// 動作ログにはパスを記録しない(パスにはユーザ名が含まれうるため。既存の
// maskPathInError と同じ方針)。記録するのは件数と有効・無効のみ。
func (a *App) GetStorageInfo() (*StorageInfo, error) {
	return appOp(a, "GetStorageInfo", nil,
		func(s *service.ProfileService) (*StorageInfo, []slog.Attr, error) {
			info, err := s.GetStorageInfo()
			if err != nil {
				return nil, nil, err
			}
			out := &StorageInfo{
				ConfigDir:  info.ConfigDir,
				Databases:  info.Databases,
				LogPath:    a.log.Path(),
				LogEnabled: a.log.Enabled(),
			}
			return out, []slog.Attr{
				slog.Int("count", len(out.Databases)),
				slog.Bool("logEnabled", out.LogEnabled),
			}, nil
		})
}

// AppVersionInfo はアプリのバージョン情報(frontend/src/lib/backend.ts の AppVersion と対)。
type AppVersionInfo struct {
	Version string `json:"version"`
}

// GetAppVersion はビルド時に埋め込まれたバージョンを返す(フッタ表示・問い合わせ時の特定用)。
func (a *App) GetAppVersion() (*AppVersionInfo, error) {
	return &AppVersionInfo{Version: version}, nil
}
