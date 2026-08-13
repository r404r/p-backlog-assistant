package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// 課題コメントのローカル保存(v5)。
//
// コメントは課題詳細ポップアップの「最新の状態を取得」で 1 課題ぶんだけ
// Backlog から取得し、以後はローカル DB から表示する。
// 通常の同期(internal/sync)はコメントに一切触れない。

// IssueComment はローカルに保存した課題コメント 1 件。
type IssueComment struct {
	ID         int64
	IssueID    int64
	ProjectID  int64
	AuthorName string
	Content    string
	Created    string
	Updated    string
}

// CommentFetchStatus は課題単位のコメント取得結果(issues 側に保存する)。
type CommentFetchStatus struct {
	FetchedAt   string // 取得時刻(空 = 未取得)
	Truncated   bool   // 取得上限に達し、古いコメントを取得しきれていない
	HistoryOnly int    // 本文が無い(変更履歴のみの)項目の件数
}

// ReplaceIssueComments は課題 1 件ぶんのコメントを全入れ替えし、取得結果を
// 記録する(既存コメントを削除 → 渡された行を挿入 → issues の取得結果列を更新)。
//
// 差分更新にしないのは、Backlog 側で削除・編集されたコメントを
// ローカルに残さないため(取得したものがその課題のコメント全体になる)。
//
// comments には「本文のあるコメントだけ」を渡すこと。Backlog のコメント一覧には
// 状態変更などの変更履歴だけで本文が空の項目が含まれるため、呼び出し元が
// それらを除外し、件数だけ status.HistoryOnly で渡す契約とする
// (空文字が渡ってもエラーにはせず、そのまま保存する)。
// 各要素の IssueID / ProjectID は参照せず、引数の issueID / projectID を保存する
// (課題とコメントの取り違えを防ぐため)。
//
// comments が空スライス / nil でも、既存コメントの削除と取得結果の記録は行う
// (コメントが 0 件になったことをローカルへ反映するため)。
//
// q に *sql.Tx を渡した場合は呼び出し元のトランザクションに乗る
// (全体を単一トランザクションで完結させるのは呼び出し元の責務)。
func ReplaceIssueComments(ctx context.Context, q dbtx, issueID, projectID int64,
	comments []*IssueComment, status CommentFetchStatus) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM issue_comments WHERE issue_id = ?`, issueID); err != nil {
		return fmt.Errorf("課題 %d の既存コメントの削除に失敗しました: %w", issueID, err)
	}
	for _, c := range comments {
		if c == nil {
			continue
		}
		if _, err := q.ExecContext(ctx, `
			INSERT INTO issue_comments (
				id, issue_id, project_id, author_name, content, created, updated
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.ID, issueID, projectID, c.AuthorName, c.Content, c.Created, c.Updated); err != nil {
			return fmt.Errorf("コメント %d の保存に失敗しました: %w", c.ID, err)
		}
	}
	// 取得結果は課題行に持たせる(コメントは課題を開いたときだけ取得するため、
	// プロジェクト単位の sync_state では表せない。migrate.go の v5 参照)。
	// 対象課題が無ければ 0 行更新となるが、コメントを 1 件でも渡していれば
	// 上の INSERT が FK で失敗するため、ここで別途エラーにはしない。
	if _, err := q.ExecContext(ctx, `
		UPDATE issues SET
			comments_fetched_at = ?,
			comments_truncated = ?,
			comments_history_only = ?
		WHERE id = ?`,
		status.FetchedAt, boolToInt(status.Truncated), status.HistoryOnly, issueID); err != nil {
		return fmt.Errorf("課題 %d のコメント取得結果の記録に失敗しました: %w", issueID, err)
	}
	return nil
}

// ReplaceIssueComments は Store 直接実行版(全体を 1 トランザクションにまとめる)。
func (s *Store) ReplaceIssueComments(ctx context.Context, issueID, projectID int64,
	comments []*IssueComment, status CommentFetchStatus) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return ReplaceIssueComments(ctx, tx, issueID, projectID, comments, status)
	})
}

// ListIssueComments は課題のコメントを新しい順(created 降順、同着は id 降順)で
// 返す。created は秒精度で同着がありうるため、id で並びを安定させる。
// 取り違えを防ぐため、必ず issue_id で限定する。
func ListIssueComments(ctx context.Context, q dbtx, issueID int64) ([]IssueComment, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, issue_id, project_id, author_name, content, created, updated
		FROM issue_comments WHERE issue_id = ?
		ORDER BY created DESC, id DESC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IssueComment{}
	for rows.Next() {
		var c IssueComment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.ProjectID,
			&c.AuthorName, &c.Content, &c.Created, &c.Updated); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListIssueComments は Store 直接実行版。
func (s *Store) ListIssueComments(ctx context.Context, issueID int64) ([]IssueComment, error) {
	return ListIssueComments(ctx, s.db, issueID)
}

// GetIssueCommentStatus は課題のコメント取得結果を返す。
// 未取得の課題・存在しない課題 ID ではゼロ値を返す(エラーにしない)。
func GetIssueCommentStatus(ctx context.Context, q dbtx, issueID int64) (CommentFetchStatus, error) {
	// comments_fetched_at は v5 で追加した列で、移行時の既存行は NULL になる
	// (= 未取得)。NullString で受けて空文字に落とす。
	var (
		fetchedAt   sql.NullString
		truncated   int
		historyOnly int
	)
	err := q.QueryRowContext(ctx, `
		SELECT comments_fetched_at, comments_truncated, comments_history_only
		FROM issues WHERE id = ?`, issueID).Scan(&fetchedAt, &truncated, &historyOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return CommentFetchStatus{}, nil
	}
	if err != nil {
		return CommentFetchStatus{}, err
	}
	return CommentFetchStatus{
		FetchedAt:   fetchedAt.String,
		Truncated:   truncated != 0,
		HistoryOnly: historyOnly,
	}, nil
}

// GetIssueCommentStatus は Store 直接実行版。
func (s *Store) GetIssueCommentStatus(ctx context.Context, issueID int64) (CommentFetchStatus, error) {
	return GetIssueCommentStatus(ctx, s.db, issueID)
}
