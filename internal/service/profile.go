// Package service はユースケース層。Wails バインディング(app.go)は
// このパッケージへ委譲するだけの薄い層に保つ(設計書 1 節)。
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/kenzo0107/backlog"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/secret"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

// connector は service 層が Backlog クライアントに要求する操作の最小集合。
// 実体は *backlogclient.Client(テストではフェイクに差し替える)。
// 同期エンジンが使う取得系は syncpkg.API をそのまま埋め込む。
type connector interface {
	TestConnection(ctx context.Context) (*backlogclient.ConnectionInfo, error)
	GetUsers(ctx context.Context) ([]*backlog.User, error)
	GetTeams(ctx context.Context) ([]*backlog.Team, error)
	InitRateLimit(ctx context.Context) error
	syncpkg.API
	// 一括更新・追加(書き込み + マスタ取得)。GetIssue は syncpkg.API と
	// 同じシグネチャのため重複してもよい(埋め込みインターフェースの重複メソッド)。
	bulk.API
}

// コンパイル時チェック: *backlogclient.Client が connector を満たすこと。
var _ connector = (*backlogclient.Client)(nil)

// rateLimitInitRetryInterval は InitRateLimit 失敗後の再試行間隔。
const rateLimitInitRetryInterval = 30 * time.Second

// clientEntry はクライアントキャッシュの 1 エントリ。
// InitRateLimit の初期化状態を保持し、失敗したままのプロファイルが
// 恒久的にレート制限パススルーで動き続けないよう再試行を管理する(中 1)。
type clientEntry struct {
	client        connector
	rlInitialized bool      // InitRateLimit が成功済みか
	nextInitRetry time.Time // 未初期化時の次回再試行時刻
}

// ProfileService は接続プロファイル関連のユースケースを担う。
type ProfileService struct {
	cfg *config.Manager
	// newClient はテスト差し替え用のファクトリ。
	newClient func(spaceURL, apiKey string) (connector, error)
	// removeDB はローカル DB 削除処理(テスト差し替え用)。
	removeDB func(host string, userID int) error
	// dbPathFor はローカル DB のパス解決(テスト差し替え用)。
	dbPathFor func(host string, userID int) (string, error)
	// openStore はローカル DB のオープン(テスト差し替え用)。
	openStore func(path string) (*store.Store, error)
	// now は現在時刻の取得(テスト差し替え用)。
	now func() time.Time

	// profileMu はプロファイルのライフサイクル(作成・更新・削除)と、
	// そのプロファイルを使う操作(同期・store 参照)を排他する(高 2)。
	//   - Lock  : SaveProfile / DeleteProfile
	//   - RLock : SyncProjects / SyncIssues / SearchIssues / ListFilterOptions /
	//             GetSyncState / ListSyncStates / ListProjects
	//             (= storeForProfile を使う操作)
	//             TestConnectionForProfile / GetPermissionStatus
	//             (= 保存済み設定・キーチェーン・クライアントキャッシュを使う操作。中 1)
	// これが無いと、削除直後に並行中の同期が古いプロファイル情報で
	// ローカル DB を再作成しうる。
	//
	// ロック順序は必ず profileMu → opMu → (syncMu | clientsMu | storesMu)。
	// 逆順取得は作らないこと。特に storeForProfile / clientForProfile /
	// closeStore / invalidateClient は profileMu を取らない(RLock は
	// 再入不可であり、待機中の Lock があると入れ子 RLock が自己デッドロック
	// するため、profileMu の取得は必ず公開メソッドの入口 1 か所に限る)。
	profileMu sync.RWMutex

	// opMu は SaveProfile / DeleteProfile の全体を直列化する(高 3)。
	// config・キーチェーン・DB を跨ぐ複合操作の交錯を防ぐ。
	// ロック順序は必ず opMu → clientsMu の順のみ(逆順取得を作らないこと)。
	opMu sync.Mutex

	// clients は保存済みプロファイル ID → クライアントのキャッシュ。
	// リクエストごとにクライアントを作り直すとレート制限の状態が失われるため、
	// プロファイル単位で使い回す。
	clientsMu sync.Mutex
	clients   map[string]*clientEntry

	// stores は保存済みプロファイル ID → ローカル DB のキャッシュ。
	// DB は「スペースホスト × 認証ユーザ ID」ごとに 1 ファイル(設計書 2 節)。
	// ロック順序は opMu → clientsMu / storesMu(逆順取得を作らないこと)。
	storesMu sync.Mutex
	stores   map[string]*storeEntry

	// syncMu は同期処理(API 取得 + DB 書き込み)を直列化する。
	// SQLite の接続数を 1 に絞っているため、並行同期は待ちを生むだけで
	// 利点が無く、進捗表示も分かりにくくなる。
	syncMu sync.Mutex

	// onProgress は同期進捗の通知先(app.go が設定する)。
	progressMu sync.Mutex
	onProgress SyncProgressFunc

	// bulkCancels は実行中の一括更新ジョブ(プロファイル + ジョブ ID)→ キャンセルフラグ。
	// 実行中に別スレッドから中断を指示するため、他のロックとは独立させる
	// (syncMu を取ると実行完了まで待たされ、キャンセルにならない)。
	bulkMu      sync.Mutex
	bulkCancels map[bulkRunKey]*atomic.Bool
}

// bulkRunKey は実行中ジョブの識別子(中 2)。
// ジョブ ID はプロファイル(ローカル DB)ごとの採番であり、ID だけをキーにすると
// 別プロファイルの同番ジョブを誤って中断しうるため、プロファイル ID と組で扱う。
type bulkRunKey struct {
	profileID string
	jobID     int64
}

// storeEntry はローカル DB キャッシュの 1 エントリ。
// path を保持し、接続ユーザ変更で DB ファイルが変わったら開き直す。
type storeEntry struct {
	store *store.Store
	path  string
}

// NewProfileService は既定の構成で ProfileService を生成する。
func NewProfileService(cfg *config.Manager) *ProfileService {
	return &ProfileService{
		cfg: cfg,
		newClient: func(spaceURL, apiKey string) (connector, error) {
			return backlogclient.New(spaceURL, apiKey)
		},
		removeDB:    store.RemoveDatabase,
		dbPathFor:   store.DBPath,
		openStore:   store.Open,
		now:         time.Now,
		clients:     map[string]*clientEntry{},
		stores:      map[string]*storeEntry{},
		bulkCancels: map[bulkRunKey]*atomic.Bool{},
	}
}

// keyFingerprint は API キーのフィンガープリント(SHA-256 hex 先頭 16 文字)を返す。
// config.json にはこの値のみ保存し、キー本体は保存しない。
func keyFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])[:16]
}

// errKeyMismatch はキーチェーンのキーと config のフィンガープリントの不一致エラー。
var errKeyMismatch = errors.New("保存された API キーが設定と一致しません。API キーを入力し直してください")

// verifyStoredKey はキーチェーンから取得したキーが config のフィンガープリントと
// 一致するか照合する(高 1(b))。キーチェーン保存後・config 保存前のクラッシュで
// 残った「新キー + 旧設定」の不整合を、不一致キーで API へ送信する前に検知する。
// フィンガープリントが空の既存プロファイルは後方互換として照合をスキップする。
func verifyStoredKey(profile *config.Profile, apiKey string) error {
	if profile.KeyFingerprint == "" {
		return nil // 旧バージョンで保存されたプロファイル(後方互換)
	}
	if keyFingerprint(apiKey) != profile.KeyFingerprint {
		return errKeyMismatch
	}
	return nil
}

// clientForProfile は保存済みプロファイル用のクライアントをキャッシュから返す。
// 初回生成時は GET /rateLimit でレートリミッタを初期化する。初期化失敗は
// エラーにしない(上限取得の失敗で本来の操作まで失敗させない)が、
// パススルーのまま放置せず rateLimitInitRetryInterval 経過ごとに再試行する(中 1)。
func (s *ProfileService) clientForProfile(ctx context.Context, profileID string) (connector, error) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if e, ok := s.clients[profileID]; ok {
		s.retryInitLocked(ctx, e)
		return e.client, nil
	}
	profile, err := s.cfg.Get(profileID)
	if err != nil {
		return nil, err
	}
	apiKey, err := secret.Get(profileID)
	if err != nil {
		return nil, err
	}
	// キーチェーンのキーが config と整合しているか照合する(高 1(b))
	if err := verifyStoredKey(profile, apiKey); err != nil {
		return nil, err
	}
	client, err := s.newClient(profile.SpaceURL, apiKey)
	if err != nil {
		return nil, err
	}
	e := &clientEntry{client: client}
	if ierr := client.InitRateLimit(ctx); ierr != nil {
		// ベストエフォート: 失敗しても本来の操作は続行し、後で再試行する
		e.nextInitRetry = s.now().Add(rateLimitInitRetryInterval)
	} else {
		e.rlInitialized = true
	}
	s.clients[profileID] = e
	return client, nil
}

// retryInitLocked は未初期化エントリの InitRateLimit を再試行する(clientsMu 保持前提)。
// 失敗したら次回再試行時刻を rateLimitInitRetryInterval 後に更新する。
func (s *ProfileService) retryInitLocked(ctx context.Context, e *clientEntry) {
	if e.rlInitialized || s.now().Before(e.nextInitRetry) {
		return
	}
	if err := e.client.InitRateLimit(ctx); err != nil {
		e.nextInitRetry = s.now().Add(rateLimitInitRetryInterval)
		return
	}
	e.rlInitialized = true
}

// invalidateClient はプロファイルのクライアントキャッシュを破棄する
// (キー変更・URL 変更・プロファイル削除時)。
// 接続ユーザが変われば参照する DB ファイルも変わるため、
// キャッシュしているローカル DB 接続も閉じる。
// ロックは入れ子にせず順に取得する(clientsMu → storesMu)。
func (s *ProfileService) invalidateClient(profileID string) {
	s.clientsMu.Lock()
	delete(s.clients, profileID)
	s.clientsMu.Unlock()
	s.closeStore(profileID)
}

// ListProfiles は保存済みプロファイル一覧を返す。
func (s *ProfileService) ListProfiles() ([]config.Profile, error) {
	return s.cfg.List()
}

// GetActiveProfile は現在の接続先プロファイル ID を返す(未選択なら空文字)。
func (s *ProfileService) GetActiveProfile() (string, error) {
	return s.cfg.GetActiveProfileID()
}

// SetActiveProfile は接続先プロファイル ID を保存する(空文字 = 選択解除)。
func (s *ProfileService) SetActiveProfile(id string) error {
	return s.cfg.SetActiveProfileID(id)
}

// SaveProfileResult は保存結果(保存されたプロファイル + 接続テスト結果)。
type SaveProfileResult struct {
	Profile    config.Profile                `json:"profile"`
	Connection *backlogclient.ConnectionInfo `json:"connection"`
}

// validateAPIKey はトリム後の API キーの形式を検証する。
// 公式仕様はキーの文字種を規定していないため、拒否するのは
// 「どのような資格情報形式でもあり得ない」内部の空白・制御文字のみに限定する
// (コピー範囲の誤り確実。それ以外はサーバの判定に委ねる)。
func validateAPIKey(apiKey string) error {
	for _, r := range apiKey {
		// Unicode の空白(U+00A0 等)・制御文字(U+0085 等)も対象にする。
		// それ以外の非 ASCII 文字は拒否しない(正当なキーを弾かない)
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New("API キーの途中に空白または改行が含まれています。コピー範囲を確認してください")
		}
	}
	return nil
}

// dbReferencedByOthers は excludeID 以外のプロファイルに、同一 (host, userID) の
// ローカル DB を参照するものがあるかを返す(中 2 の共有参照チェック)。
func (s *ProfileService) dbReferencedByOthers(excludeID, host string, userID int) (bool, error) {
	profiles, err := s.cfg.List()
	if err != nil {
		return false, err
	}
	for i := range profiles {
		p := &profiles[i]
		if p.ID == excludeID || p.LastUserID != userID {
			continue
		}
		h, herr := backlogclient.SpaceHost(p.SpaceURL)
		if herr != nil {
			continue // URL が不正なプロファイルは参照とみなさない
		}
		if h == host {
			return true, nil
		}
	}
	return false, nil
}

// SaveProfile はプロファイルを保存する。保存前に必ず接続テスト
// (GET /users/myself)を実行し、成功した場合のみ config.json と
// OS キーチェーンへ保存する(設計書 4.1 節)。
//
// id が空なら新規作成。apiKey が空で既存プロファイルの場合は
// キーチェーンの既存キーを使う(URL・名前のみの変更)。
//
// 整合性: キーチェーン保存 → config 保存の順で行い、config 保存に失敗したら
// キーチェーンを保存前の状態へ戻す(既存キーは復元、新規キーは削除)。
// さらに config にはキーのフィンガープリントを保存し、キーチェーン保存後・
// config 保存前のクラッシュで残る「新キー + 旧設定」を読み出し時に検知する(高 1)。
func (s *ProfileService) SaveProfile(ctx context.Context, id, name, spaceURL, apiKey string) (*SaveProfileResult, error) {
	// 実行中の同期・store 利用操作と排他する(高 2。ロック順序 profileMu → opMu)
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	// SaveProfile / DeleteProfile を跨ぐ複合操作を直列化する(高 3)
	s.opMu.Lock()
	defer s.opMu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("プロファイル名が空です")
	}
	// コピー&ペースト由来の前後空白・改行を除去する(混入すると API が 401 を返す)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" { // 空は「既存キーを維持」の合図なので検証しない
		if err := validateAPIKey(apiKey); err != nil {
			return nil, err
		}
	}
	canonical, err := backlogclient.ValidateSpaceURL(spaceURL)
	if err != nil {
		return nil, err
	}

	isNew := id == ""
	var existing *config.Profile
	if isNew {
		id, err = newProfileID()
		if err != nil {
			return nil, err
		}
	} else {
		existing, err = s.cfg.Get(id)
		if err != nil {
			return nil, err
		}
	}

	if apiKey == "" {
		if isNew {
			return nil, errors.New("API キーが入力されていません")
		}
		apiKey, err = secret.Get(id)
		if err != nil {
			return nil, fmt.Errorf("既存の API キーを取得できません(API キーを入力し直してください): %w", err)
		}
		// キー流用経路でもフィンガープリントを照合する(高 1(b)。
		// 不整合キーのまま接続テスト・再保存して不整合を固定化しない)
		if err := verifyStoredKey(existing, apiKey); err != nil {
			return nil, err
		}
	}

	// 保存前の接続テスト(成功時のみ保存)
	client, err := s.newClient(canonical, apiKey)
	if err != nil {
		return nil, err
	}
	info, err := client.TestConnection(ctx)
	if err != nil {
		return nil, err
	}

	// 補償用に上書き前の既存キーを退避する。not-found は「旧キー無し」として
	// 続行するが、それ以外の取得エラーは補償不能な上書きを避けるため中断する(高 1(a))
	oldKey := ""
	hadOldKey := false
	if !isNew {
		k, gerr := secret.Get(id)
		switch {
		case gerr == nil:
			oldKey, hadOldKey = k, true
		case errors.Is(gerr, secret.ErrNotFound):
			// 旧キー無し(補償時は新キーを削除する)
		default:
			return nil, fmt.Errorf("既存 API キーの退避に失敗したため保存を中断しました(再試行してください): %w", gerr)
		}
	}

	// キーチェーンへ先に保存する(こちらが失敗したら config も保存しない)
	if err := secret.Save(id, apiKey); err != nil {
		return nil, err
	}
	profile := config.Profile{
		ID:             id,
		Name:           name,
		SpaceURL:       canonical,
		LastUserName:   info.Name,
		LastUserID:     info.UserID, // DB ファイルの特定(削除時)に使用
		KeyFingerprint: keyFingerprint(apiKey),
	}
	if err := s.cfg.Upsert(profile); err != nil {
		// 補償: キーチェーンを保存前の状態へ戻す(復元失敗はベストエフォート)
		var rerr error
		if hadOldKey {
			rerr = secret.Save(id, oldKey)
		} else {
			rerr = secret.Delete(id)
		}
		if rerr != nil {
			return nil, fmt.Errorf("設定の保存に失敗し、キーチェーンの復元にも失敗しました(API キーを保存し直してください): %w", err)
		}
		return nil, fmt.Errorf("設定の保存に失敗しました(API キーは変更されていません): %w", err)
	}

	// API キー変更で接続ユーザが変わった場合、旧 (host, LastUserID) の DB は
	// このプロファイルからは参照されなくなる。他プロファイルからも参照されて
	// いなければ孤児 DB としてベストエフォートで削除する(中 2(b)。
	// 削除失敗は無視する: 残っても孤児ファイルが占有するだけで動作に影響しないため)
	if existing != nil && existing.LastUserID != 0 && existing.LastUserID != info.UserID {
		if oldHost, herr := backlogclient.SpaceHost(existing.SpaceURL); herr == nil {
			if shared, serr := s.dbReferencedByOthers(id, oldHost, existing.LastUserID); serr == nil && !shared {
				// 開いたままのファイルは削除できない(Windows)ため先に閉じる
				s.closeStore(id)
				_ = s.removeDB(oldHost, existing.LastUserID)
			}
		}
	}

	// URL・キーが変わった可能性があるためキャッシュを無効化する
	s.invalidateClient(id)
	return &SaveProfileResult{Profile: profile, Connection: info}, nil
}

// DeleteProfile はプロファイルを削除する。OS キーチェーンの API キーは必ず削除し、
// deleteDB が真なら当該プロファイルのローカル DB も削除する(設計書 4.1 節)。
//
// 実行順序: DB 削除(要求時)→ キーチェーン削除 → config 削除。
// DB 削除に失敗した場合はプロファイルを残したまま中断し、再試行を可能にする
// (先に config を消すと DB の場所を特定できなくなり、データが残留するため)。
// DB は <ホスト>_<LastUserID>.db のみ削除し、同一ホストの別プロファイル
// (別ユーザ)の DB は巻き添えにしない。さらに同一 (host, LastUserID) を
// 参照する別プロファイルが残っている場合は DB 削除自体をスキップする(中 2(a))。
//
// config 削除が失敗した場合は、先に消したキーチェーンのキーを復元する
// (「設定は残っているのにキーだけ消えた」状態を作らないための補償。高 2)。
func (s *ProfileService) DeleteProfile(id string, deleteDB bool) error {
	// 実行中の同期・store 利用操作の完了を待ってから削除する(高 2)。
	// これにより、削除後に同期が store キャッシュ・DB を再作成することはない。
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	// SaveProfile / DeleteProfile を跨ぐ複合操作を直列化する(高 3)
	s.opMu.Lock()
	defer s.opMu.Unlock()

	profile, err := s.cfg.Get(id)
	if err != nil {
		return err
	}
	if deleteDB {
		host, err := backlogclient.SpaceHost(profile.SpaceURL)
		if err != nil {
			return err
		}
		// LastUserID が 0 のプロファイルは接続実績が無く DB も存在しない
		if profile.LastUserID != 0 {
			// 同一 (host, LastUserID) の DB を共有する別プロファイルが残っている
			// 場合は削除をスキップする(共有 DB の巻き添え削除を防ぐ。中 2(a))
			shared, err := s.dbReferencedByOthers(id, host, profile.LastUserID)
			if err != nil {
				return err
			}
			if !shared {
				// 開いたままのファイルは削除できない(Windows)ため先に閉じる
				s.closeStore(id)
				if err := s.removeDB(host, profile.LastUserID); err != nil {
					return fmt.Errorf("ローカル DB の削除に失敗しました(プロファイルは削除していません。再試行してください): %w", err)
				}
			}
		}
	}
	// キーチェーン削除前にキーを退避する(config 削除失敗時の復元用。高 2)。
	// not-found は「キー無し」として退避なしで続行し、それ以外の取得エラーは
	// 補償不能になるため中断する。
	backupKey := ""
	hasBackup := false
	if k, gerr := secret.Get(id); gerr == nil {
		backupKey, hasBackup = k, true
	} else if !errors.Is(gerr, secret.ErrNotFound) {
		return fmt.Errorf("API キーの退避に失敗したため削除を中断しました(再試行してください): %w", gerr)
	}
	// キーチェーンの削除は必須(設定だけ消えて資格情報が残る状態を作らない)
	if err := secret.Delete(id); err != nil {
		return err
	}
	if err := s.cfg.Delete(id); err != nil {
		// 補償: 先に消したキーをキーチェーンへ復元する(プロファイルは残っている
		// ため、キーだけ消えた状態を作らない。復元失敗はベストエフォート)
		if hasBackup {
			if rerr := secret.Save(id, backupKey); rerr != nil {
				s.invalidateClient(id)
				return fmt.Errorf("設定の削除に失敗し、API キーの復元にも失敗しました(API キーを保存し直してください): %w", err)
			}
		}
		s.invalidateClient(id)
		return err
	}
	s.invalidateClient(id)
	return nil
}

// TestConnection は入力値のまま接続テストを行う(保存はしない)。
func (s *ProfileService) TestConnection(ctx context.Context, spaceURL, apiKey string) (*backlogclient.ConnectionInfo, error) {
	client, err := s.newClient(spaceURL, apiKey)
	if err != nil {
		return nil, err
	}
	return client.TestConnection(ctx)
}

// TestConnectionForProfile は接続テストを行う。apiKey が空で profileID が
// 指定されている場合は、キーチェーンの既存キーを使う(SaveProfile と同じ規約)。
//
// 保存済み設定・キーチェーンを読むため、入口で profileMu.RLock を取り
// SaveProfile / DeleteProfile と排他する(中 1)。削除中のプロファイルの
// 旧キーで API を呼ばないための保証。SaveProfile(Lock 保持)からは
// このメソッドを呼ばない(内部では s.newClient を直接使う)。
func (s *ProfileService) TestConnectionForProfile(ctx context.Context, profileID, spaceURL, apiKey string) (*backlogclient.ConnectionInfo, error) {
	// コピー&ペースト由来の前後空白・改行を除去する(混入すると API が 401 を返す)
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" { // 空は「既存キーを使う」の合図なので形式検証しない
		if err := validateAPIKey(apiKey); err != nil {
			return nil, err
		}
	}
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()

	if apiKey == "" {
		if profileID == "" {
			return nil, errors.New("API キーが入力されていません")
		}
		profile, err := s.cfg.Get(profileID)
		if err != nil {
			return nil, err
		}
		apiKey, err = secret.Get(profileID)
		if err != nil {
			return nil, fmt.Errorf("既存の API キーを取得できません(API キーを入力し直してください): %w", err)
		}
		// キーチェーンのキーが config と整合しているか照合する(高 1(b))
		if err := verifyStoredKey(profile, apiKey); err != nil {
			return nil, err
		}
	}
	return s.TestConnection(ctx, spaceURL, apiKey)
}

// PermissionStatus は管理者系 API の利用可否(UI の機能縮退表示に使う)。
type PermissionStatus struct {
	AdminAvailable bool   `json:"adminAvailable"` // users・teams 一覧 API が両方利用可能か
	Degraded       bool   `json:"degraded"`       // 縮退状態(プロジェクト単位取得へフォールバック)
	Message        string `json:"message"`
}

// GetPermissionStatus は保存済みプロファイルで GET /users と GET /teams を
// 各 1 回呼び、実権限を確認する。両方成功なら管理者機能利用可、
// いずれかが 403 なら縮退状態(どちらが不可かをメッセージに含める)を返す。
//
// 保存済み設定・キーチェーン・クライアントキャッシュに触るため、入口で
// profileMu.RLock を取り SaveProfile / DeleteProfile と排他する(中 1)。
// ロック順序は profileMu → clientsMu(clientForProfile は profileMu を取らない)。
func (s *ProfileService) GetPermissionStatus(ctx context.Context, profileID string) (*PermissionStatus, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()

	client, err := s.clientForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	_, usersErr := client.GetUsers(ctx)
	_, teamsErr := client.GetTeams(ctx)

	denied := func(e error) bool { return errors.Is(e, backlogclient.ErrPermissionDenied) }
	// 403 以外の失敗(ネットワークエラー等)は権限判定できないためエラーを返す
	if usersErr != nil && !denied(usersErr) {
		return nil, usersErr
	}
	if teamsErr != nil && !denied(teamsErr) {
		return nil, teamsErr
	}

	switch {
	case usersErr == nil && teamsErr == nil:
		return &PermissionStatus{AdminAvailable: true, Message: "管理者機能を利用できます(ユーザ一覧・チーム一覧とも取得可能)"}, nil
	case denied(usersErr) && denied(teamsErr):
		return &PermissionStatus{
			Degraded: true,
			Message:  "管理者機能は利用不可です(ユーザ一覧・チーム一覧とも権限がありません。プロジェクト単位の取得に縮退します)",
		}, nil
	case denied(usersErr):
		return &PermissionStatus{
			Degraded: true,
			Message:  "一部の管理者機能が利用不可です(ユーザ一覧の権限がありません。ユーザ取得はプロジェクト単位に縮退します。チーム一覧は取得可能です)",
		}, nil
	default: // denied(teamsErr)
		return &PermissionStatus{
			Degraded: true,
			Message:  "一部の管理者機能が利用不可です(チーム一覧の権限がありません。ユーザ一覧は取得可能です)",
		}, nil
	}
}

// newProfileID は暗号乱数由来の 16 バイト hex ID を生成する。
func newProfileID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("プロファイル ID の生成に失敗しました: %w", err)
	}
	return hex.EncodeToString(b), nil
}
