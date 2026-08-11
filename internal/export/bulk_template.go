package export

// bulk_template.go は一括更新・追加テンプレート(xlsx)の出力を担う。
//
// 設計書 5 節「Excel 入出力仕様 / 入力(一括更新・追加)」準拠:
//   - issueKey 列が空なら新規追加、値ありなら更新。
//   - 更新は値が入っているセルのみ反映(空セルは「変更しない」)。クリアは #CLEAR#。
//   - 状態・種別・優先度・担当者は名前列を正とし、ID 列は参考情報
//     (利用者は「マスタ」シートの候補から名前で選ぶ。取り込み側は名前列を常に
//     優先し、食い違う ID 列は警告して無視する)。
//   - 競合検知用に基準 updated(base_updated)を埋め込む。
//
// シート構成は「一括更新」(記入対象)・「記入方法」(運用ルール + プロジェクト ID)・
// 「マスタ」(選択候補)の 3 枚。名前列にはマスタシートを参照するデータ入力規則
// (ドロップダウン)を設定し、生の ID を知らなくても編集できるようにする。
//
// ヘッダ名と列順は取り込み側(internal/bulk のパーサ)が列を解決するための契約であり、
// 変更すると取り込みが壊れる。bulk_template_test.go で完全一致を固定している。
//
// 出力の流儀(StreamWriter・ヘッダ太字・オートフィルタ・ヘッダ行固定)は
// 課題出力(issue.go)・ユーザ出力(user.go)と揃える。
// 実データ(スペース名・プロジェクト名等)は書き出さない(設計書 7 節)。

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// シート名。
const (
	// SheetBulkTemplate は記入対象のデータシート。
	SheetBulkTemplate = "一括更新"
	// SheetBulkGuide は記入方法(運用ルール)を書くシート。
	SheetBulkGuide = "記入方法"
	// SheetBulkMaster は種別・状態・優先度・担当者の候補を並べるシート。
	// データシートのドロップダウン(データ入力規則)の参照先になる。
	SheetBulkMaster = "マスタ"
)

// bulkValidationRows はドロップダウンを設定するデータ行数。
// 既存課題より下の空行にも新規追加を書けるよう、行数に余裕を持たせる。
const bulkValidationRows = 1000

// ClearMarker はフィールドのクリアを指示する専用値(設計書 5 節)。
// 空セルは「変更しない」を意味するため、明示的なクリアはこの値で指定する。
const ClearMarker = "#CLEAR#"

// BulkProjectIDLabel は「記入方法」シートに埋め込むプロジェクト ID 行の見出し(高 2)。
//
// テンプレートがどのプロジェクト用に出力されたかを機械可読な形で持たせ、
// 取り込み時に UI で選択したプロジェクトと一致するかを検証する
// (別プロジェクトのテンプレートを取り込んで誤って書き込むことを防ぐ)。
// この文字列は取り込み側(internal/bulk のパーサ)との契約であり、変更すると
// 検証が働かなくなる。
const BulkProjectIDLabel = "プロジェクトID"

// BulkTemplateRow はテンプレート 1 行分のデータ。
//
// export パッケージは表示・整形のみを責務とするため、store へ依存せず独自に定義する。
// ID 系フィールドが 0 の場合は「未設定」として空セルを出力する
// (0 を書くと取り込み側が「ID 0 の値が指定された」と誤読するため)。
type BulkTemplateRow struct {
	// IssueKey は課題キー。空文字なら新規追加行のひな形になる。
	IssueKey string
	// Summary は件名。
	Summary string
	// IssueTypeID / IssueTypeName は種別。名前列が正、ID 列は参考(取り込み時は名前列を優先)。
	IssueTypeID   int64
	IssueTypeName string
	// StatusID / StatusName は状態。名前列が正、ID 列は参考。
	StatusID   int64
	StatusName string
	// PriorityID / PriorityName は優先度。名前列が正、ID 列は参考。
	PriorityID   int64
	PriorityName string
	// AssigneeID / AssigneeName は担当者。名前列(表示名 (ID) 形式)が正、ID 列は参考。
	AssigneeID   int64
	AssigneeName string
	// DueDate は期限(RFC3339 または YYYY-MM-DD)。出力は YYYY-MM-DD に整形する。
	DueDate string
	// Description は詳細本文。
	Description string
	// BaseUpdated は競合検知の基準となる更新日時(取得時の生値)。整形せずそのまま出力する。
	BaseUpdated string
}

// NamedRef は ID と表示名の組(種別・状態・優先度・担当者の候補 1 件)。
//
// export パッケージは表示・整形のみを責務とするため、bulk・store へ依存せず
// 独自に定義する(呼び出し側がマスタから詰め替える)。
type NamedRef struct {
	ID   int64
	Name string
}

// BulkTemplateMasters はテンプレートの「マスタ」シートに載せる選択候補。
// 空のフィールドはその列を見出しだけにし、ドロップダウンも設定しない。
type BulkTemplateMasters struct {
	IssueTypes []NamedRef
	Statuses   []NamedRef
	Priorities []NamedRef
	Assignees  []NamedRef
}

// AssigneeLabel は担当者セルの表記「表示名 (ID)」を作る。
//
// 担当者は同名ユーザが存在しうるため、名前だけでは一意に決められない。
// 表示名の後ろに ID を添えることで、利用者は名前で選びながら
// 取り込み側は ID で確実に解決できる。
func AssigneeLabel(name string, id int64) string {
	if id == 0 {
		return name
	}
	if name == "" {
		return "(" + strconv.FormatInt(id, 10) + ")"
	}
	return name + " (" + strconv.FormatInt(id, 10) + ")"
}

// assigneeLabelPattern は「表示名 (ID)」の末尾の ID 部分。
// 手入力・IME 変換で全角括弧になる場合があるため、どちらも受け付ける。
var assigneeLabelPattern = regexp.MustCompile(`[(（]\s*(\d+)\s*[)）]$`)

// ParseAssigneeLabel は「表示名 (ID)」から ID を取り出す。
// この表記でない(名前だけの)場合は ok = false を返す。
func ParseAssigneeLabel(s string) (id int64, ok bool) {
	m := assigneeLabelPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// bulkColumn は 1 出力列の定義。
type bulkColumn struct {
	header string                        // 1 行目に出力するヘッダ(取り込み契約)
	value  func(*BulkTemplateRow) string // セル値の生成
	width  float64                       // 列幅(文字数目安)
}

// bulkColumns は一括更新テンプレートの列定義(この並びがそのまま列順になる)。
// ヘッダ名・順序は取り込み側との契約のため、変更時は取り込みパーサも同時に更新する。
var bulkColumns = []bulkColumn{
	{"issueKey", func(r *BulkTemplateRow) string { return r.IssueKey }, 14},
	{"件名", func(r *BulkTemplateRow) string { return r.Summary }, 48},
	{"種別ID", func(r *BulkTemplateRow) string { return formatID(r.IssueTypeID) }, 10},
	{"種別名", func(r *BulkTemplateRow) string { return r.IssueTypeName }, 14},
	{"状態ID", func(r *BulkTemplateRow) string { return formatID(r.StatusID) }, 10},
	{"状態名", func(r *BulkTemplateRow) string { return r.StatusName }, 12},
	{"優先度ID", func(r *BulkTemplateRow) string { return formatID(r.PriorityID) }, 10},
	{"優先度名", func(r *BulkTemplateRow) string { return r.PriorityName }, 10},
	{"担当者ID", func(r *BulkTemplateRow) string { return formatID(r.AssigneeID) }, 12},
	{"担当者名", func(r *BulkTemplateRow) string { return AssigneeLabel(r.AssigneeName, r.AssigneeID) }, 24},
	{"期限", func(r *BulkTemplateRow) string { return formatDate(r.DueDate) }, 12},
	{"詳細", func(r *BulkTemplateRow) string { return r.Description }, 60},
	{BaseUpdatedHeader, func(r *BulkTemplateRow) string { return r.BaseUpdated }, 22},
}

// bulkMasterColumn は「マスタ」シートの 1 列。
// target はドロップダウンを設定するデータシートの列ヘッダ(bulkColumns の header)。
type bulkMasterColumn struct {
	header string
	target string
	width  float64
	values func(BulkTemplateMasters) []string
}

// bulkMasterColumns はマスタシートの列定義(この並びがそのまま列順になる)。
var bulkMasterColumns = []bulkMasterColumn{
	{"種別", "種別名", 20, func(m BulkTemplateMasters) []string { return refNames(m.IssueTypes) }},
	{"状態", "状態名", 16, func(m BulkTemplateMasters) []string { return refNames(m.Statuses) }},
	{"優先度", "優先度名", 12, func(m BulkTemplateMasters) []string { return refNames(m.Priorities) }},
	{"担当者", "担当者名", 28, func(m BulkTemplateMasters) []string { return assigneeLabels(m.Assignees) }},
}

// refNames は候補の表示名を並べる(種別・状態・優先度)。
func refNames(refs []NamedRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Name != "" {
			out = append(out, r.Name)
		}
	}
	return out
}

// assigneeLabels は担当者候補を「表示名 (ID)」形式で並べる(同名対策)。
func assigneeLabels(refs []NamedRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if label := AssigneeLabel(r.Name, r.ID); label != "" {
			out = append(out, label)
		}
	}
	return out
}

// bulkColumnNumber はデータシートの列番号(1 始まり)をヘッダ名から求める。
// 見つからない場合は 0(ドロップダウンを設定しない)。
func bulkColumnNumber(header string) int {
	for i, c := range bulkColumns {
		if c.header == header {
			return i + 1
		}
	}
	return 0
}

// formatID は ID をセル値にする。0 は「未設定」として空文字にする。
func formatID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// BulkTemplateHeaders はテンプレートのヘッダを列順に返す
// (取り込み側が列を解決するための定義。呼び出し側が書き換えても内部に影響しない)。
func BulkTemplateHeaders() []string {
	out := make([]string, len(bulkColumns))
	for i, c := range bulkColumns {
		out[i] = c.header
	}
	return out
}

// bulkGuideLines は「記入方法」シートに書く説明(見出し, 本文)。
// 取り込み仕様を利用者に伝える唯一の手段のため、取り込み実装と食い違わせない。
var bulkGuideLines = [][2]string{
	{"この Excel について", "「" + SheetBulkTemplate + "」シートに記入して取り込むと、Backlog の課題をまとめて更新・追加できます。"},
	{"新規追加", "issueKey が空の行は新規追加として扱います(既存課題の更新は issueKey に値がある行)。"},
	{"更新の範囲", "値を入れたセルのみ更新します(空欄 = 変更しない)。"},
	{"値のクリア", "担当者・期限・詳細をクリアしたい場合は " + ClearMarker + " と記入してください(空欄はクリアになりません)。(実機検証中の機能です)"},
	{"名前列とID 列", "種別・状態・優先度・担当者は、名前列のセルを選ぶと「" + SheetBulkMaster + "」シートの候補がドロップダウンで表示されます。編集は名前列から選んでください。ID 列は参考情報です(名前列に値がある行は常に名前列が優先されます。ID 列が名前列と食い違う場合は ID 列を無視し、取り込み結果に警告を表示します)。名前列が空の行は ID 列の値を使います。"},
	{"担当者の表記", "担当者名は同名の人を区別するため「表示名 (ID)」形式で出力します。ドロップダウンから選べばこの形式になります。"},
	{BaseUpdatedHeader, BaseUpdatedHeader + " 列は編集しないでください。競合検知(取り込み後にリモートが更新されていないかの確認)に使用します。"},
	{"新規行の必須項目", "件名と種別(種別名または種別ID のどちらか)が必須です(優先度は未入力なら取り込み時に指定する既定値を適用します)。"},
	{"プロジェクト", "対象プロジェクトはテンプレート出力時に固定されます。行ごとにプロジェクトを変えることはできません。先頭の「" + BulkProjectIDLabel + "」行は編集・削除しないでください(取り込み時に選択したプロジェクトと照合します)。"},
	{"行の削除・並べ替え", "不要な行は行ごと削除してください。列の追加・削除・並べ替え、ヘッダ名の変更はしないでください。"},
	{"実行にかかる時間", "1 件ずつ Backlog へ送信するため、1,000 件でおおよそ 8〜10 分かかります。"},
}

// ExportBulkTemplateToFile は一括更新テンプレートを xlsx として path に書き出す。
// 書き出しに失敗した場合、書きかけのファイルは残さない。
//
// projectID は出力対象プロジェクト(「記入方法」シートへ埋め込み、取り込み時の
// プロジェクト一致検証に使う。0 以下なら埋め込まない)。
// masters は「マスタ」シートに載せる選択候補(空でも出力できる)。
func ExportBulkTemplateToFile(path string, projectID int64, rows []BulkTemplateRow, masters BulkTemplateMasters) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := ExportBulkTemplate(f, projectID, rows, masters); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// ExportBulkTemplate は一括更新テンプレートを xlsx として w に書き出す。
// rows が空でも「記入方法」付きの空テンプレート(新規追加専用)として出力する。
func ExportBulkTemplate(w io.Writer, projectID int64, rows []BulkTemplateRow, masters BulkTemplateMasters) error {
	f := excelize.NewFile()
	defer f.Close()

	// 既定シートをデータシートにリネームし、記入方法・マスタを追加する。
	// マスタシートはデータ入力規則の参照先のため、データシートより先に用意する。
	if err := f.SetSheetName(f.GetSheetName(0), SheetBulkTemplate); err != nil {
		return err
	}
	if err := writeBulkGuideSheet(f, projectID); err != nil {
		return err
	}
	if err := writeBulkMasterSheet(f, masters); err != nil {
		return err
	}
	if err := writeBulkTemplateSheet(f, rows, masters); err != nil {
		return err
	}
	return f.Write(w)
}

// writeBulkMasterSheet は選択候補を並べた「マスタ」シートを書き出す。
// 1 行目は見出し、2 行目以降が候補(この範囲をドロップダウンが参照する)。
func writeBulkMasterSheet(f *excelize.File, masters BulkTemplateMasters) error {
	if _, err := f.NewSheet(SheetBulkMaster); err != nil {
		return err
	}
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#DDEBF7"}},
	})
	if err != nil {
		return err
	}
	for i, c := range bulkMasterColumns {
		col, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		if err := f.SetColWidth(SheetBulkMaster, col, col, c.width); err != nil {
			return err
		}
		head := fmt.Sprintf("%s1", col)
		if err := f.SetCellValue(SheetBulkMaster, head, c.header); err != nil {
			return err
		}
		if err := f.SetCellStyle(SheetBulkMaster, head, head, headerStyle); err != nil {
			return err
		}
		for j, v := range c.values(masters) {
			// 文字列として書く(「1」等の名前が数値に化けると参照が食い違うため)
			if err := f.SetCellStr(SheetBulkMaster, fmt.Sprintf("%s%d", col, j+2), v); err != nil {
				return err
			}
		}
	}
	return nil
}

// addBulkDropDowns はデータシートの名前列へマスタシート参照のドロップダウン
// (データ入力規則)を設定する。
//
// 候補が 0 件の列には設定しない(参照範囲が作れないため)。
// また入力エラーは表示しない設定にする(#CLEAR# の記入や、名前が変わった
// 古いテンプレートの取り込みまで Excel 側で拒否してしまわないようにする)。
func addBulkDropDowns(f *excelize.File, masters BulkTemplateMasters, dataRows int) error {
	lastRow := bulkValidationRows + 1
	if dataRows+1 > lastRow {
		lastRow = dataRows + 1
	}
	for i, c := range bulkMasterColumns {
		values := c.values(masters)
		if len(values) == 0 {
			continue
		}
		target := bulkColumnNumber(c.target)
		if target == 0 {
			continue
		}
		targetCol, err := excelize.ColumnNumberToName(target)
		if err != nil {
			return err
		}
		masterCol, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			return err
		}
		dv := excelize.NewDataValidation(true)
		dv.SetSqref(fmt.Sprintf("%s2:%s%d", targetCol, targetCol, lastRow))
		dv.SetSqrefDropList(fmt.Sprintf("'%s'!$%s$2:$%s$%d",
			SheetBulkMaster, masterCol, masterCol, len(values)+1))
		if err := f.AddDataValidation(SheetBulkTemplate, dv); err != nil {
			return err
		}
	}
	return nil
}

// writeBulkGuideSheet は記入方法シートを書き出す。
// projectID が正なら、先頭の行に対象プロジェクト ID を数値で埋め込む(高 2)。
func writeBulkGuideSheet(f *excelize.File, projectID int64) error {
	if _, err := f.NewSheet(SheetBulkGuide); err != nil {
		return err
	}
	if err := f.SetColWidth(SheetBulkGuide, "A", "A", 20); err != nil {
		return err
	}
	if err := f.SetColWidth(SheetBulkGuide, "B", "B", 90); err != nil {
		return err
	}
	headerStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	wrapStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"},
	})
	if err != nil {
		return err
	}

	if err := f.SetCellValue(SheetBulkGuide, "A1", "項目"); err != nil {
		return err
	}
	if err := f.SetCellValue(SheetBulkGuide, "B1", "説明"); err != nil {
		return err
	}
	if err := f.SetCellStyle(SheetBulkGuide, "A1", "B1", headerStyle); err != nil {
		return err
	}

	// 先頭のデータ行は対象プロジェクト ID(取り込み側が読み取る機械可読な値)。
	// 値は数値で書き、B 列に置く(高 2)。
	offset := 2
	if projectID > 0 {
		if err := f.SetCellValue(SheetBulkGuide, "A2", BulkProjectIDLabel); err != nil {
			return err
		}
		if err := f.SetCellValue(SheetBulkGuide, "B2", projectID); err != nil {
			return err
		}
		if err := f.SetCellStyle(SheetBulkGuide, "A2", "A2", headerStyle); err != nil {
			return err
		}
		offset++
	}

	for i, line := range bulkGuideLines {
		row := i + offset
		if err := f.SetCellValue(SheetBulkGuide, fmt.Sprintf("A%d", row), line[0]); err != nil {
			return err
		}
		if err := f.SetCellValue(SheetBulkGuide, fmt.Sprintf("B%d", row), line[1]); err != nil {
			return err
		}
		if err := f.SetCellStyle(SheetBulkGuide, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), headerStyle); err != nil {
			return err
		}
		if err := f.SetCellStyle(SheetBulkGuide, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), wrapStyle); err != nil {
			return err
		}
	}
	return nil
}

// writeBulkTemplateSheet はデータシートを StreamWriter で書き出す。
func writeBulkTemplateSheet(f *excelize.File, rows []BulkTemplateRow, masters BulkTemplateMasters) error {
	sw, err := f.NewStreamWriter(SheetBulkTemplate)
	if err != nil {
		return err
	}

	colCount := len(bulkColumns)

	// 列幅・固定行は最初の SetRow より前に設定する(StreamWriter の制約)。
	for i, c := range bulkColumns {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return err
		}
	}
	// ヘッダ行(1 行目)を固定表示にする。
	if err := sw.SetPanes(&excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
		Selection:   []excelize.Selection{{Pane: "bottomLeft", ActiveCell: "A2", SQRef: "A2"}},
	}); err != nil {
		return err
	}

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#DDEBF7"}},
	})
	if err != nil {
		return err
	}

	// 1 行目: ヘッダ(取り込み契約)。
	header := make([]any, 0, colCount)
	for _, c := range bulkColumns {
		header = append(header, excelize.Cell{StyleID: headerStyle, Value: c.header})
	}
	if err := sw.SetRow("A1", header); err != nil {
		return err
	}

	// 2 行目以降: テンプレートデータ。
	values := make([]any, colCount)
	for n := range rows {
		row := &rows[n]
		for i, c := range bulkColumns {
			values[i] = c.value(row)
		}
		cell, err := excelize.CoordinatesToCellName(1, n+2)
		if err != nil {
			return err
		}
		if err := sw.SetRow(cell, values); err != nil {
			return err
		}
	}

	// オートフィルタはヘッダ行に設定する(Flush 前に設定する必要がある)。
	lastCol, err := excelize.ColumnNumberToName(colCount)
	if err != nil {
		return err
	}
	if err := f.AutoFilter(SheetBulkTemplate, fmt.Sprintf("A1:%s1", lastCol), nil); err != nil {
		return err
	}
	// データ入力規則もオートフィルタと同じく Flush 前に設定する
	// (Flush 後はシートが書き出し済みになり、追加が反映されない)。
	if err := addBulkDropDowns(f, masters, len(rows)); err != nil {
		return err
	}
	return sw.Flush()
}
