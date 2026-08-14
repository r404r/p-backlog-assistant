package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"backlog-assistant/internal/customfield"
)

// DefaultSearchLimit は SearchIssues の既定の返却上限(UI プレビュー用)。
const DefaultSearchLimit = 5000

// IssueFilter は課題抽出(画面 2)の検索条件。
// ProjectID は必須(スペース横断検索は行わない)。
type IssueFilter struct {
	ProjectID int64 `json:"projectId"` // 必須
	// Keyword は課題キー + 件名 + 詳細の部分一致(search_text が対象)。空白区切りで複数語。
	// 3 文字以上の語は FTS5 索引で候補を絞り込んでから LIKE で再判定し、
	// 3 文字未満の語は LIKE のみで判定する(結果は従来と同一。issue_fts.go 参照)。
	Keyword string `json:"keyword"`
	// KeywordMode は複数語の連結方法("and" = 全語を含む / "or" = いずれかを含む)。
	// 空文字・未知の値は "and" として扱う。
	KeywordMode  string `json:"keywordMode"`
	CreatedFrom  string `json:"createdFrom"`  // yyyy-MM-dd または ISO8601
	CreatedTo    string `json:"createdTo"`    //
	UpdatedFrom  string `json:"updatedFrom"`  //
	UpdatedTo    string `json:"updatedTo"`    //
	StatusName   string `json:"statusName"`   // 完全一致
	AssigneeName string `json:"assigneeName"` // 完全一致
	// CustomFieldFilters はカスタム属性の絞り込み条件(定義ごとに 1 条件・AND)。
	//
	// カスタム属性の値は issues.raw_json の中にしか無いため SQL では絞り込めない。
	// この条件は SQL 実行後に Go 側で適用される(SearchIssues の 2 段階検索)。
	CustomFieldFilters []customfield.Filter `json:"customFieldFilters"`
	Limit              int                  `json:"limit"` // 0 なら DefaultSearchLimit
	// Offset は条件に一致した行の先頭から読み飛ばす件数(画面のページング用)。
	// 0 なら先頭から。負値は 0 として扱う(呼び出し側のバグで検索全体を
	// 失敗させない)。カスタム属性経路では「SQL の行」ではなく
	// 「カスタム属性条件にも一致した行」を基準に数える。
	Offset int `json:"offset"`
}

// IssueSearchResult は検索結果(上限で切っても総件数を返す)。
type IssueSearchResult struct {
	Issues []Issue `json:"issues"`
	Total  int     `json:"total"` // 条件に一致した総件数(Offset の影響を受けない)
	// Truncated は「この応答より後ろに一致行が残っているか」
	// (= Total > Offset + len(Issues))。Offset を使わない検索では
	// 従来どおり「上限で切り詰めたか」と同じ意味になる。
	Truncated bool `json:"truncated"`
	// Unverifiable はカスタム属性条件を判定できなかった課題の件数
	// (生 JSON が空・壊れている行)。0 でなければ結果は「条件に合う全件」では
	// ないため、呼び出し側は利用者へその旨を伝えること。
	// カスタム属性条件が無い検索では常に 0(生 JSON を読まないため)。
	Unverifiable int `json:"unverifiable"`
}

// FilterOptions は抽出条件の候補値(DISTINCT)。
type FilterOptions struct {
	StatusNames   []string `json:"statusNames"`
	AssigneeNames []string `json:"assigneeNames"`
}

// issueQuerySpec は課題検索の SQL 断片(FROM 句・WHERE 句・並び順・引数)。
// FTS 索引を使うかどうかで FROM 句と並び順が変わるため、WHERE 句だけでなく
// まとめて持ち回る(issue_fts.go 参照)。
type issueQuerySpec struct {
	from    string
	where   string
	orderBy string
	args    []any
}

// selectQuery は条件に一致する課題を ID 昇順で取り出す SELECT 文を組み立てる
// (検索・走査で同じ並びを使うため 1 か所にまとめる)。
func (s issueQuerySpec) selectQuery() string {
	return `SELECT ` + issueColumns + ` FROM ` + s.from +
		` WHERE ` + s.where + ` ORDER BY ` + s.orderBy
}

// countQuery は条件に一致する課題の総件数を数える SELECT 文を組み立てる。
func (s issueQuerySpec) countQuery() string {
	return `SELECT COUNT(*) FROM ` + s.from + ` WHERE ` + s.where
}

// buildFilter は検索条件から SQL 断片を組み立てる(deleted = 0 のみ対象)。
//
// 列名はすべて issues. で修飾する。FTS を使う場合の FROM 句には
// issues_fts が加わり、search_text 等の列名が曖昧になるため。
func (f IssueFilter) buildFilter() (issueQuerySpec, error) {
	if f.ProjectID == 0 {
		return issueQuerySpec{}, errors.New("プロジェクトが指定されていません")
	}
	spec := issueQuerySpec{from: issuesFrom, orderBy: issuesOrderBy}
	where := []string{"issues.deleted = 0", "issues.project_id = ?"}
	args := []any{f.ProjectID}

	if terms := splitKeywords(f.Keyword); len(terms) > 0 {
		or := isOrKeywordMode(f.KeywordMode)
		// FTS5 索引による事前絞り込み(R3)。索引を使えない語しか無い場合は
		// 従来どおり LIKE だけで検索する。LIKE 条件は FTS を使う場合も残し、
		// 結果集合が従来と完全に一致することを保証する(issue_fts.go 参照)。
		if expr, ok := ftsMatchExpr(terms, or); ok {
			spec.from = issuesFTSFrom
			spec.orderBy = issuesFTSOrderBy
			where = append(where, ftsMatchCond)
			args = append(args, expr)
		}
		conds := make([]string, 0, len(terms))
		for _, t := range terms {
			conds = append(conds, `issues.search_text LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(t)+"%")
		}
		if or {
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
	addRange("issues.created", f.CreatedFrom, f.CreatedTo)
	addRange("issues.updated", f.UpdatedFrom, f.UpdatedTo)

	if f.StatusName != "" {
		where = append(where, "issues.status_name = ?")
		args = append(args, f.StatusName)
	}
	if f.AssigneeName != "" {
		where = append(where, "issues.assignee_name = ?")
		args = append(args, f.AssigneeName)
	}
	spec.where = strings.Join(where, " AND ")
	spec.args = args
	return spec, nil
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
// FTS 索引と結合する場合に列名が曖昧にならないよう issues. で修飾する。
const issueColumns = `issues.id, issues.issue_key, issues.project_id, issues.summary, issues.description,
	issues.status_id, issues.status_name, issues.assignee_id, issues.assignee_name,
	issues.issue_type_name, issues.priority_name, issues.created, issues.updated, issues.due_date,
	issues.raw_json, issues.fetched_at`

func scanIssues(ctx context.Context, q dbtx, query string, args ...any) ([]Issue, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		var i Issue
		if err := scanIssueRow(rows, &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// scanIssueRow は issueColumns の並びで 1 行を Issue に読み込む
// (scanIssues と iterateIssueRows で列の並びを共有するため関数に切り出す)。
func scanIssueRow(rows *sql.Rows, i *Issue) error {
	return rows.Scan(&i.ID, &i.IssueKey, &i.ProjectID, &i.Summary, &i.Description,
		&i.StatusID, &i.StatusName, &i.AssigneeID, &i.AssigneeName,
		&i.IssueTypeName, &i.PriorityName, &i.Created, &i.Updated, &i.DueDate,
		&i.RawJSON, &i.FetchedAt)
}

// issueMatch は 2 段階目(Go 側でのカスタム属性判定)の結果。
type issueMatch int

const (
	matchNo           issueMatch = iota // 条件を満たさない
	matchYes                            // 条件を満たす
	matchUnverifiable                   // 生 JSON が無い・壊れていて判定できない
)

// iterateIssueRows は SQL の結果を SQL カーソル(rows.Next)で 1 行ずつ読み、
// match が真の行だけを visit へ渡す。全行を保持しないため、一致件数がいくら
// 多くてもメモリ使用量は 1 行ぶんで頭打ちになる(R4)。
//
// 返す total は match が真だった件数、unverifiable は判定できなかった件数。
// visit がエラーを返した時点で走査を打ち切り、そのエラーをそのまま返す
// (呼び出し側は errors.Is で自分の打ち切り理由を判定できる)。
//
// visit へ渡す *Issue は行ごとに新しく確保する。使い回すと、呼び出し側が
// うっかり保持したときに全行が最後の 1 件に化けるという分かりにくい不具合を
// 招くため(1 行ぶんの確保コストより誤用防止を優先する)。
func iterateIssueRows(ctx context.Context, q dbtx, query string,
	match func(*Issue) issueMatch, visit IssueVisitor, args ...any) (total, unverifiable int, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		i := new(Issue)
		if err := scanIssueRow(rows, i); err != nil {
			return 0, 0, err
		}
		switch match(i) {
		case matchUnverifiable:
			unverifiable++
		case matchYes:
			total++
			if err := visit(i); err != nil {
				return total, unverifiable, err
			}
		}
	}
	return total, unverifiable, rows.Err()
}

// matchAll は絞り込みを行わない match 関数(SQL だけで条件が完結する場合)。
func matchAll(*Issue) issueMatch { return matchYes }

// scanIssuesMatching は SQL で絞った行を走査し、match が真の行のうち先頭 offset 件を
// 読み飛ばした続きを最大 limit 件返す。読み飛ばし・上限に関わらず一致件数は
// 数え続け、total として返す(UI の「N 件中 M 件を表示」を SQL 側の COUNT(*) と
// 同じ意味に保つため)。判定できなかった行は結果に含めず、件数だけを
// unverifiable として返す。
//
// offset は「SQL の行」ではなく「match が真だった行」を基準に数える。
// SQL 側で LIMIT / OFFSET を使うとカスタム属性条件の適用前の行を飛ばすことになり、
// ページごとに件数が食い違うため(SearchIssues のコメント参照)。
//
// 保持するのは limit 件までなので、一致件数が多くてもメモリは上限で頭打ちになる。
// ただし SQL 側で LIMIT できない(絞り込みが Go 側でしか行えない)ため、
// 条件に合う行の判定自体は SQL 一致行の全件に対して行われる。
func scanIssuesMatching(ctx context.Context, q dbtx, query string,
	match func(*Issue) issueMatch, offset, limit int, args ...any) (issues []Issue, total, unverifiable int, err error) {
	out := []Issue{}
	matched := 0
	total, unverifiable, err = iterateIssueRows(ctx, q, query, match, func(i *Issue) error {
		matched++
		if matched > offset && len(out) < limit {
			out = append(out, *i)
		}
		return nil
	}, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	return out, total, unverifiable, nil
}

// customFieldMatcher は課題 1 件がカスタム属性条件を満たすかを判定する関数を返す。
// 生 JSON の解析は 1 行につき 1 回だけ行い、全条件で使い回す。
func customFieldMatcher(filters []customfield.Filter) func(*Issue) issueMatch {
	return func(i *Issue) issueMatch {
		parsed, err := customfield.ParseValuesDetail(i.RawJSON)
		if err != nil {
			// 生 JSON が空(旧バージョンで同期した課題)・壊れている行は
			// 条件を満たすか確認できない。結果に含めると利用者は絞り込み結果を
			// 「条件を満たすものの集合」と誤解するため含めないが、黙って捨てると
			// 今度は「条件に合う全件」と誤解されるため、件数を呼び出し側へ返す。
			// 検索全体は失敗させない(1 件のデータ不備で抽出できなくなる方が損失が大きい)。
			return matchUnverifiable
		}
		if !parsed.HasCustomFields {
			// customFields キーが無い / null の行。空配列 [] とは扱いを分ける:
			//   - 空配列 …………… 「属性 0 件」という確定した応答なので値なしとして判定する
			//     (下の MatchValues に進み、条件があれば不一致になる)
			//   - キー無し / null … 値が無いのか、そもそも取得していないのかを区別できない。
			//     カスタム属性を保存していなかった頃に同期した課題が該当しうるため、
			//     「条件を満たさない」と断定せず判定不能として数える。
			//
			// なお、この経路はカスタム属性条件が指定されたときにしか通らない。
			// 条件を出せるのは定義が 1 件以上あるプロジェクトだけなので、
			// 属性を持たないプロジェクト全件が判定不能になることはない。
			return matchUnverifiable
		}
		if customfield.MatchValues(parsed.Values, filters, NormalizeSearchText) {
			return matchYes
		}
		return matchNo
	}
}

// SearchIssues はローカル DB を検索する(設計書 4 節)。
// 総件数は上限・読み飛ばし(Offset)に関わらず返し、UI が
// 「N 件中 x〜y 件目を表示」と示せるようにする。
//
// Offset は一致行の先頭からの読み飛ばし件数で、画面のページングに使う
// (1 ページ目は 0、2 ページ目はページサイズ、…)。
//
// 件数と行の 2 クエリを発行するため、呼び出し側は同一トランザクションを
// 渡すこと(Store.SearchIssues は WithReadTx で包む)。別々の接続で実行すると
// 2 クエリの間に同期の書き込みが割り込み、total と行数が食い違う(中 2)。
//
// カスタム属性条件(CF4)が指定された場合は 2 段階の検索になる:
//
//	(a) SQL で既存条件(プロジェクト・キーワード・期間・状態・担当者)を適用し、
//	(b) その結果を 1 行ずつ走査しながら、生 JSON から取り出したカスタム属性の値へ
//	    Go 側で条件を適用する。
//
// SQL で JSON を解析しない(json_extract 等を使わない)のは、値の形が型ごとに
// 異なり選択肢 ID の照合まで SQL で書くと条件が読めなくなること、表示・出力と
// 判定規約がずれること(customfield パッケージに集約している)を避けるため。
// この経路では SQL 側で LIMIT / OFFSET / COUNT(*) を使えない(絞り込み前の
// 件数・行になる)ため、上限・読み飛ばし・総件数は Go 側の走査で決める。
func SearchIssues(ctx context.Context, q dbtx, f IssueFilter) (*IssueSearchResult, error) {
	spec, err := f.buildFilter()
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	// 負の Offset は 0 に丸める(SQL へ渡すとエラーになるため入口で潰す)
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	// 条件が空のカスタム属性は最初に落とし、条件が実質無いときは
	// 従来どおり SQL だけで完結させる(生 JSON の解析コストを掛けない)。
	cfFilters := customfield.ActiveFilters(f.CustomFieldFilters)
	if err := customfield.ValidateFilters(cfFilters); err != nil {
		return nil, err
	}
	if len(cfFilters) > 0 {
		issues, total, unverifiable, err := scanIssuesMatching(ctx, q,
			spec.selectQuery(), customFieldMatcher(cfFilters), offset, limit, spec.args...)
		if err != nil {
			return nil, err
		}
		return &IssueSearchResult{
			Issues:       issues,
			Total:        total,
			Truncated:    total > offset+len(issues),
			Unverifiable: unverifiable,
		}, nil
	}

	var total int
	if err := q.QueryRowContext(ctx, spec.countQuery(), spec.args...).Scan(&total); err != nil {
		return nil, err
	}
	issues, err := scanIssues(ctx, q,
		spec.selectQuery()+` LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset), spec.args...)
	if err != nil {
		return nil, err
	}
	return &IssueSearchResult{Issues: issues, Total: total, Truncated: total > offset+len(issues)}, nil
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
