package main

// app_profile.go は接続設定画面向けのバインディング
// (プロファイル CRUD・接続テスト・権限確認・レート制限残量)。

import (
	"log/slog"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/service"
)

// ListProfiles は保存済みプロファイル一覧を返す。
func (a *App) ListProfiles() ([]config.Profile, error) {
	return appOp(a, "ListProfiles", nil,
		func(s *service.ProfileService) ([]config.Profile, []slog.Attr, error) {
			profiles, err := s.ListProfiles()
			if err != nil {
				return nil, nil, err
			}
			return profiles, []slog.Attr{slog.Int("count", len(profiles))}, nil
		})
}

// GetActiveProfile は保存済みの接続先プロファイル ID を返す(未選択なら空文字)。
// 起動時に ListProfiles と併せて呼び、セレクタの初期選択に使う。
func (a *App) GetActiveProfile() (string, error) {
	return appOp(a, "GetActiveProfile", nil,
		func(s *service.ProfileService) (string, []slog.Attr, error) {
			id, err := s.GetActiveProfile()
			if err != nil {
				return "", nil, err
			}
			return id, []slog.Attr{slog.String("profileId", id)}, nil
		})
}

// SetActiveProfile は接続先プロファイル ID を保存する(空文字 = 選択解除)。
// フロントの接続先セレクタ変更時に呼ばれる。
//
// ここで行うのは設定の永続化(次回起動時の初期選択)のみでよい。
// ローカル DB(store.Open)と API クライアントは、各操作が引数で受け取った
// profileID から service 側で解決・キャッシュする(ProfileService.storeForProfile /
// clientForProfile)ため、接続先の切り替えはこの値に依存しない。
func (a *App) SetActiveProfile(id string) error {
	attrs := []slog.Attr{slog.String("profileId", id)}
	return appOpErr(a, "SetActiveProfile", attrs,
		func(s *service.ProfileService) ([]slog.Attr, error) {
			return nil, s.SetActiveProfile(id)
		})
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
	// API キーは値を一切記録せず、入力されたか(bool)だけを記録する。
	// 表示名・スペース URL も利用者を特定しうるため記録しない。
	attrs := []slog.Attr{
		slog.String("profileId", input.ID),
		slog.Bool("new", input.ID == ""),
		slog.Bool("apiKeyProvided", input.APIKey != ""),
	}
	return appOp(a, "SaveProfile", attrs,
		func(s *service.ProfileService) (*config.Profile, []slog.Attr, error) {
			res, err := s.SaveProfile(a.ctx, input.ID, input.Name, input.SpaceURL, input.APIKey)
			if err != nil {
				return nil, nil, err
			}
			return &res.Profile, []slog.Attr{slog.String("savedProfileId", res.Profile.ID)}, nil
		})
}

// DeleteProfile はプロファイルを削除する。キーチェーンの API キーは必ず削除し、
// deleteDB が真ならローカル DB も削除する。
func (a *App) DeleteProfile(id string, deleteDB bool) error {
	attrs := []slog.Attr{slog.String("profileId", id), slog.Bool("deleteLocalData", deleteDB)}
	return appOpErr(a, "DeleteProfile", attrs,
		func(s *service.ProfileService) ([]slog.Attr, error) {
			return nil, s.DeleteProfile(id, deleteDB)
		})
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
	// API キー・スペース URL・ユーザ名は記録しない(キー入力の有無のみ)
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Bool("apiKeyProvided", apiKey != ""),
	}
	return appOp(a, "TestConnection", attrs,
		func(s *service.ProfileService) (*ConnectionTestResult, []slog.Attr, error) {
			info, err := s.TestConnectionForProfile(a.ctx, profileID, spaceURL, apiKey)
			if err != nil {
				return nil, nil, err
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
				}, []slog.Attr{
					slog.Int("roleType", info.RoleType),
					slog.Bool("adminAvailable", admin),
				}, nil
		})
}

// GetPermissionStatus は GET /users と GET /teams を各 1 回呼び、
// 実権限を確認する(いずれかが 403 なら縮退状態を返す)。
func (a *App) GetPermissionStatus(profileID string) (*service.PermissionStatus, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "GetPermissionStatus", attrs,
		func(s *service.ProfileService) (*service.PermissionStatus, []slog.Attr, error) {
			st, err := s.GetPermissionStatus(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			return st, []slog.Attr{
				slog.Bool("adminAvailable", st.AdminAvailable),
				slog.Bool("degraded", st.Degraded),
			}, nil
		})
}

// GetRateLimitStatus は区分別(read / update / search / icon)のレート制限残量を返す。
// 追加の API 呼び出しは行わず、これまでの通信で観測した値と経過時間だけで算出する
// (observed が false の区分は実値を取得できていない = UI では「不明」扱い)。
func (a *App) GetRateLimitStatus(profileID string) (*service.RateLimitStatus, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "GetRateLimitStatus", attrs,
		func(s *service.ProfileService) (*service.RateLimitStatus, []slog.Attr, error) {
			st, err := s.GetRateLimitStatus(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			// 記録するのは区分数と実値を取得できた区分数のみ(残量値は記録しない)
			observed := 0
			for _, c := range st.Categories {
				if c.Observed {
					observed++
				}
			}
			return st, []slog.Attr{
				slog.Int("count", len(st.Categories)),
				slog.Int("observedCount", observed),
			}, nil
		})
}
