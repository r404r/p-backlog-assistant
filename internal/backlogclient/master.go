package backlogclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// 一括更新・追加の入力検証で使うマスタ取得(設計書 5 節)。
// 種別・状態・優先度は ID 列を正とするため、ID の実在性をこれらで確認する。

// IssueType はプロジェクトの課題種別(GET /projects/:id/issueTypes)。
type IssueType struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ProjectID int64  `json:"projectId"`
}

// Priority は優先度(GET /priorities)。スペース共通。
type Priority struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Status はプロジェクトの状態(GET /projects/:id/statuses)。
type Status struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// GetProjectIssueTypes はプロジェクトの課題種別一覧を取得する。
func (c *Client) GetProjectIssueTypes(ctx context.Context, projectID int64) ([]IssueType, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+strconv.FormatInt(projectID, 10)+"/issueTypes", nil)
	if err != nil {
		return nil, err
	}
	items, err := parseNamedItems(body, "種別一覧")
	if err != nil {
		return nil, err
	}
	out := make([]IssueType, 0, len(items))
	for _, it := range items {
		out = append(out, IssueType{ID: it.id, Name: it.name, ProjectID: it.projectID})
	}
	return out, nil
}

// GetPriorities は優先度一覧を取得する(スペース共通)。
func (c *Client) GetPriorities(ctx context.Context) ([]Priority, error) {
	body, err := c.rawGet(ctx, "/api/v2/priorities", nil)
	if err != nil {
		return nil, err
	}
	items, err := parseNamedItems(body, "優先度一覧")
	if err != nil {
		return nil, err
	}
	out := make([]Priority, 0, len(items))
	for _, it := range items {
		out = append(out, Priority{ID: it.id, Name: it.name})
	}
	return out, nil
}

// GetProjectStatuses はプロジェクトの状態一覧を取得する。
func (c *Client) GetProjectStatuses(ctx context.Context, projectID int64) ([]Status, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+strconv.FormatInt(projectID, 10)+"/statuses", nil)
	if err != nil {
		return nil, err
	}
	items, err := parseNamedItems(body, "状態一覧")
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(items))
	for _, it := range items {
		out = append(out, Status{ID: it.id, Name: it.name})
	}
	return out, nil
}

// namedItem は id / name を持つマスタ 1 件の中間表現。
type namedItem struct {
	id        int64
	name      string
	projectID int64
}

// parseNamedItems は id / name 配列の応答を解析する。
// 既存の取得系(GetProjects・parseUsers)と同じ流儀で、
// JSON null(配列ではない)と id <= 0 の要素はエラーにする。
// マスタは「入力 ID の実在性判定」の根拠になるため、
// 異常応答を空配列・ゼロ値として受理すると正しい入力を誤って弾く。
func parseNamedItems(body []byte, what string) ([]namedItem, error) {
	elems, err := decodeArray(body, what)
	if err != nil {
		return nil, err
	}
	if elems == nil {
		return nil, fmt.Errorf("%sの応答が不正です(JSON 配列ではありません)", what)
	}
	out := make([]namedItem, 0, len(elems))
	for _, e := range elems {
		var v struct {
			ID        *int64  `json:"id"`
			Name      *string `json:"name"`
			ProjectID *int64  `json:"projectId"`
		}
		if err := json.Unmarshal(e, &v); err != nil {
			return nil, fmt.Errorf("%sを解析できません: %w", what, err)
		}
		if derefInt64(v.ID) <= 0 {
			return nil, fmt.Errorf("%sの応答が不正です(id が無い要素が含まれています)", what)
		}
		out = append(out, namedItem{
			id:        derefInt64(v.ID),
			name:      derefString(v.Name),
			projectID: derefInt64(v.ProjectID),
		})
	}
	return out, nil
}
