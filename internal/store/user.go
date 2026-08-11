package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"backlog-assistant/internal/backlogclient"
)

// DefaultUserListLimit は ListUserRows の既定の返却上限(UI プレビュー用)。
const DefaultUserListLimit = 5000

// userIDChunkSize は関連情報(所属チーム・参加プロジェクト)を引く際に
// IN 句へ展開するユーザ ID の 1 回あたりの上限(中 3)。
// SQLite のホスト変数上限(既定 32,766)を超えないよう、返却件数に関わらず
// この件数ずつに分割して問い合わせる(Excel 出力は上限 100 万件で呼ばれる)。
const userIDChunkSize = 500

// User はローカルキャッシュのユーザ 1 行(users テーブル)。
type User struct {
	ID          int64
	UserCode    string // ログイン ID(API の userId)
	Name        string
	MailAddress string
	RoleType    int // API 実値(1〜6。backlogclient.RoleType で解釈する)
	RawJSON     string
	FetchedAt   string
}

// Team はローカルキャッシュのチーム 1 行 + そのメンバー(team_members)。
type Team struct {
	ID        int64
	Name      string
	MemberIDs []int64
	RawJSON   string
	FetchedAt string
}

// ProjectUser はプロジェクト参加者 1 行(project_users)。
type ProjectUser struct {
	UserID  int64
	IsAdmin bool // プロジェクト管理者(roleType とは別の、プロジェクト単位のフラグ)
}

// UserFilter はユーザ抽出(画面 4)の検索条件。
type UserFilter struct {
	// Keyword は名前・ログイン ID・メールアドレスの部分一致(空なら絞り込まない)。
	Keyword string `json:"keyword"`
	// RoleType は API 実値での絞り込み(0 なら全て)。
	RoleType int `json:"roleType"`
	// Limit は返却上限(0 なら DefaultUserListLimit)。
	Limit int `json:"limit"`
}

// UserRow はユーザ抽出の 1 行(所属チーム・参加プロジェクトを JOIN で解決済み)。
// json タグは Wails バインディング(app.go)がそのまま UI へ渡す契約。
type UserRow struct {
	ID          int64  `json:"id"`
	UserCode    string `json:"userCode"`
	Name        string `json:"name"`
	MailAddress string `json:"mailAddress"`
	RoleType    int    `json:"roleType"`
	// RoleName は roleType の表示名。未知の値は「不明(N)」形式で数値を含む(中 4)。
	RoleName string `json:"roleName"`
	// TeamNames / ProjectKeys / AdminProjectKeys は該当が無くても
	// 空スライスを返す(JSON で null にしない)。
	TeamNames        []string `json:"teamNames"`
	ProjectKeys      []string `json:"projectKeys"`
	AdminProjectKeys []string `json:"adminProjectKeys"`
}

// UserListResult はユーザ抽出の結果(上限で切っても総件数を返す)。
type UserListResult struct {
	Users     []UserRow `json:"users"`
	Total     int       `json:"total"`
	Truncated bool      `json:"truncated"`
}

// ReplaceUsers は users をスペース単位で全置換する(設計書 3 節)。
// 退会ユーザの残留を防ぐため、UPSERT ではなく削除 → 挿入で行う。
// 呼び出し側は Store.ReplaceUsers(トランザクション込み)を使うこと。
func ReplaceUsers(ctx context.Context, q dbtx, users []*User) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM users`); err != nil {
		return err
	}
	for _, u := range users {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO users (id, user_code, name, mail, role_type, raw_json, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.UserCode, u.Name, u.MailAddress, u.RoleType, u.RawJSON, u.FetchedAt); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceUsers は Store 直接実行版(全置換を 1 トランザクションで実行する)。
// 途中の INSERT が失敗した場合は先頭の DELETE ごと巻き戻る(低 2a)。
func (s *Store) ReplaceUsers(ctx context.Context, users []*User) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return ReplaceUsers(ctx, tx, users) })
}

// UpsertUsers は users を UPSERT する(既存ユーザは削除しない)。
// 縮退パスで一部プロジェクトの参加者取得に失敗した場合、合成したユーザ集合は
// スペース全体を網羅していない。この状態で ReplaceUsers を使うと、
// 取得に失敗したプロジェクトにのみ所属するユーザが消えるため、
// 削除反映を伴わないこちらを使う(高 2)。
func UpsertUsers(ctx context.Context, q dbtx, users []*User) error {
	for _, u := range users {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO users (id, user_code, name, mail, role_type, raw_json, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				user_code = excluded.user_code, name = excluded.name, mail = excluded.mail,
				role_type = excluded.role_type, raw_json = excluded.raw_json,
				fetched_at = excluded.fetched_at`,
			u.ID, u.UserCode, u.Name, u.MailAddress, u.RoleType, u.RawJSON, u.FetchedAt); err != nil {
			return err
		}
	}
	return nil
}

// UpsertUsers は Store 直接実行版(1 トランザクションで実行する)。
func (s *Store) UpsertUsers(ctx context.Context, users []*User) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return UpsertUsers(ctx, tx, users) })
}

// ReplaceTeams は teams と team_members をまとめてスペース単位で全置換する。
// 2 テーブルを別々に置換すると、途中で失敗した場合に解散チームのメンバー関係が
// 残るため、必ず同一トランザクションで実行する(設計書 3 節)。
func ReplaceTeams(ctx context.Context, q dbtx, teams []*Team) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM team_members`); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM teams`); err != nil {
		return err
	}
	for _, t := range teams {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO teams (id, name, raw_json, fetched_at) VALUES (?, ?, ?, ?)`,
			t.ID, t.Name, t.RawJSON, t.FetchedAt); err != nil {
			return err
		}
		for _, uid := range t.MemberIDs {
			if _, err := q.ExecContext(ctx, `
				INSERT INTO team_members (team_id, user_id) VALUES (?, ?)
				ON CONFLICT(team_id, user_id) DO NOTHING`, t.ID, uid); err != nil {
				return err
			}
		}
	}
	return nil
}

// ReplaceTeams は Store 直接実行版(全置換を 1 トランザクションで実行する)。
// teams を nil / 空で呼ぶとチーム関連のキャッシュを全消去する
// (権限が縮退して teams が 403 になった場合に管理者由来キャッシュを破棄する経路。設計書 2 節)。
func (s *Store) ReplaceTeams(ctx context.Context, teams []*Team) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return ReplaceTeams(ctx, tx, teams) })
}

// MergeTeams は指定されたチームのみを UPSERT する(未指定のチームは削除しない)。
// 各チームのメンバーは「そのチーム分だけ」置換する。
// 縮退パスでプロジェクト単位にチームを集めた場合、取得できたチームは
// スペース全体を網羅していないため、全置換すると未取得のチームが消える(高 1・高 2)。
func MergeTeams(ctx context.Context, q dbtx, teams []*Team) error {
	for _, t := range teams {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO teams (id, name, raw_json, fetched_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name, raw_json = excluded.raw_json, fetched_at = excluded.fetched_at`,
			t.ID, t.Name, t.RawJSON, t.FetchedAt); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = ?`, t.ID); err != nil {
			return err
		}
		for _, uid := range t.MemberIDs {
			if _, err := q.ExecContext(ctx, `
				INSERT INTO team_members (team_id, user_id) VALUES (?, ?)
				ON CONFLICT(team_id, user_id) DO NOTHING`, t.ID, uid); err != nil {
				return err
			}
		}
	}
	return nil
}

// MergeTeams は Store 直接実行版(1 トランザクションで実行する)。
func (s *Store) MergeTeams(ctx context.Context, teams []*Team) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return MergeTeams(ctx, tx, teams) })
}

// ReplaceProjectUsers は project_users を projectID 単位で全置換する。
// 脱退した参加関係の残留を防ぐため、対象プロジェクトの行を削除してから挿入する。
func ReplaceProjectUsers(ctx context.Context, q dbtx, projectID int64, members []ProjectUser) error {
	if _, err := q.ExecContext(ctx, `DELETE FROM project_users WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO project_users (project_id, user_id, is_admin) VALUES (?, ?, ?)
			ON CONFLICT(project_id, user_id) DO UPDATE SET is_admin = excluded.is_admin`,
			projectID, m.UserID, boolToInt(m.IsAdmin)); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceProjectUsers は Store 直接実行版(全置換を 1 トランザクションで実行する)。
func (s *Store) ReplaceProjectUsers(ctx context.Context, projectID int64, members []ProjectUser) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return ReplaceProjectUsers(ctx, tx, projectID, members) })
}

// buildUserFilter は WHERE 句と引数を組み立てる。
func (f UserFilter) buildUserFilter() (string, []any) {
	where := []string{"1 = 1"}
	var args []any
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		// 名前・ログイン ID・メールアドレスの部分一致(中 2。TS 契約・画面の説明と一致させる)。
		// SQLite の LIKE は既定で ASCII 範囲のみ大文字小文字を同一視する
		// (課題検索のような正規化列は持たない)。
		pattern := "%" + escapeLike(kw) + "%"
		where = append(where, `(name LIKE ? ESCAPE '\' OR user_code LIKE ? ESCAPE '\' OR mail LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern)
	}
	if f.RoleType != 0 { // 0 は「全て」
		where = append(where, "role_type = ?")
		args = append(args, f.RoleType)
	}
	return strings.Join(where, " AND "), args
}

// ListUserRows はユーザ一覧を所属チーム・参加プロジェクト・管理者プロジェクト付きで
// 返す(画面 4)。並びは名前順。総件数は上限に関わらず返し、
// UI が「N 件中 M 件を表示」と示せるようにする。
//
// 複数クエリ(件数・本体・チーム・プロジェクト)を発行するため、呼び出し側は
// 同一トランザクションを渡すこと(Store.ListUserRows は WithReadTx で包む)。
func ListUserRows(ctx context.Context, q dbtx, f UserFilter) (*UserListResult, error) {
	where, args := f.buildUserFilter()

	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultUserListLimit
	}
	rows, err := q.QueryContext(ctx, `
		SELECT id, user_code, name, mail, role_type FROM users
		WHERE `+where+`
		ORDER BY name, id LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []UserRow{}
	index := map[int64]int{} // ユーザ ID → users のインデックス
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.UserCode, &u.Name, &u.MailAddress, &u.RoleType); err != nil {
			return nil, err
		}
		u.RoleName = RoleName(u.RoleType)
		u.TeamNames = []string{}
		u.ProjectKeys = []string{}
		u.AdminProjectKeys = []string{}
		index[u.ID] = len(users)
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return &UserListResult{Users: users, Total: total}, nil
	}

	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}

	// 関連情報は ID を userIDChunkSize ずつに分割して引く(中 3)。
	// 返却全件分の変数を 1 クエリへ展開すると SQLite の上限(32,766)を超える。
	for _, chunk := range chunkIDs(ids, userIDChunkSize) {
		if err := appendUserTeams(ctx, q, users, index, chunk); err != nil {
			return nil, err
		}
		if err := appendUserProjects(ctx, q, users, index, chunk); err != nil {
			return nil, err
		}
	}

	return &UserListResult{Users: users, Total: total, Truncated: total > len(users)}, nil
}

// appendUserTeams はチャンク分のユーザについて所属チーム(チーム名順)を追加する。
func appendUserTeams(ctx context.Context, q dbtx, users []UserRow, index map[int64]int, ids []int64) error {
	args, in := idArgs(ids)
	rows, err := q.QueryContext(ctx, `
		SELECT tm.user_id, t.name FROM team_members tm
		JOIN teams t ON t.id = tm.team_id
		WHERE tm.user_id IN `+in+`
		ORDER BY t.name, t.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var name string
		if err := rows.Scan(&userID, &name); err != nil {
			return err
		}
		if i, ok := index[userID]; ok {
			users[i].TeamNames = append(users[i].TeamNames, name)
		}
	}
	return rows.Err()
}

// appendUserProjects はチャンク分のユーザについて参加プロジェクト
// (プロジェクトキー順)と管理者フラグを追加する。
// ローカルに projects 行が無いプロジェクト(アクセス不能になり破棄されたもの)は
// JOIN で自然に除外される。
func appendUserProjects(ctx context.Context, q dbtx, users []UserRow, index map[int64]int, ids []int64) error {
	args, in := idArgs(ids)
	rows, err := q.QueryContext(ctx, `
		SELECT pu.user_id, p.project_key, pu.is_admin FROM project_users pu
		JOIN projects p ON p.id = pu.project_id
		WHERE pu.user_id IN `+in+`
		ORDER BY p.project_key, p.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var key string
		var isAdmin int
		if err := rows.Scan(&userID, &key, &isAdmin); err != nil {
			return err
		}
		i, ok := index[userID]
		if !ok {
			continue
		}
		users[i].ProjectKeys = append(users[i].ProjectKeys, key)
		if isAdmin != 0 {
			users[i].AdminProjectKeys = append(users[i].AdminProjectKeys, key)
		}
	}
	return rows.Err()
}

// chunkIDs は ID 列を size 件ずつに分割する(size <= 0 なら分割しない)。
func chunkIDs(ids []int64, size int) [][]int64 {
	if size <= 0 || len(ids) <= size {
		return [][]int64{ids}
	}
	out := make([][]int64, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[start:end])
	}
	return out
}

// idArgs は IN 句のプレースホルダと引数列を組み立てる。
func idArgs(ids []int64) ([]any, string) {
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return args, "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
}

// RoleName は roleType(API 実値)の表示名を返す。
// 既知の値は名称、未知の値は「不明(N)」形式で数値を含める(中 4)。
// 数値を落とすと、API にロールが追加された場合に画面・Excel から
// 値を識別できなくなるため。
func RoleName(roleType int) string {
	r := backlogclient.RoleType(roleType)
	if r.IsValid() {
		return r.String()
	}
	return fmt.Sprintf("不明(%d)", roleType)
}

// ListUserRows は Store 直接実行版。件数と各 JOIN を単一の読み取り
// トランザクションで取得し、同期の書き込みが割り込んでも一貫した結果を返す。
func (s *Store) ListUserRows(ctx context.Context, f UserFilter) (*UserListResult, error) {
	var res *UserListResult
	if err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = ListUserRows(ctx, tx, f)
		return err
	}); err != nil {
		return nil, err
	}
	return res, nil
}
