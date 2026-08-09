// Package applog はアプリの動作ログ(操作ログ)を担う。
//
// 情報分離ポリシー(AGENTS.md 6):
// API キー・課題本文・課題タイトル・ユーザ名・メールアドレスはログに書かない。
// 記録するのは「どの操作が、どのプロファイル・プロジェクトに対して、
// どんな結果(件数・所要時間・エラー)になったか」までに留める。
// エラーメッセージは internal/backlogclient でマスク済みのものが渡る前提だが、
// 保険として apiKey= パターンをこの層でも再マスクする。
// 絶対パスはローカルユーザ名や保存先フォルダ名(顧客名を含みうる)が露出するため、
// ホームディレクトリ配下は "~" に置換してから記録する(高 1)。
// URL のホスト部はスペース URL(実案件情報)が露出するため "<host>" に置換する。
// ログはユーザが開発者へ共有する想定のため、実案件情報を残さない。
package applog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// logFilePrefix はログファイル名の接頭辞。
	logFilePrefix = "backlog-assistant-"
	// logFileExt はログファイルの拡張子。
	logFileExt = ".log"
	// retentionDays は保持日数。「当日 + 直前 13 日 = 14 日分」を保持し、
	// それより古い自ファイルは起動時とローテーション時に削除する。
	retentionDays = 14
	// appDirName はフォールバック先(ユーザ設定ディレクトリ配下)のフォルダ名。
	appDirName = "backlog-assistant"
	// logDirName はログ保存フォルダ名。
	logDirName = "logs"
	// dayLayout は日付キー(ログファイル名の日付部)のレイアウト。
	dayLayout = "20060102"
)

// logFilePattern は自アプリのログファイル名(backlog-assistant-YYYYMMDD.log)。
// 削除対象はこのパターンに一致するファイルのみに限定する(他ファイルは温存)。
var logFilePattern = regexp.MustCompile(`^` + regexp.QuoteMeta(logFilePrefix) + `(\d{8})` + regexp.QuoteMeta(logFileExt) + `$`)

// apiKeyPattern は URL・エラーメッセージ中の apiKey パラメータ値にマッチする
// (internal/backlogclient の MaskAPIKey と同等。applog を単体で完結させるため再定義)。
var apiKeyPattern = regexp.MustCompile(`(?i)(apiKey=)[^&\s"']*`)

// MaskAPIKey は文字列中の apiKey パラメータ値を "***" に置換する。
func MaskAPIKey(s string) string {
	return apiKeyPattern.ReplaceAllString(s, "${1}***")
}

// ---- URL ホスト部のマスク ----------------------------------------------------

// urlHostPattern は URL のスキーム直後のホスト部(ユーザ情報・ポートを含む)にマッチする。
// ホストの終端はパス・クエリ・フラグメントの開始、空白、引用符、山括弧のいずれか。
var urlHostPattern = regexp.MustCompile(`(?i)(https?://)[^/?#\s"'<>\\]+`)

// MaskURLHost は文字列中の URL のホスト部を "<host>" に置換する。
// スペース URL(https://<スペース>.backlog.jp)はそれ自体が実案件情報のため、
// API 通信エラーのメッセージ等に含まれていてもログには残さない。
// どの API を叩いて失敗したかは追えるよう、パス・クエリはそのまま残す。
func MaskURLHost(s string) string {
	return urlHostPattern.ReplaceAllString(s, "${1}<host>")
}

// ---- パスのマスク(高 1) ----------------------------------------------------

// userHomeDir はホームディレクトリの取得(テスト差し替え用)。
var userHomeDir = os.UserHomeDir

// foldPathCase はパス比較で大文字小文字を無視するかどうか。
// Windows のファイルシステムは大文字小文字を区別しないため、
// 表記ゆれ(c:\users\... と C:\Users\...)でもマスクが外れないようにする。
// テストからは差し替える。
var foldPathCase = runtime.GOOS == "windows"

// MaskPath は文字列中のホームディレクトリのパスを "~" に置換する。
// ログにローカルユーザ名(= ホームディレクトリ名)や、その配下の
// フォルダ名(顧客名を含みうる)を絶対パスのまま残さないための保険。
// ホーム外のパスは書き換えない。パス単体でも文中に埋め込まれたパスでも動く。
func MaskPath(s string) string {
	home := maskableHome()
	if s == "" || home == "" {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if i+len(home) <= len(s) &&
			pathPartEqual(s[i:i+len(home)], home) &&
			isPathBoundary(s[i+len(home):]) {
			b.WriteString("~")
			i += len(home)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// maskableHome は置換に使うホームディレクトリ(末尾区切りなし)を返す。
// 取得できない場合と、根そのもの("/" や "C:\")の場合は空を返す
// (根はあらゆるパスに一致してしまうため。空文字を返した場合はマスクを行わない)。
// ルート直下 1 階層のホーム(/root 等)も置換対象にする(中 1)。
func maskableHome() string {
	h, err := userHomeDir()
	if err != nil || h == "" {
		return ""
	}
	h = strings.TrimRight(filepath.Clean(h), `/\`)
	if h == "" {
		return "" // "/" のみ
	}
	// ドライブレター("C:")を除いた残りが空なら根そのものなので対象外。
	// filepath.VolumeName は実行 OS 依存のため、ここでは自前で判定する。
	rest := h
	if len(rest) >= 2 && rest[1] == ':' && isASCIILetter(rest[0]) {
		rest = rest[2:]
	}
	if strings.Trim(rest, `/\`) == "" {
		return ""
	}
	return h
}

// isASCIILetter は ASCII の英字かどうかを返す(ドライブレターの判定用)。
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// pathPartEqual はパスの一部同士を比較する。
// Windows では区切り文字に "\" と "/" の両方が使われ、ファイルシステムは
// 大文字小文字を区別しないため、区切りを正規化した上で(Windows では
// 大文字小文字も無視して)比較する(中 1)。
// 正規化は 1 バイト同士の置換なので、比較元の長さは変わらない。
func pathPartEqual(a, b string) bool {
	a = normalizeSeparators(a)
	b = normalizeSeparators(b)
	if foldPathCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// normalizeSeparators はパス区切りを "/" に揃える(比較用。置換結果には使わない)。
func normalizeSeparators(s string) string {
	return strings.ReplaceAll(s, `\`, "/")
}

// isPathBoundary はホームディレクトリ一致の直後がパスの区切り(または終端)かを返す。
// 直後がディレクトリ名の続き(例: /home/user2)なら別のパスなので置換しない。
func isPathBoundary(rest string) bool {
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	if r == '/' || r == '\\' {
		return true
	}
	return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_')
}

// maskText はログへ書く文字列に全てのマスクを適用する。
func maskText(s string) string {
	return MaskAPIKey(MaskURLHost(MaskPath(s)))
}

// ---- ロガー -----------------------------------------------------------------

// Logger は動作ログの書き出し口。
// 初期化に失敗した場合は nil のまま扱ってよい(全メソッドが nil セーフ)。
//
// 書き込み・日付ローテーション・Close は mu で排他する(中 3)。
// Close 後の書き込みは no-op になる。
type Logger struct {
	mu     sync.Mutex
	logger *slog.Logger
	file   *os.File
	path   string
	// dir はログ保存先ディレクトリ(ローテーション先の決定に使う)。
	dir string
	// day は現在開いているファイルの日付キー(YYYYMMDD)。
	day string
	// now は現在時刻の取得(テスト差し替え用)。
	now func() time.Time
	// rotateErrDay はローテーション失敗を記録済みの日付キー。
	// 失敗のたびに同じエラー行を量産しないための抑制用。
	rotateErrDay string
}

// Init はログ保存先を決定してログファイルを開く。
//
// 保存先の決定規則:
//  1. 実行ファイル(os.Executable)と同じフォルダの logs/(第一候補)
//  2. 1 が作成・書き込みできない場合は os.UserConfigDir()/backlog-assistant/logs/
//
// wails dev 等で実行ファイルが一時領域にある場合も同じ規則で動く
// (一時領域が書き込み可能ならそこに出力される)。
// フォールバックした場合はその事実を最初のログ行に記録する。
func Init() (*Logger, error) {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	configBase, _ := os.UserConfigDir() // 取得できなければ空文字(resolveLogDir 側で判定)
	return openLogger(exeDir, configBase, time.Now)
}

// openLogger は保存先を決定してロガーを開く。
// ディレクトリの書き込みテストに成功しても当日のログファイルを開けないこと
// (同名のディレクトリが存在する・ファイルが読み取り専用など)があるため、
// 第一候補でファイルを開けなかった場合はフォールバック先で再試行する(中 2)。
func openLogger(exeDir, configBase string, now func() time.Time) (*Logger, error) {
	dir, fallbackReason, err := resolveLogDir(exeDir, configBase)
	if err != nil {
		return nil, err
	}
	lg, err := newLogger(dir, now, fallbackReason)
	if err == nil {
		return lg, nil
	}
	if fallbackReason != "" {
		// すでにフォールバック先で失敗している(再試行先が無い)
		return nil, err
	}
	if configBase == "" {
		return nil, err
	}
	fallback := filepath.Join(configBase, appDirName, logDirName)
	if werr := ensureWritableDir(fallback); werr != nil {
		return nil, fmt.Errorf("ログ保存先を確保できません: %w", werr)
	}
	reason := fmt.Sprintf("実行ファイルと同じフォルダの logs にログファイルを作成できませんでした: %v", err)
	return newLogger(fallback, now, reason)
}

// resolveLogDir はログ保存先ディレクトリを決定して作成する。
// 戻り値 fallbackReason はフォールバックした場合のみ非空(第一候補を使えた理由なし = 空)。
func resolveLogDir(exeDir, configBase string) (dir string, fallbackReason string, err error) {
	if exeDir == "" {
		fallbackReason = "実行ファイルの位置を特定できませんでした"
	} else {
		primary := filepath.Join(exeDir, logDirName)
		werr := ensureWritableDir(primary)
		if werr == nil {
			return primary, "", nil
		}
		fallbackReason = fmt.Sprintf("実行ファイルと同じフォルダの logs を使用できませんでした: %v", werr)
	}
	if configBase == "" {
		return "", fallbackReason, errors.New("ログ保存先を決定できません(ユーザ設定ディレクトリを取得できませんでした)")
	}
	fallback := filepath.Join(configBase, appDirName, logDirName)
	if werr := ensureWritableDir(fallback); werr != nil {
		return "", fallbackReason, fmt.Errorf("ログ保存先を確保できません: %w", werr)
	}
	return fallback, fallbackReason, nil
}

// ensureWritableDir はディレクトリを作成し、実際に書き込めるかを確認する。
// 権限だけでなく読み取り専用ボリューム等も検出するため、一時ファイルの
// 作成・削除まで行う(確認用ファイルは必ず後始末する)。
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
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

// logFileName は指定時刻(ローカル日付)のログファイル名を返す。
func logFileName(at time.Time) string {
	return logFilePrefix + at.Format(dayLayout) + logFileExt
}

// openLogFile は指定日付のログファイルを追記モードで開く。
func openLogFile(dir string, at time.Time) (*os.File, string, error) {
	path := filepath.Join(dir, logFileName(at))
	// 追記オープン(同日の再起動でも 1 ファイルにまとめる)。権限は 0600。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("ログファイルを開けません: %w", err)
	}
	return f, path, nil
}

// newLogger は dir 配下のログファイルを開いてロガーを構築する。
// now は現在時刻の取得関数(テスト差し替え用。nil なら time.Now)。
// 最初のログ行に保存先とフォールバックの事実を記録し、その後で古いログを削除する。
func newLogger(dir string, now func() time.Time, fallbackReason string) (*Logger, error) {
	if now == nil {
		now = time.Now
	}
	at := now()
	f, path, err := openLogFile(dir, at)
	if err != nil {
		return nil, err
	}
	l := &Logger{
		logger: slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})),
		file:   f,
		path:   path,
		dir:    dir,
		day:    at.Format(dayLayout),
		now:    now,
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	// 最初の 1 行: 保存先とフォールバックの事実(パスはマスクされる)
	startAttrs := []slog.Attr{
		slog.String("path", path),
		slog.Bool("fallback", fallbackReason != ""),
	}
	if fallbackReason != "" {
		startAttrs = append(startAttrs, slog.String("fallbackReason", fallbackReason))
	}
	l.logAttrsLocked(slog.LevelInfo, "動作ログを開始しました", startAttrs...)
	// 起動時の保持期間超過ファイル削除(失敗してもログ出力は継続する)
	l.cleanOldLocked(at)
	return l, nil
}

// cleanOldLocked は保持期間を過ぎたログを削除し、結果を記録する(mu 保持前提)。
func (l *Logger) cleanOldLocked(at time.Time) {
	removed, cerr := cleanOldLogFiles(l.dir, at, retentionDays)
	switch {
	case cerr != nil:
		l.logAttrsLocked(slog.LevelInfo, "古いログの削除に失敗しました", slog.String("error", cerr.Error()))
	case len(removed) > 0:
		l.logAttrsLocked(slog.LevelInfo, "古いログを削除しました",
			slog.Int("count", len(removed)), slog.Int("retentionDays", retentionDays))
	}
}

// cleanOldLogFiles は dir 内の「自アプリのログファイル」のうち、
// 保持日数より古い日付のものだけを削除し、削除したファイル名を返す。
// 他アプリのファイル・命名パターン外のファイル・ディレクトリは対象外。
func cleanOldLogFiles(dir string, now time.Time, days int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// 保持するのは「当日 + 直前 (days-1) 日 = days 日分」。
	// 当日を含めて days 日分になるよう境界日は today-(days-1) とし、
	// それより古い日付を削除する(境界日ちょうどは残す)。
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	cutoff := today.AddDate(0, 0, -(days - 1))

	var removed []string
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := logFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		day, perr := time.ParseInLocation(dayLayout, m[1], time.Local)
		if perr != nil {
			continue // 日付として解釈できないものは触らない
		}
		if !day.Before(cutoff) {
			continue
		}
		if rerr := os.Remove(filepath.Join(dir, e.Name())); rerr != nil {
			if firstErr == nil {
				firstErr = rerr
			}
			continue
		}
		removed = append(removed, e.Name())
	}
	return removed, firstErr
}

// Path はログファイルのパスを返す(無効な場合は空文字)。
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// Enabled はログ出力が有効かどうかを返す。
func (l *Logger) Enabled() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.logger != nil
}

// Op は操作ログを 1 行記録する(情報レベル)。
func (l *Logger) Op(name string, attrs ...slog.Attr) {
	l.write(slog.LevelInfo, name, attrs...)
}

// OpError は失敗した操作を記録する(エラーレベル)。
// err のメッセージは backlogclient でマスク済みの想定だが、保険で再マスクする。
func (l *Logger) OpError(name string, err error, attrs ...slog.Attr) {
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	l.write(slog.LevelError, name, attrs...)
}

// write は排他を取り、必要ならローテーションしてから 1 行書き出す。
func (l *Logger) write(level slog.Level, msg string, attrs ...slog.Attr) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logger == nil {
		return // Close 済み(中 3)
	}
	l.rotateIfNeededLocked()
	l.logAttrsLocked(level, msg, attrs...)
}

// rotateIfNeededLocked は日付が変わっていればログファイルを切り替える(mu 保持前提。高 2)。
// 長時間起動したままでも日付ごとにファイルが分かれるようにする。
// 切り替え後は保持期間超過ファイルの削除も再実行する。
func (l *Logger) rotateIfNeededLocked() {
	if l.logger == nil || l.file == nil {
		return
	}
	at := l.now()
	day := at.Format(dayLayout)
	if day == l.day {
		return
	}
	f, path, err := openLogFile(l.dir, at)
	if err != nil {
		// 切り替えに失敗しても現行ファイルへの記録は続ける(ログ欠落を避ける)。
		// 同じ日付で何度も同じエラー行を出さないよう 1 回だけ記録する。
		if l.rotateErrDay != day {
			l.rotateErrDay = day
			l.logAttrsLocked(slog.LevelError, "ログファイルの切り替えに失敗しました",
				slog.String("error", err.Error()))
		}
		return
	}
	old := l.file
	oldName := filepath.Base(l.path)
	l.file = f
	l.path = path
	l.day = day
	l.rotateErrDay = ""
	l.logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_ = old.Close()
	l.logAttrsLocked(slog.LevelInfo, "日付が変わったためログファイルを切り替えました",
		slog.String("previousFile", oldName))
	l.cleanOldLocked(at)
}

// logAttrsLocked は属性値の再マスクを行って 1 行書き出す(mu 保持前提)。
func (l *Logger) logAttrsLocked(level slog.Level, msg string, attrs ...slog.Attr) {
	if l.logger == nil {
		return
	}
	masked := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		masked = append(masked, maskAttr(a))
	}
	l.logger.LogAttrs(context.Background(), level, maskText(msg), masked...)
}

// maskAttr は文字列属性の apiKey パターン・URL ホスト部・ホームディレクトリの
// パスを再マスクする。slog.Group の中の文字列属性も再帰的にマスクする(低 1)。
func maskAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, maskText(a.Value.String()))
	case slog.KindGroup:
		group := a.Value.Group()
		masked := make([]slog.Attr, 0, len(group))
		for _, g := range group {
			masked = append(masked, maskAttr(g))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(masked...)}
	default:
		return a
	}
}

// Close は終了ログを書いてファイルを閉じる(nil セーフ・多重呼び出し可)。
// Close 後の Op / OpError は no-op になる(中 3)。
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	l.logAttrsLocked(slog.LevelInfo, "動作ログを終了しました")
	err := l.file.Close()
	l.file = nil
	l.logger = nil
	return err
}
