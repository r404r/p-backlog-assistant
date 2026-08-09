package store

import (
	"context"
	"database/sql"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// NormalizeSearchText は検索用テキストを生成する:
// NFKC 正規化(全角英数・半角カナ等の統一)→ Unicode ケースフォールド
// (cases.Fold。ß→ss 等の言語非依存の大文字小文字同一視)。
//
// 注意: cases.Caser はステートフルで並行使用できないため、呼び出しごとに生成する。
func NormalizeSearchText(s string) string {
	return cases.Fold().String(norm.NFKC.String(s))
}

// Issue はローカルキャッシュの課題 1 行。
type Issue struct {
	ID            int64
	IssueKey      string
	ProjectID     int64
	Summary       string
	Description   string
	StatusID      int64
	StatusName    string
	AssigneeID    int64 // 0 = 未割当
	AssigneeName  string
	IssueTypeName string
	PriorityName  string
	Created       string
	Updated       string
	DueDate       string
	RawJSON       string
	FetchedAt     string
}

// UpsertIssue は課題を id で UPSERT する。search_text(件名 + 詳細の正規化)は
// 保存・更新時にここで必ず生成する(設計書 2 節)。
// q には *sql.DB(単発)または *sql.Tx(Store.WithTx 内)を渡す。
func UpsertIssue(ctx context.Context, q dbtx, i *Issue) error {
	searchText := NormalizeSearchText(i.Summary + "\n" + i.Description)
	_, err := q.ExecContext(ctx, `
		INSERT INTO issues (
			id, issue_key, project_id, summary, description,
			status_id, status_name, assignee_id, assignee_name,
			issue_type_name, priority_name, created, updated, due_date,
			raw_json, search_text, fetched_at, deleted
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(id) DO UPDATE SET
			issue_key = excluded.issue_key,
			project_id = excluded.project_id,
			summary = excluded.summary,
			description = excluded.description,
			status_id = excluded.status_id,
			status_name = excluded.status_name,
			assignee_id = excluded.assignee_id,
			assignee_name = excluded.assignee_name,
			issue_type_name = excluded.issue_type_name,
			priority_name = excluded.priority_name,
			created = excluded.created,
			updated = excluded.updated,
			due_date = excluded.due_date,
			raw_json = excluded.raw_json,
			search_text = excluded.search_text,
			fetched_at = excluded.fetched_at,
			deleted = 0`,
		i.ID, i.IssueKey, i.ProjectID, i.Summary, i.Description,
		i.StatusID, i.StatusName, i.AssigneeID, i.AssigneeName,
		i.IssueTypeName, i.PriorityName, i.Created, i.Updated, i.DueDate,
		i.RawJSON, searchText, i.FetchedAt)
	return err
}

// UpsertIssue は Store 直接実行版(トランザクション不要な単発更新用)。
func (s *Store) UpsertIssue(ctx context.Context, i *Issue) error {
	return UpsertIssue(ctx, s.db, i)
}

// SearchIssueIDs はキーワードで search_text を LIKE 部分一致検索し、
// 該当課題 ID を返す。検索語にも保存時と同じ正規化を適用する。
func SearchIssueIDs(ctx context.Context, q dbtx, keyword string) ([]int64, error) {
	kw := NormalizeSearchText(keyword)
	pattern := "%" + escapeLike(kw) + "%"
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM issues WHERE deleted = 0 AND search_text LIKE ? ESCAPE '\' ORDER BY id`,
		pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SearchIssueIDs は Store 直接実行版。
func (s *Store) SearchIssueIDs(ctx context.Context, keyword string) ([]int64, error) {
	return SearchIssueIDs(ctx, s.db, keyword)
}

// escapeLike は LIKE パターンのメタ文字(% _ \)をエスケープする。
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// issueSearchText はテスト用に search_text 列を取得する。
func issueSearchText(ctx context.Context, q dbtx, id int64) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT search_text FROM issues WHERE id = ?`, id).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}
