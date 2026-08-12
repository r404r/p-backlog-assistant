// app.go は Wails バインディングの土台(App の生成・起動・終了と共通ヘルパ)。
//
// バインディングのメソッドは責務ごとに別ファイルへ分けている(R13)。
// Wails がバインドするのは *App のメソッドなので、ファイル分割は結線に影響しない。
//
//	app_info.go        アプリ情報(バージョン・保存データ・動作ログ)
//	app_profile.go     プロファイル・接続テスト・権限・レート制限
//	app_sync_search.go 同期(プロジェクト・課題・ユーザ)とローカル検索
//	app_export.go      Excel 出力(課題・ユーザ)と列メタデータ
//	app_bulk.go        一括更新・追加
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/applog"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/service"
)

// App は Wails バインディングの薄い層。ロジックは internal/service に置く。
type App struct {
	ctx      context.Context
	profiles *service.ProfileService
	initErr  error
	// log は動作ログ。初期化に失敗した場合は nil のまま(全メソッドが nil セーフ)。
	log *applog.Logger
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// 動作ログは最初に初期化する(以降の初期化失敗も記録できるようにするため)。
	// 失敗してもアプリの起動は継続し、ログ無効の旨だけ stderr へ出す。
	lg, err := applog.Init()
	if err != nil {
		fmt.Fprintf(os.Stderr, "動作ログを初期化できませんでした(ログ出力は無効です): %v\n", err)
	} else {
		a.log = lg
	}
	a.log.Op("アプリを起動しました", slog.String("version", version))

	mgr, err := config.NewManager()
	if err != nil {
		a.initErr = err
		a.log.OpError("アプリの初期化", err)
		return
	}
	a.profiles = service.NewProfileService(mgr)
	// 課題同期の進捗を画面へ流す(一括更新の 'bulk:progress' と同じ流儀)。
	// フル同期は数万件になり得るため、進捗が無いとスピナーだけの無反応な
	// 待ち時間になってしまう。
	a.profiles.SetSyncProgressHandler(func(ev service.SyncProgressEvent) {
		wailsruntime.EventsEmit(a.ctx, syncProgressEvent, syncProgressPayload(ev))
	})
}

// syncProgressEvent は課題同期の進捗を伝える Wails イベント名
// (frontend/src/lib/backend.ts の onSyncProgress と対)。
const syncProgressEvent = "sync:progress"

// syncProgressPayload は sync:progress イベントのペイロードを組み立てる。
//
// 画面は「自分が開始した同期」だけを表示する。その判定には
// runId(SyncIssues の呼び出し元が採番した実行識別子)を使う。プロファイル ID +
// プロジェクト ID だけでは、同じ対象を続けて同期し直した場合や、
// 画面を切り替えて失効した実行がまだ動いている場合に新旧を区別できず、
// 古い実行の進捗を表示してしまうため(中 4)。
// profileId / projectId は補助情報として併せて載せる。
func syncProgressPayload(ev service.SyncProgressEvent) map[string]any {
	return map[string]any{
		"profileId": ev.ProfileID,
		"runId":     ev.RunID,
		"projectId": ev.ProjectID,
		"phase":     string(ev.Progress.Phase),
		"fetched":   ev.Progress.Fetched,
		"total":     ev.Progress.Total, // 総件数が不明な段階(差分同期)は 0
	}
}

// shutdown は Wails の OnShutdown から呼ばれる(main.go で結線)。
//
// 実行順序は「サービス Close(SQLite/WAL のクローズ)→ 結果をログ記録 →
// ロガー Close」(中 4)。ロガーを先に閉じると DB クローズの失敗を記録できず、
// WAL/SHM が残った原因を後から追えなくなる。
func (a *App) shutdown(ctx context.Context) {
	a.log.Op("アプリを終了します")
	if a.profiles != nil {
		if err := a.profiles.Close(); err != nil {
			a.log.OpError("ローカル DB のクローズ", err)
		} else {
			a.log.Op("ローカル DB をクローズしました")
		}
	}
	_ = a.log.Close()
}

func (a *App) svc() (*service.ProfileService, error) {
	if a.profiles == nil {
		if a.initErr != nil {
			return nil, a.initErr
		}
		return nil, errors.New("アプリの初期化が完了していません")
	}
	return a.profiles, nil
}

// ---- 動作ログのヘルパー ------------------------------------------------------
//
// 記録するのは操作名と非機密パラメータ(プロファイル ID・プロジェクト ID・件数等)
// のみ。API キー・課題本文・課題タイトル・ユーザ名・メールアドレスは記録しない。

// logStart は操作の入口を記録する。
func (a *App) logStart(op string, attrs ...slog.Attr) {
	a.log.Op(op+" 開始", attrs...)
}

// logEnd は操作の出口を記録する(err が非 nil ならエラーとして記録)。
func (a *App) logEnd(op string, err error, attrs ...slog.Attr) {
	if err != nil {
		a.log.OpError(op+" 失敗", err, attrs...)
		return
	}
	a.log.Op(op+" 完了", attrs...)
}

// opLog は 1 操作ぶんの動作ログ(開始 → 完了 / 失敗)をまとめて扱う(R13)。
//
// 「開始を記録 → 失敗したら基本属性だけで失敗を記録 → 成功したら結果属性を足して
// 完了を記録」という定型が全バインディングで同じだったため、その形をここへ集約した。
// 記録する内容は集約前と同一(操作名・属性・順序とも変えない)。
type opLog struct {
	a     *App
	op    string
	attrs []slog.Attr
}

// begin は操作の開始を記録し、完了・失敗を記録するための opLog を返す。
func (a *App) begin(op string, attrs ...slog.Attr) *opLog {
	a.logStart(op, attrs...)
	return &opLog{a: a, op: op, attrs: attrs}
}

// add は以降の完了・失敗ログに載せる属性を追加する。
// 操作の途中で確定する情報(保存先の拡張子など)を、完了・失敗の双方へ
// 1 度の記述で反映するために使う。
func (o *opLog) add(attrs ...slog.Attr) {
	o.attrs = append(o.attrs, attrs...)
}

// fail は失敗を記録し、受け取ったエラーをそのまま返す(return o.fail(err) の形で使う)。
func (o *opLog) fail(err error) error {
	o.a.logEnd(o.op, err, o.attrs...)
	return err
}

// failMasked は失敗を記録し、受け取ったエラーをそのまま返す。
// ログにはファイルパスを伏せたエラーを記録する(高 2 / 2 回目 低 1)。
// 画面へは、ユーザ自身が選んだパスを含む元のエラーをそのまま返す。
func (o *opLog) failMasked(err error, path string) error {
	o.a.logEnd(o.op, maskPathInError(err, path), o.attrs...)
	return err
}

// done は完了を記録する(extra は件数など、成功時にだけ載せる属性)。
func (o *opLog) done(extra ...slog.Attr) {
	o.a.logEnd(o.op, nil, append(o.attrs, extra...)...)
}

// appOp はバインディング共通の定型を 1 か所にまとめたヘルパー(R13)。
//
// 「開始ログ → サービス取得 → 処理 → 完了 / 失敗ログ」の流れを引き受け、
// 各バインディングは attrs(基本属性)と fn(処理本体)だけを書けばよくなる。
// fn は結果と「完了ログに追加する属性」を返す。失敗時は基本属性だけで
// エラーを記録し、結果は T のゼロ値(ポインタなら nil)を返す。
//
// ファイルダイアログを挟む操作(Excel 出力・取り込み)は途中に分岐と
// キャンセル経路があるため、appOp ではなく opLog を直接使う。
func appOp[T any](a *App, op string, attrs []slog.Attr, fn func(*service.ProfileService) (T, []slog.Attr, error)) (T, error) {
	var zero T
	lg := a.begin(op, attrs...)
	s, err := a.svc()
	if err != nil {
		return zero, lg.fail(err)
	}
	res, done, err := fn(s)
	if err != nil {
		return zero, lg.fail(err)
	}
	lg.done(done...)
	return res, nil
}

// appOpErr は戻り値がエラーだけのバインディング向けの appOp(結果を持たない)。
func appOpErr(a *App, op string, attrs []slog.Attr, fn func(*service.ProfileService) ([]slog.Attr, error)) error {
	_, err := appOp(a, op, attrs, func(s *service.ProfileService) (struct{}, []slog.Attr, error) {
		done, err := fn(s)
		return struct{}{}, done, err
	})
	return err
}

// ---- ファイル入出力の共通ヘルパー --------------------------------------------

// maskedPathPlaceholder はエラーメッセージ中のファイルパスの置換先。
const maskedPathPlaceholder = "<file>"

// maskPathInError はエラーメッセージ中の path を固定のプレースホルダへ置換した
// 新しいエラーを返す(動作ログ用。高 2 / 2 回目 低 1)。
//
// 保存先ディレクトリはローカルユーザ名や顧客名を含みうる。ファイル名も
// ユーザが自由に付けられ顧客名・案件名を含みうるため、ベース名も残さない
// (形式の取り違えは fileExtAttr の拡張子で追える)。
// err が nil、または path が空の場合は元のエラーをそのまま返す。
func maskPathInError(err error, path string) error {
	if err == nil || path == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), path, maskedPathPlaceholder)
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

// fileExtAttr は保存・取り込みファイルの拡張子だけをログ属性にする(低 1)。
//
// ファイル名はユーザが自由に付けられ、顧客名・案件名を含みうるため記録しない。
// 拡張子だけであれば個人・顧客を特定しえないため、形式の取り違え(csv を選んだ 等)を
// 追える最小限の情報として残す。
func fileExtAttr(path string) slog.Attr {
	return slog.String("ext", strings.ToLower(filepath.Ext(path)))
}

// xlsxFilters は Excel ブックだけを選ばせるダイアログのフィルタ。
func xlsxFilters() []wailsruntime.FileFilter {
	return []wailsruntime.FileFilter{
		{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
	}
}

// saveExcelDialog は Excel の保存先を尋ねる(ユーザがキャンセルすると空文字)。
func (a *App) saveExcelDialog(title, defaultFilename string) (string, error) {
	return wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: defaultFilename,
		Filters:         xlsxFilters(),
	})
}

// openExcelDialog は取り込む Excel を尋ねる(ユーザがキャンセルすると空文字)。
func (a *App) openExcelDialog(title string) (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:   title,
		Filters: xlsxFilters(),
	})
}
