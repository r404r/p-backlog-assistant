// Package storagepath はデータ保存先(config.json と data/*.db を置く
// 基点フォルダ)の決定を担う。
//
// 決定規則(優先順位。設計「データ保存先カスタマイズ」§3.1):
//  1. ポータブル: アプリと同じフォルダに portable.txt があれば、その隣の userdata/
//     (macOS は .app バンドルの隣。バンドル内はアプリ更新で消えるため)
//  2. 環境変数: BACKLOG_ASSISTANT_HOME が非空ならその値(絶対パスのみ)
//  3. 既定: os.UserConfigDir()/backlog-assistant
//
// **明示指定(1・2)が使えない場合は既定へフォールバックせずエラーにする。**
// 黙って既定へ移ると設定・DB が別の場所に新規作成され、利用者からは
// 「データが消えた」ように見えるため(USB の一時切断時などに特に危険)。
//
// エラーには **生パスを含めない**(理由コードのみ)。任意の保存先には
// 顧客名等が含まれうるため、crash.txt・動作ログに残してはならない。
//
// 解決は起動時に 1 回だけ行い、実行中の切替はしない(DB 接続・同期排他と
// 干渉させないため)。本番用のグローバル解決は sync.OnceValues で包み、
// テストは Resolver に依存を注入して order-independent に検証する。
package storagepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// EnvVar は基点フォルダを指定する環境変数名。
	EnvVar = "BACKLOG_ASSISTANT_HOME"
	// MarkerFileName はポータブルモードのマーカーファイル名(内容は問わない)。
	MarkerFileName = "portable.txt"
	// PortableDirName はポータブルモードでデータを置くフォルダ名。
	PortableDirName = "userdata"
	// AppDirName は既定モードでユーザ設定ディレクトリ配下に作るフォルダ名。
	AppDirName = "backlog-assistant"
)

// Mode は保存先の決定方法。フォールバックを行わない設計のため、この 3 値のみ。
type Mode string

const (
	// ModeDefault は既定(ユーザ設定ディレクトリ配下)。
	ModeDefault Mode = "default"
	// ModeEnv は環境変数 BACKLOG_ASSISTANT_HOME による指定。
	ModeEnv Mode = "env"
	// ModePortable は portable.txt によるポータブルモード。
	ModePortable Mode = "portable"
)

// Reason は解決に失敗した理由コード(生パスの代わりに記録・表示する)。
type Reason string

const (
	// ReasonNotAbsolute は指定が絶対パスでない(~ や %VAR% は展開しない)。
	ReasonNotAbsolute Reason = "not-absolute"
	// ReasonQuestionMark は指定に '?' が含まれる(SQLite の DSN と衝突する)。
	ReasonQuestionMark Reason = "question-mark"
	// ReasonNotDirectory は指定の位置にフォルダ以外(通常ファイル等)がある。
	ReasonNotDirectory Reason = "not-directory"
	// ReasonCreateFailed はフォルダを作成できない。
	ReasonCreateFailed Reason = "create-failed"
	// ReasonNotWritable はフォルダへ書き込めない。
	ReasonNotWritable Reason = "not-writable"
	// ReasonNoUserConfigDir はユーザ設定ディレクトリを取得できない(既定モード)。
	ReasonNoUserConfigDir Reason = "no-user-config-dir"
	// ReasonNoExecutable は実行ファイルの位置が分からず、ポータブル指定の
	// 有無を確認できない。
	ReasonNoExecutable Reason = "no-executable"
	// ReasonMarkerCheckFailed は portable.txt の有無を確認できない
	// (権限不足・I/O エラー等。「存在しない」ことが確認できた場合は含まない)。
	ReasonMarkerCheckFailed Reason = "marker-check-failed"
)

// Error は保存先を決定できなかったことを表す。
//
// **メッセージに生パスを含めない**(理由コードと一般的な対処のみ)。
// 下位の os エラーもパスを含むため、意図的にラップしない。
type Error struct {
	Mode   Mode
	Reason Reason
}

func (e *Error) Error() string {
	return fmt.Sprintf("データの保存先を決定できません(指定方法=%s, 理由=%s): %s",
		e.Mode, e.Reason, reasonText(e.Reason))
}

// reasonText は理由コードに対応する日本語の説明を返す(パスは含めない)。
func reasonText(r Reason) string {
	switch r {
	case ReasonNotAbsolute:
		return "保存先は絶対パスで指定してください(相対パスや ~ ・ %VAR% は使えません)"
	case ReasonQuestionMark:
		return "保存先のパスに '?' は使えません"
	case ReasonNotDirectory:
		return "指定した保存先にフォルダ以外のファイルがあります"
	case ReasonCreateFailed:
		return "指定した保存先のフォルダを作成できません"
	case ReasonNotWritable:
		return "指定した保存先に書き込めません(取り外し・アクセス権をご確認ください)"
	case ReasonNoUserConfigDir:
		return "ユーザ設定ディレクトリを取得できません"
	case ReasonNoExecutable:
		return "実行ファイルの位置を特定できないため、ポータブルモードかどうかを判定できません"
	case ReasonMarkerCheckFailed:
		return "ポータブルモードのマーカーファイル(" + MarkerFileName + ")の有無を確認できません"
	default:
		return "原因を特定できません"
	}
}

// Resolved は解決結果(基点フォルダとその決定方法)。
type Resolved struct {
	// BaseDir は config.json と data/ を置く基点フォルダ。
	BaseDir string
	// Mode は基点の決定方法。
	Mode Mode
}

// ---- 純粋部分(ファイルシステムに触れない) ----------------------------------

// selection は解決の入力のうち、ファイルシステム参照を済ませた後の値。
type selection struct {
	// PortableBase はマーカーが見つかった場合のポータブル基点(見つからなければ空)。
	PortableBase string
	// Env は環境変数 BACKLOG_ASSISTANT_HOME の値。
	Env string
	// UserConfigDir は os.UserConfigDir() の値。
	UserConfigDir string
	// UserConfigErr は os.UserConfigDir() のエラー。
	UserConfigErr error
}

// selectBase は優先順位に従って基点とモードを決め、明示指定には検証規則を適用する。
// ファイルシステムには一切触れない(存在確認・作成は Resolver 側で行う)。
func selectBase(in selection) (Resolved, error) {
	if in.PortableBase != "" {
		return validateExplicit(ModePortable, in.PortableBase)
	}
	if strings.TrimSpace(in.Env) != "" {
		// 値はそのまま使う(前後の空白も有効なパス構成要素になりうるため、
		// 「未指定かどうか」の判定にだけ TrimSpace を使う)
		return validateExplicit(ModeEnv, in.Env)
	}
	if in.UserConfigErr != nil {
		return Resolved{}, &Error{Mode: ModeDefault, Reason: ReasonNoUserConfigDir}
	}
	if in.UserConfigDir == "" {
		return Resolved{}, &Error{Mode: ModeDefault, Reason: ReasonNoUserConfigDir}
	}
	base := filepath.Join(in.UserConfigDir, AppDirName)
	// 既定パスは OS が返す絶対パスのため、絶対パス検証は行わない(ここで
	// エラーにすると、従来は起動できていた環境が起動できなくなる)。
	// ただし '?' だけは字句検証する — store.dsnFor が拒否するため、放置すると
	// 起動後に DB 操作のたびに理由の分かりにくい失敗を繰り返すことになる。
	if strings.Contains(base, "?") {
		return Resolved{}, &Error{Mode: ModeDefault, Reason: ReasonQuestionMark}
	}
	return Resolved{BaseDir: base, Mode: ModeDefault}, nil
}

// validateExplicit は明示指定(ポータブル・環境変数)の検証を行う。
//
//   - 絶対パスのみ許可する。GUI 起動時の作業ディレクトリは不定のため相対パスは
//     解釈できず、`~` や `%VAR%` も展開しない(絶対パスでないためここで弾かれる)。
//   - '?' を含むパスは SQLite の DSN と衝突する(store.dsnFor が拒否する)ため、
//     DB を開く前のこの時点で明確にエラーにする。
func validateExplicit(mode Mode, path string) (Resolved, error) {
	if !filepath.IsAbs(path) {
		return Resolved{}, &Error{Mode: mode, Reason: ReasonNotAbsolute}
	}
	if strings.Contains(path, "?") {
		return Resolved{}, &Error{Mode: mode, Reason: ReasonQuestionMark}
	}
	return Resolved{BaseDir: filepath.Clean(path), Mode: mode}, nil
}

// ---- ファイルシステム検証部分(Resolver) ------------------------------------

// Deps は Resolver が使う外部依存(テストから差し替える)。
// nil のフィールドは New が本番用の実装で埋める。
type Deps struct {
	// Getenv は環境変数の取得。
	Getenv func(key string) string
	// Executable は実行ファイルのパス取得。
	Executable func() (string, error)
	// UserConfigDir はユーザ設定ディレクトリの取得。
	UserConfigDir func() (string, error)
	// GOOS は実行 OS(ポータブル基点の OS 別規則に使う)。
	GOOS string
	// Stat はファイル情報の取得。
	Stat func(name string) (fs.FileInfo, error)
	// MkdirAll はフォルダの作成。
	MkdirAll func(path string, perm fs.FileMode) error
	// Probe は書込プローブ(実際に書けるかの最小確認)。
	Probe func(dir string) error
}

// Resolver は保存先を解決する。依存を注入できるためテストから決定的に動かせる。
type Resolver struct {
	deps Deps
}

// New は Resolver を返す(未指定の依存は本番用の実装で埋める)。
func New(deps Deps) *Resolver {
	if deps.Getenv == nil {
		deps.Getenv = os.Getenv
	}
	if deps.Executable == nil {
		deps.Executable = os.Executable
	}
	if deps.UserConfigDir == nil {
		deps.UserConfigDir = os.UserConfigDir
	}
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	if deps.Stat == nil {
		deps.Stat = func(name string) (fs.FileInfo, error) { return os.Stat(name) }
	}
	if deps.MkdirAll == nil {
		deps.MkdirAll = func(path string, perm fs.FileMode) error { return os.MkdirAll(path, os.FileMode(perm)) }
	}
	if deps.Probe == nil {
		deps.Probe = writeProbe
	}
	return &Resolver{deps: deps}
}

// Resolve は基点フォルダとモードを決定する。
//
// 明示指定(ポータブル・環境変数)の場合のみ、フォルダの作成と書込プローブまで
// 行う(使えないなら起動を止めるため)。既定モードでは何も作らない —
// 従来どおり、実際の Save / DB Open の時点で作成・失敗させる(挙動を変えない)。
func (r *Resolver) Resolve() (Resolved, error) {
	portable, err := r.portableBase()
	if err != nil {
		return Resolved{}, err
	}
	ucd, ucdErr := r.deps.UserConfigDir()
	res, err := selectBase(selection{
		PortableBase:  portable,
		Env:           r.deps.Getenv(EnvVar),
		UserConfigDir: ucd,
		UserConfigErr: ucdErr,
	})
	if err != nil {
		return Resolved{}, err
	}
	if res.Mode == ModeDefault {
		return res, nil
	}
	if err := r.ensureUsable(res.Mode, res.BaseDir); err != nil {
		return Resolved{}, err
	}
	return res, nil
}

// portableBase はポータブルモードの基点を返す(マーカーが無ければ空文字)。
//
// 「マーカーが無い」と言い切れるのは、実行ファイルの位置が分かり、かつ
// マーカーの不在(fs.ErrNotExist)を確認できた場合だけ。位置が分からない・
// 有無を確認できない場合に「指定なし」とみなすと、ポータブル運用中の利用者の
// データが別の場所に新規作成されてしまうため、エラーにして起動を中止する。
func (r *Resolver) portableBase() (string, error) {
	exe, err := r.deps.Executable()
	if err != nil || exe == "" {
		return "", &Error{Mode: ModePortable, Reason: ReasonNoExecutable}
	}
	anchor := portableAnchorDir(filepath.Dir(exe), r.deps.GOOS)
	fi, err := r.deps.Stat(filepath.Join(anchor, MarkerFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // マーカーが無い = ポータブル指定なし
		}
		return "", &Error{Mode: ModePortable, Reason: ReasonMarkerCheckFailed}
	}
	if fi == nil || fi.IsDir() {
		// 同名のフォルダはマーカーとみなさない(誤って作られた場合に
		// ポータブル運用へ引きずり込まない)
		return "", nil
	}
	return filepath.Join(anchor, PortableDirName), nil
}

// portableAnchorDir はマーカーを探し、userdata/ を置くフォルダを返す。
//
// Windows / Linux は実行ファイルのフォルダ。macOS は os.Executable() が
// `<App>.app/Contents/MacOS/` を指すため、`.app` を含むフォルダまで遡って
// **その隣**を使う(バンドル内に置くとアプリ更新で消えるため)。
// `.app` 配下でない場合(開発ビルド等)は実行ファイルのフォルダを使う。
func portableAnchorDir(exeDir, goos string) string {
	if goos != "darwin" {
		return exeDir
	}
	for dir := exeDir; ; {
		if strings.HasSuffix(filepath.Base(dir), ".app") {
			return filepath.Dir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return exeDir
		}
		dir = parent
	}
}

// ensureUsable は明示指定の基点が実際に使えることを確認する(最小限のプローブ)。
// ここを通った後の失敗(config の Load/Save・SQLite Open)は既存のエラー経路で
// 表面化する(起動直後に判明する)。
func (r *Resolver) ensureUsable(mode Mode, base string) error {
	if fi, err := r.deps.Stat(base); err == nil && fi != nil && !fi.IsDir() {
		return &Error{Mode: mode, Reason: ReasonNotDirectory}
	}
	if err := r.deps.MkdirAll(base, 0o700); err != nil {
		return &Error{Mode: mode, Reason: ReasonCreateFailed}
	}
	if err := r.deps.Probe(base); err != nil {
		return &Error{Mode: mode, Reason: ReasonNotWritable}
	}
	return nil
}

// writeProbe は実際に書き込めるかを確認する(確認用ファイルは必ず後始末する)。
// 権限だけでなく読み取り専用ボリューム・切断された外部ドライブも検出できる。
func writeProbe(dir string) error {
	f, err := os.CreateTemp(dir, ".writetest-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// ---- 本番用のグローバル解決 --------------------------------------------------

// resolveOnce はプロセス内で 1 回だけ解決する(起動時に確定し、以降変わらない)。
var resolveOnce = sync.OnceValues(func() (Resolved, error) {
	return New(Deps{}).Resolve()
})

// Current は解決結果を返す(初回呼び出し時に 1 回だけ解決する)。
// main はこれを起動時に呼び、エラーなら起動を中止する。
func Current() (Resolved, error) { return resolveOnce() }

// BaseDir は基点フォルダを返す。
func BaseDir() (string, error) {
	res, err := resolveOnce()
	if err != nil {
		return "", err
	}
	return res.BaseDir, nil
}

// CurrentMode は基点の決定方法を返す(解決に失敗している場合は空文字)。
// 表示用のため、失敗をここでは再度エラーにしない(起動時に中止済み)。
func CurrentMode() Mode {
	res, err := resolveOnce()
	if err != nil {
		return ""
	}
	return res.Mode
}
