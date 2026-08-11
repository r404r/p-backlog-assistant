package applog

import (
	"compress/gzip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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

// --- ログの定期アーカイブ -----------------------------------------------------

// setArchiveFn はアーカイブ処理をテスト用の関数へ差し替える。
// 差し替えは newLogger の時点で Logger のフィールドへ取り込まれるため、
// ロガー生成前に呼ぶこと。
func setArchiveFn(t *testing.T, fn func(dir string, now time.Time, retentionDays, archiveDays int) (archiveResult, error)) {
	t.Helper()
	prev := archiveLogsFn
	archiveLogsFn = fn
	t.Cleanup(func() { archiveLogsFn = prev })
}

// readGzip は gzip ファイルを解凍して内容を返す。
func readGzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("アーカイブを開けません: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip として読めません(%s): %v", path, err)
	}
	defer zr.Close()
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("アーカイブを読み出せません: %v", err)
	}
	return string(b)
}

func TestArchiveOldLogsMovesOnlyOwnExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)

	// 保持するのは「当日 + 直前 13 日 = 14 日分」(retentionDays = 14)。
	// 当日が 20260809 なら保持境界は 20260727 で、それより古い日付をアーカイブする。
	files := []string{
		"backlog-assistant-20260725.log",     // 15 日前 → アーカイブ
		"backlog-assistant-20260726.log",     // 14 日前(15 日分目) → アーカイブ
		"backlog-assistant-20260727.log",     // 13 日前(14 日分目) → 保持
		"backlog-assistant-20260809.log",     // 当日 → 保持
		"backlog-assistant-20260601.log",     // さらに古い(90 日以内) → アーカイブ
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
	// サブディレクトリが同名でも触らない
	if err := os.Mkdir(filepath.Join(dir, "backlog-assistant-20250102.log"), 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("archiveOldLogs が失敗しました: %v", err)
	}
	if res.archived != 3 {
		t.Errorf("アーカイブ件数 = %d, want 3", res.archived)
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
			t.Errorf("触ってはいけない %q が消えました: %v", f, err)
		}
	}
	wantArchived := []string{
		"backlog-assistant-20260725.log.gz",
		"backlog-assistant-20260726.log.gz",
		"backlog-assistant-20260601.log.gz",
	}
	for _, f := range wantArchived {
		if _, err := os.Stat(filepath.Join(dir, archiveDirName, f)); err != nil {
			t.Errorf("アーカイブ %q がありません: %v", f, err)
		}
	}
	for _, f := range []string{
		"backlog-assistant-20260725.log",
		"backlog-assistant-20260726.log",
		"backlog-assistant-20260601.log",
	} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("アーカイブ済みの元ログ %q が残っています(err=%v)", f, err)
		}
	}
}

func TestArchiveOldLogsKeepsContentInGzip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	content := strings.Repeat("time=2026-07-01T00:00:00Z level=INFO msg=操作\n", 200)
	src := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("archiveOldLogs が失敗しました: %v", err)
	}
	if res.archived != 1 {
		t.Fatalf("アーカイブ件数 = %d, want 1", res.archived)
	}
	gz := filepath.Join(dir, archiveDirName, "backlog-assistant-20260701.log.gz")
	if got := readGzip(t, gz); got != content {
		t.Errorf("解凍結果が元のログと一致しません(len=%d, want %d)", len(got), len(content))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("圧縮後に元ファイルが削除されていません(err=%v)", err)
	}
	// 圧縮されていること(同内容の平文よりは小さい)
	fi, err := os.Stat(gz)
	if err != nil {
		t.Fatalf("アーカイブの情報を取れません: %v", err)
	}
	if fi.Size() >= int64(len(content)) {
		t.Errorf("アーカイブが圧縮されていません(size=%d, 元 %d)", fi.Size(), len(content))
	}
}

func TestArchiveOldLogsAddsSequenceWhenArchiveExists(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	archiveDir := filepath.Join(dir, archiveDirName)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	// 既存アーカイブ(中身は上書きされてはならない)
	existing := filepath.Join(archiveDir, "backlog-assistant-20260701.log.gz")
	if err := os.WriteFile(existing, []byte("既存のアーカイブ"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backlog-assistant-20260701.log"), []byte("新しい内容"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	if _, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays); err != nil {
		t.Fatalf("archiveOldLogs が失敗しました: %v", err)
	}

	b, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("既存アーカイブを読めません: %v", err)
	}
	if string(b) != "既存のアーカイブ" {
		t.Errorf("既存アーカイブが上書きされました: %q", string(b))
	}
	seq := filepath.Join(archiveDir, "backlog-assistant-20260701.log.1.gz")
	if got := readGzip(t, seq); got != "新しい内容" {
		t.Errorf("連番アーカイブの内容 = %q, want %q", got, "新しい内容")
	}
}

func TestArchiveOldLogsRemovesExpiredArchives(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	archiveDir := filepath.Join(dir, archiveDirName)
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	// 保持は「当日 + 直前 89 日 = 90 日分」。当日が 20260809 なら境界は 20260512。
	files := []string{
		"backlog-assistant-20260510.log.gz",   // 境界より古い → 削除
		"backlog-assistant-20260511.log.gz",   // 境界より古い → 削除
		"backlog-assistant-20260511.log.1.gz", // 連番付きも削除対象
		"backlog-assistant-20260512.log.gz",   // 境界(90 日分目) → 保持
		"backlog-assistant-20260701.log.gz",   // 保持
		"other-app-20250101.log.gz",           // 他アプリ → 保持
		"backlog-assistant-20250101.log",      // 非 gz(パターン外) → 保持
		"notes.txt",                           // 無関係 → 保持
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(archiveDir, f), []byte("x"), 0o600); err != nil {
			t.Fatalf("前提ファイルを作成できません: %v", err)
		}
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("archiveOldLogs が失敗しました: %v", err)
	}
	if res.removed != 3 {
		t.Errorf("削除件数 = %d, want 3", res.removed)
	}
	for _, f := range []string{
		"backlog-assistant-20260510.log.gz",
		"backlog-assistant-20260511.log.gz",
		"backlog-assistant-20260511.log.1.gz",
	} {
		if _, err := os.Stat(filepath.Join(archiveDir, f)); !os.IsNotExist(err) {
			t.Errorf("保持期間を過ぎたアーカイブ %q が残っています(err=%v)", f, err)
		}
	}
	for _, f := range []string{
		"backlog-assistant-20260512.log.gz",
		"backlog-assistant-20260701.log.gz",
		"other-app-20250101.log.gz",
		"backlog-assistant-20250101.log",
		"notes.txt",
	} {
		if _, err := os.Stat(filepath.Join(archiveDir, f)); err != nil {
			t.Errorf("削除してはいけない %q が消えました: %v", f, err)
		}
	}
}

// TestArchiveOldLogsSkipsWhenArchiveIsNotDirectory は、logs/archive が
// ディレクトリ以外(通常ファイル)として存在する場合にアーカイブ処理全体を
// スキップし、元ファイルに触らないことを確認する(中 3)。
func TestArchiveOldLogsSkipsWhenArchiveIsNotDirectory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	src := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.WriteFile(src, []byte("残っているべき内容"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	// archive を「通常ファイル」として作る(アーカイブ先として使えない状態)
	if err := os.WriteFile(filepath.Join(dir, archiveDirName), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("スキップはエラーにしない: %v", err)
	}
	if res.skipped == "" {
		t.Error("スキップ理由が返っていません")
	}
	if res.archived != 0 || res.removed != 0 {
		t.Errorf("結果 = %+v, want 0 件", res)
	}
	b, rerr := os.ReadFile(src)
	if rerr != nil {
		t.Fatalf("スキップ時に元ファイルが失われました: %v", rerr)
	}
	if string(b) != "残っているべき内容" {
		t.Errorf("元ファイルの内容 = %q(変化しています)", string(b))
	}
}

// TestArchiveOldLogsSkipsSymlinkedLogFiles は、ログ名のシンボリックリンクを
// アーカイブ対象にしないことを確認する(中 3)。
// リンクを辿ると、リンク先の実体(他の場所のファイル)を圧縮して削除してしまう。
func TestArchiveOldLogsSkipsSymlinkedLogFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ではシンボリックリンクの作成に特権が必要なため検証しない")
	}
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)

	// リンク先(ログ保存先の外にある想定の実体)
	target := filepath.Join(t.TempDir(), "important.dat")
	if err := os.WriteFile(target, []byte("消えてはいけない内容"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	link := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("シンボリックリンクを作成できません: %v", err)
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("archiveOldLogs が失敗しました: %v", err)
	}
	if res.archived != 0 {
		t.Errorf("アーカイブ件数 = %d, want 0(シンボリックリンクは対象外)", res.archived)
	}
	if _, lerr := os.Lstat(link); lerr != nil {
		t.Errorf("シンボリックリンクが消えました: %v", lerr)
	}
	b, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatalf("リンク先が失われました: %v", rerr)
	}
	if string(b) != "消えてはいけない内容" {
		t.Errorf("リンク先の内容 = %q(変化しています)", string(b))
	}
	if _, serr := os.Stat(filepath.Join(dir, archiveDirName)); !os.IsNotExist(serr) {
		t.Errorf("対象が無いのにアーカイブフォルダが作成されました(err=%v)", serr)
	}
}

// TestArchiveOldLogsSkipsWhenArchiveIsSymlink は、logs/archive が
// シンボリックリンクの場合にアーカイブ処理全体をスキップすることを確認する(中 3)。
// リンクを辿ると、リンク先のフォルダへログを移動し、そこのファイルを
// 保持期間判定で削除しうる。
func TestArchiveOldLogsSkipsWhenArchiveIsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ではシンボリックリンクの作成に特権が必要なため検証しない")
	}
	dir := t.TempDir()
	now := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)

	// リンク先フォルダ(保持期間を過ぎた名前のファイルを置き、削除されないことを見る)
	linkTargetDir := t.TempDir()
	outside := filepath.Join(linkTargetDir, "backlog-assistant-20260101.log.gz")
	if err := os.WriteFile(outside, []byte("消えてはいけない内容"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	if err := os.Symlink(linkTargetDir, filepath.Join(dir, archiveDirName)); err != nil {
		t.Fatalf("シンボリックリンクを作成できません: %v", err)
	}
	src := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.WriteFile(src, []byte("残っているべき内容"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	res, err := archiveOldLogs(dir, now, retentionDays, archiveRetentionDays)
	if err != nil {
		t.Fatalf("スキップはエラーにしない: %v", err)
	}
	if res.skipped == "" {
		t.Error("スキップ理由が返っていません")
	}
	if res.archived != 0 || res.removed != 0 {
		t.Errorf("結果 = %+v, want 0 件", res)
	}
	if _, serr := os.Stat(src); serr != nil {
		t.Errorf("元ログが移動されました: %v", serr)
	}
	if _, serr := os.Stat(outside); serr != nil {
		t.Errorf("リンク先のファイルが削除されました: %v", serr)
	}
	entries, rerr := os.ReadDir(linkTargetDir)
	if rerr != nil {
		t.Fatalf("リンク先を読めません: %v", rerr)
	}
	if len(entries) != 1 {
		t.Errorf("リンク先のファイル数 = %d, want 1(何も移動しない)", len(entries))
	}
}

// TestNewLoggerLogsArchiveSkipWarning は、アーカイブをスキップした事実を
// 警告として 1 行だけ記録することを確認する(中 3)。
func TestNewLoggerLogsArchiveSkipWarning(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	if err := os.WriteFile(filepath.Join(dir, "backlog-assistant-20260701.log"), []byte("古いログ"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, archiveDirName), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	lg, err := newLogger(dir, fixedClock(at), "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	lg.Op("スキップ後の操作")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	if got := strings.Count(content, "ログのアーカイブをスキップしました"); got != 1 {
		t.Errorf("スキップ警告の行数 = %d, want 1:\n%s", got, content)
	}
	if !strings.Contains(content, "level=WARN") {
		t.Errorf("警告レベルで記録されていません:\n%s", content)
	}
	if !strings.Contains(content, "スキップ後の操作") {
		t.Errorf("スキップ後もログ出力が継続していません:\n%s", content)
	}
}

func TestCompressFileRemovesPartialOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	// 読み出しが必ず失敗する入力(ディレクトリ)で途中失敗を再現する
	src := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatalf("前提ディレクトリを作成できません: %v", err)
	}
	dst := filepath.Join(dir, "backlog-assistant-20260701.log.gz")

	if err := compressFile(src, dst); err == nil {
		t.Fatal("入力を読めない場合はエラーを期待しました")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("書きかけのアーカイブが残っています(err=%v)", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("失敗時に入力が失われました: %v", err)
	}
}

func TestNewLoggerArchivesOldFilesInBackground(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 9, 10, 0, 0, 0, time.Local)
	// 保持期間(14 日)超過かつアーカイブ保持期間(90 日)内の日付
	old := filepath.Join(dir, "backlog-assistant-20260701.log")
	if err := os.WriteFile(old, []byte("古いログ"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	lg, err := newLogger(dir, fixedClock(at), "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("起動時に古いログがアーカイブされていません(err=%v)", err)
	}
	gz := filepath.Join(dir, archiveDirName, "backlog-assistant-20260701.log.gz")
	if got := readGzip(t, gz); got != "古いログ" {
		t.Errorf("アーカイブの内容 = %q, want %q", got, "古いログ")
	}
	content := readLogFile(t, lg.Path())
	if !strings.Contains(content, "archived=1") {
		t.Errorf("アーカイブ結果がログに記録されていません:\n%s", content)
	}
}

func TestNewLoggerDoesNotCreateArchiveDirWhenNothingToArchive(t *testing.T) {
	dir := t.TempDir()
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, archiveDirName)); !os.IsNotExist(err) {
		t.Errorf("対象が無いのにアーカイブフォルダが作成されました(err=%v)", err)
	}
}

func TestLoggerDoesNotStartArchiveTwice(t *testing.T) {
	dir := t.TempDir()
	var calls int32
	release := make(chan struct{})
	setArchiveFn(t, func(string, time.Time, int, int) (archiveResult, error) {
		atomic.AddInt32(&calls, 1)
		<-release
		return archiveResult{}, nil
	})

	// newLogger が 1 回目を起動し、テストが release を閉じるまで実行中のままになる
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	defer lg.Close()

	// 実行中の起動要求は無視される(二重起動防止)
	lg.startArchive(time.Now())
	close(release)
	lg.waitArchive()
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("実行回数 = %d, want 1(実行中の再起動は無視)", got)
	}
	// 完了後は再び起動できる
	lg.startArchive(time.Now())
	lg.waitArchive()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("完了後の実行回数 = %d, want 2", got)
	}
}

func TestLoggerLogsArchiveFailureWithoutBreakingLogging(t *testing.T) {
	dir := t.TempDir()
	setArchiveFn(t, func(string, time.Time, int, int) (archiveResult, error) {
		return archiveResult{}, errFmt("アーカイブ先が読めません")
	})
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	lg.Op("アーカイブ失敗後の操作")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	if !strings.Contains(content, "アーカイブ先が読めません") {
		t.Errorf("アーカイブ失敗が記録されていません:\n%s", content)
	}
	if !strings.Contains(content, "アーカイブ失敗後の操作") {
		t.Errorf("アーカイブ失敗後もログ出力が継続していません:\n%s", content)
	}
}

func TestLoggerRecoversFromArchivePanic(t *testing.T) {
	dir := t.TempDir()
	setArchiveFn(t, func(string, time.Time, int, int) (archiveResult, error) {
		panic("想定外の失敗")
	})
	lg, err := newLogger(dir, time.Now, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	lg.Op("パニック後の操作")
	if err := lg.Close(); err != nil {
		t.Fatalf("Close が失敗しました: %v", err)
	}

	content := readLogFile(t, lg.Path())
	if !strings.Contains(content, "想定外の失敗") {
		t.Errorf("パニックが記録されていません:\n%s", content)
	}
	if !strings.Contains(content, "パニック後の操作") {
		t.Errorf("パニック後もログ出力が継続していません:\n%s", content)
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
	// 起動時は保持境界内、ローテーション後に境界外になるファイル(アーカイブの再実行を確認)
	expiring := filepath.Join(dir, "backlog-assistant-20260727.log")
	if err := os.WriteFile(expiring, []byte("x"), 0o600); err != nil {
		t.Fatalf("前提ファイルを作成できません: %v", err)
	}

	current := day1
	lg, err := newLogger(dir, func() time.Time { return current }, "")
	if err != nil {
		t.Fatalf("newLogger が失敗しました: %v", err)
	}
	lg.waitArchive()
	lg.Op("日付が変わる前の操作")
	if _, err := os.Stat(expiring); err != nil {
		t.Fatalf("保持境界内のファイルがアーカイブされました: %v", err)
	}

	current = day2
	lg.Op("日付が変わった後の操作")
	if want := filepath.Join(dir, "backlog-assistant-20260810.log"); lg.Path() != want {
		t.Errorf("ローテーション後の Path = %q, want %q", lg.Path(), want)
	}
	lg.waitArchive()
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
		t.Errorf("ローテーション時に保持期間切れのファイルがアーカイブされていません(err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, archiveDirName, "backlog-assistant-20260727.log.gz")); err != nil {
		t.Errorf("ローテーション時のアーカイブが作成されていません: %v", err)
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
