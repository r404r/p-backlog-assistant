package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DefaultSearchLimit は SearchIssues の既定の返却上限(UI プレビュー用)。
const DefaultSearchLimit = 5000

// IssueFilter は課題抽出(画面 2)の検索条件。
// ProjectID は必須(スペース横断検索は行わない)。
type IssueFilter struct {
	ProjectID int64  `json:"projectId"` // 必須
	Keyword   string `json:"keyword"`   // 件名 + 詳細の部分一致(search_text へ LIKE)。空白区切りで複数語
	// KeywordMode は複数語の連結方法("and" = 全語を含む / "or" = いずれかを含む)。
	// 空文字・未知の値は "and" として扱う。
	KeywordMode  string `json:"keywordMode"`
	CreatedFrom  string `json:"createdFrom"`  // yyyy-MM-dd または ISO8601
	CreatedTo    string `json:"createdTo"`    //
	UpdatedFrom  string `json:"updatedFrom"`  //
	UpdatedTo    string `json:"updatedTo"`    //
	StatusName   string `json:"statusName"`   // 完全一致
	AssigneeName string `json:"assigneeName"` // 完全一致
	Limit        int    `json:"limit"`        // 0 なら DefaultSearchLimit
}

// IssueSearchResult は検索結果(上限で切っても総件数を返す)。
type IssueSearchResult struct {
	Issues    []Issue `json:"issues"`
	Total     int     `json:"total"`     // 条件に一致した総件数
	Truncated bool    `json:"truncated"` // 上限で切り詰めたか
}

// FilterOptions は抽出条件の候補値(DISTINCT)。
type FilterOptions struct {
	StatusNames   []string `json:"statusNames"`
	AssigneeNames []string `json:"assigneeNames"`
}

// buildFilter は WHERE 句と引数を組み立てる(deleted = 0 のみ対象)。
func (f IssueFilter) buildFilter() (string, []any, error) {
	if f.ProjectID == 0 {
		return "", nil, errors.New("プロジェクトが指定されていません")
	}
	where := []string{"deleted = 0", "project_id = ?"}
	args := []any{f.ProjectID}

	if terms := splitKeywords(f.Keyword); len(terms) > 0 {
		conds := make([]string, 0, len(terms))
		for _, t := range terms {
			conds = append(conds, `search_text LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(t)+"%")
		}
		if isOrKeywordMode(f.KeywordMode) {
			// OR は必ず括弧で括る。括らないと project_id 等の他条件まで
			// OR の被演算子になり、別プロジェクトの課題が混入する。
			where = append(where, "("+strings.Join(conds, " OR ")+")")
		} else {
			where = append(where, conds...)
		}
	}
	addRange := func(col, from, to string) {
		if from != "" {
			where = append(where, col+" >= ?")
			args = append(args, rangeStart(from))
		}
		if to != "" {
			where = append(where, col+" <= ?")
			args = append(args, rangeEnd(to))
		}
	}
	addRange("created", f.CreatedFrom, f.CreatedTo)
	addRange("updated", f.UpdatedFrom, f.UpdatedTo)

	if f.StatusName != "" {
		where = append(where, "status_name = ?")
		args = append(args, f.StatusName)
	}
	if f.AssigneeName != "" {
		where = append(where, "assignee_name = ?")
		args = append(args, f.AssigneeName)
	}
	return strings.Join(where, " AND "), args, nil
}

// KeywordMode の値(IssueFilter.KeywordMode)。
const (
	KeywordModeAnd = "and" // 全語を含む(既定)
	KeywordModeOr  = "or"  // いずれかを含む
)

// splitKeywords はキーワード入力を空白で語に分割し、保存時と同じ正規化を
// 語ごとに適用して返す(空要素は除去)。strings.Fields は unicode.IsSpace で
// 分割するため、半角スペースだけでなく全角スペース(U+3000)・タブも区切りになる。
// 正規化後に空になる語(制御文字のみ等)は検索対象から外す。
// 残さないと "%%" となり、その語が全件一致してしまう。
func splitKeywords(keyword string) []string {
	fields := strings.Fields(keyword)
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		// 保存時と同じ正規化を検索語にも適用する(設計書 4 節の補足)
		if t := NormalizeSearchText(f); t != "" {
			terms = append(terms, t)
		}
	}
	return terms
}

// isOrKeywordMode は複数キーワードを OR で連結するかを判定する。
// 契約上の許可値は "and" / "or" のみ(フロントは小文字で送る)。
// "or" と完全一致した場合だけ OR とし、空文字・大文字・空白付きを含む
// それ以外の値はすべて既定の AND として扱う(寛容に受理しない)。
func isOrKeywordMode(mode string) bool {
	return mode == KeywordModeOr
}

// rangeStart は日付のみ(yyyy-MM-dd)の下限をその日の 00:00:00Z に展開する。
// created / updated は ISO8601(...T..:..:..Z)で保存されているため、
// 辞書順比較がそのまま時刻比較になる。
func rangeStart(v string) string {
	if isDateOnly(v) {
		return v + "T00:00:00Z"
	}
	return v
}

// rangeEnd は日付のみの上限をその日の終わり(23:59:59)まで含める。
func rangeEnd(v string) string {
	if isDateOnly(v) {
		return v + "T23:59:59Z"
	}
	return v
}

func isDateOnly(v string) bool {
	if len(v) != 10 {
		return false
	}
	for i, r := range v {
		if i == 4 || i == 7 {
			if r != '-' {
				return false
			}
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// issueColumns は Issue の SELECT 列。
const issueColumns = `id, issue_key, project_id, summary, description,
	status_id, status_name, assignee_id, assignee_name,
	issue_type_name, priority_name, created, updated, due_date,
	raw_json, fetched_at`

func scanIssues(ctx context.Context, q dbtx, query string, args ...any) ([]Issue, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		var i Issue
		if err := rows.Scan(&i.ID, &i.IssueKey, &i.ProjectID, &i.Summary, &i.Description,
			&i.StatusID, &i.StatusName, &i.AssigneeID, &i.AssigneeName,
			&i.IssueTypeName, &i.PriorityName, &i.Created, &i.Updated, &i.DueDate,
			&i.RawJSON, &i.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SearchIssues はローカル DB を検索する(設計書 4 節)。
// 総件数は上限に関わらず返し、UI が「N 件中 M 件を表示」と示せるようにする。
//
// 件数と行の 2 クエリを発行するため、呼び出し側は同一トランザクションを
// 渡すこと(Store.SearchIssues は WithReadTx で包む)。別々の接続で実行すると
// 2 クエリの間に同期の書き込みが割り込み、total と行数が食い違う(中 2)。
func SearchIssues(ctx context.Context, q dbtx, f IssueFilter) (*IssueSearchResult, error) {
	where, args, err := f.buildFilter()
	if err != nil {
		return nil, err
	}
	var total int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues WHERE `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	issues, err := scanIssues(ctx, q,
		`SELECT `+issueColumns+` FROM issues WHERE `+where+` ORDER BY id LIMIT `+strconv.Itoa(limit),
		args...)
	if err != nil {
		return nil, err
	}
	return &IssueSearchResult{Issues: issues, Total: total, Truncated: total > len(issues)}, nil
}

// SearchIssues は Store 直接実行版。件数と行を単一の読み取りトランザクションで
// 取得し、同期の書き込みが割り込んでも一貫したスナップショットを返す(中 2)。
func (s *Store) SearchIssues(ctx context.Context, f IssueFilter) (*IssueSearchResult, error) {
	var res *IssueSearchResult
	if err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = SearchIssues(ctx, tx, f)
		return err
	}); err != nil {
		return nil, err
	}
	return res, nil
}

// ListFilterOptions は指定プロジェクトの状態・担当者の候補値を返す
// (削除済み課題は除外)。
//
// 状態・担当者で 2 クエリを発行するため、SearchIssues と同様に呼び出し側が
// 同一トランザクションを渡すこと(Store.ListFilterOptions は WithReadTx で包む。中 2)。
func ListFilterOptions(ctx context.Context, q dbtx, projectID int64) (*FilterOptions, error) {
	if projectID == 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	distinct := func(col string) ([]string, error) {
		rows, err := q.QueryContext(ctx,
			`SELECT DISTINCT `+col+` FROM issues
			 WHERE project_id = ? AND deleted = 0 AND `+col+` <> '' AND `+col+` IS NOT NULL
			 ORDER BY `+col, projectID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	statuses, err := distinct("status_name")
	if err != nil {
		return nil, err
	}
	assignees, err := distinct("assignee_name")
	if err != nil {
		return nil, err
	}
	return &FilterOptions{StatusNames: statuses, AssigneeNames: assignees}, nil
}

// ListFilterOptions は Store 直接実行版(単一の読み取りトランザクション。中 2)。
func (s *Store) ListFilterOptions(ctx context.Context, projectID int64) (*FilterOptions, error) {
	var opts *FilterOptions
	if err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		opts, err = ListFilterOptions(ctx, tx, projectID)
		return err
	}); err != nil {
		return nil, err
	}
	return opts, nil
}

// UpsertIssues は課題を一括 UPSERT する(search_text 生成込み)。
// 呼び出し側が Store.WithTx で包めば 1 ページぶんが原子的に反映される。
func UpsertIssues(ctx context.Context, q dbtx, issues []*Issue) error {
	for _, i := range issues {
		if err := UpsertIssue(ctx, q, i); err != nil {
			return fmt.Errorf("課題 %d の保存に失敗しました: %w", i.ID, err)
		}
	}
	return nil
}

// UpsertIssues は Store 直接実行版(トランザクションで包む)。
func (s *Store) UpsertIssues(ctx context.Context, issues []*Issue) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return UpsertIssues(ctx, tx, issues)
	})
}

// ListIssueIDs は指定プロジェクトの未削除課題 ID を昇順で返す
// (フル同期の削除候補検出・リコンシリエーション用)。
func ListIssueIDs(ctx context.Context, q dbtx, projectID int64) ([]int64, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id FROM issues WHERE project_id = ? AND deleted = 0 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListIssueIDs は Store 直接実行版。
func (s *Store) ListIssueIDs(ctx context.Context, projectID int64) ([]int64, error) {
	return ListIssueIDs(ctx, s.db, projectID)
}

// IssueRef は課題の識別子だけを持つ軽量表現。
type IssueRef struct {
	ID       int64  `json:"id"`
	IssueKey string `json:"issueKey"`
}

// ListIssueRefs は指定プロジェクトの未削除課題の ID と課題キーを返す
// (フル同期の削除候補確認で GET /issues/:issueKey を呼ぶために使う)。
func ListIssueRefs(ctx context.Context, q dbtx, projectID int64) ([]IssueRef, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, issue_key FROM issues WHERE project_id = ? AND deleted = 0 ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []IssueRef{}
	for rows.Next() {
		var r IssueRef
		if err := rows.Scan(&r.ID, &r.IssueKey); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// ListIssueRefs は Store 直接実行版。
func (s *Store) ListIssueRefs(ctx context.Context, projectID int64) ([]IssueRef, error) {
	return ListIssueRefs(ctx, s.db, projectID)
}

// GetIssueUpdatedMap は指定プロジェクトの課題 ID → updated のマップを返す
// (差分同期で「変化した行だけ更新する」ための比較用)。
func GetIssueUpdatedMap(ctx context.Context, q dbtx, projectID int64) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, updated FROM issues WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var id int64
		var updated string
		if err := rows.Scan(&id, &updated); err != nil {
			return nil, err
		}
		m[id] = updated
	}
	return m, rows.Err()
}

// GetIssueUpdatedMap は Store 直接実行版。
func (s *Store) GetIssueUpdatedMap(ctx context.Context, projectID int64) (map[int64]string, error) {
	return GetIssueUpdatedMap(ctx, s.db, projectID)
}

// 論理削除は必ず projectID で限定する(中 1)。削除の根拠となる識別子
// (アクティビティの content.id / key_id)は実機未確認で信頼度が低く、
// プロジェクト条件が無いと他プロジェクトの課題を巻き添えにしうるため、
// 以下の 3 関数はいずれも project_id 条件を必須にしている。

// MarkIssuesDeleted は指定プロジェクトの課題を論理削除する(deleted = 1)。
func MarkIssuesDeleted(ctx context.Context, q dbtx, projectID int64, ids []int64) error {
	for _, id := range ids {
		if _, err := q.ExecContext(ctx,
			`UPDATE issues SET deleted = 1 WHERE id = ? AND project_id = ?`, id, projectID); err != nil {
			return err
		}
	}
	return nil
}

// MarkIssuesDeleted は Store 直接実行版。
func (s *Store) MarkIssuesDeleted(ctx context.Context, projectID int64, ids []int64) error {
	return MarkIssuesDeleted(ctx, s.db, projectID, ids)
}

// MarkIssueDeletedByKey は指定プロジェクトの課題を課題キー(EXA-1 等)で
// 論理削除する。該当行が無ければ false を返す(アクティビティの content から
// 課題 ID が取れず key_id 由来のキーで削除する場合に使う)。
func MarkIssueDeletedByKey(ctx context.Context, q dbtx, projectID int64, issueKey string) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE issues SET deleted = 1 WHERE issue_key = ? AND project_id = ?`, issueKey, projectID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkIssueDeletedByKey は Store 直接実行版。
func (s *Store) MarkIssueDeletedByKey(ctx context.Context, projectID int64, issueKey string) (bool, error) {
	return MarkIssueDeletedByKey(ctx, s.db, projectID, issueKey)
}

// MarkIssueDeletedByID は指定プロジェクトの課題を ID で論理削除する。
// 該当行が無ければ false。
func MarkIssueDeletedByID(ctx context.Context, q dbtx, projectID, id int64) (bool, error) {
	res, err := q.ExecContext(ctx,
		`UPDATE issues SET deleted = 1 WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// MarkIssueDeletedByID は Store 直接実行版。
func (s *Store) MarkIssueDeletedByID(ctx context.Context, projectID, id int64) (bool, error) {
	return MarkIssueDeletedByID(ctx, s.db, projectID, id)
}
