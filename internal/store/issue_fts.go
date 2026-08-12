package store

// issue_fts.go はキーワード検索の FTS5 索引まわり(R3)。
//
// 背景: 従来のキーワード検索は search_text LIKE '%語%' の前方ワイルドカードで、
// 索引が使えず issues を全走査していた(件数と一覧で二重走査)。10 万件規模で
// 顕著に遅くなるため、FTS5(trigram トークナイザ)の索引で候補行を絞り込む。
//
// 設計の要点:
//
//	(1) トークナイザは trigram。既定の unicode61 は空白区切りの言語しか
//	    分かち書きできず、日本語の部分一致検索に使えない。trigram は
//	    「連続する 3 文字」を索引語にするため、LIKE '%語%' と同じ部分一致の
//	    意味を保てる(空白・記号も 1 文字として索引される)。
//	(2) trigram は 3 文字未満の検索語を扱えない(索引語が作れないため常に
//	    0 件になる)。1〜2 文字の語は FTS を使わず従来どおり LIKE で検索する。
//	(3) FTS はあくまで「候補の事前絞り込み」であり、LIKE 条件は残す。
//	    trigram は既定で大文字小文字を畳み込む(case_sensitive 0)ため、
//	    Go 側の NormalizeSearchText とわずかに畳み込み方が違えば FTS が
//	    LIKE より多く拾う可能性がある。LIKE で再判定すれば結果集合は
//	    従来と完全に一致する(取りこぼしは起きない。trigram の畳み込みは
//	    1 文字 → 1 文字の写像なので部分文字列の包含関係が保たれるため)。
//	(4) 結合は CROSS JOIN で FTS 側を外側ループに固定する。通常の JOIN や
//	    id IN (SELECT ...) では、統計情報(ANALYZE の有無)によって
//	    issues 側を外側にする実行計画が選ばれ、全走査に戻ってしまう。
//	(5) 並び順は issues.id ではなく issues_fts.rowid で指定する。両者は
//	    結合条件により同値だが、FTS5 は rowid 昇順の ORDER BY を自前で
//	    消化できるため、並べ替え用の一時 B-Tree(全件展開)を避けられる。

import (
	"strings"
	"unicode/utf8"
)

// ftsMinTermRunes は FTS 索引を使える検索語の最小文字数。
// trigram トークナイザは 3 文字未満の語を索引できない。
const ftsMinTermRunes = 3

// ftsMaxTerms は MATCH 式に載せる語数の上限。
//
// SQLite は式木の深さを 1000 に制限しており(SQLITE_MAX_EXPR_DEPTH)、
// 語数が多いと MATCH 式の解析でエラーになる。事前絞り込みは一部の語だけでも
// 十分効くため、上限を超える場合は語を減らす(AND)か FTS を使わない(OR)。
// 実際の検索で数十語を超える入力は想定していない。
const ftsMaxTerms = 32

// issuesFrom は FTS を使わない場合の FROM 句。
const issuesFrom = `issues`

// issuesFTSFrom は FTS を使う場合の FROM 句。
// CROSS JOIN は SQLite に結合順の入れ替えを禁じる指示で、
// FTS 索引を必ず外側ループにする(実行計画を統計情報に依存させない)。
const issuesFTSFrom = `issues_fts CROSS JOIN issues ON issues.id = issues_fts.rowid`

// issuesOrderBy / issuesFTSOrderBy は課題の並び順(いずれも課題 ID 昇順)。
const (
	issuesOrderBy    = `issues.id`
	issuesFTSOrderBy = `issues_fts.rowid`
)

// ftsMatchCond は FTS5 の全文検索条件。
const ftsMatchCond = `issues_fts MATCH ?`

// ftsMatchExpr は検索語から FTS5 の MATCH 式を組み立てる。
//
// 各語はダブルクォートで囲んで文字列(フレーズ)にする。こうすると FTS5 の
// クエリ構文(AND / OR / NOT / * / ( ) / : など)を語の中身として解釈させずに
// 済む。語に含まれるダブルクォート自体は 2 つ重ねてエスケープする。
//
// or が真なら語を OR で、偽なら AND で連結する。
//
// 索引を使えない語(ftsIndexableTerm を満たさない語)や、語数が多すぎる場合の
// 扱いはモードで変わる:
//   - AND: どの語も必須条件なので、一部の語だけで絞り込んでも候補の上位集合に
//     なる。LIKE が全語を再判定するため結果は変わらない。使えない語は式から外す。
//   - OR: 外した語だけに一致する行を FTS が拾えず、事前絞り込みが
//     「候補の上位集合」でなくなる(取りこぼす)。1 語でも外れるなら
//     FTS 自体を使わない。
//
// 使える語が 1 つも無ければ ("", false) を返す(呼び出し側は LIKE のみで検索する)。
func ftsMatchExpr(terms []string, or bool) (string, bool) {
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		if !ftsIndexableTerm(t) {
			if or {
				return "", false
			}
			continue
		}
		quoted = append(quoted, ftsQuoteTerm(t))
	}
	if len(quoted) > ftsMaxTerms {
		if or {
			return "", false
		}
		quoted = quoted[:ftsMaxTerms]
	}
	if len(quoted) == 0 {
		return "", false
	}
	op := " AND "
	if or {
		op = " OR "
	}
	return strings.Join(quoted, op), true
}

// ftsIndexableTerm は語を FTS の MATCH 式に載せられるかを判定する。
//
// 除外するもの:
//   - 3 文字未満の語(trigram が索引語を作れず、常に 0 件になる)
//   - NUL(U+0000)を含む語。FTS5 のクエリ解析は文字列を NUL 終端として扱うため、
//     途中で切れて「unterminated string」エラーになる。
//     (索引される本文側の NUL は問題にならない。FTS5 は NUL 以降も索引するのに対し
//     SQLite の LIKE は NUL で打ち切るため、FTS の結果は LIKE の上位集合になる。)
func ftsIndexableTerm(term string) bool {
	if utf8.RuneCountInString(term) < ftsMinTermRunes {
		return false
	}
	return !strings.ContainsRune(term, 0)
}

// ftsQuoteTerm は検索語を FTS5 の文字列リテラル(ダブルクォート囲み)にする。
func ftsQuoteTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}
