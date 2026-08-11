package bulk

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// テンプレートの列キー(内部表現)。ヘッダ名から解決するため、
// 列の並び順には依存しない(設計書 5 節)。
const (
	colIssueKey       = "issueKey"
	colSummary        = "summary"
	colIssueTypeID    = "issueTypeId"
	colIssueTypeName  = "issueTypeName"
	colStatusID       = "statusId"
	colStatusName     = "statusName"
	colPriorityID     = "priorityId"
	colPriorityName   = "priorityName"
	colAssigneeID     = "assigneeId"
	colAssigneeName   = "assigneeName"
	colDueDate        = "dueDate"
	colDescription    = "description"
	colParentIssueKey = "parentIssueKey"
	colBaseUpdated    = "baseUpdated"
)

// headerAliases はヘッダ名(正規化済み)→ 列キー。
// テンプレートの正式名に加え、課題抽出 Excel のヘッダ(キー・状態・担当者 等)も
// 受け付ける(抽出結果へ base_updated を付けたファイルをそのまま取り込めるようにする)。
// 正規化は normalizeHeader(NFKC + ケースフォールド)で行うため、
// ここのキーは小文字・半角で書く。
var headerAliases = map[string]string{
	"issuekey": colIssueKey, "キー": colIssueKey, "課題キー": colIssueKey,
	"件名": colSummary, "summary": colSummary,
	"種別id": colIssueTypeID, "種別名": colIssueTypeName, "種別": colIssueTypeName,
	"状態id": colStatusID, "状態名": colStatusName, "状態": colStatusName,
	"優先度id": colPriorityID, "優先度名": colPriorityName, "優先度": colPriorityName,
	"担当者id": colAssigneeID, "担当者名": colAssigneeName, "担当者": colAssigneeName,
	"期限": colDueDate, "詳細": colDescription,
	// 親課題(CF5)。抽出出力・テンプレートとも「親課題キー」を使う
	"親課題キー": colParentIssueKey, "親課題": colParentIssueKey,
	"base_updated": colBaseUpdated,
}

// idColumnLabels / nameColumnLabels は列キー → テンプレート上の見出し
// (エラーメッセージでどのセルを直せばよいかを示すため)。
var idColumnLabels = map[string]string{
	colIssueTypeID: "種別ID",
	colStatusID:    "状態ID",
	colPriorityID:  "優先度ID",
	colAssigneeID:  "担当者ID",
}

var nameColumnLabels = map[string]string{
	colIssueTypeName: "種別名",
	colStatusName:    "状態名",
	colPriorityName:  "優先度名",
	colAssigneeName:  "担当者名",
}

// customHeaderPrefix は正規化済みのカスタム属性ヘッダ接頭辞
// (ヘッダ側も normalizeHeader で正規化してから比較する)。
var customHeaderPrefix = normalizeHeader(export.BulkCustomColumnPrefix)

// customColKey はカスタム属性の列キー(定義 ID から作る内部表現)。
// 固定列のキー(issueKey 等)と衝突しないよう接頭辞を付ける。
func customColKey(defID int64) string {
	return "customField:" + strconv.FormatInt(defID, 10)
}

// rawRow は Excel の 1 行(列キー → トリム済みセル値)。
type rawRow struct {
	rowNo int // Excel の行番号(ヘッダが 1 行目、データは 2 行目から)
	cells map[string]string
}

// cell は列の値を返す(列が無ければ空文字)。
func (r rawRow) cell(key string) string { return r.cells[key] }

// has は値が入っているかを返す。
func (r rawRow) has(key string) bool { return r.cells[key] != "" }

// sheetData は取り込んだシートの内容。
type sheetData struct {
	rows    []rawRow
	columns map[string]bool // 存在した列キー
	// projectID は「記入方法」シートに埋め込まれた対象プロジェクト ID(高 2)。
	// 0 はメタ情報が無い(旧テンプレート・手作りファイル)ことを表す。
	projectID int64
}

// normalizeHeader はヘッダ名を比較用に正規化する
// (全角・半角、大文字・小文字の違いを吸収する)。
func normalizeHeader(s string) string {
	return store.NormalizeSearchText(strings.TrimSpace(s))
}

// parseWorkbook は xlsx を読み、テンプレート列を解決した行を返す。
//
// シートは「issueKey 列を持つ最初のシート」を対象にする
// (抽出 Excel の「情報」シートのような付随シートを自然に読み飛ばせる)。
// defs はプロジェクトのカスタム属性定義。「属性:{定義名}」列の解決に使う。
func parseWorkbook(path string, defs []customfield.Def) (*sheetData, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("Excel ファイルを開けません: %w", err)
	}
	defer func() { _ = f.Close() }()

	// テンプレートに埋め込まれた対象プロジェクト ID(高 2)。
	// 取得できない場合は 0(メタ情報無し)として続行し、判断は呼び出し側に委ねる。
	projectID, err := readTemplateProjectID(f)
	if err != nil {
		return nil, err
	}
	// カスタム属性列は定義名で解決するため、名前の索引を先に作る
	// (定義名が空・重複しているプロジェクトはここで取り込みを止める)。
	// 正規化はヘッダ照合と同じ normalizeHeader を使う。
	customByName, err := customfield.DefsByName(defs, normalizeHeader)
	if err != nil {
		return nil, err
	}

	for _, name := range f.GetSheetList() {
		rows, err := f.GetRows(name)
		if err != nil {
			return nil, fmt.Errorf("シート %q を読み取れません: %w", name, err)
		}
		if len(rows) == 0 {
			continue
		}
		colOf, columns, err := mapHeaders(rows[0], customByName)
		if err != nil {
			return nil, err
		}
		if !columns[colIssueKey] {
			continue // 対象シートではない
		}
		data := &sheetData{columns: columns, projectID: projectID}
		for i, row := range rows[1:] {
			r := rawRow{rowNo: i + 2, cells: map[string]string{}}
			empty := true
			for idx, key := range colOf {
				if idx >= len(row) {
					continue
				}
				v := strings.TrimSpace(row[idx])
				if v == "" {
					continue
				}
				r.cells[key] = v
				empty = false
			}
			if empty {
				continue // 空行は無視する
			}
			data.rows = append(data.rows, r)
		}
		if len(data.rows) == 0 {
			return nil, errors.New("データ行がありません(ヘッダ行のみのファイルです)")
		}
		return data, nil
	}
	return nil, errors.New("issueKey 列が見つかりません(一括更新テンプレートの Excel を指定してください)")
}

// readTemplateProjectID は「記入方法」シートに埋め込まれた対象プロジェクト ID を読む(高 2)。
//
// 見出し(export.BulkProjectIDLabel)が A 列にある行の B 列を数値として解釈する。
// シートや行が無い場合は 0 を返す(旧テンプレート・抽出結果を加工したファイル)。
// 行はあるのに値が数値でない場合は、誤ったプロジェクトへの書き込みを防ぐため
// エラーにする(黙って「メタ情報無し」として扱わない)。
func readTemplateProjectID(f *excelize.File) (int64, error) {
	for _, name := range f.GetSheetList() {
		if name != export.SheetBulkGuide {
			continue
		}
		rows, err := f.GetRows(name)
		if err != nil {
			return 0, fmt.Errorf("シート %q を読み取れません: %w", name, err)
		}
		for _, row := range rows {
			if len(row) == 0 || normalizeHeader(row[0]) != normalizeHeader(export.BulkProjectIDLabel) {
				continue
			}
			if len(row) < 2 || strings.TrimSpace(row[1]) == "" {
				return 0, fmt.Errorf("「%s」シートの %s に値がありません。テンプレートから出力した Excel を使用してください",
					export.SheetBulkGuide, export.BulkProjectIDLabel)
			}
			id, perr := strconv.ParseInt(strings.TrimSpace(row[1]), 10, 64)
			if perr != nil || id <= 0 {
				return 0, fmt.Errorf("「%s」シートの %s が不正です(%q)。テンプレートから出力した Excel を使用してください",
					export.SheetBulkGuide, export.BulkProjectIDLabel, strings.TrimSpace(row[1]))
			}
			return id, nil
		}
	}
	return 0, nil
}

// mapHeaders はヘッダ行から「列インデックス → 列キー」と存在する列の集合を作る。
// 同じ列キーに対応するヘッダが複数あるファイルは、どちらを使うか決められないためエラーにする。
//
// 「属性:{定義名}」のヘッダはカスタム属性列として定義名で解決する。
// 定義に無い名前はエラーにする(黙って無視すると、記入した内容が反映されない
// まま実行され、利用者は更新されたと誤解する)。
func mapHeaders(header []string, customByName map[string]customfield.Def) (map[int]string, map[string]bool, error) {
	colOf := map[int]string{}
	columns := map[string]bool{}
	seen := map[string]string{} // 列キー → 最初に見つかったヘッダ名
	for i, h := range header {
		key, err := headerColumnKey(h, customByName)
		if err != nil {
			return nil, nil, err
		}
		if key == "" {
			continue // 未知の列は無視する(作成日時 等)
		}
		if first, dup := seen[key]; dup {
			return nil, nil, fmt.Errorf("同じ意味の列が重複しています(%q と %q)", first, strings.TrimSpace(h))
		}
		seen[key] = strings.TrimSpace(h)
		colOf[i] = key
		columns[key] = true
	}
	return colOf, columns, nil
}

// headerColumnKey はヘッダ 1 つを列キーへ解決する(該当しない列は空文字)。
func headerColumnKey(header string, customByName map[string]customfield.Def) (string, error) {
	norm := normalizeHeader(header)
	if name, ok := strings.CutPrefix(norm, customHeaderPrefix); ok {
		def, found := customByName[strings.TrimSpace(name)]
		if !found {
			return "", fmt.Errorf("カスタム属性「%s」の定義が見つかりません(テンプレートを出力し直してください)",
				strings.TrimPrefix(strings.TrimSpace(header), export.BulkCustomColumnPrefix))
		}
		return customColKey(def.ID), nil
	}
	return headerAliases[norm], nil
}
