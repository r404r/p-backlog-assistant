package export

// bulk_result.go は一括更新・追加の実行結果レポート(xlsx)の出力を担う(高 5)。
//
// 画面の一覧だけでは実行後に「どの行がどうなったか」を追えないため、
// 行番号・処理区分・課題キー・結果・エラー理由を 1 シートに書き出す。
//
// 出力の流儀(StreamWriter・ヘッダ太字・オートフィルタ・ヘッダ行固定)は
// 課題出力(issue.go)・ユーザ出力(user.go)・テンプレート出力(bulk_template.go)と揃える。
// 送信内容(payload)・base_updated は書き出さない(課題本文を含みうるため。設計書 7 節)。

import (
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"
)

// SheetBulkResult は実行結果を書き出すシート。
const SheetBulkResult = "実行結果"

// BulkResultRow は実行結果 1 行分のデータ。
//
// 表示用の文字列(Action・Status)は呼び出し側が解決済みの値を渡す
// (export パッケージは表示・整形のみを責務とし、実行状態の意味は解釈しない)。
type BulkResultRow struct {
	// RowNo は取り込んだ Excel の行番号(ヘッダが 1 行目)。
	RowNo int
	// Action は処理区分(追加 / 更新 / 変更なし 等)。
	Action string
	// IssueKey は更新対象の課題キー(新規追加行は空)。
	IssueKey string
	// ResultIssueID は新規追加で作成された課題 ID(それ以外は 0 = 空セル)。
	ResultIssueID int64
	// Status は行の結果(完了 / 失敗 / 競合 等)。
	Status string
	// ErrorMessage は失敗理由(成功行は空)。
	ErrorMessage string
}

// bulkResultColumn は 1 出力列の定義。
type bulkResultColumn struct {
	header string                      // 1 行目に出力するヘッダ
	value  func(*BulkResultRow) string // セル値の生成
	width  float64                     // 列幅(文字数目安)
}

// bulkResultColumns は実行結果シートの列定義(この並びがそのまま列順になる)。
var bulkResultColumns = []bulkResultColumn{
	{"行番号", func(r *BulkResultRow) string { return fmt.Sprintf("%d", r.RowNo) }, 10},
	{"処理", func(r *BulkResultRow) string { return r.Action }, 12},
	{"issueKey", func(r *BulkResultRow) string { return r.IssueKey }, 14},
	{"作成された課題ID", func(r *BulkResultRow) string { return formatID(r.ResultIssueID) }, 16},
	{"結果", func(r *BulkResultRow) string { return r.Status }, 12},
	{"エラー", func(r *BulkResultRow) string { return r.ErrorMessage }, 60},
}

// BulkResultHeaders は実行結果シートのヘッダを列順に返す
// (呼び出し側が返り値を書き換えても内部定義には影響しない)。
func BulkResultHeaders() []string {
	out := make([]string, len(bulkResultColumns))
	for i, c := range bulkResultColumns {
		out[i] = c.header
	}
	return out
}

// ExportBulkResultToFile は実行結果を xlsx として path に書き出す。
// 一時ファイルへ書き切ってから置換するため、失敗しても出力先の既存ファイルは
// そのまま残り、書きかけのファイルも残らない(writeFileAtomic。R5)。
func ExportBulkResultToFile(path string, rows []BulkResultRow) error {
	return writeFileAtomic(path, func(w io.Writer) error {
		return ExportBulkResult(w, rows)
	})
}

// ExportBulkResult は実行結果を xlsx として w に書き出す。
// rows が空でもヘッダのみのシートを出力する(実行対象が 0 件だった記録になる)。
func ExportBulkResult(w io.Writer, rows []BulkResultRow) error {
	f := excelize.NewFile()
	defer f.Close()

	// 既定シートを実行結果シートにリネームし、情報シートを 2 枚目として追加する。
	if err := f.SetSheetName(f.GetSheetName(0), SheetBulkResult); err != nil {
		return err
	}
	if err := writeInfoSheet(f, len(rows)); err != nil {
		return err
	}
	if err := writeBulkResultSheet(f, rows); err != nil {
		return err
	}
	return f.Write(w)
}

// writeBulkResultSheet は実行結果シートを StreamWriter で書き出す。
func writeBulkResultSheet(f *excelize.File, rows []BulkResultRow) error {
	sw, err := f.NewStreamWriter(SheetBulkResult)
	if err != nil {
		return err
	}

	colCount := len(bulkResultColumns)

	// 列幅・固定行は最初の SetRow より前に設定する(StreamWriter の制約)。
	for i, c := range bulkResultColumns {
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

	// 1 行目: ヘッダ。
	header := make([]any, 0, colCount)
	for _, c := range bulkResultColumns {
		header = append(header, excelize.Cell{StyleID: headerStyle, Value: c.header})
	}
	if err := sw.SetRow("A1", header); err != nil {
		return err
	}

	// 2 行目以降: 実行結果。
	values := make([]any, colCount)
	for n := range rows {
		row := &rows[n]
		for i, c := range bulkResultColumns {
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
	if err := f.AutoFilter(SheetBulkResult, fmt.Sprintf("A1:%s1", lastCol), nil); err != nil {
		return err
	}
	return sw.Flush()
}
