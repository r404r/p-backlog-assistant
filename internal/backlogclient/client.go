// Package backlogclient は kenzo0107/backlog のラッパー。
// ライブラリを直接使わず、必ずこのパッケージ経由でアクセスすること(設計書 1 節)。
package backlogclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kenzo0107/backlog"
)

// httpDoer は HTTP リクエストの送信口(実体は *interceptor)。
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client は Backlog API クライアント(レート制限・リトライ・エラー正規化込み)。
type Client struct {
	spaceURL string // 正規化済み(https://<host>)
	host     string
	apiKey   string // 自前 HTTP リクエスト(api.go)用。ログ・設定には出さない
	api      *backlog.Client
	httpDo   httpDoer // ライブラリと共用する transport(レート制限・リトライ・マスク)
	limiter  *RateLimiter
}

// New はスペース URL を検証してクライアントを生成する。
// URL が不正(HTTP、許可外ホスト等)ならエラーを返す。
func New(spaceURL, apiKey string) (*Client, error) {
	canonical, err := ValidateSpaceURL(spaceURL)
	if err != nil {
		return nil, err
	}
	if apiKey == "" {
		return nil, fmt.Errorf("API キーが空です")
	}
	limiter := NewRateLimiter()
	// ライブラリ経由・自前 HTTP のどちらも同じ interceptor を通し、
	// レート制限のトークンバケットを共有する。
	ic := newInterceptor(limiter)
	api := backlog.New(apiKey, canonical, backlog.OptionHTTPClient(ic))
	return &Client{
		spaceURL: canonical,
		host:     canonical[len("https://"):],
		apiKey:   apiKey,
		api:      api,
		httpDo:   ic,
		limiter:  limiter,
	}, nil
}

// SpaceURL は正規化済みスペース URL を返す。
func (c *Client) SpaceURL() string { return c.spaceURL }

// Host はスペースのホスト名を返す(DB ファイル名等に使用)。
func (c *Client) Host() string { return c.host }

// RateLimiter はレート制限の状態(UI 残量表示用)を返す。
func (c *Client) RateLimiter() *RateLimiter { return c.limiter }

// RateLimitSnapshot は区分別のレート制限残量を返す(UI 表示用)。
// 追加の API 呼び出しは行わず、これまでの観測値と経過時間だけで算出する。
func (c *Client) RateLimitSnapshot() []CategoryStatus { return c.limiter.Snapshot() }

// InitRateLimit は GET /api/v2/rateLimit で区分別の実上限を取得し、
// トークンバケットを構成する(上限値はハードコードしない)。
func (c *Client) InitRateLimit(ctx context.Context) error {
	rl, err := c.api.GetRateLimitContext(ctx)
	if err != nil {
		return fmt.Errorf("レート制限情報の取得に失敗しました: %w", err)
	}
	c.limiter.ConfigureFromRateLimit(rl)
	return nil
}

// ConnectionInfo は接続テスト(GET /users/myself)の結果。
type ConnectionInfo struct {
	UserID      int    `json:"userId"`      // 数値 ID(DB ファイル名にも使用)
	UserCode    string `json:"userCode"`    // ログイン ID
	Name        string `json:"name"`        // 表示名
	RoleType    int    `json:"roleType"`    // API 実値(自前 RoleType 定数で解釈)
	RoleName    string `json:"roleName"`    // 表示用ロール名
	MailAddress string `json:"mailAddress"` //
}

// TestConnection は GET /users/myself を呼び、認証ユーザの ID・名前・roleType を返す。
// URL の書式・到達性・API キーの有効性がここで検証される。
func (c *Client) TestConnection(ctx context.Context) (*ConnectionInfo, error) {
	me, err := c.api.GetUserMySelfContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("接続テストに失敗しました: %w", err)
	}
	info := &ConnectionInfo{}
	if me.ID != nil {
		info.UserID = *me.ID
	}
	if me.UserID != nil {
		info.UserCode = *me.UserID
	}
	if me.Name != nil {
		info.Name = *me.Name
	}
	if me.MailAddress != nil {
		info.MailAddress = *me.MailAddress
	}
	role := RoleTypeOf(me)
	info.RoleType = int(role)
	info.RoleName = role.String()
	return info, nil
}

// GetUsers はスペースの全ユーザを取得する(管理者権限が必要)。
// 権限が無い場合は ErrPermissionDenied(errors.Is で判定可能)を返す。
func (c *Client) GetUsers(ctx context.Context) ([]*backlog.User, error) {
	users, err := c.api.GetUsersContext(ctx)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// GetTeams はスペースの全チームを取得する(管理者権限が必要)。
// 権限が無い場合は ErrPermissionDenied(errors.Is で判定可能)を返す。
func (c *Client) GetTeams(ctx context.Context) ([]*backlog.Team, error) {
	teams, err := c.api.GetTeamsContext(ctx, nil)
	if err != nil {
		return nil, err
	}
	return teams, nil
}
