// Package bulk は一括更新・追加(画面 3)のバックエンド(設計書 5 節)。
//
// 流れは「テンプレート Excel の取り込み → バリデーション → dry-run 差分プレビュー
// → 実行(進捗・中断・再開)」。実行は 1 件ずつ POST/PATCH で行う
// (Backlog にバルク API は無い)。
//
// 安全策として次の 3 点を必ず守る:
//  1. 実行前に dry-run プレビューを返す(呼び出し側が確認を挟める)。
//  2. 実行直前に対象課題を再取得し、Excel 出力時点の updated と異なれば
//     conflict として送信しない(リモートの変更を黙って上書きしない)。
//  3. 送信前に sending を記録し、再開時に sending 行を自動再送しない
//     (二重作成防止)。
package bulk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/customfield"
)

// ClearToken はフィールドのクリアを指示する専用値(設計書 5 節)。
// 担当者・期限・詳細でのみ使用でき、新規追加行では使えない。
const ClearToken = "#CLEAR#"

// 行の処理区分。
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionSkip   = "skip" // 変更が 1 つも無い行(送信しない)
)

// API は bulk が必要とする Backlog API 操作。
// 実体は *backlogclient.Client(テストではフェイクに差し替える)。
type API interface {
	GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error)
	// GetIssues は再送前突合(高 3)で「ジョブ作成後に作られた課題」を取得する。
	GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error)
	CreateIssue(ctx context.Context, in backlogclient.IssueCreate) (*backlogclient.Issue, error)
	UpdateIssue(ctx context.Context, issueIDOrKey string, in backlogclient.IssueUpdate) (*backlogclient.Issue, error)
	GetProjectIssueTypes(ctx context.Context, projectID int64) ([]backlogclient.IssueType, error)
	GetPriorities(ctx context.Context) ([]backlogclient.Priority, error)
	GetProjectStatuses(ctx context.Context, projectID int64) ([]backlogclient.Status, error)
	// GetProjectCustomFields はカスタム属性の定義を取得する。
	// プロジェクトはキーでも指定できる API のため、引数は文字列
	// (backlogclient のシグネチャに合わせる)。
	GetProjectCustomFields(ctx context.Context, projectIDOrKey string) ([]customfield.Def, error)
}

// コンパイル時チェック: *backlogclient.Client が API を満たすこと。
var _ API = (*backlogclient.Client)(nil)

// NamedID は ID と表示名の組(種別・優先度・状態)。
type NamedID struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// MasterData は入力検証・名前解決に使うマスタ。
type MasterData struct {
	IssueTypes []NamedID `json:"issueTypes"`
	Priorities []NamedID `json:"priorities"`
	Statuses   []NamedID `json:"statuses"`
	// CustomFields はプロジェクトのカスタム属性定義。
	// 未対応・権限不足のスペースでは空になる(FetchMasterData の縮退)。
	CustomFields []customfield.Def `json:"customFields"`
}

// FetchMasterData はプロジェクトの種別・状態・カスタム属性定義と
// スペースの優先度を取得する。
func FetchMasterData(ctx context.Context, api API, projectID int64) (*MasterData, error) {
	types, err := api.GetProjectIssueTypes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("種別一覧の取得に失敗しました: %w", err)
	}
	priorities, err := api.GetPriorities(ctx)
	if err != nil {
		return nil, fmt.Errorf("優先度一覧の取得に失敗しました: %w", err)
	}
	statuses, err := api.GetProjectStatuses(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("状態一覧の取得に失敗しました: %w", err)
	}
	customFields, err := fetchCustomFields(ctx, api, projectID)
	if err != nil {
		return nil, err
	}
	m := &MasterData{
		IssueTypes:   make([]NamedID, 0, len(types)),
		Priorities:   make([]NamedID, 0, len(priorities)),
		Statuses:     make([]NamedID, 0, len(statuses)),
		CustomFields: customFields,
	}
	for _, t := range types {
		m.IssueTypes = append(m.IssueTypes, NamedID{ID: t.ID, Name: t.Name})
	}
	for _, p := range priorities {
		m.Priorities = append(m.Priorities, NamedID{ID: p.ID, Name: p.Name})
	}
	for _, s := range statuses {
		m.Statuses = append(m.Statuses, NamedID{ID: s.ID, Name: s.Name})
	}
	return m, nil
}

// fetchCustomFields はカスタム属性定義を取得する。
//
// 他のマスタと違い、404(カスタム属性 API が使えないプラン・
// プロジェクト未参照)と 403(権限不足)はエラーにせず空スライスへ縮退する。
// カスタム属性は種別・状態・優先度と異なり「無くても一括更新は成立する」
// 付加情報であり、使っていないスペースで画面全体を止めないため。
// それ以外の失敗(通信断・サーバエラー等)は隠さずエラーにする。
// 縮退したことは戻り値では区別しない(ログ出力は呼び出し元の責務)。
func fetchCustomFields(ctx context.Context, api API, projectID int64) ([]customfield.Def, error) {
	defs, err := api.GetProjectCustomFields(ctx, strconv.FormatInt(projectID, 10))
	if err != nil {
		if errors.Is(err, backlogclient.ErrNotFound) || errors.Is(err, backlogclient.ErrPermissionDenied) {
			return []customfield.Def{}, nil
		}
		return nil, fmt.Errorf("カスタム属性一覧の取得に失敗しました: %w", err)
	}
	if defs == nil {
		return []customfield.Def{}, nil
	}
	return defs, nil
}

// Payload は job_rows.payload に保存する送信内容。
// 再開時のリクエスト再構築に使うため、解決済みの ID のみを持つ
// (名前解決・既定値の適用は取り込み時に完了させる)。
//
// ポインタの意味は backlogclient.IssueUpdate と同じ:
// nil = 変更しない / 値あり = その値 / 空値(*int64 が 0・*string が "")= クリア。
type Payload struct {
	Action      string   `json:"action"`
	ProjectID   int64    `json:"projectId,omitempty"` // 新規追加でのみ使用
	Summary     *string  `json:"summary,omitempty"`
	Description *string  `json:"description,omitempty"`
	IssueTypeID *int64   `json:"issueTypeId,omitempty"`
	PriorityID  *int64   `json:"priorityId,omitempty"`
	StatusID    *int64   `json:"statusId,omitempty"`
	AssigneeID  *int64   `json:"assigneeId,omitempty"`
	DueDate     *string  `json:"dueDate,omitempty"`
	Changes     []string `json:"changes,omitempty"` // dry-run で表示した差分(結果レポート用)
	// CustomFields は変更するカスタム属性の値(定義順)。
	// 変更しない属性は載せない(空セル = 変更しない)。
	// 省略可のため、この項目が無い旧ジョブの payload もそのまま復元できる。
	CustomFields []customfield.InputValue `json:"customFields,omitempty"`
}

// EncodePayload は Payload を JSON 文字列にする。
func EncodePayload(p Payload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("送信内容を保存できません: %w", err)
	}
	return string(b), nil
}

// DecodePayload は job_rows.payload を Payload へ復元する。
func DecodePayload(s string) (*Payload, error) {
	var p Payload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("保存された送信内容を解析できません: %w", err)
	}
	return &p, nil
}

// ptrInt64 / ptrString はポインタ生成のヘルパー。
func ptrInt64(v int64) *int64    { return &v }
func ptrString(v string) *string { return &v }
