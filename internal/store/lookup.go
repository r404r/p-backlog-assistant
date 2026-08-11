package store

import (
	"context"
)

// 一括更新・追加(internal/bulk)の検証・差分表示で使うローカル参照。

// GetIssueByKey は課題キー(EXA-1 等)で未削除の課題 1 件を返す。
// 見つからない場合は nil を返す(エラーにしない)。
// プロジェクトを跨いだ誤更新を防ぐため、必ず projectID で限定する。
func GetIssueByKey(ctx context.Context, q dbtx, projectID int64, issueKey string) (*Issue, error) {
	issues, err := scanIssues(ctx, q,
		`SELECT `+issueColumns+` FROM issues
		 WHERE project_id = ? AND issue_key = ? AND deleted = 0 LIMIT 1`,
		projectID, issueKey)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, nil
	}
	return &issues[0], nil
}

// GetIssueByKey は Store 直接実行版。
func (s *Store) GetIssueByKey(ctx context.Context, projectID int64, issueKey string) (*Issue, error) {
	return GetIssueByKey(ctx, s.db, projectID, issueKey)
}

// UserRef はユーザの識別情報だけを持つ軽量表現(担当者 ID・名前の解決用)。
type UserRef struct {
	ID       int64  `json:"id"`
	UserCode string `json:"userCode"`
	Name     string `json:"name"`
}

// ListUserRefs はローカルキャッシュの全ユーザを ID 昇順で返す。
func ListUserRefs(ctx context.Context, q dbtx) ([]UserRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, user_code, name FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var r UserRef
		if err := rows.Scan(&r.ID, &r.UserCode, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListUserRefs は Store 直接実行版。
func (s *Store) ListUserRefs(ctx context.Context) ([]UserRef, error) {
	return ListUserRefs(ctx, s.db)
}

// ListProjectUserRefs は指定プロジェクトの参加者を ID 昇順で返す(中 1)。
//
// 一括更新の担当者検証は、スペース全体ではなく対象プロジェクトの参加者に
// 限定する(参加していないユーザを担当者に指定すると API が拒否するため、
// 取り込み時点で弾いたほうが早く気づける)。
// ユーザ同期が未実施の場合は 0 件を返すため、呼び出し側でフォールバックすること。
func ListProjectUserRefs(ctx context.Context, q dbtx, projectID int64) ([]UserRef, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT u.id, u.user_code, u.name FROM users u
		JOIN project_users pu ON pu.user_id = u.id
		WHERE pu.project_id = ? ORDER BY u.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var r UserRef
		if err := rows.Scan(&r.ID, &r.UserCode, &r.Name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListProjectUserRefs は Store 直接実行版。
func (s *Store) ListProjectUserRefs(ctx context.Context, projectID int64) ([]UserRef, error) {
	return ListProjectUserRefs(ctx, s.db, projectID)
}
