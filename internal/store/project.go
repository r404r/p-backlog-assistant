package store

import (
	"context"
	"strings"
)

// Project はローカルキャッシュのプロジェクト 1 行。
type Project struct {
	ID         int64  `json:"id"`
	ProjectKey string `json:"projectKey"`
	Name       string `json:"name"`
	Archived   bool   `json:"archived"`
	RawJSON    string `json:"rawJson"`
	FetchedAt  string `json:"fetchedAt"`
}

// UpsertProject はプロジェクトを id で UPSERT する。
func UpsertProject(ctx context.Context, q dbtx, p *Project) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO projects (id, project_key, name, archived, raw_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			project_key = excluded.project_key,
			name = excluded.name,
			archived = excluded.archived,
			raw_json = excluded.raw_json,
			fetched_at = excluded.fetched_at`,
		p.ID, p.ProjectKey, p.Name, boolToInt(p.Archived), p.RawJSON, p.FetchedAt)
	return err
}

// UpsertProject は Store 直接実行版。
func (s *Store) UpsertProject(ctx context.Context, p *Project) error {
	return UpsertProject(ctx, s.db, p)
}

// ListProjects はキャッシュ済みプロジェクトを id 昇順で返す。
func ListProjects(ctx context.Context, q dbtx) ([]Project, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, project_key, name, archived, raw_json, fetched_at FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var archived int
		if err := rows.Scan(&p.ID, &p.ProjectKey, &p.Name, &archived, &p.RawJSON, &p.FetchedAt); err != nil {
			return nil, err
		}
		p.Archived = archived != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListProjects は Store 直接実行版。
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	return ListProjects(ctx, s.db)
}

// DeleteProjectsNotIn は keepIDs に含まれないプロジェクト行と、
// その課題・課題コメント・プロジェクトユーザ・プロジェクト別同期状態・
// 一括更新ジョブ(jobs / job_rows)のキャッシュを破棄する
// (設計書 2 節: プロジェクトから除外された場合に旧データをローカル検索から
// 閲覧できる状態を残さない)。
//
// 呼び出し元は全削除を 1 つのトランザクションで実行すること(SyncProjects は
// そうしている)。部分的に消えると、プロジェクトは消えたのにジョブの payload
// だけ残る、といった状態になりうる。
//
// keepIDs が空の場合は「参加プロジェクトが 0 件」という正常応答を意味するため、
// 全プロジェクトのキャッシュを破棄する(高 1)。呼び出し元(internal/sync の
// SyncProjects)は GET /projects が失敗した時点で return しており、取得失敗で
// ここへ到達することはない。取得失敗を空応答と取り違えないよう、この関数を
// 新たな箇所から呼ぶ場合も「API 応答が成功したこと」を前提とすること。
func DeleteProjectsNotIn(ctx context.Context, q dbtx, keepIDs []int64) (int, error) {
	// ID は int64 なので直接埋め込んでも SQL インジェクションは起きない
	placeholders := make([]string, len(keepIDs))
	args := make([]any, len(keepIDs))
	for i, id := range keepIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	// keepIDs が空なら条件を付けず全件を対象にする
	// (SQL の "NOT IN ()" は構文エラーになるため句自体を落とす)
	notIn := ""
	if len(keepIDs) > 0 {
		notIn = " WHERE id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	res, err := q.ExecContext(ctx, `DELETE FROM projects`+notIn, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	byProject := ""
	if len(keepIDs) > 0 {
		byProject = " WHERE project_id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	// 課題コメント(v5)。課題の詳細と同じく閲覧できなくなったプロジェクトの
	// ものは残さない。issue_comments.issue_id は ON DELETE CASCADE なので
	// 課題の削除でも消えるが、FK が無効な接続(PRAGMA foreign_keys = OFF)や
	// 将来 issues の削除経路が変わった場合に取り残さないよう明示的に消す。
	// 課題より先に実行するのは、issue_comments.project_id を条件に使うため
	// 順序自体は問わないが、親(issues)より先に子を消す流儀に揃えている。
	if _, err := q.ExecContext(ctx, `DELETE FROM issue_comments`+byProject, args...); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM issues`+byProject, args...); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM project_users`+byProject, args...); err != nil {
		return 0, err
	}
	// 一括更新ジョブ(R2)。job_rows の payload には件名・詳細・カスタム属性が
	// 入るため、閲覧できなくなったプロジェクトのものは課題と同じ扱いで破棄する。
	// 行を先に消してから親ジョブを消す(親を先に消すと対象を特定できない)。
	if _, err := q.ExecContext(ctx,
		`DELETE FROM job_rows WHERE job_id IN (SELECT id FROM jobs`+byProject+`)`, args...); err != nil {
		return 0, err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM jobs`+byProject, args...); err != nil {
		return 0, err
	}
	// project_id = 0 はスペース共通(projects / users / teams)の同期状態なので残す
	syncWhere := ` WHERE project_id <> 0`
	if len(keepIDs) > 0 {
		syncWhere += ` AND project_id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM sync_state`+syncWhere, args...); err != nil {
		return 0, err
	}
	return int(n), nil
}

// DeleteProjectsNotIn は Store 直接実行版。
func (s *Store) DeleteProjectsNotIn(ctx context.Context, keepIDs []int64) (int, error) {
	return DeleteProjectsNotIn(ctx, s.db, keepIDs)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
