package export

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"backlog-assistant/internal/store"
)

// generatedIssues は「スライスを一切作らない」課題イテレータを返す。
// 全件をメモリに溜めずに書き出せること(R4)を、供給側から確認するために使う。
func generatedIssues(n int) IssueSeq {
	return func(yield func(*store.Issue) error) error {
		// 1 件ぶんの構造体を使い回す(呼び出し先が保持していないことも同時に確かめる)。
		var issue store.Issue
		for i := 1; i <= n; i++ {
			issue = store.Issue{
				ID:       int64(i),
				IssueKey: fmt.Sprintf("EX-%d", i),
				Summary:  fmt.Sprintf("件名 %d", i),
				Updated:  "2026-02-03T04:05:06Z",
			}
			if err := yield(&issue); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestExportIssues_WritesRowsFromIterator はイテレータから供給された課題が
// そのまま行として書き出され、情報シートの件数も一致することを確認する。
func TestExportIssues_WritesRowsFromIterator(t *testing.T) {
	const n = 25
	path := filepath.Join(t.TempDir(), "issues.xlsx")
	if err := ExportIssuesToFile(path, generatedIssues(n), Options{Columns: []string{"issueKey", "summary"}}); err != nil {
		t.Fatalf("ExportIssuesToFile: %v", err)
	}
	f := openExported(t, path)

	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n+1 { // ヘッダ + データ
		t.Fatalf("行数 = %d, want %d", len(rows), n+1)
	}
	// 供給側が構造体を使い回しても、各行が別々の値で書き出されていること
	// (= 出力側が課題を溜め込んでいないこと)。
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("EX-%d", i)
		if rows[i][0] != want {
			t.Errorf("%d 行目のキー = %q, want %q", i+1, rows[i][0], want)
		}
		if got, want := rows[i][1], fmt.Sprintf("件名 %d", i); got != want {
			t.Errorf("%d 行目の件名 = %q, want %q", i+1, got, want)
		}
	}

	count, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if count != strconv.Itoa(n) {
		t.Errorf("情報シートの件数 = %q, want %q", count, strconv.Itoa(n))
	}
}

// TestExportIssues_PropagatesIteratorError はイテレータが返したエラーが
// 加工されずに伝わることを確認する(呼び出し側が errors.Is で
// 件数上限による打ち切りを判定するため)。
func TestExportIssues_PropagatesIteratorError(t *testing.T) {
	stop := errors.New("打ち切り")

	t.Run("イテレータ自身のエラー", func(t *testing.T) {
		var buf bytes.Buffer
		err := ExportIssues(&buf, func(func(*store.Issue) error) error { return stop }, Options{})
		if !errors.Is(err, stop) {
			t.Fatalf("err = %v, want %v", err, stop)
		}
	})

	t.Run("yield が返したエラー", func(t *testing.T) {
		var buf bytes.Buffer
		seq := func(yield func(*store.Issue) error) error {
			rows := sampleIssues()
			for i := range rows {
				if err := yield(&rows[i]); err != nil {
					return err // 加工せずそのまま返すのがイテレータの契約
				}
			}
			return nil
		}
		wrapped := func(yield func(*store.Issue) error) error {
			n := 0
			return seq(func(is *store.Issue) error {
				n++
				if n > 1 {
					return stop
				}
				return yield(is)
			})
		}
		if err := ExportIssues(&buf, wrapped, Options{}); !errors.Is(err, stop) {
			t.Fatalf("err = %v, want %v", err, stop)
		}
	})
}

// TestExportIssues_NilIteratorWritesHeaderOnly は nil イテレータでも
// ヘッダだけの空ファイルを作れることを確認する(呼び出し側での nil 判定を不要にする)。
func TestExportIssues_NilIteratorWritesHeaderOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.xlsx")
	if err := ExportIssuesToFile(path, nil, Options{}); err != nil {
		t.Fatalf("ExportIssuesToFile: %v", err)
	}
	f := openExported(t, path)
	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("行数 = %d, want 1(ヘッダのみ)", len(rows))
	}
}

// TestIssueSlice はスライスをイテレータへ変換するアダプタを確認する
// (件数が小さい呼び出し・テストのための互換手段)。
func TestIssueSlice(t *testing.T) {
	rows := sampleIssues()
	var keys []string
	if err := IssueSlice(rows)(func(is *store.Issue) error {
		keys = append(keys, is.IssueKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(keys, []string{"EX-1", "EX-2"}) {
		t.Errorf("キー = %v, want [EX-1 EX-2]", keys)
	}
}

// generatedBulkRows はスライスを作らないテンプレート行のイテレータ。
func generatedBulkRows(n int) BulkTemplateSeq {
	return func(yield func(*BulkTemplateRow) error) error {
		var row BulkTemplateRow
		for i := 1; i <= n; i++ {
			row = BulkTemplateRow{
				IssueKey:    fmt.Sprintf("EX-%d", i),
				Summary:     fmt.Sprintf("件名 %d", i),
				BaseUpdated: "2026-02-03T04:05:06Z",
			}
			if err := yield(&row); err != nil {
				return err
			}
		}
		return nil
	}
}

// TestExportBulkTemplate_WritesRowsFromIterator はテンプレートもイテレータから
// 逐次書き出せること(全件を溜めないこと)を確認する。
func TestExportBulkTemplate_WritesRowsFromIterator(t *testing.T) {
	const n = 25
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := ExportBulkTemplateToFile(path, testTemplateProjectID, generatedBulkRows(n), sampleBulkMasters()); err != nil {
		t.Fatalf("ExportBulkTemplateToFile: %v", err)
	}
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n+1 {
		t.Fatalf("行数 = %d, want %d", len(rows), n+1)
	}
	for i := 1; i <= n; i++ {
		if want := fmt.Sprintf("EX-%d", i); rows[i][0] != want {
			t.Errorf("%d 行目のキー = %q, want %q", i+1, rows[i][0], want)
		}
	}
}

// TestExportBulkTemplate_DropDownCoversStreamedRows は、データ行が
// 既定の入力規則行数(bulkValidationRows)を超えても、全データ行に
// ドロップダウンが掛かることを確認する(件数を数え終えてから設定するため)。
func TestExportBulkTemplate_DropDownCoversStreamedRows(t *testing.T) {
	n := bulkValidationRows + 5
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := ExportBulkTemplateToFile(path, testTemplateProjectID, generatedBulkRows(n), sampleBulkMasters()); err != nil {
		t.Fatalf("ExportBulkTemplateToFile: %v", err)
	}
	f := openExported(t, path)

	dvs, err := f.GetDataValidations(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(dvs) == 0 {
		t.Fatal("データ入力規則が設定されていない")
	}
	want := strconv.Itoa(n + 1) // ヘッダ 1 行ぶん下にずれる
	for _, dv := range dvs {
		if !hasSuffixRow(dv.Sqref, want) {
			t.Errorf("入力規則の範囲 = %q, want 末尾行 %s", dv.Sqref, want)
		}
	}
}

// hasSuffixRow は "B2:B1005" 形式の範囲が指定行で終わるかを返す。
func hasSuffixRow(sqref, row string) bool {
	return len(sqref) > len(row) && sqref[len(sqref)-len(row):] == row
}

// TestExportBulkTemplate_PropagatesIteratorError はテンプレート出力でも
// イテレータのエラーがそのまま伝わることを確認する。
func TestExportBulkTemplate_PropagatesIteratorError(t *testing.T) {
	stop := errors.New("打ち切り")
	var buf bytes.Buffer
	err := ExportBulkTemplate(&buf, testTemplateProjectID,
		func(func(*BulkTemplateRow) error) error { return stop }, sampleBulkMasters())
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want %v", err, stop)
	}
}

// TestBulkTemplateSlice はスライスをイテレータへ変換するアダプタを確認する。
func TestBulkTemplateSlice(t *testing.T) {
	var keys []string
	if err := BulkTemplateSlice(sampleBulkRows())(func(r *BulkTemplateRow) error {
		keys = append(keys, r.IssueKey)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !equalStrings(keys, []string{"EX-1", "EX-2"}) {
		t.Errorf("キー = %v, want [EX-1 EX-2]", keys)
	}
}
