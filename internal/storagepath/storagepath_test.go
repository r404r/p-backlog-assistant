package storagepath

// storagepath の単体テスト(設計 §4)。
//
// パス選択の純粋部分(selectBase)と、ファイルシステム検証部分(Resolver.Resolve)を
// 分けて検証する。前者は依存ゼロ、後者は依存注入(Deps)で決定的に動かす。

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- 純粋部分(selectBase) --------------------------------------------------

// TestSelectBase_Priority は優先順位(portable > env > 既定)を固定する。
func TestSelectBase_Priority(t *testing.T) {
	tests := []struct {
		name     string
		in       selection
		wantBase string
		wantMode Mode
	}{
		{
			name: "マーカーと環境変数が併存する場合はポータブルが勝つ",
			in: selection{
				PortableBase:  filepath.FromSlash("/apps/userdata"),
				Env:           filepath.FromSlash("/env/base"),
				UserConfigDir: filepath.FromSlash("/home/u/.config"),
			},
			wantBase: filepath.FromSlash("/apps/userdata"),
			wantMode: ModePortable,
		},
		{
			name: "マーカーが無ければ環境変数",
			in: selection{
				Env:           filepath.FromSlash("/env/base"),
				UserConfigDir: filepath.FromSlash("/home/u/.config"),
			},
			wantBase: filepath.FromSlash("/env/base"),
			wantMode: ModeEnv,
		},
		{
			name: "どちらも無ければ既定(ユーザ設定ディレクトリ配下)",
			in: selection{
				UserConfigDir: filepath.FromSlash("/home/u/.config"),
			},
			wantBase: filepath.Join(filepath.FromSlash("/home/u/.config"), AppDirName),
			wantMode: ModeDefault,
		},
		{
			name: "環境変数が空白のみの場合は未指定として扱う",
			in: selection{
				Env:           "   ",
				UserConfigDir: filepath.FromSlash("/home/u/.config"),
			},
			wantBase: filepath.Join(filepath.FromSlash("/home/u/.config"), AppDirName),
			wantMode: ModeDefault,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectBase(tt.in)
			if err != nil {
				t.Fatalf("エラーになった: %v", err)
			}
			if got.BaseDir != tt.wantBase {
				t.Errorf("BaseDir = %q, want %q", got.BaseDir, tt.wantBase)
			}
			if got.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", got.Mode, tt.wantMode)
			}
		})
	}
}

// TestSelectBase_ExplicitValidation は明示指定の検証規則
// (絶対パスのみ・`?` 不可・`~` / `%VAR%` 非展開)を固定する。
func TestSelectBase_ExplicitValidation(t *testing.T) {
	tests := []struct {
		name       string
		in         selection
		wantMode   Mode
		wantReason Reason
	}{
		{
			name:       "環境変数の相対パスはエラー",
			in:         selection{Env: filepath.FromSlash("data/base"), UserConfigDir: filepath.FromSlash("/home/u/.config")},
			wantMode:   ModeEnv,
			wantReason: ReasonNotAbsolute,
		},
		{
			name:       "チルダは展開せず相対パスとして拒否する",
			in:         selection{Env: "~/backlog", UserConfigDir: filepath.FromSlash("/home/u/.config")},
			wantMode:   ModeEnv,
			wantReason: ReasonNotAbsolute,
		},
		{
			name:       "%VAR% は展開せず相対パスとして拒否する",
			in:         selection{Env: `%APPDATA%\backlog`, UserConfigDir: filepath.FromSlash("/home/u/.config")},
			wantMode:   ModeEnv,
			wantReason: ReasonNotAbsolute,
		},
		{
			name:       "環境変数のパスに ? が含まれる場合はエラー",
			in:         selection{Env: filepath.FromSlash("/env/ba?se"), UserConfigDir: filepath.FromSlash("/home/u/.config")},
			wantMode:   ModeEnv,
			wantReason: ReasonQuestionMark,
		},
		{
			name:       "ポータブル基点に ? が含まれる場合はエラー",
			in:         selection{PortableBase: filepath.FromSlash("/ap?ps/userdata"), UserConfigDir: filepath.FromSlash("/home/u/.config")},
			wantMode:   ModePortable,
			wantReason: ReasonQuestionMark,
		},
		{
			name:       "ユーザ設定ディレクトリを取得できない場合はエラー",
			in:         selection{UserConfigErr: errors.New("取得できません")},
			wantMode:   ModeDefault,
			wantReason: ReasonNoUserConfigDir,
		},
		{
			// 既定パスでも '?' があると DB を開けない(store.dsnFor が拒否する)。
			// 起動後に理由の分かりにくい失敗を繰り返すより、ここで明示する。
			name:       "既定パスに ? が含まれる場合もエラー",
			in:         selection{UserConfigDir: filepath.FromSlash("/home/u?/.config")},
			wantMode:   ModeDefault,
			wantReason: ReasonQuestionMark,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := selectBase(tt.in)
			assertReason(t, err, tt.wantMode, tt.wantReason)
		})
	}
}

// TestError_HidesRawPath は、エラーに生パスを含めないこと(理由コードのみ)を固定する。
// 任意の保存先には顧客名等が含まれうるため、crash.txt・ログへ残してはならない。
func TestError_HidesRawPath(t *testing.T) {
	secret := filepath.FromSlash("/顧客A/案件B/data")
	_, err := selectBase(selection{Env: filepath.Join(secret, "ba?se"), UserConfigDir: filepath.FromSlash("/home/u/.config")})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if strings.Contains(err.Error(), "顧客A") || strings.Contains(err.Error(), secret) {
		t.Errorf("エラーに生パスが含まれている: %q", err.Error())
	}
	if !strings.Contains(err.Error(), string(ReasonQuestionMark)) {
		t.Errorf("理由コードが含まれていない: %q", err.Error())
	}
}

// ---- ファイルシステム検証部分(Resolver) ------------------------------------

// fakeDeps はテスト用の依存一式を組み立てる(実ファイルシステムを使う既定)。
func fakeDeps(env, exe string, goos string) Deps {
	return Deps{
		Getenv:        func(key string) string { return map[string]string{EnvVar: env}[key] },
		Executable:    func() (string, error) { return exe, nil },
		UserConfigDir: func() (string, error) { return filepath.FromSlash("/home/u/.config"), nil },
		GOOS:          goos,
	}
}

// TestResolve_PortableCreatesUserdata は、マーカーがあるとき実行ファイル横の
// userdata/ を基点として作成することを確認する。
func TestResolve_PortableCreatesUserdata(t *testing.T) {
	appDir := t.TempDir()
	writeMarker(t, appDir)
	exe := filepath.Join(appDir, "backlog-assistant")

	got, err := New(fakeDeps("", exe, "windows")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	want := filepath.Join(appDir, PortableDirName)
	if got.BaseDir != want {
		t.Errorf("BaseDir = %q, want %q", got.BaseDir, want)
	}
	if got.Mode != ModePortable {
		t.Errorf("Mode = %q, want %q", got.Mode, ModePortable)
	}
	if fi, serr := os.Stat(want); serr != nil || !fi.IsDir() {
		t.Errorf("userdata フォルダが作成されていない: %v", serr)
	}
}

// TestResolve_PortableIgnoresDirectoryMarker は、portable.txt が
// フォルダだった場合にポータブルモードとみなさないことを確認する。
func TestResolve_PortableIgnoresDirectoryMarker(t *testing.T) {
	appDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(appDir, MarkerFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(appDir, "backlog-assistant")

	got, err := New(fakeDeps("", exe, "linux")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if got.Mode != ModeDefault {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeDefault)
	}
}

// TestResolve_MacAppBundle は macOS で .app バンドルの隣を基点にすることを確認する
// (バンドル内に置くとアプリ更新で消えるため)。
func TestResolve_MacAppBundle(t *testing.T) {
	root := t.TempDir()
	macOSDir := filepath.Join(root, "Backlog Assistant.app", "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, root)
	exe := filepath.Join(macOSDir, "backlog-assistant")

	got, err := New(fakeDeps("", exe, "darwin")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	want := filepath.Join(root, PortableDirName)
	if got.BaseDir != want {
		t.Errorf("BaseDir = %q, want %q", got.BaseDir, want)
	}
	if got.Mode != ModePortable {
		t.Errorf("Mode = %q, want %q", got.Mode, ModePortable)
	}
}

// TestResolve_MacAppBundleMarkerInsideIsIgnored は、.app の内側にマーカーを
// 置いてもポータブルモードにならないことを確認する(判定は .app の隣のみ)。
func TestResolve_MacAppBundleMarkerInsideIsIgnored(t *testing.T) {
	root := t.TempDir()
	macOSDir := filepath.Join(root, "Backlog Assistant.app", "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMarker(t, macOSDir)
	exe := filepath.Join(macOSDir, "backlog-assistant")

	got, err := New(fakeDeps("", exe, "darwin")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if got.Mode != ModeDefault {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeDefault)
	}
}

// TestResolve_MacWithoutBundle は macOS でも .app 配下でなければ
// 実行ファイルのフォルダを基点にすることを確認する(開発ビルド)。
func TestResolve_MacWithoutBundle(t *testing.T) {
	appDir := t.TempDir()
	writeMarker(t, appDir)
	exe := filepath.Join(appDir, "backlog-assistant")

	got, err := New(fakeDeps("", exe, "darwin")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if want := filepath.Join(appDir, PortableDirName); got.BaseDir != want {
		t.Errorf("BaseDir = %q, want %q", got.BaseDir, want)
	}
}

// TestResolve_ExecutableUnavailableIsError は、実行ファイルの位置が分からない
// 場合にエラーとなることを確認する。
//
// 位置が分からないとポータブル指定(portable.txt)の有無を確認できない。
// 「指定されていない」と決めつけて次の優先順位へ進むと、ポータブル運用中の
// 利用者のデータが別の場所に新規作成されてしまうため、起動を中止する。
func TestResolve_ExecutableUnavailableIsError(t *testing.T) {
	deps := fakeDeps(t.TempDir(), "", "linux")
	deps.Executable = func() (string, error) { return "", errors.New("/secret/path が特定できません") }

	_, err := New(deps).Resolve()
	assertReason(t, err, ModePortable, ReasonNoExecutable)
	if strings.Contains(err.Error(), "/secret/path") {
		t.Errorf("エラーに下位のパスが含まれている: %q", err.Error())
	}
}

// TestResolve_MarkerCheckFailedIsError は、マーカーの有無を確認できない場合
// (権限不足・I/O エラー等。「存在しない」とは区別する)にエラーとなることを確認する。
func TestResolve_MarkerCheckFailedIsError(t *testing.T) {
	deps := fakeDeps(t.TempDir(), filepath.Join(t.TempDir(), "app"), "linux")
	deps.Stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }

	_, err := New(deps).Resolve()
	assertReason(t, err, ModePortable, ReasonMarkerCheckFailed)
}

// TestResolve_MarkerAbsentContinues は、マーカーが「存在しない」ことを
// 確認できた場合は次の優先順位へ進む(エラーにしない)ことを確認する。
func TestResolve_MarkerAbsentContinues(t *testing.T) {
	base := t.TempDir()

	got, err := New(fakeDeps(base, filepath.Join(t.TempDir(), "app"), "linux")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if got.Mode != ModeEnv || got.BaseDir != base {
		t.Errorf("解決結果 = %+v, want mode=env base=%q", got, base)
	}
}

// TestResolve_EnvCreatesBase は環境変数指定の基点を作成することを確認する。
func TestResolve_EnvCreatesBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "custom base")

	got, err := New(fakeDeps(base, filepath.Join(t.TempDir(), "app"), "linux")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if got.BaseDir != base || got.Mode != ModeEnv {
		t.Errorf("解決結果 = %+v, want base=%q mode=env", got, base)
	}
	if fi, serr := os.Stat(base); serr != nil || !fi.IsDir() {
		t.Errorf("基点フォルダが作成されていない: %v", serr)
	}
}

// TestResolve_EnvUnicodeAndSpacePath は空白・Unicode を含むパスでも動くことを確認する。
func TestResolve_EnvUnicodeAndSpacePath(t *testing.T) {
	base := filepath.Join(t.TempDir(), "日本語 フォルダ", "データ置き場")

	got, err := New(fakeDeps(base, filepath.Join(t.TempDir(), "app"), "linux")).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	if got.BaseDir != base {
		t.Errorf("BaseDir = %q, want %q", got.BaseDir, base)
	}
	if fi, serr := os.Stat(base); serr != nil || !fi.IsDir() {
		t.Errorf("基点フォルダが作成されていない: %v", serr)
	}
}

// TestResolve_EnvBaseIsRegularFile は、基点に通常ファイルがある場合の
// エラー(フォールバックしない)を確認する。
func TestResolve_EnvBaseIsRegularFile(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	if err := os.WriteFile(base, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := New(fakeDeps(base, filepath.Join(t.TempDir(), "app"), "linux")).Resolve()
	assertReason(t, err, ModeEnv, ReasonNotDirectory)
}

// TestResolve_EnvNotWritable は、明示指定が書き込めない場合に
// 既定へフォールバックせずエラーになることを確認する(設計 §3.1 の要)。
func TestResolve_EnvNotWritable(t *testing.T) {
	base := t.TempDir()
	deps := fakeDeps(base, filepath.Join(t.TempDir(), "app"), "linux")
	deps.Probe = func(string) error { return errors.New("書き込めません") }

	_, err := New(deps).Resolve()
	assertReason(t, err, ModeEnv, ReasonNotWritable)
}

// TestResolve_PortableCreateFailed は、ポータブル基点を作成できない場合に
// エラーになることを確認する。
func TestResolve_PortableCreateFailed(t *testing.T) {
	appDir := t.TempDir()
	writeMarker(t, appDir)
	deps := fakeDeps("", filepath.Join(appDir, "app"), "linux")
	deps.MkdirAll = func(string, fs.FileMode) error { return errors.New("作成できません") }

	_, err := New(deps).Resolve()
	assertReason(t, err, ModePortable, ReasonCreateFailed)
}

// TestResolve_DefaultDoesNotTouchFilesystem は、既定モードでは起動時に
// フォルダ作成・書込プローブを行わない(従来の挙動を変えない)ことを確認する。
func TestResolve_DefaultDoesNotTouchFilesystem(t *testing.T) {
	deps := fakeDeps("", filepath.Join(t.TempDir(), "app"), "linux")
	deps.MkdirAll = func(string, fs.FileMode) error {
		t.Error("既定モードでフォルダ作成が呼ばれた")
		return nil
	}
	deps.Probe = func(string) error {
		t.Error("既定モードで書込プローブが呼ばれた")
		return nil
	}

	got, err := New(deps).Resolve()
	if err != nil {
		t.Fatalf("解決に失敗した: %v", err)
	}
	want := filepath.Join(filepath.FromSlash("/home/u/.config"), AppDirName)
	if got.BaseDir != want || got.Mode != ModeDefault {
		t.Errorf("解決結果 = %+v, want base=%q mode=default", got, want)
	}
}

// TestNew_FillsProductionDefaults は、Deps を省略しても本番用の実装が
// 埋まる(nil 呼び出しで panic しない)ことを確認する。
func TestNew_FillsProductionDefaults(t *testing.T) {
	r := New(Deps{})
	if r.deps.Getenv == nil || r.deps.Executable == nil || r.deps.UserConfigDir == nil ||
		r.deps.Stat == nil || r.deps.MkdirAll == nil || r.deps.Probe == nil || r.deps.GOOS == "" {
		t.Errorf("既定の依存が埋まっていない: %+v", r.deps)
	}
}

// TestWriteProbe は本番用の書込プローブが後始末まで行うことを確認する。
func TestWriteProbe(t *testing.T) {
	dir := t.TempDir()
	if err := writeProbe(dir); err != nil {
		t.Fatalf("書込プローブに失敗した: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("確認用ファイルが残っている: %v", entries)
	}

	if err := writeProbe(filepath.Join(dir, "missing")); err == nil {
		t.Error("存在しないフォルダでもエラーにならなかった")
	}
}

// ---- 補助 --------------------------------------------------------------------

// writeMarker は dir に portable.txt を作成する。
func writeMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, MarkerFileName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertReason は err が期待するモード・理由コードの *Error であることを確認する。
func assertReason(t *testing.T, err error, mode Mode, reason Reason) {
	t.Helper()
	if err == nil {
		t.Fatalf("エラーにならなかった(want mode=%s reason=%s)", mode, reason)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("型 = %T, want *storagepath.Error(%v)", err, err)
	}
	if e.Mode != mode {
		t.Errorf("Mode = %q, want %q", e.Mode, mode)
	}
	if e.Reason != reason {
		t.Errorf("Reason = %q, want %q", e.Reason, reason)
	}
}
