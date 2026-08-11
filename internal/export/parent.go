package export

// parent.go は親課題(CF5)の表記を担う。
//
// 親課題は Excel 上では「課題キー」で扱う(利用者が見て分かる唯一の識別子)。
// ただしローカル DB に無い親(未同期・別プロジェクト)はキーを引き当てられない。
// その場合に空欄・数値だけを書くと往復(抽出 → 記入 → 取り込み)で情報が落ちる
// ため、課題キーと取り違えようのない「ID:<数値>」形式で出力し、取り込み側も
// 同じ形式を受理する(計画 CF5)。
//
// この表記は取り込み側(internal/bulk)との契約であり、変更すると
// 抽出結果をそのまま取り込む運用が壊れる。

import (
	"strconv"
	"strings"
)

// ParentIssueKeyColumn は親課題キー列の列キー(抽出の列選択で指定する)。
const ParentIssueKeyColumn = "parentIssueKey"

// ParentIssueKeyHeader は親課題キー列のヘッダ(抽出・一括更新テンプレート共通)。
// 一括更新の取り込みはこのヘッダで列を解決する。
const ParentIssueKeyHeader = "親課題キー"

// ParentIssueIDPrefix はローカルに無い親課題を表す接頭辞(「ID:123」)。
const ParentIssueIDPrefix = "ID:"

// FormatParentIssueRef は親課題 ID をセル値へ整形する。
//
//	0             → 空文字(親なし)
//	keys に有る   → 課題キー
//	keys に無い   → ID:<数値>(未同期・別プロジェクトの親)
func FormatParentIssueRef(parentID int64, keys map[int64]string) string {
	if parentID <= 0 {
		return ""
	}
	if key := keys[parentID]; key != "" {
		return key
	}
	return ParentIssueIDPrefix + strconv.FormatInt(parentID, 10)
}

// ParseParentIssueIDRef は「ID:<数値>」形式から課題 ID を取り出す。
//
// matched は接頭辞が「ID:」だったことだけを表す。値が数値でない・0 以下・
// 桁あふれの場合は matched = true / id = 0 を返し、呼び出し側が書式エラーとして
// 扱えるようにする(課題キーとして再検索させないため。ID:0 のような入力が
// 「そんな課題キーは無い」という的外れなエラーになるのを防ぐ)。
//
// 全角・大文字小文字の揺れは normalizeCustomFieldName(NFKC + ケースフォールド)で
// 吸収する。手入力・IME 変換で「ＩＤ:123」になっても受理するため。
func ParseParentIssueIDRef(s string) (id int64, matched bool) {
	norm := normalizeCustomFieldName(s)
	rest, found := strings.CutPrefix(norm, normalizeCustomFieldName(ParentIssueIDPrefix))
	if !found {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil || n <= 0 {
		return 0, true // 形式は「ID:」だが値が不正
	}
	return n, true
}
