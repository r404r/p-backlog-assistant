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

// buildSearchText は検索用テキスト(search_text 列)を組み立てる。
//
// 対象は「課題キー + 件名 + 詳細」。課題キー(EXA-123)を含めることで、
// 利用者が課題キーを貼り付けてそのまま検索できる(部分一致なので
// プロジェクトキーだけ・番号だけでも当たる)。
//
// 連結は改行 1 つ。改行は NFKC でもケースフォールドでも前後の文字と
// 合成・変化しないため、要素ごとに正規化した結果と、連結してから
// 正規化した結果が必ず一致する(v6 マイグレーションはこの性質を使い、
// 既存の search_text の先頭に課題キーを足すだけで作り直している)。
//
// 課題キーは 3 文字未満(2 文字のプロジェクトキー等)になりうるが、
// trigram 索引を使えない語は従来どおり LIKE のみで判定されるため、
// FTS 側に追加の対応は要らない(issue_fts.go の (2))。
func buildSearchText(i *Issue) string {
	return NormalizeSearchText(i.IssueKey + "\n" + i.Summary + "\n" + i.Description)
}

// UpsertIssue は課題を id で UPSERT する。search_text(課題キー + 件名 + 詳細の
// 正規化)は保存・更新時にここで必ず生成する(設計書 2 節)。
// q には *sql.DB(単発)または *sql.Tx(Store.WithTx 内)を渡す。
func UpsertIssue(ctx context.Context, q dbtx, i *Issue) error {
	searchText := buildSearchText(i)
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

// SearchIssueIDs はキーワードで search_text を部分一致検索し、
// 該当課題 ID を返す。検索語にも保存時と同じ正規化を適用する。
//
// SearchIssues と同じく、3 文字以上のキーワードは FTS5 索引で候補を絞り込み、
// LIKE で再判定する(結果は LIKE のみの場合と同一。issue_fts.go 参照)。
// キーワードは分割せず 1 語として扱う(空白も部分一致の対象)。
func SearchIssueIDs(ctx context.Context, q dbtx, keyword string) ([]int64, error) {
	kw := NormalizeSearchText(keyword)
	pattern := "%" + escapeLike(kw) + "%"
	from := issuesFrom
	where := `issues.deleted = 0 AND issues.search_text LIKE ? ESCAPE '\'`
	orderBy := issuesOrderBy
	args := []any{pattern}
	if expr, ok := ftsMatchExpr([]string{kw}, false); ok {
		from = issuesFTSFrom
		where = ftsMatchCond + ` AND ` + where
		orderBy = issuesFTSOrderBy
		args = append([]any{expr}, args...)
	}
	rows, err := q.QueryContext(ctx,
		`SELECT issues.id FROM `+from+` WHERE `+where+` ORDER BY `+orderBy, args...)
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
