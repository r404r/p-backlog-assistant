package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/applog"
	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
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

// LogInfo は動作ログの状態(frontend/src/lib/backend.ts の LogInfo と対)。
type LogInfo struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// GetLogInfo は動作ログの出力先と有効・無効を返す(画面の案内表示用)。
func (a *App) GetLogInfo() (*LogInfo, error) {
	return &LogInfo{Path: a.log.Path(), Enabled: a.log.Enabled()}, nil
}

// AppVersionInfo はアプリのバージョン情報(frontend/src/lib/backend.ts の AppVersion と対)。
type AppVersionInfo struct {
	Version string `json:"version"`
}

// GetAppVersion はビルド時に埋め込まれたバージョンを返す(フッタ表示・問い合わせ時の特定用)。
func (a *App) GetAppVersion() (*AppVersionInfo, error) {
	return &AppVersionInfo{Version: version}, nil
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

// ListProfiles は保存済みプロファイル一覧を返す。
func (a *App) ListProfiles() ([]config.Profile, error) {
	const op = "ListProfiles"
	a.logStart(op)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err)
		return nil, err
	}
	profiles, err := s.ListProfiles()
	if err != nil {
		a.logEnd(op, err)
		return nil, err
	}
	a.logEnd(op, nil, slog.Int("count", len(profiles)))
	return profiles, nil
}

// GetActiveProfile は保存済みの接続先プロファイル ID を返す(未選択なら空文字)。
// 起動時に ListProfiles と併せて呼び、セレクタの初期選択に使う。
func (a *App) GetActiveProfile() (string, error) {
	const op = "GetActiveProfile"
	a.logStart(op)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err)
		return "", err
	}
	id, err := s.GetActiveProfile()
	if err != nil {
		a.logEnd(op, err)
		return "", err
	}
	a.logEnd(op, nil, slog.String("profileId", id))
	return id, nil
}

// SetActiveProfile は接続先プロファイル ID を保存する(空文字 = 選択解除)。
// フロントの接続先セレクタ変更時に呼ばれる。
//
// TODO(マイルストーン 2): activeProfileId の変更を DB オープン(store.Open)と
// 同期処理の接続先切り替えへ結線する。現時点では設定の永続化のみ行う。
func (a *App) SetActiveProfile(id string) error {
	const op = "SetActiveProfile"
	a.logStart(op, slog.String("profileId", id))
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, slog.String("profileId", id))
		return err
	}
	err = s.SetActiveProfile(id)
	a.logEnd(op, err, slog.String("profileId", id))
	return err
}

// ProfileInput はフロントエンドの保存フォーム入力(frontend/src/lib/backend.ts と対)。
type ProfileInput struct {
	ID       string `json:"id"` // 空なら新規作成
	Name     string `json:"name"`
	SpaceURL string `json:"spaceUrl"`
	APIKey   string `json:"apiKey"` // 空 + 既存プロファイル = キー維持
}

// SaveProfile はプロファイルを保存する(保存前に接続テストを実施し、
// 成功時のみ config.json と OS キーチェーンへ保存する)。
func (a *App) SaveProfile(input ProfileInput) (*config.Profile, error) {
	const op = "SaveProfile"
	// API キーは値を一切記録せず、入力されたか(bool)だけを記録する。
	// 表示名・スペース URL も利用者を特定しうるため記録しない。
	base := []slog.Attr{
		slog.String("profileId", input.ID),
		slog.Bool("new", input.ID == ""),
		slog.Bool("apiKeyProvided", input.APIKey != ""),
	}
	a.logStart(op, base...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, base...)
		return nil, err
	}
	res, err := s.SaveProfile(a.ctx, input.ID, input.Name, input.SpaceURL, input.APIKey)
	if err != nil {
		a.logEnd(op, err, base...)
		return nil, err
	}
	a.logEnd(op, nil, append(base, slog.String("savedProfileId", res.Profile.ID))...)
	return &res.Profile, nil
}

// DeleteProfile はプロファイルを削除する。キーチェーンの API キーは必ず削除し、
// deleteDB が真ならローカル DB も削除する。
func (a *App) DeleteProfile(id string, deleteDB bool) error {
	const op = "DeleteProfile"
	attrs := []slog.Attr{slog.String("profileId", id), slog.Bool("deleteLocalData", deleteDB)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return err
	}
	err = s.DeleteProfile(id, deleteDB)
	a.logEnd(op, err, attrs...)
	return err
}

// ConnectionTestResult はフロントエンド向けの接続テスト結果
// (frontend/src/lib/backend.ts の ConnectionTestResult と対)。
type ConnectionTestResult struct {
	Ok             bool   `json:"ok"`
	UserID         int    `json:"userId"`
	UserName       string `json:"userName"`
	RoleType       int    `json:"roleType"`
	AdminAvailable bool   `json:"adminAvailable"` // roleType による暫定判定(確定は GetPermissionStatus)
	Message        string `json:"message"`
}

// TestConnection は接続テスト(GET /users/myself)を行う(保存はしない)。
// apiKey が空で profileID が指定されている場合はキーチェーンの既存キーでテストする
// (変更フォームでキーを再入力せずに再テストするための規約)。
func (a *App) TestConnection(profileID, spaceURL, apiKey string) (*ConnectionTestResult, error) {
	const op = "TestConnection"
	// API キー・スペース URL・ユーザ名は記録しない(キー入力の有無のみ)
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Bool("apiKeyProvided", apiKey != ""),
	}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	info, err := s.TestConnectionForProfile(a.ctx, profileID, spaceURL, apiKey)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	admin := backlogclient.RoleType(info.RoleType) == backlogclient.RoleAdmin
	msg := "接続に成功しました(ロール: " + info.RoleName + ")"
	if !admin {
		msg += "。管理者権限が無いため、ユーザ・チーム抽出は縮退する可能性があります"
	}
	a.logEnd(op, nil, append(attrs,
		slog.Int("roleType", info.RoleType),
		slog.Bool("adminAvailable", admin))...)
	return &ConnectionTestResult{
		Ok:             true,
		UserID:         info.UserID,
		UserName:       info.Name,
		RoleType:       info.RoleType,
		AdminAvailable: admin,
		Message:        msg,
	}, nil
}

// GetPermissionStatus は GET /users と GET /teams を各 1 回呼び、
// 実権限を確認する(いずれかが 403 なら縮退状態を返す)。
func (a *App) GetPermissionStatus(profileID string) (*service.PermissionStatus, error) {
	const op = "GetPermissionStatus"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	st, err := s.GetPermissionStatus(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs,
		slog.Bool("adminAvailable", st.AdminAvailable),
		slog.Bool("degraded", st.Degraded))...)
	return st, nil
}

// GetRateLimitStatus は区分別(read / update / search / icon)のレート制限残量を返す。
// 追加の API 呼び出しは行わず、これまでの通信で観測した値と経過時間だけで算出する
// (observed が false の区分は実値を取得できていない = UI では「不明」扱い)。
func (a *App) GetRateLimitStatus(profileID string) (*service.RateLimitStatus, error) {
	const op = "GetRateLimitStatus"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	st, err := s.GetRateLimitStatus(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	// 記録するのは区分数と実値を取得できた区分数のみ(残量値は記録しない)
	observed := 0
	for _, c := range st.Categories {
		if c.Observed {
			observed++
		}
	}
	a.logEnd(op, nil, append(attrs,
		slog.Int("count", len(st.Categories)),
		slog.Int("observedCount", observed))...)
	return st, nil
}

// ---- M2: 同期・課題抽出・Excel 出力(frontend/src/lib/backend.ts の契約と対) ----

// ProjectRow はプロジェクト一覧の 1 行(課題同期の最終時刻付き)。
type ProjectRow struct {
	ID           int64  `json:"id"`
	ProjectKey   string `json:"projectKey"`
	Name         string `json:"name"`
	LastSyncedAt string `json:"lastSyncedAt"`
	// SyncStateUnknown は同期状態の取得に失敗したことを示す(中 1)。
	// 真のときの LastSyncedAt は「未同期」ではなく「不明」であり、
	// UI は未同期の警告を出してはならない。
	SyncStateUnknown bool `json:"syncStateUnknown"`
}

// ListProjects はローカル DB のプロジェクト一覧を返す。
func (a *App) ListProjects(profileID string) ([]ProjectRow, error) {
	const op = "ListProjects"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	projects, err := s.ListProjects(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	rows := make([]ProjectRow, 0, len(projects))
	for _, p := range projects {
		last := ""
		unknown := false
		st, serr := s.GetSyncState(a.ctx, profileID, "", p.ID)
		switch {
		case serr != nil:
			// 鮮度が取れないと同期済みでも「未同期」と表示されてしまうため、
			// 「不明」であることを UI へ伝えつつ原因をログに残す(黙って握り潰さない)
			unknown = true
			a.log.OpError("ListProjects 同期状態の取得", serr,
				slog.String("profileId", profileID), slog.Int64("projectId", p.ID))
		case st != nil:
			last = st.LastSyncedAt
		}
		rows = append(rows, ProjectRow{
			ID:               p.ID,
			ProjectKey:       p.ProjectKey,
			Name:             p.Name,
			LastSyncedAt:     last,
			SyncStateUnknown: unknown,
		})
	}
	a.logEnd(op, nil, append(attrs, slog.Int("count", len(rows)))...)
	return rows, nil
}

// SyncProjects は参加プロジェクト一覧を API から取得してローカル DB へ反映する。
func (a *App) SyncProjects(profileID string) error {
	const op = "SyncProjects"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return err
	}
	res, err := s.SyncProjects(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return err
	}
	a.logEnd(op, nil, append(attrs,
		slog.Int("fetched", res.Fetched),
		slog.Int("upserted", res.Upserted),
		slog.Int("deleted", res.Deleted),
		slog.Int("warnings", len(res.Warnings)),
		slog.Int64("durationMs", res.DurationMs))...)
	return nil
}

// SyncResultDTO は同期結果(フロント契約: warnings は null 不可)。
type SyncResultDTO struct {
	Mode       string   `json:"mode"`
	Fetched    int      `json:"fetched"`
	Upserted   int      `json:"upserted"`
	Deleted    int      `json:"deleted"`
	Warnings   []string `json:"warnings"`
	DurationMs int64    `json:"durationMs"`
}

// SyncIssues は指定プロジェクトの課題を同期する(mode: full / incremental / auto)。
func (a *App) SyncIssues(profileID string, projectID int64, mode string) (*SyncResultDTO, error) {
	const op = "SyncIssues"
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("projectId", projectID),
		slog.String("mode", mode),
	}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	res, err := s.SyncIssues(a.ctx, profileID, projectID, mode)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	// 警告本文は課題名等を含みうるため件数のみ記録する
	a.logEnd(op, nil, append(attrs,
		slog.String("executedMode", string(res.Mode)),
		slog.Int("fetched", res.Fetched),
		slog.Int("upserted", res.Upserted),
		slog.Int("deleted", res.Deleted),
		slog.Int("warnings", len(warnings)),
		slog.Int64("durationMs", res.DurationMs))...)
	return &SyncResultDTO{
		Mode:       string(res.Mode),
		Fetched:    res.Fetched,
		Upserted:   res.Upserted,
		Deleted:    res.Deleted,
		Warnings:   warnings,
		DurationMs: res.DurationMs,
	}, nil
}

// IssueRowDTO は検索結果の 1 行(プレビュー・Excel 出力の共通形)。
type IssueRowDTO struct {
	IssueKey      string `json:"issueKey"`
	Summary       string `json:"summary"`
	StatusName    string `json:"statusName"`
	AssigneeName  string `json:"assigneeName"`
	IssueTypeName string `json:"issueTypeName"`
	PriorityName  string `json:"priorityName"`
	Created       string `json:"created"`
	Updated       string `json:"updated"`
	DueDate       string `json:"dueDate"`
}

// IssueSearchDTO は検索結果(表示上限で切っても total は総件数)。
type IssueSearchDTO struct {
	Rows  []IssueRowDTO `json:"rows"`
	Total int           `json:"total"`
}

// SearchIssues はローカル DB から課題を抽出する(store.IssueFilter の json 名は
// フロント契約 IssueQuery と一致)。
func (a *App) SearchIssues(profileID string, query store.IssueFilter) (*IssueSearchDTO, error) {
	const op = "SearchIssues"
	attrs := a.searchAttrs(profileID, query)
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	res, err := s.SearchIssues(a.ctx, profileID, query)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	rows := make([]IssueRowDTO, 0, len(res.Issues))
	for _, is := range res.Issues {
		rows = append(rows, IssueRowDTO{
			IssueKey:      is.IssueKey,
			Summary:       is.Summary,
			StatusName:    is.StatusName,
			AssigneeName:  is.AssigneeName,
			IssueTypeName: is.IssueTypeName,
			PriorityName:  is.PriorityName,
			Created:       is.Created,
			Updated:       is.Updated,
			DueDate:       is.DueDate,
		})
	}
	a.logEnd(op, nil, append(attrs,
		slog.Int("rows", len(rows)),
		slog.Int("total", res.Total),
		slog.Bool("truncated", res.Truncated))...)
	return &IssueSearchDTO{Rows: rows, Total: res.Total}, nil
}

// searchAttrs は検索条件のうち非機密なものだけをログ属性にする。
// キーワード・状態名・担当者名は課題内容や個人名を含みうるため、
// 値は記録せず「指定の有無」だけを記録する。
func (a *App) searchAttrs(profileID string, query store.IssueFilter) []slog.Attr {
	return []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("projectId", query.ProjectID),
		slog.Bool("hasKeyword", query.Keyword != ""),
		slog.Bool("hasStatus", query.StatusName != ""),
		slog.Bool("hasAssignee", query.AssigneeName != ""),
		slog.Bool("hasDateRange", query.UpdatedFrom != "" || query.UpdatedTo != "" ||
			query.CreatedFrom != "" || query.CreatedTo != ""),
		slog.Int("limit", query.Limit),
	}
}

// FilterOptionsDTO は抽出条件の候補(フロント契約: statuses / assignees)。
type FilterOptionsDTO struct {
	Statuses  []string `json:"statuses"`
	Assignees []string `json:"assignees"`
}

// ListFilterOptions は状態・担当者の候補値をローカル DB から返す。
func (a *App) ListFilterOptions(profileID string, projectID int64) (*FilterOptionsDTO, error) {
	const op = "ListFilterOptions"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	opts, err := s.ListFilterOptions(a.ctx, profileID, projectID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	statuses := opts.StatusNames
	if statuses == nil {
		statuses = []string{}
	}
	assignees := opts.AssigneeNames
	if assignees == nil {
		assignees = []string{}
	}
	// 候補値そのもの(状態名・担当者名)は記録せず件数のみ記録する
	a.logEnd(op, nil, append(attrs,
		slog.Int("statuses", len(statuses)),
		slog.Int("assignees", len(assignees)))...)
	return &FilterOptionsDTO{Statuses: statuses, Assignees: assignees}, nil
}

// SyncStateRow は同期状態画面の 1 行。
type SyncStateRow struct {
	DataKind     string `json:"dataKind"`
	ProjectID    int64  `json:"projectId"`
	LastSyncedAt string `json:"lastSyncedAt"`
}

// GetSyncState は全同期状態の一覧を返す(フロント契約に合わせ配列を返す)。
func (a *App) GetSyncState(profileID string) ([]SyncStateRow, error) {
	const op = "GetSyncState"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	states, err := s.ListSyncStates(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	rows := make([]SyncStateRow, 0, len(states))
	for _, st := range states {
		rows = append(rows, SyncStateRow{DataKind: st.DataKind, ProjectID: st.ProjectID, LastSyncedAt: st.LastSyncedAt})
	}
	a.logEnd(op, nil, append(attrs, slog.Int("count", len(rows)))...)
	return rows, nil
}

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

// ExportResultDTO は Excel 出力結果(キャンセル時は path 空・rows 0)。
type ExportResultDTO struct {
	Path string `json:"path"`
	Rows int    `json:"rows"`
}

// exportSearchLimit は Excel 出力時の取得上限。フロント契約では「条件一致全件」
// を出力するため、実質無制限の大きな値を使う。
const exportSearchLimit = 1_000_000

// ExportIssuesExcel は検索条件に一致する課題全件を Excel に出力する。
// 保存先は OS の保存ダイアログでユーザが選択する(キャンセル時は path 空)。
func (a *App) ExportIssuesExcel(profileID string, query store.IssueFilter, columns []string) (*ExportResultDTO, error) {
	const op = "ExportIssuesExcel"
	attrs := append(a.searchAttrs(profileID, query), slog.Int("columns", len(columns)))
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	query.Limit = exportSearchLimit
	res, err := s.SearchIssues(a.ctx, profileID, query)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	// 上限で切り詰められた場合は黙って部分出力せず、明示的にエラーを返す(中 3)
	if res.Truncated {
		err := errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください")
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	// カスタム属性列が選ばれている場合のみ、ヘッダ(定義名)と値の解決に必要な
	// 定義を取得する(選ばれていなければ API 呼び出しを増やさない)。
	// 保存先を尋ねる前に取得し、失敗した場合はダイアログを出さずにエラーを返す
	// (利用者が明示的に選んだ列を黙って空欄・欠落にしない)。
	opts := export.Options{Columns: columns}
	if export.HasCustomColumns(columns) {
		master, err := s.GetMasterData(a.ctx, profileID, query.ProjectID)
		if err != nil {
			a.logEnd(op, err, attrs...)
			return nil, err
		}
		opts.CustomFields = master.CustomFields
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Excel 出力先を選択",
		DefaultFilename: "backlog-issues.xlsx",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		a.logEnd(op, nil, append(attrs, slog.Bool("canceled", true))...)
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	// 保存先・ファイル名はユーザが決めるため、ローカルユーザ名や顧客名を
	// 含みうる。パスもファイル名も記録せず、拡張子だけを残す(低 1)。
	fileAttr := fileExtAttr(path)
	if err := export.ExportIssuesToFile(path, res.Issues, opts); err != nil {
		// 失敗時のエラーメッセージにも保存先のフルパスが含まれるため、
		// ログへ渡す前にプレースホルダへ置換する(高 2 / 2 回目 低 1)。
		// 画面へ返すエラーは、ユーザ自身が選んだパスなのでそのままにする。
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs,
		fileAttr,
		slog.Int("rows", len(res.Issues)))...)
	return &ExportResultDTO{Path: path, Rows: len(res.Issues)}, nil
}

// ---- M3: ユーザ抽出(frontend/src/lib/backend.ts の契約と対) ----

// userAttrs はユーザ検索条件の動作ログ属性(キーワード本文は個人名を含みうるため有無のみ)。
func userAttrs(profileID string, filter store.UserFilter) []slog.Attr {
	return []slog.Attr{
		slog.String("profileId", profileID),
		slog.Bool("hasKeyword", filter.Keyword != ""),
		slog.Int("roleType", filter.RoleType),
	}
}

// SyncUsers はユーザ・チーム・プロジェクト参加情報を同期する
// (権限が無い場合はプロジェクト単位の取得へ自動縮退する)。
func (a *App) SyncUsers(profileID string) (*SyncResultDTO, error) {
	const op = "SyncUsers"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	res, err := s.SyncUsers(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	// 警告本文はプロジェクト名等を含みうるため件数のみ記録する
	a.logEnd(op, nil, append(attrs,
		slog.String("executedMode", string(res.Mode)),
		slog.Int("fetched", res.Fetched),
		slog.Int("upserted", res.Upserted),
		slog.Int("warnings", len(warnings)),
		slog.Int64("durationMs", res.DurationMs))...)
	return &SyncResultDTO{
		Mode:       string(res.Mode),
		Fetched:    res.Fetched,
		Upserted:   res.Upserted,
		Deleted:    res.Deleted,
		Warnings:   warnings,
		DurationMs: res.DurationMs,
	}, nil
}

// UserListDTO はユーザ一覧の検索結果(フロント契約: rows / total)。
type UserListDTO struct {
	Rows  []store.UserRow `json:"rows"`
	Total int             `json:"total"`
}

// ListUsers はローカル DB からユーザ一覧を返す(所属チーム・参加プロジェクト付き)。
func (a *App) ListUsers(profileID string, query store.UserFilter) (*UserListDTO, error) {
	const op = "ListUsers"
	attrs := userAttrs(profileID, query)
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	res, err := s.ListUsers(a.ctx, profileID, query)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	rows := res.Users
	if rows == nil {
		rows = []store.UserRow{}
	}
	a.logEnd(op, nil, append(attrs, slog.Int("rows", len(rows)), slog.Int("total", res.Total))...)
	return &UserListDTO{Rows: rows, Total: res.Total}, nil
}

// ExportUsersExcel は条件に一致するユーザ全件を Excel に出力する。
func (a *App) ExportUsersExcel(profileID string, query store.UserFilter, columns []string) (*ExportResultDTO, error) {
	const op = "ExportUsersExcel"
	attrs := append(userAttrs(profileID, query), slog.Int("columns", len(columns)))
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	query.Limit = exportSearchLimit
	res, err := s.ListUsers(a.ctx, profileID, query)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if res.Truncated {
		err := errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください")
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Excel 出力先を選択",
		DefaultFilename: "backlog-users.xlsx",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		a.logEnd(op, nil, append(attrs, slog.Bool("canceled", true))...)
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	exportRows := make([]export.UserExportRow, 0, len(res.Users))
	for _, u := range res.Users {
		exportRows = append(exportRows, export.UserExportRow{
			ID:               u.ID,
			UserCode:         u.UserCode,
			Name:             u.Name,
			MailAddress:      u.MailAddress,
			RoleType:         u.RoleType,
			RoleName:         u.RoleName,
			TeamNames:        u.TeamNames,
			ProjectKeys:      u.ProjectKeys,
			AdminProjectKeys: u.AdminProjectKeys,
		})
	}
	fileAttr := fileExtAttr(path)
	if err := export.ExportUsersToFile(path, exportRows, export.UserOptions{Columns: columns}); err != nil {
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs, fileAttr, slog.Int("rows", len(exportRows)))...)
	return &ExportResultDTO{Path: path, Rows: len(exportRows)}, nil
}

// ---- M4: 一括更新・追加(frontend/src/lib/backend.ts の契約と対) ----

// rawIssueIDs は課題の raw_json から種別 ID・優先度 ID を取り出す
// (store.Issue は名前のみ保持しているため、テンプレートの ID 列はここで補完する)。
func rawIssueIDs(rawJSON string) (issueTypeID, priorityID int64) {
	var v struct {
		IssueType struct {
			ID int64 `json:"id"`
		} `json:"issueType"`
		Priority struct {
			ID int64 `json:"id"`
		} `json:"priority"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &v); err != nil {
		return 0, 0
	}
	return v.IssueType.ID, v.Priority.ID
}

// bulkTemplateMasters はテンプレートの「マスタ」シートに載せる選択候補を集める。
//
// 種別・状態・優先度は API のマスタ(取り込み時の検証と同じ内容)、
// 担当者はローカルのプロジェクト参加者(未同期ならスペース全体)を使う。
// export へ渡す型に詰め替えることで、export が bulk・store に依存しないようにする。
func (a *App) bulkTemplateMasters(profileID string, projectID int64) (export.BulkTemplateMasters, error) {
	var out export.BulkTemplateMasters
	s, err := a.svc()
	if err != nil {
		return out, err
	}
	master, err := s.GetMasterData(a.ctx, profileID, projectID)
	if err != nil {
		return out, err
	}
	out.IssueTypes = namedRefsOf(master.IssueTypes)
	out.Statuses = namedRefsOf(master.Statuses)
	out.Priorities = namedRefsOf(master.Priorities)

	users, err := s.ListAssigneeCandidates(a.ctx, profileID, projectID)
	if err != nil {
		return out, err
	}
	out.Assignees = make([]export.NamedRef, 0, len(users))
	for _, u := range users {
		out.Assignees = append(out.Assignees, export.NamedRef{ID: u.ID, Name: u.Name})
	}
	return out, nil
}

// namedRefsOf はマスタ(bulk.NamedID)を export の候補型へ詰め替える。
func namedRefsOf(items []bulk.NamedID) []export.NamedRef {
	out := make([]export.NamedRef, 0, len(items))
	for _, it := range items {
		out = append(out, export.NamedRef{ID: it.ID, Name: it.Name})
	}
	return out
}

// ExportBulkTemplate は一括更新テンプレート(既存課題 + base_updated)を Excel 出力する。
func (a *App) ExportBulkTemplate(profileID string, projectID int64, query store.IssueFilter) (*ExportResultDTO, error) {
	const op = "ExportBulkTemplate"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	query.ProjectID = projectID
	query.Limit = exportSearchLimit
	res, err := s.SearchIssues(a.ctx, profileID, query)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if res.Truncated {
		err := errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください")
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	// 名前で編集できるようにするため、テンプレートへ選択候補(種別・状態・優先度・担当者)を載せる。
	// 保存先を尋ねる前に取得し、失敗した場合はダイアログを出さずに終わる。
	masters, err := a.bulkTemplateMasters(profileID, projectID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "テンプレートの出力先を選択",
		DefaultFilename: "backlog-bulk-template.xlsx",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		a.logEnd(op, nil, append(attrs, slog.Bool("canceled", true))...)
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	rows := make([]export.BulkTemplateRow, 0, len(res.Issues))
	for _, is := range res.Issues {
		typeID, priorityID := rawIssueIDs(is.RawJSON)
		rows = append(rows, export.BulkTemplateRow{
			IssueKey:      is.IssueKey,
			Summary:       is.Summary,
			IssueTypeID:   typeID,
			IssueTypeName: is.IssueTypeName,
			StatusID:      is.StatusID,
			StatusName:    is.StatusName,
			PriorityID:    priorityID,
			PriorityName:  is.PriorityName,
			AssigneeID:    is.AssigneeID,
			AssigneeName:  is.AssigneeName,
			DueDate:       is.DueDate,
			Description:   is.Description,
			BaseUpdated:   is.Updated,
		})
	}
	fileAttr := fileExtAttr(path)
	if err := export.ExportBulkTemplateToFile(path, projectID, rows, masters); err != nil {
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs, fileAttr, slog.Int("rows", len(rows)))...)
	return &ExportResultDTO{Path: path, Rows: len(rows)}, nil
}

// ImportBulkFile は記入済み Excel を選択して取り込み、検証 + dry-run プレビューを返す。
// ファイル選択キャンセル時は jobId=0 かつ totalRows=0 を返す(フロント契約)。
func (a *App) ImportBulkFile(profileID string, projectID int64, defaultPriorityID int64) (*bulk.ImportResult, error) {
	const op = "ImportBulkFile"
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("projectId", projectID),
		slog.Int64("defaultPriorityId", defaultPriorityID),
	}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "記入済みの Excel ファイルを選択",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		a.logEnd(op, nil, append(attrs, slog.Bool("canceled", true))...)
		return &bulk.ImportResult{
			ProjectID: projectID,
			Errors:    []bulk.RowError{},
			Previews:  []bulk.RowPreview{},
			Warnings:  []string{},
		}, nil
	}
	fileAttr := fileExtAttr(path)
	res, err := s.ImportBulkFile(a.ctx, profileID, projectID, path, defaultPriorityID)
	if err != nil {
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	if res.Errors == nil {
		res.Errors = []bulk.RowError{}
	}
	if res.Previews == nil {
		res.Previews = []bulk.RowPreview{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	// 警告本文はプロジェクト情報等を含みうるため件数のみ記録する
	a.logEnd(op, nil, append(attrs,
		fileAttr,
		slog.Int64("jobId", res.JobID),
		slog.Int("totalRows", res.TotalRows),
		slog.Int("creates", res.Creates),
		slog.Int("updates", res.Updates),
		slog.Int("errors", len(res.Errors)),
		slog.Int("warnings", len(res.Warnings)),
		slog.Bool("valid", res.Valid))...)
	return res, nil
}

// RunBulkJob は取り込み済みジョブを実行する(1 件ずつ POST/PATCH。進捗は
// Wails イベント 'bulk:progress' {jobId, processed, total} で通知)。
func (a *App) RunBulkJob(profileID string, jobID int64, force, resendSending bool) (*bulk.RunResult, error) {
	const op = "RunBulkJob"
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("jobId", jobID),
		slog.Bool("force", force),
		slog.Bool("resendSending", resendSending),
	}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	onProgress := func(p bulk.Progress) {
		wailsruntime.EventsEmit(a.ctx, "bulk:progress", map[string]any{
			"jobId":     jobID,
			"processed": p.Processed,
			"total":     p.Total,
		})
	}
	res, err := s.RunBulkJob(a.ctx, profileID, jobID, bulk.RunOptions{Force: force, ResendSending: resendSending}, onProgress)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	// 警告本文は課題キー等を含みうるため件数のみ記録する
	a.logEnd(op, nil, append(attrs,
		slog.Int("done", res.Done),
		slog.Int("failed", res.Failed),
		slog.Int("conflict", res.Conflict),
		slog.Int("skipped", res.Skipped),
		slog.Int("warnings", len(res.Warnings)),
		slog.Int64("durationMs", res.DurationMs))...)
	return res, nil
}

// CancelBulkRun は実行中の一括ジョブへキャンセルを要求する(行間で反映される)。
// ジョブ ID はプロファイルごとの採番のため、プロファイル ID と併せて指定する(中 2)。
func (a *App) CancelBulkRun(profileID string, jobID int64) error {
	const op = "CancelBulkRun"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("jobId", jobID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return err
	}
	s.CancelBulkRun(profileID, jobID)
	a.logEnd(op, nil, attrs...)
	return nil
}

// ListBulkJobs は一括ジョブの履歴(行数集計付き)を返す。
func (a *App) ListBulkJobs(profileID string) ([]store.JobSummary, error) {
	const op = "ListBulkJobs"
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	jobs, err := s.ListBulkJobs(a.ctx, profileID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if jobs == nil {
		jobs = []store.JobSummary{}
	}
	a.logEnd(op, nil, append(attrs, slog.Int("jobs", len(jobs)))...)
	return jobs, nil
}

// BulkJobRowDTO は一括ジョブの行明細 1 行(フロント契約)。
//
// payload(送信内容)・baseUpdated は返さない。課題本文・件名を含みうるうえ、
// 画面での結果確認には不要なため(設計書 7 節)。
type BulkJobRowDTO struct {
	RowNo         int    `json:"rowNo"`
	IssueKey      string `json:"issueKey"`
	Status        string `json:"status"`
	ResultIssueID int64  `json:"resultIssueId"`
	Error         string `json:"error"`
}

// GetBulkJobRows はジョブの行明細を行番号順で返す(実行結果の確認用)。
func (a *App) GetBulkJobRows(profileID string, jobID int64) ([]BulkJobRowDTO, error) {
	const op = "GetBulkJobRows"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("jobId", jobID)}
	a.logStart(op, attrs...)
	rows, err := a.bulkJobRows(profileID, jobID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	out := make([]BulkJobRowDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, BulkJobRowDTO{
			RowNo:         r.RowNo,
			IssueKey:      r.IssueKey,
			Status:        r.Status,
			ResultIssueID: r.ResultIssueID,
			Error:         r.Error,
		})
	}
	a.logEnd(op, nil, append(attrs, slog.Int("rows", len(out)))...)
	return out, nil
}

// bulkJobRows はジョブの行明細を取得する(バインディング共通の前処理)。
func (a *App) bulkJobRows(profileID string, jobID int64) ([]store.JobRow, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.GetBulkJobRows(a.ctx, profileID, jobID)
}

// bulkRowAction は行の処理区分の表示名を返す。
// payload を解析せず、行状態と課題キーの有無だけで判断する
// (送信内容を画面・Excel へ持ち出さないため)。
func bulkRowAction(row store.JobRow) string {
	switch {
	case row.Status == store.RowStatusSkip:
		return "変更なし"
	case row.IssueKey == "":
		return "追加"
	default:
		return "更新"
	}
}

// bulkRowStatusLabels は行状態の表示名(Excel 用)。
var bulkRowStatusLabels = map[string]string{
	store.RowStatusPending:  "未実行",
	store.RowStatusSending:  "送信中(結果未確認)",
	store.RowStatusDone:     "完了",
	store.RowStatusError:    "失敗",
	store.RowStatusConflict: "競合",
	store.RowStatusSkip:     "変更なし",
}

// bulkRowStatusLabel は行状態の表示名を返す(未知の値はそのまま返す)。
func bulkRowStatusLabel(status string) string {
	if label, ok := bulkRowStatusLabels[status]; ok {
		return label
	}
	return status
}

// ExportBulkResultExcel はジョブの実行結果を Excel に出力する(高 5)。
// 保存先は OS の保存ダイアログでユーザが選択する(キャンセル時は path 空)。
func (a *App) ExportBulkResultExcel(profileID string, jobID int64) (*ExportResultDTO, error) {
	const op = "ExportBulkResultExcel"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("jobId", jobID)}
	a.logStart(op, attrs...)
	rows, err := a.bulkJobRows(profileID, jobID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "実行結果の出力先を選択",
		DefaultFilename: "backlog-bulk-result.xlsx",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		a.logEnd(op, nil, append(attrs, slog.Bool("canceled", true))...)
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	exportRows := make([]export.BulkResultRow, 0, len(rows))
	for _, r := range rows {
		exportRows = append(exportRows, export.BulkResultRow{
			RowNo:         r.RowNo,
			Action:        bulkRowAction(r),
			IssueKey:      r.IssueKey,
			ResultIssueID: r.ResultIssueID,
			Status:        bulkRowStatusLabel(r.Status),
			ErrorMessage:  r.Error,
		})
	}
	fileAttr := fileExtAttr(path)
	if err := export.ExportBulkResultToFile(path, exportRows); err != nil {
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs, fileAttr, slog.Int("rows", len(exportRows)))...)
	return &ExportResultDTO{Path: path, Rows: len(exportRows)}, nil
}

// CustomFieldItemDTO はリスト系カスタム属性の選択肢
// (frontend/src/lib/backend.ts の CustomFieldItem と対)。
type CustomFieldItemDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// CustomFieldDefDTO はカスタム属性の定義
// (frontend/src/lib/backend.ts の CustomFieldDef と対)。
//
// typeName は画面での型判定・表示に使うため Go 側で解決して渡す
// (型 ID の対応表をフロントへ二重に持たせない)。
type CustomFieldDefDTO struct {
	ID          int64  `json:"id"`
	TypeID      int    `json:"typeId"`
	TypeName    string `json:"typeName"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	// ApplicableIssueTypes は適用対象の課題種別 ID(空 = 全課題種別)。
	ApplicableIssueTypes []int64              `json:"applicableIssueTypes"`
	AllowInput           bool                 `json:"allowInput"`
	AllowAddItem         bool                 `json:"allowAddItem"`
	Items                []CustomFieldItemDTO `json:"items"`
}

// MasterDataDTO は種別・優先度・状態・カスタム属性のマスタ
// (frontend/src/lib/backend.ts の MasterData と対。各配列は null を返さない)。
type MasterDataDTO struct {
	IssueTypes   []bulk.NamedID      `json:"issueTypes"`
	Priorities   []bulk.NamedID      `json:"priorities"`
	Statuses     []bulk.NamedID      `json:"statuses"`
	CustomFields []CustomFieldDefDTO `json:"customFields"`
}

// newMasterDataDTO はマスタを DTO へ写す(nil スライスは空スライスへ正規化)。
func newMasterDataDTO(md *bulk.MasterData) *MasterDataDTO {
	dto := &MasterDataDTO{
		IssueTypes:   md.IssueTypes,
		Priorities:   md.Priorities,
		Statuses:     md.Statuses,
		CustomFields: make([]CustomFieldDefDTO, 0, len(md.CustomFields)),
	}
	if dto.IssueTypes == nil {
		dto.IssueTypes = []bulk.NamedID{}
	}
	if dto.Priorities == nil {
		dto.Priorities = []bulk.NamedID{}
	}
	if dto.Statuses == nil {
		dto.Statuses = []bulk.NamedID{}
	}
	for _, def := range md.CustomFields {
		d := CustomFieldDefDTO{
			ID:                   def.ID,
			TypeID:               def.TypeID,
			TypeName:             customfield.TypeName(def.TypeID),
			Name:                 def.Name,
			Description:          def.Description,
			Required:             def.Required,
			ApplicableIssueTypes: def.ApplicableIssueTypes,
			AllowInput:           def.AllowInput,
			AllowAddItem:         def.AllowAddItem,
			Items:                make([]CustomFieldItemDTO, 0, len(def.Items)),
		}
		if d.ApplicableIssueTypes == nil {
			d.ApplicableIssueTypes = []int64{}
		}
		for _, it := range def.Items {
			d.Items = append(d.Items, CustomFieldItemDTO{ID: it.ID, Name: it.Name})
		}
		dto.CustomFields = append(dto.CustomFields, d)
	}
	return dto
}

// GetMasterData は種別・優先度・状態・カスタム属性のマスタを返す
// (取り込みの既定優先度選択などに使用)。
func (a *App) GetMasterData(profileID string, projectID int64) (*MasterDataDTO, error) {
	const op = "GetMasterData"
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	a.logStart(op, attrs...)
	s, err := a.svc()
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	md, err := s.GetMasterData(a.ctx, profileID, projectID)
	if err != nil {
		a.logEnd(op, err, attrs...)
		return nil, err
	}
	dto := newMasterDataDTO(md)
	a.logEnd(op, nil, append(attrs,
		slog.Int("issueTypes", len(dto.IssueTypes)),
		slog.Int("priorities", len(dto.Priorities)),
		slog.Int("statuses", len(dto.Statuses)),
		slog.Int("customFields", len(dto.CustomFields)))...)
	return dto, nil
}
