package backlogclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"backlog-assistant/internal/customfield"
)

// 一括更新・追加の入力検証で使うマスタ取得(設計書 5 節)。
// 種別・状態・優先度は名前と ID の対応をこれで解決し、入力の実在性を確認する
// (テンプレートの選択候補「マスタ」シートの内容にもなる)。

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

// GetProjectCustomFields はプロジェクトのカスタム属性定義一覧を取得する。
//
// プロジェクトはキーでも ID でも指定できる(GetIssue と同じく文字列で受け、
// パスへはエスケープして埋め込む)。
// カスタム属性が使えないプラン・権限では 404 / 403 になるため、
// 呼び出し側は ErrNotFound / ErrPermissionDenied で縮退を判断できる。
func (c *Client) GetProjectCustomFields(ctx context.Context, projectIDOrKey string) ([]customfield.Def, error) {
	body, err := c.rawGet(ctx, "/api/v2/projects/"+url.PathEscape(projectIDOrKey)+"/customFields", nil)
	if err != nil {
		return nil, err
	}
	return parseCustomFieldDefs(body)
}

// parseCustomFieldDefs はカスタム属性定義一覧の応答を解析する。
// items / applicableIssueTypes が null になり得るため parseNamedItems では
// 表現できず、専用に解析する。
//
// 異常応答の扱いは既存のマスタ取得(parseNamedItems)と同じ厳格さで、
// JSON null(配列ではない)、id <= 0・typeId <= 0・名前欠落の要素、
// 選択肢や適用課題種別の ID 欠落はエラーにする
// (定義は列見出し・入力候補の根拠になるため、欠落を空として受理しない。
// 未知の typeId 自体は将来の型追加に備えて受理する)。
func parseCustomFieldDefs(body []byte) ([]customfield.Def, error) {
	const what = "カスタム属性一覧"
	elems, err := decodeArray(body, what)
	if err != nil {
		return nil, err
	}
	if elems == nil {
		return nil, fmt.Errorf("%sの応答が不正です(JSON 配列ではありません)", what)
	}
	out := make([]customfield.Def, 0, len(elems))
	for _, e := range elems {
		var v struct {
			ID                   *int64   `json:"id"`
			TypeID               *int     `json:"typeId"`
			Name                 *string  `json:"name"`
			Description          *string  `json:"description"`
			Required             *bool    `json:"required"`
			ApplicableIssueTypes []*int64 `json:"applicableIssueTypes"`
			AllowInput           *bool    `json:"allowInput"`
			AllowAddItem         *bool    `json:"allowAddItem"`
			Items                []struct {
				ID           *int64  `json:"id"`
				Name         *string `json:"name"`
				DisplayOrder *int    `json:"displayOrder"`
			} `json:"items"`
		}
		if err := json.Unmarshal(e, &v); err != nil {
			return nil, fmt.Errorf("%sを解析できません: %w", what, err)
		}
		if derefInt64(v.ID) <= 0 {
			return nil, fmt.Errorf("%sの応答が不正です(id が無い要素が含まれています)", what)
		}
		if v.TypeID == nil || *v.TypeID <= 0 {
			return nil, fmt.Errorf("%sの応答が不正です(typeId が無い要素が含まれています)", what)
		}
		if derefString(v.Name) == "" {
			return nil, fmt.Errorf("%sの応答が不正です(名前が無い要素が含まれています)", what)
		}
		def := customfield.Def{
			ID:          derefInt64(v.ID),
			TypeID:      *v.TypeID,
			Name:        derefString(v.Name),
			Description: derefString(v.Description),
			Required:    v.Required != nil && *v.Required,
			// null は「全課題種別に適用」を意味するため、空スライスへ寄せる
			ApplicableIssueTypes: make([]int64, 0, len(v.ApplicableIssueTypes)),
			AllowInput:           v.AllowInput != nil && *v.AllowInput,
			AllowAddItem:         v.AllowAddItem != nil && *v.AllowAddItem,
			Items:                make([]customfield.Item, 0, len(v.Items)),
		}
		for _, id := range v.ApplicableIssueTypes {
			if derefInt64(id) <= 0 {
				return nil, fmt.Errorf("%sの応答が不正です(適用課題種別の id が無い要素が含まれています)", what)
			}
			def.ApplicableIssueTypes = append(def.ApplicableIssueTypes, derefInt64(id))
		}
		for _, it := range v.Items {
			if derefInt64(it.ID) <= 0 {
				return nil, fmt.Errorf("%sの応答が不正です(選択肢の id が無い要素が含まれています)", what)
			}
			item := customfield.Item{ID: derefInt64(it.ID), Name: derefString(it.Name)}
			if it.DisplayOrder != nil {
				item.DisplayOrder = *it.DisplayOrder
			}
			def.Items = append(def.Items, item)
		}
		out = append(out, def)
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
