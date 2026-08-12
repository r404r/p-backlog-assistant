package store

// issue_iterate.go は課題を全件メモリに載せずに 1 件ずつ扱うための走査 API(R4)。
// Excel 出力・一括更新テンプレート出力のように「条件一致全件を逐次書き出す」
// 経路がここを使う(画面プレビュー等の上限付き取得は issue_search.go の
// SearchIssues を使う)。

import (
	"context"
	"database/sql"

	"backlog-assistant/internal/customfield"
)

// ValidateIssueFilter は抽出条件の妥当性だけを検証する(DB へは触れない)。
//
// 逐次出力(R4)では走査を始めるまで条件の不備に気付けず、保存ダイアログを
// 出した後で「条件が不正です」と言うことになる。呼び出し側はダイアログを
// 出す前にこれで弾くこと。判定内容は IterateIssues / SearchIssues の
// 入口と同じ(プロジェクト必須・カスタム属性条件の妥当性)。
func ValidateIssueFilter(f IssueFilter) error {
	if _, err := f.buildFilter(); err != nil {
		return err
	}
	return customfield.ValidateFilters(customfield.ActiveFilters(f.CustomFieldFilters))
}

// IssueVisitor は IterateIssues が条件に一致した課題を 1 件ずつ渡すコールバック。
//
// 非 nil のエラーを返すと走査はその場で打ち切られ、そのエラーが
// IterateIssues の戻り値になる(件数上限での打ち切り等に使う)。
type IssueVisitor func(*Issue) error

// IssueIterateResult は IterateIssues の集計結果。
type IssueIterateResult struct {
	// Total は条件に一致し visit へ渡した件数(打ち切った場合はそこまでの件数)。
	Total int
	// Unverifiable はカスタム属性条件を判定できなかった課題の件数
	// (IssueSearchResult.Unverifiable と同じ意味)。
	Unverifiable int
}

// IterateIssues は条件に一致する課題を SQL カーソルで 1 件ずつ visit へ渡す(R4)。
//
// SearchIssues が結果をスライスに溜めるのに対し、こちらは全件をメモリに
// 保持しない。Excel 出力・一括更新テンプレート出力のように「条件一致全件を
// 逐次書き出す」用途で使う。
//
// SearchIssues との違い:
//   - IssueFilter.Limit は無視する(常に全件を走査する)。件数上限の判定は
//     visit の中で行い、エラーを返して打ち切ること。
//   - Truncated に相当する概念は無い。
//
// カスタム属性条件の 2 段階判定(SQL → Go)と Unverifiable の数え方は
// SearchIssues と同じ経路(iterateIssueRows)で処理するため、同じ条件なら
// 同じ集合・同じ件数になる。
//
// 呼び出しの約束: visit の中からローカル DB を触らないこと。DB 接続は 1 本に
// 絞っており(store.Open)、Store.IterateIssues は読み取り Tx を保持したまま
// 走査するため、Store の別メソッドを呼ぶとデッドロックする。
// (同じ Tx を使った問い合わせであればカーソルを開いたまま実行できることは
// issue_iterate_test.go で確認済み。ID を先に集めてチャンク読みする方式は
// 不要と判断した。)
func IterateIssues(ctx context.Context, q dbtx, f IssueFilter, visit IssueVisitor) (IssueIterateResult, error) {
	spec, err := f.buildFilter()
	if err != nil {
		return IssueIterateResult{}, err
	}
	// 条件が空のカスタム属性は最初に落とし、条件が実質無いときは
	// 生 JSON の解析コストを掛けない(SearchIssues と同じ扱い)。
	cfFilters := customfield.ActiveFilters(f.CustomFieldFilters)
	if err := customfield.ValidateFilters(cfFilters); err != nil {
		return IssueIterateResult{}, err
	}
	match := matchAll
	if len(cfFilters) > 0 {
		match = customFieldMatcher(cfFilters)
	}
	// 打ち切られた場合も、そこまでの集計を返す(呼び出し側のログ用)。
	total, unverifiable, err := iterateIssueRows(ctx, q, spec.selectQuery(), match, visit, spec.args...)
	return IssueIterateResult{Total: total, Unverifiable: unverifiable}, err
}

// IterateIssues は Store 直接実行版。走査中は読み取りトランザクションを保持し、
// 同期の書き込みが割り込んで途中から別のスナップショットになることを防ぐ(中 2)。
func (s *Store) IterateIssues(ctx context.Context, f IssueFilter, visit IssueVisitor) (IssueIterateResult, error) {
	var res IssueIterateResult
	err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		res, err = IterateIssues(ctx, tx, f, visit)
		return err
	})
	return res, err
}
