package applog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// readLogFile はログファイル全体を文字列で返す。
func readLogFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ログファイルを読めません: %v", err)
	}
	return string(b)
}

// fixedClock は固定時刻を返す時計(newLogger のテスト用)。
func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// setHome はホームディレクトリの取得をテスト値へ差し替える。
func setHome(t *testing.T, home string) {
	t.Helper()
	prev := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = prev })
}

// setFoldPathCase はパス比較の大文字小文字無視(Windows 相当)を切り替える。
func setFoldPathCase(t *testing.T, fold bool) {
	t.Helper()
	prev := foldPathCase
	foldPathCase = fold
	t.Cleanup(func() { foldPathCase = prev })
}

func TestLogFileNameUsesLocalDate(t *testing.T) {
	at := time.Date(2026, 8, 9, 23, 30, 0, 0, time.Local)
	if got, want := logFileName(at), "backlog-assistant-20260809.log"; got != want {
		t.Errorf("logFileName = %q, want %q", got, want)
	}
}

func TestResolveLogDirPrefersExecutableDir(t *testing.T) {
	exeDir := t.TempDir()
	configBase := t.TempDir()

	dir, fallbackReason, err := resolveLogDir(exeDir, configBase)
	if err != nil {
		t.Fatalf("resolveLogDir が失敗しました: %v", err)
	}
	if want := filepath.Join(exeDir, "logs"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if fallbackReason != "" {
		t.Errorf("フォールバックしていないのに理由が返りました: %q", fallbackReason)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("ログディレクトリが作成されていません: %v", err)
	}
	// 書き込みテスト用の一時ファイルが残っていないこと
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ディレクトリを読めません: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("書き込みテストの残骸があります: %v", entries)
	}
}

func TestResolveLogDirFallsBackWhenPrimaryUnwritable(t *testing.T) {
	exeDir := t.TempDir()
	configBase := t.TempDir()
	// logs を「通常ファイル」として作り、ディレクトリ作成を必ず失敗させる
	// (パーミッションによる不可化は root 実行時に効かないため使わない)
	if err := os.WriteFile(filepath.Join(exeDir, "logs"), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	dir, fallbackReason, err := resolveLogDir(exeDir, configBase)
	if err != nil {
		t.Fatalf("resolveLogDir が失敗しました: %v", err)
	}
	if want := filepath.Join(configBase, "backlog-assistant", "logs"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if fallbackReason == "" {
		t.Error("フォールバックしたのに理由が空です")
	}
}

func TestResolveLogDirFallsBackWhenExeDirUnknown(t *testing.T) {
	configBase := t.TempDir()

	dir, fallbackReason, err := resolveLogDir("", configBase)
	if err != nil {
		t.Fatalf("resolveLogDir が失敗しました: %v", err)
	}
	if want := filepath.Join(configBase, "backlog-assistant", "logs"); dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
	if fallbackReason == "" {
		t.Error("フォールバックしたのに理由が空です")
	}
}

func TestResolveLogDirErrorsWhenNoCandidate(t *testing.T) {
	exeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(exeDir, "logs"), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	if _, _, err := resolveLogDir(exeDir, ""); err == nil {
		t.Error("保存先が確保できない場合はエラーを期待しました")
	}
}

func TestCleanOldLogFilesRemovesOnlyOwnOldFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)

	// 保持するのは「当日 + 直前 13 日 = 14 日分」(retentionDays = 14)。
	// 当日が 20260809 なら保持境界は 20260727 で、それより古い日付は削除する。
	files := []string{
		"backlog-assistant-20260725.log",     // 15 日前 → 削除
		"backlog-assistant-20260726.log",     // 14 日前(15 日分目) → 削除
		"backlog-assistant-20260727.log",     // 13 日前(14 日分目) → 保持
		"backlog-assistant-20260809.log",     // 当日 → 保持
		"backlog-assistant-20250101.log",     // 大幅に古い → 削除
		"other-app-20250101.log",             // 他アプリのログ → 保持
		"backlog-assistant-2025-01-01.log",   // 命名パターン外 → 保持
		"backlog-assistant-20250101.log.bak", // 命名パターン外 → 保持
		"backlog-assistant.log",              // 命名パターン外 → 保持
		"config.json",                        // 無関係 → 保持
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatalf("前提ファイルを作成できません: %v", err)
		}
	}
	// サブディレクトリが同名でも削除しない
	if err := os.Mkdir(filepath.Join(dir, "backlog-assistant-20250102.log"), 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}

	removed, err := cleanOldLogFiles(dir, now, retentionDays)
	if err != nil {
		t.Fatalf("cleanOldLogFiles が失敗しました: %v", err)
	}
	if len(removed) != 3 {
		t.Errorf("削除件数 = %d(%v), want 3", len(removed), removed)
	}

	wantKept := []string{
		"backlog-assistant-20260727.log",
		"backlog-assistant-20260809.log",
		"other-app-20250101.log",
		"backlog-assistant-2025-01-01.log",
		"backlog-assistant-20250101.log.bak",
		"backlog-assistant.log",
		"config.json",
		"backlog-assistant-20250102.log", // ディレクトリ
	}
	for _, f := range wantKept {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("削除してはいけない %q が消えました: %v", f, err)
		}
	}
	wantGone := []string{
		"backlog-assistant-20260725.log",
		"backlog-assistant-20260726.log",
		"backlog-assistant-20250101.log",
	}
	for _, f := range wantGone {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("古いログ %q が削除されていません(err=%v)", f, err)
		}
	}
}

func TestMaskAPIKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			`Get "https://example.backlog.jp/api/v2/users?apiKey=SECRET123": timeout`,
			`Get "https://example.backlog.jp/api/v2/users?apiKey=***": timeout`,
		},
		{
			"https://example.backlog.jp/api/v2/issues?apiKey=SECRET123&count=100",
			"https://example.backlog.jp/api/v2/issues?apiKey=***&count=100",
		},
		{"?APIKEY=abc", "?APIKEY=***"},
		{"apiKey を含まない文字列", "apiKey を含まない文字列"},
	}
	for _, c := range cases {
		if got := MaskAPIKey(c.in); got != c.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- 2 回目レビュー 高 1: URL ホスト部のマスク --------------------------------

func TestMaskURLHost(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			"スペース URL(パス・クエリは維持)",
			"https://example.backlog.jp/api/v2/users?apiKey=***",
			"https://<host>/api/v2/users?apiKey=***",
		},
		{
			"http と大文字スキーム",
			"HTTP://Example.Backlog.COM/api/v2/issues",
			"HTTP://<host>/api/v2/issues",
		},
		{
			"ポート付き・ホストのみ",
			"https://example.backlog.jp:8443",
			"https://<host>",
		},
		{
			"文中の URL(引用符の内側)",
			`Get "https://example.backlog.jp/api/v2/users?apiKey=***": dial tcp: i/o timeout`,
			`Get "https://<host>/api/v2/users?apiKey=***": dial tcp: i/o timeout`,
		},
		{"URL を含まない文字列", "同期に失敗しました", "同期に失敗しました"},
		{"空文字", "", ""},
	}
	for _, c := range cases {
		if got := MaskURLHost(c.in); got != c.want {
			t.Errorf("%s: MaskURLHost(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestLoggerMasksURLHostInMessageAndAttrs(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("接続テスト https://example.backlog.jp/api/v2/space",
		slog.String("url", "https://example.backlog.jp/api/v2/users?apiKey=SECRET"))
	lg.OpError("同期", errFmt(`Get "https://example.backlog.jp/api/v2/issues?apiKey=SECRET": timeout`))
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	if strings.Contains(content, "example.backlog.jp") {
		t.Errorf("スペース URL のホストがログに残っています:\n%s", content)
	}
	if !strings.Contains(content, "https://<host>/api/v2/users") {
		t.Errorf("ホストをマスクした URL(パスは維持)がログにありません:\n%s", content)
	}
}

func TestLoggerMasksAPIKeyInMessageAndAttrs(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("接続テスト apiKey=MSG_SECRET",
		slog.String("url", "https://example.backlog.jp/api/v2/users?apiKey=ATTR_SECRET"))
	lg.OpError("同期", errFmt("要求に失敗しました: https://example.backlog.jp/api/v2/issues?apiKey=ERR_SECRET"))
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	for _, secret := range []string{"MSG_SECRET", "ATTR_SECRET", "ERR_SECRET"} {
		if strings.Contains(content, secret) {
			t.Errorf("API キー %q がログに出力されました:\n%s", secret, content)
		}
	}
	if !strings.Contains(content, "apiKey=***") {
		t.Errorf("マスク後の apiKey=*** がありません:\n%s", content)
	}
}

func TestNewLoggerWritesFallbackNoteInFirstLine(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "実行ファイルと同じフォルダに書き込めません")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("ダミー操作")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(readLogFile(t, lg.Path())), "\n")
	if len(lines) == 0 {
		t.Fatal("ログが空です")
	}
	first := lines[0]
	if !strings.Contains(first, "fallback=true") {
		t.Errorf("最初の行にフォールバックの事実がありません: %s", first)
	}
	if !strings.Contains(first, "実行ファイルと同じフォルダに書き込めません") {
		t.Errorf("最初の行にフォールバック理由がありません: %s", first)
	}
}

func TestNewLoggerUsesLocalTimeZone(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	lg, err := newLogger(dir, fixedClock(at), "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	if want := filepath.Join(dir, logFileName(at)); lg.Path() != want {
		t.Errorf("Path = %q, want %q", lg.Path(), want)
	}
	lg.Op("操作")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}
	// ローカル TZ のオフセットが記録されていること(UTC 環境では Z になる)
	_, offset := at.Zone()
	content := readLogFile(t, lg.Path())
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	marker := "Z"
	if offset != 0 {
		marker = sign + zeroPad(offset/3600) + ":" + zeroPad((offset%3600)/60)
	}
	if !strings.Contains(content, marker) {
		t.Errorf("ローカル TZ(%s)の時刻が記録されていません:\n%s", marker, content)
	}
}

func TestNewLoggerRemovesOldFilesOnStart(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "backlog-assistant-20200101.log")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	defer lg.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("起動時に古いログが削除されていません(err=%v)", err)
	}
}

func TestNewLoggerAppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	at := time.Now()
	lg1, err := newLogger(dir, fixedClock(at), "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg1.Op("1 回目の起動")
	if err := lg1.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}
	lg2, err := newLogger(dir, fixedClock(at), "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg2.Op("2 回目の起動")
	if err := lg2.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}
	content := readLogFile(t, lg2.Path())
	if !strings.Contains(content, "1 回目の起動") || !strings.Contains(content, "2 回目の起動") {
		t.Errorf("既存ファイルへ追記されていません:\n%s", content)
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var lg *Logger
	// nil でもパニックせず、無効として扱われること(ログ初期化失敗時の動作)
	lg.Op("操作", slog.String("k", "v"))
	lg.OpError("操作", errFmt("エラー"))
	if lg.Path() != "" {
		t.Errorf("nil の Path = %q, want \"\"", lg.Path())
	}
	if lg.Enabled() {
		t.Error("nil の Enabled = true, want false")
	}
	if err := lg.Close(); err != nil {
		t.Errorf("nil の Close = %v, want nil", err)
	}
}

func TestInitOpensLogFile(t *testing.T) {
	lg, err := Init()
	if err != nil {
		t.Fatalf("Init が失敗しました: %v", err)
	}
	defer func() {
		_ = lg.Close()
		_ = os.Remove(lg.Path())
	}()
	if !lg.Enabled() {
		t.Error("Init 直後の Enabled = false, want true")
	}
	if filepath.Base(lg.Path()) != logFileName(time.Now()) {
		t.Errorf("Path = %q, want ファイル名 %q", lg.Path(), logFileName(time.Now()))
	}
	if _, err := os.Stat(lg.Path()); err != nil {
		t.Errorf("ログファイルが作成されていません: %v", err)
	}
}

// --- 高 1: パスのマスク -------------------------------------------------------

func TestMaskPathReplacesHomeDirectory(t *testing.T) {
	sep := string(filepath.Separator)
	home := filepath.Join(sep+"home", "someuser")
	setHome(t, home)
	setFoldPathCase(t, false)

	cases := []struct{ name, in, want string }{
		{"ホーム配下", filepath.Join(home, "logs", "a.log"), "~" + sep + filepath.Join("logs", "a.log")},
		{"ホーム自身", home, "~"},
		{"末尾区切り", home + sep, "~" + sep},
		{"ホーム外", filepath.Join(sep+"opt", "app", "logs"), filepath.Join(sep+"opt", "app", "logs")},
		{"別ユーザ(前方一致のみ)", filepath.Join(sep+"home", "someuser2", "x"), filepath.Join(sep+"home", "someuser2", "x")},
		{"文中のパス", "mkdir " + filepath.Join(home, "x") + ": permission denied",
			"mkdir ~" + sep + "x: permission denied"},
		{"空文字", "", ""},
	}
	for _, c := range cases {
		if got := MaskPath(c.in); got != c.want {
			t.Errorf("%s: MaskPath(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestMaskPathFoldsCaseOnWindowsLikeFileSystem(t *testing.T) {
	home := `C:\Users\SomeUser`
	setHome(t, home)

	setFoldPathCase(t, true)
	if got, want := MaskPath(`c:\users\someuser\logs\a.log`), `~\logs\a.log`; got != want {
		t.Errorf("大文字小文字を無視した置換 = %q, want %q", got, want)
	}
	setFoldPathCase(t, false)
	if got, want := MaskPath(`c:\users\someuser\logs\a.log`), `c:\users\someuser\logs\a.log`; got != want {
		t.Errorf("大文字小文字を区別する環境で置換されました = %q, want %q", got, want)
	}
}

// --- 2 回目レビュー 中 1(a): ルート直下 1 階層のホーム ------------------------

func TestMaskPathMasksRootLevelHome(t *testing.T) {
	setHome(t, "/root")
	setFoldPathCase(t, false)

	cases := []struct{ name, in, want string }{
		{"ルート直下ホーム配下", "/root/logs/a.log", "~/logs/a.log"},
		{"ルート直下ホーム自身", "/root", "~"},
		{"前方一致のみの別ディレクトリ", "/rootfs/x", "/rootfs/x"},
	}
	for _, c := range cases {
		if got := MaskPath(c.in); got != c.want {
			t.Errorf("%s: MaskPath(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestMaskPathIgnoresRootItself(t *testing.T) {
	setFoldPathCase(t, false)
	// 根そのもの(全パスに一致してしまう)は置換対象にしない
	for _, home := range []string{"/", `C:\`, "C:"} {
		setHome(t, home)
		in := "/opt/app/logs/a.log"
		if got := MaskPath(in); got != in {
			t.Errorf("home=%q: MaskPath(%q) = %q, want 変更なし", home, in, got)
		}
	}
}

// --- 2 回目レビュー 中 1(b): 区切り文字の違いを吸収 ---------------------------

func TestMaskPathNormalizesPathSeparators(t *testing.T) {
	setHome(t, `C:\Users\SomeUser`)
	setFoldPathCase(t, true)

	cases := []struct{ name, in, want string }{
		{"スラッシュ区切りの対象文字列", `C:/Users/SomeUser/logs/a.log`, `~/logs/a.log`},
		{"円記号区切りの対象文字列", `C:\Users\SomeUser\logs\a.log`, `~\logs\a.log`},
		{"混在", `C:/Users\SomeUser/logs/a.log`, `~/logs/a.log`},
		{"別ユーザ(前方一致のみ)", `C:/Users/SomeUser2/x`, `C:/Users/SomeUser2/x`},
	}
	for _, c := range cases {
		if got := MaskPath(c.in); got != c.want {
			t.Errorf("%s: MaskPath(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// --- 2 回目レビュー 低 1: グループ内の文字列属性のマスク ----------------------

func TestLoggerMasksStringsInsideGroup(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "someuser")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	setHome(t, home)
	setFoldPathCase(t, false)

	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("グループ付き操作",
		slog.Group("detail",
			slog.String("url", "https://example.backlog.jp/api/v2/users?apiKey=GROUP_SECRET"),
			slog.Int("count", 3),
			slog.Group("inner", slog.String("file", filepath.Join(home, "Documents", "out.xlsx"))),
		))
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	for _, secret := range []string{"GROUP_SECRET", "example.backlog.jp", home} {
		if strings.Contains(content, secret) {
			t.Errorf("グループ内の機密情報 %q がログに残っています:\n%s", secret, content)
		}
	}
	if !strings.Contains(content, "detail.count=3") {
		t.Errorf("グループ内の非文字列属性が失われています:\n%s", content)
	}
	if !strings.Contains(content, "detail.inner.file=~") {
		t.Errorf("入れ子グループ内のパスがマスクされていません:\n%s", content)
	}
}

func TestLoggerMasksHomePathInAttrsAndFallbackReason(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(t.TempDir(), "someuser")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	setHome(t, home)
	setFoldPathCase(t, false)

	reason := "実行ファイルと同じフォルダの logs を使用できませんでした: mkdir " +
		filepath.Join(home, "app", "logs") + ": permission denied"
	lg, err := newLogger(dir, time.Now, reason)
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("出力", slog.String("file", filepath.Join(home, "Documents", "顧客A", "out.xlsx")))
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	if strings.Contains(content, home) {
		t.Errorf("ホームディレクトリの絶対パスがログに残っています:\n%s", content)
	}
	if !strings.Contains(content, "~") {
		t.Errorf("マスク後の ~ がログにありません:\n%s", content)
	}
}

// --- 高 2: 日付跨ぎのローテーション -------------------------------------------

func TestLoggerRotatesFileWhenDateChanges(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 8, 9, 23, 59, 0, 0, time.Local)
	day2 := time.Date(2026, 8, 10, 0, 1, 0, 0, time.Local)
	// 起動時は保持境界内、ローテーション後に境界外になるファイル(削除の再実行を確認)
	expiring := filepath.Join(dir, "backlog-assistant-20260727.log")
	if err := os.WriteFile(expiring, []byte("x"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	current := day1
	lg, err := newLogger(dir, func() time.Time { return current }, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.Op("日付が変わる前の操作")
	if _, err := os.Stat(expiring); err != nil {
		t.Fatalf("保持境界内のファイルが削除されました: %v", err)
	}

	current = day2
	lg.Op("日付が変わった後の操作")
	if want := filepath.Join(dir, "backlog-assistant-20260810.log"); lg.Path() != want {
		t.Errorf("ローテーション後の Path = %q, want %q", lg.Path(), want)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	first := readLogFile(t, filepath.Join(dir, "backlog-assistant-20260809.log"))
	second := readLogFile(t, filepath.Join(dir, "backlog-assistant-20260810.log"))
	if !strings.Contains(first, "日付が変わる前の操作") {
		t.Errorf("旧ファイルに前日の操作がありません:\n%s", first)
	}
	if strings.Contains(first, "日付が変わった後の操作") {
		t.Errorf("旧ファイルに翌日の操作が書かれています:\n%s", first)
	}
	if !strings.Contains(second, "日付が変わった後の操作") {
		t.Errorf("新ファイルに翌日の操作がありません:\n%s", second)
	}
	if !strings.Contains(second, "動作ログを終了しました") {
		t.Errorf("終了ログが新ファイルに書かれていません:\n%s", second)
	}
	if _, err := os.Stat(expiring); !os.IsNotExist(err) {
		t.Errorf("ローテーション時に保持期間切れのファイルが削除されていません(err=%v)", err)
	}
}

// --- 中 2: 当日ファイルを開けない場合のフォールバック --------------------------

func TestOpenLoggerUsesPrimaryDir(t *testing.T) {
	exeDir := t.TempDir()
	configBase := t.TempDir()
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)

	lg, err := openLogger(exeDir, configBase, fixedClock(at))
	if err != nil {
		t.Fatalf("openLogger が失敗しました: %v", err)
	}
	defer lg.Close()
	if want := filepath.Join(exeDir, "logs", logFileName(at)); lg.Path() != want {
		t.Errorf("Path = %q, want %q", lg.Path(), want)
	}
}

func TestOpenLoggerFallsBackWhenLogFileCannotBeOpened(t *testing.T) {
	exeDir := t.TempDir()
	configBase := t.TempDir()
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	// ディレクトリ自体は書き込み可能だが、当日のログファイル名がディレクトリで
	// 占有されていてファイルとして開けない状況を作る
	if err := os.MkdirAll(filepath.Join(exeDir, "logs", logFileName(at)), 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}

	lg, err := openLogger(exeDir, configBase, fixedClock(at))
	if err != nil {
		t.Fatalf("openLogger が失敗しました: %v", err)
	}
	defer lg.Close()
	if want := filepath.Join(configBase, appDirName, logDirName, logFileName(at)); lg.Path() != want {
		t.Errorf("Path = %q, want %q(フォールバックしていません)", lg.Path(), want)
	}
	content := readLogFile(t, lg.Path())
	if !strings.Contains(content, "fallback=true") {
		t.Errorf("フォールバックの事実が記録されていません:\n%s", content)
	}
}

func TestOpenLoggerErrorsWhenBothCandidatesFail(t *testing.T) {
	exeDir := t.TempDir()
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	if err := os.MkdirAll(filepath.Join(exeDir, "logs", logFileName(at)), 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	if _, err := openLogger(exeDir, "", fixedClock(at)); err == nil {
		t.Error("両方の候補が使えない場合はエラーを期待しました")
	}
}

// --- 中 3: Close と書き込みの競合 ----------------------------------------------

func TestLoggerCloseIsSafeWithConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				lg.Op("並行操作", slog.Int("worker", n), slog.Int("seq", j))
				lg.OpError("並行エラー", errFmt("失敗"))
				_ = lg.Enabled()
				_ = lg.Path()
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = lg.Close()
	}()
	wg.Wait()
}

func TestLoggerOpAfterCloseIsNoop(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}
	lg.Op("クローズ後の操作")
	if lg.Enabled() {
		t.Error("Close 後の Enabled = true, want false")
	}
	if err := lg.Close(); err != nil {
		t.Errorf("多重 Close = %v, want nil", err)
	}
	if content := readLogFile(t, lg.Path()); strings.Contains(content, "クローズ後の操作") {
		t.Errorf("Close 後の書き込みが記録されました:\n%s", content)
	}
}

// --- テスト用ヘルパー ---------------------------------------------------------

// errFmt はテスト用の単純なエラーを作る。
type testError string

func (e testError) Error() string { return string(e) }

func errFmt(msg string) error { return testError(msg) }

// zeroPad は 2 桁ゼロ埋めする(TZ オフセットの検証用)。
func zeroPad(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}
