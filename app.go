package main

import (
	"context"
	"errors"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

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
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	mgr, err := config.NewManager()
	if err != nil {
		a.initErr = err
		return
	}
	a.profiles = service.NewProfileService(mgr)
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.ListProfiles()
}

// GetActiveProfile は保存済みの接続先プロファイル ID を返す(未選択なら空文字)。
// 起動時に ListProfiles と併せて呼び、セレクタの初期選択に使う。
func (a *App) GetActiveProfile() (string, error) {
	s, err := a.svc()
	if err != nil {
		return "", err
	}
	return s.GetActiveProfile()
}

// SetActiveProfile は接続先プロファイル ID を保存する(空文字 = 選択解除)。
// フロントの接続先セレクタ変更時に呼ばれる。
//
// TODO(マイルストーン 2): activeProfileId の変更を DB オープン(store.Open)と
// 同期処理の接続先切り替えへ結線する。現時点では設定の永続化のみ行う。
func (a *App) SetActiveProfile(id string) error {
	s, err := a.svc()
	if err != nil {
		return err
	}
	return s.SetActiveProfile(id)
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	res, err := s.SaveProfile(a.ctx, input.ID, input.Name, input.SpaceURL, input.APIKey)
	if err != nil {
		return nil, err
	}
	return &res.Profile, nil
}

// DeleteProfile はプロファイルを削除する。キーチェーンの API キーは必ず削除し、
// deleteDB が真ならローカル DB も削除する。
func (a *App) DeleteProfile(id string, deleteDB bool) error {
	s, err := a.svc()
	if err != nil {
		return err
	}
	return s.DeleteProfile(id, deleteDB)
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	info, err := s.TestConnectionForProfile(a.ctx, profileID, spaceURL, apiKey)
	if err != nil {
		return nil, err
	}
	admin := backlogclient.RoleType(info.RoleType) == backlogclient.RoleAdmin
	msg := "接続に成功しました(ロール: " + info.RoleName + ")"
	if !admin {
		msg += "。管理者権限が無いため、ユーザ・チーム抽出は縮退する可能性があります"
	}
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.GetPermissionStatus(a.ctx, profileID)
}

// ---- M2: 同期・課題抽出・Excel 出力(frontend/src/lib/backend.ts の契約と対) ----

// ProjectRow はプロジェクト一覧の 1 行(課題同期の最終時刻付き)。
type ProjectRow struct {
	ID           int64  `json:"id"`
	ProjectKey   string `json:"projectKey"`
	Name         string `json:"name"`
	LastSyncedAt string `json:"lastSyncedAt"`
}

// ListProjects はローカル DB のプロジェクト一覧を返す。
func (a *App) ListProjects(profileID string) ([]ProjectRow, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	projects, err := s.ListProjects(a.ctx, profileID)
	if err != nil {
		return nil, err
	}
	rows := make([]ProjectRow, 0, len(projects))
	for _, p := range projects {
		last := ""
		if st, serr := s.GetSyncState(a.ctx, profileID, "", p.ID); serr == nil && st != nil {
			last = st.LastSyncedAt
		}
		rows = append(rows, ProjectRow{ID: p.ID, ProjectKey: p.ProjectKey, Name: p.Name, LastSyncedAt: last})
	}
	return rows, nil
}

// SyncProjects は参加プロジェクト一覧を API から取得してローカル DB へ反映する。
func (a *App) SyncProjects(profileID string) error {
	s, err := a.svc()
	if err != nil {
		return err
	}
	_, err = s.SyncProjects(a.ctx, profileID)
	return err
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	res, err := s.SyncIssues(a.ctx, profileID, projectID, mode)
	if err != nil {
		return nil, err
	}
	warnings := res.Warnings
	if warnings == nil {
		warnings = []string{}
	}
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	res, err := s.SearchIssues(a.ctx, profileID, query)
	if err != nil {
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
	return &IssueSearchDTO{Rows: rows, Total: res.Total}, nil
}

// FilterOptionsDTO は抽出条件の候補(フロント契約: statuses / assignees)。
type FilterOptionsDTO struct {
	Statuses  []string `json:"statuses"`
	Assignees []string `json:"assignees"`
}

// ListFilterOptions は状態・担当者の候補値をローカル DB から返す。
func (a *App) ListFilterOptions(profileID string, projectID int64) (*FilterOptionsDTO, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	opts, err := s.ListFilterOptions(a.ctx, profileID, projectID)
	if err != nil {
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	states, err := s.ListSyncStates(a.ctx, profileID)
	if err != nil {
		return nil, err
	}
	rows := make([]SyncStateRow, 0, len(states))
	for _, st := range states {
		rows = append(rows, SyncStateRow{DataKind: st.DataKind, ProjectID: st.ProjectID, LastSyncedAt: st.LastSyncedAt})
	}
	return rows, nil
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
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	query.Limit = exportSearchLimit
	res, err := s.SearchIssues(a.ctx, profileID, query)
	if err != nil {
		return nil, err
	}
	// 上限で切り詰められた場合は黙って部分出力せず、明示的にエラーを返す(中 3)
	if res.Truncated {
		return nil, errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください")
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:           "Excel 出力先を選択",
		DefaultFilename: "backlog-issues.xlsx",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Excel ブック (*.xlsx)", Pattern: "*.xlsx"},
		},
	})
	if err != nil {
		return nil, err
	}
	if path == "" { // ユーザがキャンセル
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	if err := export.ExportIssuesToFile(path, res.Issues, export.Options{Columns: columns}); err != nil {
		return nil, err
	}
	return &ExportResultDTO{Path: path, Rows: len(res.Issues)}, nil
}
