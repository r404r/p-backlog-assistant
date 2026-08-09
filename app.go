package main

import (
	"context"
	"errors"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/service"
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
