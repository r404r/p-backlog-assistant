package export

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// wantBulkResultHeaders は実行結果シートのヘッダ(固定順)。
var wantBulkResultHeaders = []string{
	"行番号", "処理", "issueKey", "作成された課題ID", "結果", "エラー",
}

// sampleBulkResultRows はテスト用の実行結果(実データは含めない)。
func sampleBulkResultRows() []BulkResultRow {
	return []BulkResultRow{
		{RowNo: 2, Action: "更新", IssueKey: "EX-1", Status: "完了"},
		{RowNo: 3, Action: "追加", ResultIssueID: 1001, Status: "完了"},
		{RowNo: 4, Action: "更新", IssueKey: "EX-2", Status: "失敗", ErrorMessage: "権限がありません"},
	}
}

func exportBulkResultToTempFile(t *testing.T, rows []BulkResultRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "result.xlsx")
	if err := ExportBulkResultToFile(path, rows); err != nil {
		t.Fatalf("ExportBulkResultToFile: %v", err)
	}
	return path
}

// TestExportBulkResult_HeadersAndValues はヘッダ・値の並びを固定する。
func TestExportBulkResult_HeadersAndValues(t *testing.T) {
	path := exportBulkResultToTempFile(t, sampleBulkResultRows())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("行数 = %d, want 4(ヘッダ + 3 行)", len(rows))
	}
	if !equalStrings(rows[0], wantBulkResultHeaders) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], wantBulkResultHeaders)
	}
	if !equalStrings(BulkResultHeaders(), wantBulkResultHeaders) {
		t.Errorf("BulkResultHeaders() = %v, want %v", BulkResultHeaders(), wantBulkResultHeaders)
	}

	// 更新行: 作成された課題 ID は空セル(「ID 0 の課題」と誤読させない)
	want1 := []string{"2", "更新", "EX-1", "", "完了"}
	if !equalStrings(rows[1], want1) {
		t.Errorf("2 行目 = %v, want %v", rows[1], want1)
	}
	// 新規追加行: 課題キーは空、作成された課題 ID を出す
	want2 := []string{"3", "追加", "", "1001", "完了"}
	if !equalStrings(rows[2], want2) {
		t.Errorf("3 行目 = %v, want %v", rows[2], want2)
	}
	want3 := []string{"4", "更新", "EX-2", "", "失敗", "権限がありません"}
	if !equalStrings(rows[3], want3) {
		t.Errorf("4 行目 = %v, want %v", rows[3], want3)
	}
}

func TestExportBulkResult_Sheets(t *testing.T) {
	path := exportBulkResultToTempFile(t, sampleBulkResultRows())
	f := openExported(t, path)

	sheets := f.GetSheetList()
	if len(sheets) != 2 || sheets[0] != SheetBulkResult || sheets[1] != SheetInfo {
		t.Fatalf("シート一覧 = %v, want [%s %s]", sheets, SheetBulkResult, SheetInfo)
	}
}

// TestExportBulkResult_EmptyRows は 0 件でもヘッダのみのシートを出力することを確認する。
func TestExportBulkResult_EmptyRows(t *testing.T) {
	path := exportBulkResultToTempFile(t, nil)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数 = %d, want 1(ヘッダのみ)", len(rows))
	}
}

func TestExportBulkResult_AutoFilterAndFreezePane(t *testing.T) {
	path := exportBulkResultToTempFile(t, sampleBulkResultRows())

	if got := autoFilterRef(t, path); got != "A1:F1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:F1\"", got)
	}
	doc := readZipEntry(t, path, "xl/worksheets/sheet1.xml")
	if !strings.Contains(doc, `state="frozen"`) || !strings.Contains(doc, `ySplit="1"`) {
		t.Errorf("ヘッダ行が固定されていない: %.400s", doc)
	}
}

func TestExportBulkResult_WriterOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportBulkResult(&buf, sampleBulkResultRows()); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows(SheetBulkResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Errorf("行数 = %d, want 4", len(rows))
	}
}

// TestExportBulkResultToFile_InvalidPath は書き出し失敗時に例外なく失敗することを確認する。
func TestExportBulkResultToFile_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	if err := ExportBulkResultToFile(dir, sampleBulkResultRows()); err == nil {
		t.Fatal("ディレクトリへの書き出しが成功してしまった")
	}
}
