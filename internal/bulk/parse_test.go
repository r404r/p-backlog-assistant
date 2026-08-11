package bulk

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
)

// TestFixedColumnCount_MatchesHeaderAliases は、列数上限の計算に使う固定列数が
// 実際の固定列(headerAliases が解決する列キー)と一致することを確認する。
// 列を増やしたときに上限の見積もりだけが取り残されるのを防ぐ。
func TestFixedColumnCount_MatchesHeaderAliases(t *testing.T) {
	keys := map[string]bool{}
	for _, key := range headerAliases {
		keys[key] = true
	}
	if len(keys) != fixedColumnCount {
		t.Errorf("固定列数 = %d, headerAliases の列キー数 = %d(定数を更新してください)",
			fixedColumnCount, len(keys))
	}
}

// TestParseWorkbook_WithinLimits は上限内のファイルが従来どおり読めること、
// および Excel の行番号(空行を挟んでもずれない)が保たれることを確認する。
func TestParseWorkbook_WithinLimits(t *testing.T) {
	// 3 行目は空行(取り込み対象外)。4 行目の行番号が 4 のままであること
	path := writeXLSX(t, templateHeaders, [][]string{
		{"EXA-1", "件名1"},
		{"", ""},
		{"EXA-3", "件名3"},
	})

	data, err := parseWorkbookWithLimits(path, nil, defaultParseLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(data.rows) != 2 {
		t.Fatalf("行数 = %d, want 2", len(data.rows))
	}
	if data.rows[0].rowNo != 2 || data.rows[0].cell(colIssueKey) != "EXA-1" {
		t.Errorf("rows[0] = %+v", data.rows[0])
	}
	if data.rows[1].rowNo != 4 || data.rows[1].cell(colIssueKey) != "EXA-3" {
		t.Errorf("rows[1] = %+v", data.rows[1])
	}
}

// TestParseWorkbook_RejectsTooManyDataRows はデータ行数の上限超過を確認する。
func TestParseWorkbook_RejectsTooManyDataRows(t *testing.T) {
	path := writeXLSX(t, templateHeaders, [][]string{
		{"EXA-1"}, {"EXA-2"}, {"EXA-3"},
	})
	lim := defaultParseLimits()
	lim.maxDataRows = 2

	_, err := parseWorkbookWithLimits(path, nil, lim)
	if err == nil {
		t.Fatal("上限超過がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("エラーメッセージに上限値が含まれていない: %v", err)
	}
}

// TestParseWorkbook_RejectsLargeFile はファイルサイズの上限超過を確認する。
func TestParseWorkbook_RejectsLargeFile(t *testing.T) {
	path := writeXLSX(t, templateHeaders, [][]string{{"EXA-1"}})
	lim := defaultParseLimits()
	lim.maxFileSize = 512 // 実ファイルより小さい値を注入する

	_, err := parseWorkbookWithLimits(path, nil, lim)
	if err == nil {
		t.Fatal("上限超過がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "大きすぎます") || !strings.Contains(err.Error(), "512") {
		t.Errorf("エラーメッセージが不十分: %v", err)
	}
}

// TestParseWorkbook_RejectsTooManyColumns は列数の上限超過を確認する。
// 上限は「固定列 + カスタム属性定義数 + 余裕」で決まる。
func TestParseWorkbook_RejectsTooManyColumns(t *testing.T) {
	headers := append(append([]string{}, templateHeaders...), "作成日時", "更新日時")
	path := writeXLSX(t, headers, [][]string{{"EXA-1"}})
	lim := defaultParseLimits()
	lim.extraColumns = 0 // 固定列 14 のみを許す

	_, err := parseWorkbookWithLimits(path, nil, lim)
	if err == nil {
		t.Fatal("上限超過がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "列が多すぎます") {
		t.Errorf("エラーメッセージが不十分: %v", err)
	}

	// カスタム属性の定義がある分だけ上限が広がること
	defs := make([]customfield.Def, 2)
	for i := range defs {
		defs[i] = customfield.Def{ID: int64(i + 1), Name: "属性" + string(rune('A'+i)), TypeID: 1}
	}
	if _, err := parseWorkbookWithLimits(path, defs, lim); err != nil {
		t.Errorf("上限内のはずがエラー: %v", err)
	}
}

// TestParseWorkbook_RejectsTooManyColumnsInDataRow は、ヘッダ行が上限内でも
// データ行が上限を超えていればエラーになることを確認する(中 4)。
// ヘッダだけを見ていると、データ行に大量の列を詰めたファイルを素通ししてしまう。
func TestParseWorkbook_RejectsTooManyColumnsInDataRow(t *testing.T) {
	wide := make([]string, 20)
	for i := range wide {
		wide[i] = "x"
	}
	wide[0] = "EXA-1"
	path := writeXLSX(t, templateHeaders, [][]string{wide})
	lim := defaultParseLimits()
	lim.extraColumns = 0 // 上限 = 固定列 14(ヘッダの 13 列は上限内)

	_, err := parseWorkbookWithLimits(path, nil, lim)
	if err == nil {
		t.Fatal("上限超過がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "列が多すぎます") || !strings.Contains(err.Error(), "2 行目") {
		t.Errorf("エラーメッセージが不十分(行番号を含めること): %v", err)
	}
}

// TestParseWorkbook_CountsPhysicallyNonEmptyRows は、行数上限が
// 「取り込み対象の行」ではなく「物理的に空でない行」に対して働くことを
// 確認する(中 4)。既知列が空でも他の列に値がある行は読み込みの負荷が
// かかるため、上限のカウント対象に含める。
func TestParseWorkbook_CountsPhysicallyNonEmptyRows(t *testing.T) {
	headers := append(append([]string{}, templateHeaders...), "作成日時")
	junk := make([]string, len(headers))
	junk[len(headers)-1] = "2026-08-01T00:00:00Z" // 既知列は空、未知列だけ値あり
	normal := make([]string, len(headers))
	normal[0] = "EXA-1"
	rows := [][]string{junk, junk, normal}

	lim := defaultParseLimits()
	lim.maxDataRows = 2
	if _, err := parseWorkbookWithLimits(writeXLSX(t, headers, rows), nil, lim); err == nil {
		t.Error("物理的な非空行 3 行が上限 2 行に引っかからなかった")
	}

	// 上限内なら、取り込み対象は既知列に値がある 1 行だけ
	lim.maxDataRows = 3
	data, err := parseWorkbookWithLimits(writeXLSX(t, headers, rows), nil, lim)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.rows) != 1 || data.rows[0].cell(colIssueKey) != "EXA-1" {
		t.Errorf("取り込み行 = %+v, want EXA-1 の 1 行", data.rows)
	}

	// 空白のみのセルを持つ行もカウント対象(内容ではなくセルの存在で判定する。
	// 空白行を大量に並べて上限を回避する細工ファイルへの防御)
	blank := make([]string, len(headers))
	blank[0] = "   "
	rows = [][]string{blank, blank, normal}
	lim.maxDataRows = 2
	if _, err := parseWorkbookWithLimits(writeXLSX(t, headers, rows), nil, lim); err == nil {
		t.Error("空白のみのセルを持つ行が行数上限にカウントされなかった")
	}
}

// TestParseWorkbook_GuideScanLimit は「記入方法」シートのプロジェクト ID 探索が
// 先頭 maxGuideScanRows 行で打ち切られることを確認する(中 4)。
// 上限内にあれば従来どおり読め、超えた位置にあるものは「見出しなし」として扱う。
func TestParseWorkbook_GuideScanLimit(t *testing.T) {
	cases := []struct {
		name     string
		labelRow int
		want     int64
	}{
		{"上限内(テンプレートは 2 行目)", 2, 777},
		{"上限ちょうど", maxGuideScanRows, 777},
		{"上限超過は見出しなし扱い", maxGuideScanRows + 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeGuideRowXLSX(t, c.labelRow, 777)
			data, err := parseWorkbookWithLimits(path, nil, defaultParseLimits())
			if err != nil {
				t.Fatal(err)
			}
			if data.projectID != c.want {
				t.Errorf("projectID = %d, want %d", data.projectID, c.want)
			}
		})
	}
}

// writeGuideRowXLSX はデータシートと「記入方法」シートを持つ xlsx を作り、
// プロジェクト ID の見出しを指定行に書き込む(走査上限の検証用)。
func writeGuideRowXLSX(t *testing.T, labelRow int, projectID int64) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := f.SetSheetName(f.GetSheetName(0), export.SheetBulkTemplate); err != nil {
		t.Fatal(err)
	}
	writeSheetRows(t, f, export.SheetBulkTemplate, [][]string{templateHeaders, {"EXA-1"}})
	if _, err := f.NewSheet(export.SheetBulkGuide); err != nil {
		t.Fatal(err)
	}
	writeSheetRows(t, f, export.SheetBulkGuide, [][]string{{"項目", "説明"}})
	if err := f.SetCellStr(export.SheetBulkGuide, fmt.Sprintf("A%d", labelRow), export.BulkProjectIDLabel); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellStr(export.SheetBulkGuide, fmt.Sprintf("B%d", labelRow),
		strconv.FormatInt(projectID, 10)); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "guide.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestParseWorkbook_MissingFile はファイルが無い場合に、開く前の
// サイズ確認で分かりやすいエラーになることを確認する。
func TestParseWorkbook_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.xlsx")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("テストの前提が崩れている")
	}
	if _, err := parseWorkbookWithLimits(path, nil, defaultParseLimits()); err == nil {
		t.Fatal("存在しないファイルでエラーにならなかった")
	}
}
