// Package store はローカル SQLite キャッシュ(modernc.org/sqlite、純 Go・CGO 不要)。
// DB はスペース × 認証ユーザごとに 1 ファイル(設計書 2 節)。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // driver name: "sqlite"
)

// dbtx は *sql.DB と *sql.Tx の共通インターフェース。
// リポジトリ関数はこれを受け取り、単発実行(DB)とトランザクション(Tx)の
// どちらでも動作する。
type dbtx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// コンパイル時チェック: *sql.DB / *sql.Tx が dbtx を満たすこと。
var (
	_ dbtx = (*sql.DB)(nil)
	_ dbtx = (*sql.Tx)(nil)
)

// Store は 1 つの DB ファイルへの接続。
type Store struct {
	db   *sql.DB
	path string
}

// DefaultDataDir は DB 置き場(os.UserConfigDir()/backlog-assistant/data)を返す。
func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("ユーザ設定ディレクトリを取得できません: %w", err)
	}
	return filepath.Join(base, "backlog-assistant", "data"), nil
}

// DBPathIn は baseDir 配下の DB ファイルパス(<ホスト名>_<ユーザID>.db)を返す。
func DBPathIn(baseDir, host string, userID int) string {
	return filepath.Join(baseDir, fmt.Sprintf("%s_%d.db", sanitizeFileComponent(host), userID))
}

// DBPath は既定データディレクトリ配下の DB ファイルパスを返す。
func DBPath(host string, userID int) (string, error) {
	dir, err := DefaultDataDir()
	if err != nil {
		return "", err
	}
	return DBPathIn(dir, host, userID), nil
}

// sanitizeFileComponent はホスト名等をファイル名に安全な形へ変換する。
func sanitizeFileComponent(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, s)
}

// dsnFor は DB ファイルパスから接続文字列を組み立てる。
//
// foreign_keys は接続単位の設定で、PRAGMA を 1 度実行しただけでは
// 「その接続」にしか効かない。database/sql のプールが接続を破棄して
// 張り直すと参照整合性が黙って無効になり、v4 で導入した FK 制約
// (孤児行の防止。R8)が働かなくなる。DSN に載せておけば、
// ドライバがすべての新規接続で適用する。
//
// パスに '?' を含む場合はエラーにする。modernc.org/sqlite は
// "file:" で始まらない DSN の '?' 以降をクエリとして解釈するため、
// 素通ししてもパスは切り詰められ(別の場所に DB を作ってしまう)、
// クエリの指定も壊れる。file: URI 形式でパーセントエンコードすれば
// 扱えるが、Windows のドライブレター・UNC パスまで正しく URI 化するのは
// 複雑で、得られるものは「実運用で発生しないパスへの対応」でしかない。
// DB パスは設定ディレクトリ配下に DBPathIn が組み立てるもの(ホスト名は
// sanitizeFileComponent 済み)なので、明確なエラーで十分に安全と判断した。
// なお '#' や '%' はドライバもドライバ経由の SQLite も特別扱いしない
// (URI として解釈されるのは "file:" で始まる名前だけ)ため、対象外。
func dsnFor(path string) (string, error) {
	if strings.Contains(path, "?") {
		return "", fmt.Errorf("DB ファイルのパスに '?' は使えません: %s", path)
	}
	return path + "?_pragma=foreign_keys(1)", nil
}

// Open は DB ファイルを開き(無ければ作成し)、マイグレーションを適用する。
//
// ファイル権限: SQLite に作成を任せると umask 依存(0644 等)になり得るため、
// 先に 0600 で作成し、既存ファイルにも 0600 を適用する。
// WAL/SHM ファイルは SQLite が DB 本体の権限を引き継いで作成するため、
// DB 本体を 0600 にしておけば十分。
func Open(path string) (*Store, error) {
	// 接続文字列を先に組み立てて検証する(ファイルを作る前に弾く)
	dsn, err := dsnFor(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("データディレクトリの作成に失敗しました: %w", err)
	}
	// MkdirAll は既存ディレクトリの権限を変更しないため、毎回 0700 へ揃える
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("データディレクトリの権限設定に失敗しました: %w", err)
	}
	f, ferr := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if ferr != nil {
		return nil, fmt.Errorf("DB ファイルの作成に失敗しました: %w", ferr)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	// 既存ファイル(過去バージョンで 0644 等になっていた場合)にも 0600 を適用
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("DB ファイルの権限設定に失敗しました: %w", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("DB を開けません: %w", err)
	}
	// modernc.org/sqlite は同時書き込みに弱いため接続数を絞る
	db.SetMaxOpenConns(1)
	// journal_mode は DB ファイルに永続化されるためここで 1 度設定すればよい
	// (foreign_keys は接続単位なので DSN 側で設定する。dsnFor 参照)。
	if _, err := db.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("PRAGMA の設定に失敗しました: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	// 保持期限を過ぎた完了ジョブの整理(R2)。DB を開くのはアプリ起動後の
	// 初回アクセス時なので、ここが実質「起動時の 1 回」になる。
	// 失敗は握りつぶさずエラーにする(マイグレーションと同じ書き込み権限しか
	// 要らないため、ここで失敗する DB はそもそも使えない)。
	if _, err := s.PurgeExpiredJobs(context.Background(), time.Now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("期限切れジョブの整理に失敗しました: %w", err)
	}
	return s, nil
}

// Close は DB 接続を閉じる。
func (s *Store) Close() error { return s.db.Close() }

// Path は DB ファイルパスを返す。
func (s *Store) Path() string { return s.path }

// DB は下位レイヤの *sql.DB を返す(リポジトリ関数・テスト用)。
func (s *Store) DB() *sql.DB { return s.db }

// WithTx はトランザクション内で fn を実行するヘルパー。
// fn がエラーを返せばロールバック、成功すればコミットする。
// 接続数は 1 に制限しているため、fn の中から Store の別メソッドで
// DB へ直接アクセスしてはならない(デッドロックする)。tx を使うこと。
func (s *Store) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("トランザクションを開始できません: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return fmt.Errorf("%w(ロールバックにも失敗: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("コミットに失敗しました: %w", err)
	}
	return nil
}

// WithReadTx は「複数クエリの結果を 1 つのスナップショットで揃える」ための
// 読み取り専用トランザクションで fn を実行する(中 2)。
// 接続数を 1 に制限しているため、トランザクション保持中は同期の書き込みが
// 割り込めず、件数と行の取得結果が食い違わない。
// 読み取り専用なので終了時は常にロールバックする(コミットは不要)。
// fn の中では tx のみを使うこと(Store の別メソッドを呼ぶとデッドロックする)。
func (s *Store) WithReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("トランザクションを開始できません: %w", err)
	}
	// 読み取りのみのため、ロールバック失敗は結果に影響しない(ベストエフォート)
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

// RemoveDatabase は既定データディレクトリから、指定ホスト × ユーザ ID の
// DB ファイル(と WAL/SHM)のみを削除する(プロファイル削除時)。
// 同一ホストの別ユーザ(別プロファイル)の DB は削除しない。
func RemoveDatabase(host string, userID int) error {
	dir, err := DefaultDataDir()
	if err != nil {
		return err
	}
	return RemoveDatabaseIn(dir, host, userID)
}

// RemoveDatabaseIn は指定ディレクトリ版(テスト用にも使用)。
func RemoveDatabaseIn(baseDir, host string, userID int) error {
	base := DBPathIn(baseDir, host, userID)
	var firstErr error
	for _, p := range []string{base, base + "-wal", base + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// RemoveDatabasesForHost は既定データディレクトリから、指定ホストの
// DB ファイル(<host>_*.db と WAL/SHM)をすべて削除する。
// 注意: 同一ホストの別ユーザの DB も消えるため、プロファイル削除経路では
// 使わない(RemoveDatabase を使う)。全データ消去等の明示操作専用。
func RemoveDatabasesForHost(host string) error {
	dir, err := DefaultDataDir()
	if err != nil {
		return err
	}
	return RemoveDatabasesForHostIn(dir, host)
}

// RemoveDatabasesForHostIn は指定ディレクトリ版(テスト用にも使用)。
func RemoveDatabasesForHostIn(baseDir, host string) error {
	prefix := sanitizeFileComponent(host) + "_"
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !(strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm")) {
			continue
		}
		if err := os.Remove(filepath.Join(baseDir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// GetMeta は meta テーブルの値を返す(無ければ空文字)。
func (s *Store) GetMeta(key string) (string, error) {
	return GetMeta(context.Background(), s.db, key)
}

// SetMeta は meta テーブルの値を UPSERT する。
func (s *Store) SetMeta(key, value string) error {
	return SetMeta(context.Background(), s.db, key, value)
}

// GetMeta は meta テーブルの値を返す(無ければ空文字)。
func GetMeta(ctx context.Context, q dbtx, key string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// SetMeta は meta テーブルの値を UPSERT する。
func SetMeta(ctx context.Context, q dbtx, key, value string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
