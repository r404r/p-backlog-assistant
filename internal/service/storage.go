package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"backlog-assistant/internal/backlogclient"
)

// DatabaseInfo は 1 プロファイル分のローカル DB の所在とサイズ(画面表示用)。
type DatabaseInfo struct {
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	// Path は DB ファイルのパス。接続実績が無い(LastUserID = 0)場合や
	// スペース URL が不正でホストを特定できない場合は空文字。
	Path string `json:"path"`
	// SizeBytes は DB 本体 + WAL + SHM のうち、存在するファイルの合計バイト数。
	// 本体が無く WAL / SHM だけが残っている場合もその分を計上する
	// (占有量を過小に見せないため)。
	// 内訳は返さない(利用者に必要なのは「どれだけ占有しているか」のみ)。
	SizeBytes int64 `json:"sizeBytes"`
	// Exists は DB 本体ファイル(.db)が存在するかどうか。
	// SizeBytes > 0 でも偽になりうる(WAL / SHM のみ残存している場合)。
	Exists bool `json:"exists"`
	// Error は所在・サイズを確認できなかった理由(正常時は空文字)。
	// 「未作成」(Exists = false・Error = 空)と、確認そのものに失敗した状態
	// (URL 不正・権限不足・I/O エラー)を UI で区別するために持つ。
	Error string `json:"error"`
}

// StorageInfo は保存データ(設定ファイル・ローカル DB)の所在(画面表示用)。
// 動作ログのパスは app.go 側で合流させる(バインディングは 1 メソッドに保つ)。
type StorageInfo struct {
	ConfigDir string `json:"configDir"`
	// StorageMode は保存先の決定方法("default" / "env" / "portable")。
	// 明示指定が使えない場合は起動時にエラーにする設計のため、この 3 値のみ
	// (フォールバック状態は存在しない)。
	StorageMode string         `json:"storageMode"`
	Databases   []DatabaseInfo `json:"databases"`
}

// GetStorageInfo は設定ディレクトリと、プロファイルごとのローカル DB の
// 所在・サイズを返す(ファイルの読み書きはせず、os.Stat のみ)。
//
// パスを解決できない・参照できないプロファイルがあっても全体をエラーにせず、
// 当該プロファイルの Error に理由を入れて一覧に含める(1 件の異常で画面全体を
// 空にしないため)。未作成(ファイルがまだ無い)は異常ではないので Error は空。
//
// 保存済み設定を読むため、入口で profileMu.RLock を取り
// SaveProfile / DeleteProfile と排他する(他のロックは取らない)。
func (s *ProfileService) GetStorageInfo() (*StorageInfo, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()

	profiles, err := s.cfg.List()
	if err != nil {
		return nil, err
	}
	// nil のまま返すとフロントで null になるため空スライスで初期化する
	databases := make([]DatabaseInfo, 0, len(profiles))
	for i := range profiles {
		p := &profiles[i]
		databases = append(databases, s.databaseInfoFor(p.ID, p.Name, p.SpaceURL, p.LastUserID))
	}
	return &StorageInfo{
		// config.json の置き場所 = アプリの設定ディレクトリ
		ConfigDir:   filepath.Dir(s.cfg.Path()),
		StorageMode: s.storageMode(),
		Databases:   databases,
	}, nil
}

// databaseInfoFor は 1 プロファイル分の DB 情報を組み立てる。
func (s *ProfileService) databaseInfoFor(profileID, name, spaceURL string, lastUserID int) DatabaseInfo {
	info := DatabaseInfo{ProfileID: profileID, ProfileName: name}
	if lastUserID == 0 {
		// 接続テスト前は DB ファイル名が決まらない(未作成であり、異常ではない)
		return info
	}
	path, err := s.dbPathForProfile(spaceURL, lastUserID)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Path = path
	size, exists, serr := databaseTotalSize(path)
	info.SizeBytes, info.Exists = size, exists
	if serr != nil {
		info.Error = serr.Error()
	}
	return info
}

// dbPathForProfile はスペース URL と接続ユーザ ID から DB ファイルのパスを解決する。
// 解決できない場合は理由付きのエラーを返す(UI で「未作成」と区別するため)。
func (s *ProfileService) dbPathForProfile(spaceURL string, lastUserID int) (string, error) {
	host, err := backlogclient.SpaceHost(spaceURL)
	if err != nil {
		return "", fmt.Errorf("スペース URL からホスト名を特定できません: %w", err)
	}
	path, err := s.dbPathFor(host, lastUserID)
	if err != nil {
		return "", fmt.Errorf("ローカル DB のパスを特定できません: %w", err)
	}
	return path, nil
}

// databaseTotalSize は DB 本体と WAL / SHM のうち存在するものの合計サイズ、
// 本体(.db)の有無、および参照エラーを返す。
//
// 未作成(os.IsNotExist)はエラーにしない。権限不足・I/O エラー等は
// 「未作成」と意味が異なるため、最初の 1 件をエラーとして返す(表示は縮退させ、
// 取得できたファイルのサイズは合計に残す)。
func databaseTotalSize(path string) (int64, bool, error) {
	var (
		total   int64
		exists  bool
		firstEr error
	)
	for i, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if err != nil {
			// 未作成は正常。それ以外(権限不足等)は理由を残す
			if !errors.Is(err, os.ErrNotExist) && firstEr == nil {
				firstEr = fmt.Errorf("ファイルの情報を取得できません: %w", err)
			}
			continue
		}
		if i == 0 {
			exists = true // 本体の有無は先頭(.db)だけで判定する
		}
		total += st.Size()
	}
	return total, exists, firstEr
}
