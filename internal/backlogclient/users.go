package backlogclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// User は users 系 API(/users・/projects/:id/users・/projects/:id/administrators)
// の 1 件。RawJSON に API レスポンス要素全体(未知フィールド含む)を保持する。
//
// RoleType は API 実値をそのまま入れる(ライブラリの RoleType 定数は
// 1 ずれる既知バグがあるため使わない。roletype.go を参照)。
type User struct {
	ID          int64  `json:"id"`
	UserCode    string `json:"userId"` // ログイン ID(API の userId)
	Name        string `json:"name"`
	MailAddress string `json:"mailAddress"`
	RoleType    int    `json:"roleType"`
	RawJSON     string `json:"rawJson"`
}

// Team は GET /teams の 1 件。MemberIDs は members[].id(team_members の構築用)。
// 「ユーザ → 所属チーム」を引く API は存在しないため、members から
// クライアント側で逆引きインデックスを構築する(設計書 3 節)。
type Team struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	MemberIDs []int64 `json:"memberIds"`
	RawJSON   string  `json:"rawJson"`
}

// GetUsersRaw はスペースの全ユーザ(GET /api/v2/users)を取得する。
// この API に count / offset は無く、1 回の取得で全件が返る。
// 管理者・プロジェクト管理者以外では 403 となり ErrPermissionDenied を返すため、
// 呼び出し側(internal/sync)はプロジェクト単位取得へ縮退する(設計書 3 節)。
//
// 応答検証は GetProjects と同様に厳格に行う: この結果は users テーブルの
// 全置換に使われるため、異常応答を空配列・ゼロ値として受理すると
// キャッシュを誤って破棄・汚染する。
func (c *Client) GetUsersRaw(ctx context.Context) ([]User, error) {
	body, err := c.rawGet(ctx, "/api/v2/users", nil)
	if err != nil {
		return nil, err
	}
	return parseUsers(body, "ユーザ一覧")
}

// GetProjectUsers はプロジェクト参加者(GET /api/v2/projects/:projectId/users)を
// 取得する。すべての権限で実行可能(参加プロジェクトに限る)。
func (c *Client) GetProjectUsers(ctx context.Context, projectID int64) ([]User, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+strconv.FormatInt(projectID, 10)+"/users", nil)
	if err != nil {
		return nil, err
	}
	return parseUsers(body, "プロジェクト参加者一覧")
}

// GetProjectAdministrators はプロジェクト管理者
// (GET /api/v2/projects/:projectId/administrators)を取得する。
// roleType(スペース全体のロール)とは別のプロジェクト単位の管理者フラグ。
func (c *Client) GetProjectAdministrators(ctx context.Context, projectID int64) ([]User, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+strconv.FormatInt(projectID, 10)+"/administrators", nil)
	if err != nil {
		return nil, err
	}
	return parseUsers(body, "プロジェクト管理者一覧")
}

// GetProjectTeams はプロジェクトのチーム一覧
// (GET /api/v2/projects/:projectId/teams)を取得する。
// スペース全体の GET /teams は管理者権限が必要だが、こちらは参加プロジェクトに対して
// 一般ユーザでも実行できるため、縮退パスではプロジェクトごとに取得した結果を
// 合成してチーム情報を作る(設計書 3 節。高 1)。
// この API に count / offset は無く、1 回の取得で全件が返る。
//
// 応答検証は GetTeamsPaged と同じ流儀で厳格に行う(異常応答を空配列として
// 受理すると、チーム情報のキャッシュを誤って破棄・汚染する)。
func (c *Client) GetProjectTeams(ctx context.Context, projectID int64) ([]Team, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+strconv.FormatInt(projectID, 10)+"/teams", nil)
	if err != nil {
		return nil, err
	}
	return parseTeams(body, "プロジェクトチーム一覧")
}

// GetTeamsPaged はチーム一覧(GET /api/v2/teams)の 1 ページを取得する。
// この API は count(1〜100・既定 20)+ offset のページングが必要なため、
// 呼び出し側が返却件数 < count になるまで offset を進めて全ページ消化する。
// count が 0 以下の場合は MaxPageSize(100)を使う。
func (c *Client) GetTeamsPaged(ctx context.Context, offset, count int) ([]Team, error) {
	if count <= 0 {
		count = MaxPageSize
	}
	v := url.Values{}
	v.Set("count", strconv.Itoa(count))
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	body, err := c.rawGet(ctx, "/api/v2/teams", v)
	if err != nil {
		return nil, err
	}
	return parseTeams(body, "チーム一覧")
}

// parseTeams はチーム配列の応答を Team へ写す(RawJSON は要素の JSON をそのまま保持)。
// JSON null(配列ではない)と id <= 0 の要素・メンバーはエラーにする。
func parseTeams(body []byte, what string) ([]Team, error) {
	elems, err := decodeArray(body, what)
	if err != nil {
		return nil, err
	}
	if elems == nil {
		return nil, fmt.Errorf("%sの応答が不正です(JSON 配列ではありません)", what)
	}
	out := make([]Team, 0, len(elems))
	for _, e := range elems {
		var t struct {
			ID      *int64  `json:"id"`
			Name    *string `json:"name"`
			Members []struct {
				ID *int64 `json:"id"`
			} `json:"members"`
		}
		if err := json.Unmarshal(e, &t); err != nil {
			return nil, fmt.Errorf("チーム情報を解析できません: %w", err)
		}
		if derefInt64(t.ID) <= 0 {
			return nil, fmt.Errorf("%sの応答が不正です(id が無いチームが含まれています)", what)
		}
		team := Team{
			ID:        derefInt64(t.ID),
			Name:      derefString(t.Name),
			MemberIDs: make([]int64, 0, len(t.Members)),
			RawJSON:   string(e),
		}
		for _, m := range t.Members {
			if derefInt64(m.ID) <= 0 {
				return nil, fmt.Errorf("%sの応答が不正です(id が無いメンバーが含まれています)", what)
			}
			team.MemberIDs = append(team.MemberIDs, derefInt64(m.ID))
		}
		out = append(out, team)
	}
	return out, nil
}

// parseUsers はユーザ配列の応答を User へ写す(RawJSON は要素の JSON をそのまま保持)。
// JSON null(配列ではない)と id <= 0 の要素はエラーにする(GetProjects と同様)。
func parseUsers(body []byte, what string) ([]User, error) {
	elems, err := decodeArray(body, what)
	if err != nil {
		return nil, err
	}
	if elems == nil {
		// json.Unmarshal は JSON null をスライス未設定(nil)にする。
		// 空配列([])は長さ 0 の非 nil スライスになるため区別できる。
		return nil, fmt.Errorf("%sの応答が不正です(JSON 配列ではありません)", what)
	}
	out := make([]User, 0, len(elems))
	for _, e := range elems {
		var u struct {
			ID          *int64  `json:"id"`
			UserID      *string `json:"userId"`
			Name        *string `json:"name"`
			MailAddress *string `json:"mailAddress"`
			RoleType    *int    `json:"roleType"`
		}
		if err := json.Unmarshal(e, &u); err != nil {
			return nil, fmt.Errorf("ユーザ情報を解析できません: %w", err)
		}
		if derefInt64(u.ID) <= 0 {
			return nil, fmt.Errorf("%sの応答が不正です(id が無いユーザが含まれています)", what)
		}
		user := User{
			ID:          derefInt64(u.ID),
			UserCode:    derefString(u.UserID),
			Name:        derefString(u.Name),
			MailAddress: derefString(u.MailAddress),
			RawJSON:     string(e),
		}
		if u.RoleType != nil {
			user.RoleType = *u.RoleType
		}
		out = append(out, user)
	}
	return out, nil
}
