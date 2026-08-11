package store

// parent.go は課題の親子関係(CF5)をローカルキャッシュから読むための補助。
//
// 親課題 ID は issues テーブルの列ではなく生 JSON(raw_json)の parentIssueId に
// 入っている。列を足すとマイグレーションと再同期が要るため、当面は必要な場面で
// 生 JSON から取り出す(列追加は別バッチで扱う)。

import (
	"context"
	"encoding/json"
)

// ParentState は課題の親の有無を判定できたかどうか。
//
// 「判定できない」を「親なし」に潰すと、一括更新の 1 階層検証(親の親を持たない
// / 子を持たない)が黙って素通りしてしまう(孫の許可・子の見落とし・#CLEAR# の
// 黙殺)。検証側が安全に倒せるよう、状態として区別する。
type ParentState int

const (
	// ParentNone は親を持たないことが確認できた状態(parentIssueId が null)。
	ParentNone ParentState = iota
	// ParentSet は親を持つことが確認できた状態。
	ParentSet
	// ParentUnknown は親の有無を判定できない状態
	//(生 JSON が空 / 壊れている / parentIssueId 項目が無い)。
	// 旧バージョンで同期した課題で起こりうるため、エラーではなく状態として扱う。
	ParentUnknown
)

// ParentIssueRef は課題の生 JSON から親課題 ID と判定状態を取り出す。
func ParentIssueRef(rawJSON string) (int64, ParentState) {
	if rawJSON == "" {
		return 0, ParentUnknown
	}
	// 「項目が無い(判定できない)」と「null(親なし)」を区別するため、
	// ポインタではなく生の値として取り出す。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(rawJSON), &fields); err != nil {
		return 0, ParentUnknown
	}
	raw, ok := fields["parentIssueId"]
	if !ok {
		return 0, ParentUnknown
	}
	var id *int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, ParentUnknown // 数値でも null でもない値
	}
	if id == nil || *id <= 0 {
		return 0, ParentNone
	}
	return *id, ParentSet
}

// ParentIssueID は表示用に親課題 ID だけを返す(親なし・判定不能はどちらも 0)。
//
// Excel 出力は「今ローカルにある情報の書き出し」であり、1 件のデータ不備で
// 全件の出力を失う方が損失が大きいため、判定できない課題は空欄へ縮退させる
// (カスタム属性と同じ流儀)。検証を伴う用途では ParentIssueRef を使うこと。
func ParentIssueID(rawJSON string) int64 {
	id, state := ParentIssueRef(rawJSON)
	if state != ParentSet {
		return 0
	}
	return id
}

// IssueParent は親子関係の判定に使う軽量表現(ID・課題キー・親課題 ID・判定状態)。
type IssueParent struct {
	ID            int64
	IssueKey      string
	ParentIssueID int64 // 0 = 親なし / 判定不能(State で区別する)
	State         ParentState
}

// ListIssueParents は指定プロジェクトの未削除課題の ID・課題キー・親課題 ID を
// ID 昇順で返す。
//
// 一括更新の 1 階層制約の検証(親の親を持たないか / 子を持たないか)は、
// プロジェクト内の親子関係を全体として見ないと判定できない。行ごとに
// SQL を投げると課題数 × 行数のコストになるため、取り込みの入口で 1 回だけ
// 呼び、結果をインデックス化して使い回すこと(internal/bulk の parentIndex)。
func ListIssueParents(ctx context.Context, q dbtx, projectID int64) ([]IssueParent, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, issue_key, raw_json FROM issues
		 WHERE project_id = ? AND deleted = 0 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IssueParent{}
	for rows.Next() {
		var (
			p       IssueParent
			rawJSON string
		)
		if err := rows.Scan(&p.ID, &p.IssueKey, &rawJSON); err != nil {
			return nil, err
		}
		p.ParentIssueID, p.State = ParentIssueRef(rawJSON)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListIssueParents は Store 直接実行版。
func (s *Store) ListIssueParents(ctx context.Context, projectID int64) ([]IssueParent, error) {
	return ListIssueParents(ctx, s.db, projectID)
}
