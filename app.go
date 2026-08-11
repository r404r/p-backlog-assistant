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
	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/config"
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
	a.log.Op("アプリを起動しました")

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

// maskPathInError はエラーメッセージ中の path をそのファイル名へ置換した
// 新しいエラーを返す(動作ログ用。高 2)。
// 保存先ディレクトリはローカルユーザ名や顧客名を含みうるため、ログには残さない。
// err が nil、または path が空の場合は元のエラーをそのまま返す。
func maskPathInError(err error, path string) error {
	if err == nil || path == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), path, filepath.Base(path))
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
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
	// 保存先はユーザが選ぶため、ローカルユーザ名や顧客名を含むフォルダになりうる。
	// ディレクトリは記録せずファイル名だけを残す(高 1)。
	fileAttr := slog.String("fileName", filepath.Base(path))
	if err := export.ExportIssuesToFile(path, res.Issues, export.Options{Columns: columns}); err != nil {
		// 失敗時のエラーメッセージにも保存先のフルパスが含まれるため、
		// ログへ渡す前にファイル名だけへ置換する(高 2)。
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
	fileAttr := slog.String("fileName", filepath.Base(path))
	if err := export.ExportUsersToFile(path, exportRows, export.UserOptions{Columns: columns}); err != nil {
		a.logEnd(op, maskPathInError(err, path), append(attrs, fileAttr)...)
		return nil, err
	}
	a.logEnd(op, nil, append(attrs, fileAttr, slog.Int("rows", len(exportRows)))...)
	return &ExportResultDTO{Path: path, Rows: len(exportRows)}, nil
}
