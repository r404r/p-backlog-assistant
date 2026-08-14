package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/applog"
	"backlog-assistant/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

// version はビルド時に -ldflags "-X main.version=v1.0.0" で埋め込む。
// 未指定(ローカル開発)では "dev" のまま。
var version = "dev"

// crashFileName は動作ログを開けないときに起動失敗の理由を書き出すファイル名。
const crashFileName = "crash.txt"

// windowShowTimeout はウィンドウ表示のフォールバック待ち時間。
//
// 通常は OnDomReady(フロントエンドの読み込み完了)で表示するが、
// フロントエンドが読み込めない異常時にウィンドウが出ないままになると
// 「起動したのに何も起きない」状態になってしまう。この時間が過ぎたら
// 読み込みの成否によらず表示する。
const windowShowTimeout = 3 * time.Second

// windowShower はウィンドウの初回表示を 1 回に集約する。
//
// 表示のきっかけは OnDomReady とフォールバックタイマーの 2 経路あり、
// どちらが先になるかは起動のたびに変わる。二重の WindowShow 自体は無害だが、
// 経路を増やしても 1 回で済むことをテストで固定できるよう sync.Once で包む。
type windowShower struct {
	once sync.Once
	// show は実際の表示処理。テストでは差し替える(Wails ランタイムは結合が必要なため)。
	show func(ctx context.Context)
}

func newWindowShower() *windowShower {
	return &windowShower{show: wailsruntime.WindowShow}
}

// Show はウィンドウを表示する(2 回目以降は何もしない)。
func (s *windowShower) Show(ctx context.Context) {
	s.once.Do(func() { s.show(ctx) })
}

// startupWithWindowShow は OnStartup ハンドラを組み立てる。
//
// 表示のフォールバックタイマーは **startup を呼ぶ前に** 登録する。startup は
// 設定の読み込み・キーチェーンの初期化などを行うため環境によっては長引くことがあり、
// 登録が後だとその間ウィンドウが隠れたまま(StartHidden)になってしまうため。
func startupWithWindowShow(
	shower *windowShower,
	startup func(ctx context.Context),
	fallback time.Duration,
) func(ctx context.Context) {
	return func(ctx context.Context) {
		time.AfterFunc(fallback, func() { shower.Show(ctx) })
		startup(ctx)
	}
}

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// 起動直後のちらつき(テーマ確定前の白い画面)を避けるため、ウィンドウは
	// 隠した状態で起動し、フロントエンドの準備ができてから表示する(設計 §3.3)。
	shower := newWindowShower()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "Backlog Assistant",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		// WebView の外側に覗く地の色。A は 255 段階(0 に近いとほぼ透明)。
		// ライトの背景色で不透明に塗り、テーマ確定後はフロントエンドが
		// WindowSetBackgroundColour で同期する(frontend/src/lib/theme.ts)。
		BackgroundColour: &options.RGBA{R: 255, G: 255, B: 255, A: 255},
		StartHidden:      true,
		OnStartup:        startupWithWindowShow(shower, app.startup, windowShowTimeout),
		// prepaint スクリプトの実行後に発火するため、この時点でテーマは確定している
		OnDomReady: func(ctx context.Context) { shower.Show(ctx) },
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		reportStartupFailure(err)
		// GUI アプリのため標準出力は誰も見ていないが、終了コードは
		// バッチ・タスクスケジューラ等から失敗を検知する唯一の手段になる。
		os.Exit(1)
	}
}

// reportStartupFailure はアプリの起動に失敗した理由を、利用者が後から
// 確認できる形で残す(R18)。
//
// 制約: ここは wails.Run が失敗した後であり、ウインドウもフロントエンドも
// 存在しないため、Wails ランタイム(ダイアログ・イベント)は一切使えない。
// OS ネイティブのメッセージボックスは OS ごとの実装(Windows は user32、
// macOS は外部プロセス起動)が必要で、失敗時の挙動も検証しづらいため採らない。
// 代わりに「stderr + 動作ログ(通常はここに残る)+ 最後の手段として
// 実行ファイル横の crash.txt」へ書き出す。動作ログの場所はアプリ情報画面と
// README に記載しており、不具合報告の窓口もそこを案内している。
// なお GUI から起動された場合、stderr は画面に出ない(ファイルが頼り)。
func reportStartupFailure(runErr error) {
	text := startupFailureText(runErr, time.Now())
	fmt.Fprint(os.Stderr, text)

	// 動作ログへ残す。起動失敗の時点ではロガーが未初期化のことがある
	// (OnStartup まで到達していない)ため、ここで開き直す。
	if lg, err := applog.Init(); err == nil {
		lg.OpError("アプリの起動", runErr, slog.String("version", version))
		_ = lg.Close()
		return
	}

	// 動作ログも開けない(保存先が書き込み不可など)場合の最後の手段。
	// crash.txt も同じ理由で失敗しうるが、書けなければ stderr だけが残る。
	for _, dir := range crashFileDirs() {
		if _, werr := writeCrashFile(dir, text); werr == nil {
			return
		}
	}
	fmt.Fprintln(os.Stderr, "起動失敗の内容をファイルへ記録できませんでした")
}

// crashFileDirs は crash.txt の書き出し先候補を優先順に返す。
//
// 第一候補は実行ファイルと同じフォルダ(動作ログと同じ場所で、利用者が
// 見つけやすい)。ただし macOS の .app 配下や Program Files 配下は
// 書き込めないことがあるため、ユーザ設定ディレクトリ配下も候補に加える。
func crashFileDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if dir, err := config.DefaultDir(); err == nil {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// startupFailureText は起動失敗の記録本文(1 件ぶん)を組み立てる。
//
// この内容は利用者が開発者へ共有する想定のため、動作ログと同じ基準で
// API キー・スペースのホスト名・ホームディレクトリのパスをマスクする。
func startupFailureText(runErr error, now time.Time) string {
	msg := ""
	if runErr != nil {
		msg = applog.MaskPath(applog.MaskURLHost(applog.MaskAPIKey(runErr.Error())))
	}
	return fmt.Sprintf("%s Backlog Assistant の起動に失敗しました(version=%s): %s\n",
		now.Format("2006-01-02 15:04:05"), version, msg)
}

// writeCrashFile は dir 配下の crash.txt へ text を追記し、そのパスを返す。
// 追記なのは、繰り返し失敗したときに前回の記録を消さないため。
func writeCrashFile(dir, text string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("%s の出力先を特定できません", crashFileName)
	}
	path := filepath.Join(dir, crashFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	// Close のエラーも無視しない(書き込みが確定していない可能性があるため)
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
