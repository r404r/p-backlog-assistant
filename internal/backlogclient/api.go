package backlogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// ErrNotFound は 404 の正規化エラー。フル同期の削除候補確認
// (GET /issues/:issueKey が 404 なら削除確定)に使う。
var ErrNotFound = errors.New("対象が見つかりません")

// maxResponseBytes はレスポンス読み取りの上限(防御用)。
// count=100 の課題一覧(raw_json 含む)でも十分な余裕がある。
const maxResponseBytes = 64 << 20 // 64 MiB

// MaxPageSize は課題一覧・アクティビティ取得の 1 ページ最大件数(API 仕様)。
const MaxPageSize = 100

// Project は GET /projects の 1 件。RawJSON に API レスポンス要素全体を保持する。
type Project struct {
	ID         int64  `json:"id"`
	ProjectKey string `json:"projectKey"`
	Name       string `json:"name"`
	Archived   bool   `json:"archived"`
	RawJSON    string `json:"rawJson"`
}

// Issue は GET /issues の 1 件。store.Issue へそのまま写せる形にし、
// RawJSON に API レスポンス要素全体(未知フィールド含む)を保持する
// (Excel 出力列の追加に DB マイグレーション無しで対応するため。設計書 2 節)。
type Issue struct {
	ID            int64  `json:"id"`
	IssueKey      string `json:"issueKey"`
	ProjectID     int64  `json:"projectId"`
	Summary       string `json:"summary"`
	Description   string `json:"description"`
	StatusID      int64  `json:"statusId"`
	StatusName    string `json:"statusName"`
	AssigneeID    int64  `json:"assigneeId"`
	AssigneeName  string `json:"assigneeName"`
	IssueTypeID   int64  `json:"issueTypeId"`
	IssueTypeName string `json:"issueTypeName"`
	PriorityID    int64  `json:"priorityId"`
	PriorityName  string `json:"priorityName"`
	Created       string `json:"created"`
	Updated       string `json:"updated"`
	DueDate       string `json:"dueDate"`
	// ParentIssueID は親課題 ID(0 = 親なし。CF5)。
	// 一括更新の 1 階層制約の検証(ローカルに無い親を API で確認する)と
	// 再送前突合で使う。
	ParentIssueID int64  `json:"parentIssueId"`
	RawJSON       string `json:"rawJson"`
}

// IssueQuery は課題一覧・課題数取得のパラメータ。
// ProjectIDs は必須(指定漏れはスペース全件取得となり、プロジェクト別の
// sync_state と実データが不一致になるため。設計書 3 節)。
type IssueQuery struct {
	ProjectIDs   []int64 // projectId[](必須)
	UpdatedSince string  // updatedSince(yyyy-MM-dd)
	UpdatedUntil string  // updatedUntil(yyyy-MM-dd)
	CreatedSince string  // createdSince(yyyy-MM-dd)
	CreatedUntil string  // createdUntil(yyyy-MM-dd)
	Sort         string  // sort(created / updated 等)
	Order        string  // order(asc / desc)
	Count        int     // count(1〜100)
	Offset       int     // offset
}

// values はクエリパラメータへ変換する。ProjectIDs 未指定はエラー。
func (q IssueQuery) values() (url.Values, error) {
	if len(q.ProjectIDs) == 0 {
		return nil, errors.New("projectId[] が指定されていません(スペース全件取得を防ぐため必須です)")
	}
	v := url.Values{}
	for _, id := range q.ProjectIDs {
		v.Add("projectId[]", strconv.FormatInt(id, 10))
	}
	setIfNotEmpty(v, "updatedSince", q.UpdatedSince)
	setIfNotEmpty(v, "updatedUntil", q.UpdatedUntil)
	setIfNotEmpty(v, "createdSince", q.CreatedSince)
	setIfNotEmpty(v, "createdUntil", q.CreatedUntil)
	setIfNotEmpty(v, "sort", q.Sort)
	setIfNotEmpty(v, "order", q.Order)
	if q.Count > 0 {
		v.Set("count", strconv.Itoa(q.Count))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	return v, nil
}

// Activity は GET /space/activities の 1 件。
// Content は種別ごとに構造が異なるため生 JSON のまま保持し、
// 呼び出し側が防御的にパースする(設計書 3 節・verification.md 項目 2)。
type Activity struct {
	ID         int64           `json:"id"`
	Type       int             `json:"type"`
	ProjectID  int64           `json:"projectId"`
	ProjectKey string          `json:"projectKey"`
	Content    json.RawMessage `json:"content"`
	Created    string          `json:"created"`
	RawJSON    string          `json:"rawJson"`
}

// ActivityQuery はスペースアクティビティ取得のパラメータ。
type ActivityQuery struct {
	ActivityTypeIDs []int // activityTypeId[](削除検知は 4)
	MinID           int64 // minId(0 なら送信しない)
	MaxID           int64 // maxId(0 なら送信しない)
	Order           string
	Count           int
}

func (q ActivityQuery) values() url.Values {
	v := url.Values{}
	for _, id := range q.ActivityTypeIDs {
		v.Add("activityTypeId[]", strconv.Itoa(id))
	}
	if q.MinID > 0 {
		v.Set("minId", strconv.FormatInt(q.MinID, 10))
	}
	if q.MaxID > 0 {
		v.Set("maxId", strconv.FormatInt(q.MaxID, 10))
	}
	setIfNotEmpty(v, "order", q.Order)
	if q.Count > 0 {
		v.Set("count", strconv.Itoa(q.Count))
	}
	return v
}

func setIfNotEmpty(v url.Values, key, value string) {
	if value != "" {
		v.Set(key, value)
	}
}

// rawGet は API キー付きの GET を実行し、レスポンスボディをそのまま返す。
// kenzo0107/backlog は構造体へデコードして生 JSON を捨てるため、
// raw_json を保持する必要がある取得はこちらを使う。
// 送信は既存 transport(レート制限・429 リトライ・401/403 正規化・マスク)を経由する。
func (c *Client) rawGet(ctx context.Context, path string, q url.Values) ([]byte, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("apiKey", c.apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.spaceURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("リクエストを作成できません: %s", MaskAPIKey(err.Error()))
	}
	resp, err := c.httpDo.Do(req)
	if err != nil {
		return nil, err // transport 側でマスク・正規化済み
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("レスポンスの読み取りに失敗しました(GET %s): %s", MaskAPIKey(path), MaskAPIKey(err.Error()))
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: GET %s", ErrNotFound, MaskAPIKey(path))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// レスポンス本文は課題本文を含み得るためエラーへ載せない
		return nil, fmt.Errorf("Backlog API がエラーを返しました(HTTP %d): GET %s", resp.StatusCode, MaskAPIKey(path))
	}
	return body, nil
}

// GetProjects は参加プロジェクト一覧(GET /projects)を返す。
// 一般ユーザのキーでは参加プロジェクトのみが返るため、
// ローカルの projects との突合によりアクセス不能プロジェクトを検出できる(設計書 2 節)。
//
// この応答は「返らなくなったプロジェクトのキャッシュを破棄する」判断
// (sync.SyncProjects → store.DeleteProjectsNotIn)の根拠になるため、
// 異常応答を空配列・ゼロ値として受理しない(中 3)。
//   - JSON null(配列ではない)はエラー。空配列として扱うと全キャッシュを誤破棄する
//   - 要素の id が欠落・0・負ならエラー。ゼロ値の ID で突合すると同様に誤破棄しうる
//
// 正常な空配列([])は「参加プロジェクト 0 件」として従来どおり受理する。
func (c *Client) GetProjects(ctx context.Context) ([]Project, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects", nil)
	if err != nil {
		return nil, err
	}
	elems, err := decodeArray(body, "プロジェクト一覧")
	if err != nil {
		return nil, err
	}
	if elems == nil {
		// json.Unmarshal は JSON null をスライス未設定(nil)にする。
		// 空配列([])は長さ 0 の非 nil スライスになるため区別できる。
		return nil, errors.New("プロジェクト一覧の応答が不正です(JSON 配列ではありません)")
	}
	out := make([]Project, 0, len(elems))
	for _, e := range elems {
		var p struct {
			ID         *int64  `json:"id"`
			ProjectKey *string `json:"projectKey"`
			Name       *string `json:"name"`
			Archived   *bool   `json:"archived"`
		}
		if err := json.Unmarshal(e, &p); err != nil {
			return nil, fmt.Errorf("プロジェクト情報を解析できません: %w", err)
		}
		if derefInt64(p.ID) <= 0 {
			return nil, errors.New("プロジェクト一覧の応答が不正です(id が無いプロジェクトが含まれています)")
		}
		out = append(out, Project{
			ID:         derefInt64(p.ID),
			ProjectKey: derefString(p.ProjectKey),
			Name:       derefString(p.Name),
			Archived:   p.Archived != nil && *p.Archived,
			RawJSON:    string(e),
		})
	}
	return out, nil
}

// apiNamed は id / name を持つ入れ子オブジェクト(status・assignee 等)。
type apiNamed struct {
	ID   *int64  `json:"id"`
	Name *string `json:"name"`
}

// GetIssues は課題一覧(GET /issues)を取得する。ページングは呼び出し側
// (internal/sync)が offset を進めて行う。
func (c *Client) GetIssues(ctx context.Context, q IssueQuery) ([]Issue, error) {
	v, err := q.values()
	if err != nil {
		return nil, err
	}
	body, err := c.rawGet(ctx, "/api/v2/issues", v)
	if err != nil {
		return nil, err
	}
	elems, err := decodeArray(body, "課題一覧")
	if err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(elems))
	for _, e := range elems {
		issue, err := parseIssue(e)
		if err != nil {
			return nil, err
		}
		out = append(out, issue)
	}
	return out, nil
}

// GetIssuesCount は課題数(GET /issues/count)を取得する(進捗表示用)。
func (c *Client) GetIssuesCount(ctx context.Context, q IssueQuery) (int, error) {
	v, err := q.values()
	if err != nil {
		return 0, err
	}
	// count / offset / sort / order は件数取得では意味を持たないため落とす
	v.Del("count")
	v.Del("offset")
	v.Del("sort")
	v.Del("order")
	body, err := c.rawGet(ctx, "/api/v2/issues/count", v)
	if err != nil {
		return 0, err
	}
	var res struct {
		Count *int `json:"count"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return 0, fmt.Errorf("課題数を解析できません: %w", err)
	}
	if res.Count == nil {
		return 0, errors.New("課題数のレスポンスに count がありません")
	}
	return *res.Count, nil
}

// GetIssue は課題 1 件(GET /issues/:issueIdOrKey)を取得する。
// 存在しない場合は ErrNotFound(errors.Is で判定可能)を返す
// (フル同期の削除候補確認に使用)。
func (c *Client) GetIssue(ctx context.Context, issueIDOrKey string) (*Issue, error) {
	body, err := c.rawGet(ctx, "/api/v2/issues/"+url.PathEscape(issueIDOrKey), nil)
	if err != nil {
		return nil, err
	}
	issue, err := parseIssue(body)
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// GetSpaceActivities はスペースのアクティビティ(GET /space/activities)を取得する。
// ライブラリ(kenzo0107/backlog v1.1.2)にはユーザ別・プロジェクト別しか無く、
// スペース単位の取得と生 content の保持ができないため自前実装する。
func (c *Client) GetSpaceActivities(ctx context.Context, q ActivityQuery) ([]Activity, error) {
	body, err := c.rawGet(ctx, "/api/v2/space/activities", q.values())
	if err != nil {
		return nil, err
	}
	elems, err := decodeArray(body, "アクティビティ一覧")
	if err != nil {
		return nil, err
	}
	out := make([]Activity, 0, len(elems))
	for _, e := range elems {
		var a struct {
			ID      *int64 `json:"id"`
			Type    *int   `json:"type"`
			Project *struct {
				ID         *int64  `json:"id"`
				ProjectKey *string `json:"projectKey"`
			} `json:"project"`
			Content json.RawMessage `json:"content"`
			Created *string         `json:"created"`
		}
		if err := json.Unmarshal(e, &a); err != nil {
			return nil, fmt.Errorf("アクティビティを解析できません: %w", err)
		}
		act := Activity{
			ID:      derefInt64(a.ID),
			Content: a.Content,
			Created: derefString(a.Created),
			RawJSON: string(e),
		}
		if a.Type != nil {
			act.Type = *a.Type
		}
		if a.Project != nil {
			act.ProjectID = derefInt64(a.Project.ID)
			act.ProjectKey = derefString(a.Project.ProjectKey)
		}
		out = append(out, act)
	}
	return out, nil
}

// parseIssue は課題 1 件の JSON を Issue へ写す(RawJSON は元の JSON をそのまま保持)。
func parseIssue(raw json.RawMessage) (Issue, error) {
	var a struct {
		ID          *int64    `json:"id"`
		ProjectID   *int64    `json:"projectId"`
		IssueKey    *string   `json:"issueKey"`
		Summary     *string   `json:"summary"`
		Description *string   `json:"description"`
		IssueType   *apiNamed `json:"issueType"`
		Priority    *apiNamed `json:"priority"`
		Status      *apiNamed `json:"status"`
		Assignee    *apiNamed `json:"assignee"`
		DueDate     *string   `json:"dueDate"`
		Created     *string   `json:"created"`
		Updated     *string   `json:"updated"`
		// parentIssueId は親を持たない課題では null になる(CF5)
		ParentIssueID *int64 `json:"parentIssueId"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return Issue{}, fmt.Errorf("課題情報を解析できません: %w", err)
	}
	i := Issue{
		ID:          derefInt64(a.ID),
		ProjectID:   derefInt64(a.ProjectID),
		IssueKey:    derefString(a.IssueKey),
		Summary:     derefString(a.Summary),
		Description: derefString(a.Description),
		DueDate:     derefString(a.DueDate),
		Created:     derefString(a.Created),
		Updated:     derefString(a.Updated),
		RawJSON:     string(raw),
	}
	if id := derefInt64(a.ParentIssueID); id > 0 {
		i.ParentIssueID = id
	}
	if a.Status != nil {
		i.StatusID = derefInt64(a.Status.ID)
		i.StatusName = derefString(a.Status.Name)
	}
	if a.Assignee != nil {
		i.AssigneeID = derefInt64(a.Assignee.ID)
		i.AssigneeName = derefString(a.Assignee.Name)
	}
	if a.IssueType != nil {
		// ID は再送前突合(送信内容とリモートの完全一致確認)で使う
		i.IssueTypeID = derefInt64(a.IssueType.ID)
		i.IssueTypeName = derefString(a.IssueType.Name)
	}
	if a.Priority != nil {
		i.PriorityID = derefInt64(a.Priority.ID)
		i.PriorityName = derefString(a.Priority.Name)
	}
	return i, nil
}

// decodeArray は JSON 配列を要素ごとの生 JSON へ分解する。
func decodeArray(body []byte, what string) ([]json.RawMessage, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(body, &elems); err != nil {
		return nil, fmt.Errorf("%sのレスポンスを解析できません: %w", what, err)
	}
	return elems, nil
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
