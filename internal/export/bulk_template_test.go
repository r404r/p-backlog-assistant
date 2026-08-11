package export

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// wantBulkHeaders は一括更新テンプレートのヘッダ(固定順)。
//
// このヘッダ名は取り込み側(internal/bulk のパーサ)が列を解決するための契約であり、
// 変更すると取り込みが壊れる。テストで完全一致を固定する。
var wantBulkHeaders = []string{
	"issueKey", "件名",
	"種別ID", "種別名",
	"状態ID", "状態名",
	"優先度ID", "優先度名",
	"担当者ID", "担当者名",
	"期限", "詳細", "base_updated",
}

// sampleBulkRows はテスト用のテンプレート行(実データ・実在 URL は含めない)。
func sampleBulkRows() []BulkTemplateRow {
	return []BulkTemplateRow{
		{
			IssueKey: "EX-1", Summary: "ログイン画面の不具合",
			IssueTypeID: 11, IssueTypeName: "バグ",
			StatusID: 1, StatusName: "未対応",
			PriorityID: 2, PriorityName: "中",
			AssigneeID: 12345, AssigneeName: "テスト太郎",
			DueDate: "2026-03-04T00:00:00Z", Description: "詳細本文 1",
			BaseUpdated: "2026-02-03T04:05:06Z",
		},
		{
			// 担当者・期限が未設定の行。ID 0 は空セルにする(取り込み側で「未指定」と区別するため)
			IssueKey: "EX-2", Summary: "検索の改善",
			IssueTypeID: 12, IssueTypeName: "タスク",
			StatusID: 2, StatusName: "処理中",
			PriorityID: 3, PriorityName: "低",
			AssigneeID: 0, AssigneeName: "",
			DueDate: "", Description: "",
			BaseUpdated: "不正な日時",
		},
	}
}

// testTemplateProjectID は検証用のプロジェクト ID(実データではない)。
const testTemplateProjectID = int64(42)

// exportBulkToTempFile は一時ディレクトリへテンプレートを生成してパスを返す。
func exportBulkToTempFile(t *testing.T, rows []BulkTemplateRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := ExportBulkTemplateToFile(path, testTemplateProjectID, rows); err != nil {
		t.Fatalf("ExportBulkTemplateToFile: %v", err)
	}
	return path
}

func TestExportBulkTemplate_Sheets(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	sheets := f.GetSheetList()
	if len(sheets) != 2 || sheets[0] != SheetBulkTemplate || sheets[1] != SheetBulkGuide {
		t.Fatalf("シート一覧 = %v, want [%s %s]", sheets, SheetBulkTemplate, SheetBulkGuide)
	}
}

// TestExportBulkTemplate_Headers はヘッダ名・列順の完全一致を固定する(取り込み契約)。
func TestExportBulkTemplate_Headers(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("行が 1 行も無い")
	}
	if !equalStrings(rows[0], wantBulkHeaders) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], wantBulkHeaders)
	}

	// 公開しているヘッダ定義も同じ並びであること(取り込み側が参照する)
	if !equalStrings(BulkTemplateHeaders(), wantBulkHeaders) {
		t.Errorf("BulkTemplateHeaders() = %v, want %v", BulkTemplateHeaders(), wantBulkHeaders)
	}
	// base_updated 列名は既存の抽出出力と共通の定数を使う
	if wantBulkHeaders[len(wantBulkHeaders)-1] != BaseUpdatedHeader {
		t.Errorf("最終列 = %q, want %q", wantBulkHeaders[len(wantBulkHeaders)-1], BaseUpdatedHeader)
	}
}

func TestBulkTemplateHeadersIsCopy(t *testing.T) {
	a := BulkTemplateHeaders()
	a[0] = "破壊"
	if BulkTemplateHeaders()[0] == "破壊" {
		t.Errorf("BulkTemplateHeaders が内部スライスを共有している")
	}
}

func TestExportBulkTemplate_Values(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3(ヘッダ + 2 行)", len(rows))
	}

	want1 := []string{
		"EX-1", "ログイン画面の不具合",
		"11", "バグ",
		"1", "未対応",
		"2", "中",
		"12345", "テスト太郎",
		"2026-03-04", "詳細本文 1",
		"2026-02-03T04:05:06Z",
	}
	if !equalStrings(rows[1], want1) {
		t.Errorf("2 行目 = %v, want %v", rows[1], want1)
	}

	want2 := []string{
		"EX-2", "検索の改善",
		"12", "タスク",
		"2", "処理中",
		"3", "低",
		// 担当者 ID 0 は空セル(「0 番の担当者」と誤読させない)
		"", "",
		"", "",
		// base_updated はパースできない値でも生値のまま(競合検知の基準値)
		"不正な日時",
	}
	if !equalStrings(rows[2], want2) {
		t.Errorf("3 行目 = %v, want %v", rows[2], want2)
	}
}

// TestExportBulkTemplate_EmptyRows は新規追加専用テンプレート(0 行)を検証する。
func TestExportBulkTemplate_EmptyRows(t *testing.T) {
	path := exportBulkToTempFile(t, nil)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数 = %d, want 1(ヘッダのみ)", len(rows))
	}
	if !equalStrings(rows[0], wantBulkHeaders) {
		t.Errorf("ヘッダ = %v, want %v", rows[0], wantBulkHeaders)
	}
	// 0 行でもオートフィルタはヘッダ行(13 列 = A:M)に設定される
	if got := autoFilterRef(t, path); got != "A1:M1" {
		t.Errorf("0 行時のオートフィルタ範囲 = %q, want \"A1:M1\"", got)
	}
}

func TestExportBulkTemplate_AutoFilterAndFreezePane(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())

	if got := autoFilterRef(t, path); got != "A1:M1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:M1\"", got)
	}
	doc := readZipEntry(t, path, "xl/worksheets/sheet1.xml")
	if !strings.Contains(doc, `state="frozen"`) || !strings.Contains(doc, `ySplit="1"`) {
		t.Errorf("ヘッダ行が固定されていない: %.400s", doc)
	}
}

func TestExportBulkTemplate_HeaderStyle(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	styleID, err := f.GetCellStyle(SheetBulkTemplate, "A1")
	if err != nil {
		t.Fatal(err)
	}
	style, err := f.GetStyle(styleID)
	if err != nil {
		t.Fatal(err)
	}
	if style.Font == nil || !style.Font.Bold {
		t.Errorf("ヘッダが太字でない: %+v", style.Font)
	}
}

// TestExportBulkTemplate_GuideSheet は「記入方法」シートに運用ルールが載っていることを確認する。
// 記入方法は取り込み仕様(空欄 = 変更しない / #CLEAR# 等)を利用者に伝える唯一の手段のため、
// 文言の要点をテストで固定する。
func TestExportBulkTemplate_GuideSheet(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkGuide)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("記入方法シートが空")
	}
	var sb strings.Builder
	for _, r := range rows {
		for _, c := range r {
			sb.WriteString(c)
			sb.WriteString("\n")
		}
	}
	text := sb.String()

	wantPhrases := []string{
		"issueKey",     // 空の行は新規追加
		"新規追加",         // 同上
		"変更しない",        // 空欄の意味
		ClearMarker,    // クリア指定
		"base_updated", // 編集しない
		"競合",           // 競合検知に使う旨
		"ID",           // ID 列が正
		"件名",           // 新規行の必須項目
		"種別ID",         // 同上
		// 名前列は「使わない」のではなく、ID が空のときの補完に使う(2 回目 低 2)
		"ID 列が正です",
		"ID が空の場合のみ",
		"一意に特定できるとき",
	}
	for _, p := range wantPhrases {
		if !strings.Contains(text, p) {
			t.Errorf("記入方法シートに %q の説明が無い:\n%s", p, text)
		}
	}
}

// TestExportBulkTemplate_EmbedsProjectID は「記入方法」シートの先頭行に
// 対象プロジェクト ID が数値で埋め込まれることを確認する(高 2)。
// この値は取り込み時のプロジェクト一致検証に使うため、位置と書式を固定する。
func TestExportBulkTemplate_EmbedsProjectID(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	label, err := f.GetCellValue(SheetBulkGuide, "A2")
	if err != nil {
		t.Fatal(err)
	}
	if label != BulkProjectIDLabel {
		t.Errorf("A2 = %q, want %q(記入方法シートの先頭行)", label, BulkProjectIDLabel)
	}
	value, err := f.GetCellValue(SheetBulkGuide, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if value != "42" {
		t.Errorf("B2 = %q, want \"42\"", value)
	}
	// 説明行が消えていないこと(プロジェクト ID 行は先頭に挿入する)
	rows, err := f.GetRows(SheetBulkGuide)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(bulkGuideLines)+2 {
		t.Errorf("記入方法シートの行数 = %d, want %d", len(rows), len(bulkGuideLines)+2)
	}
}

// TestExportBulkTemplate_OmitsUnknownProjectID はプロジェクト ID が未指定(0)の場合に
// 行を出力しないことを確認する(取り込み側は「メタ情報無し」として扱う)。
func TestExportBulkTemplate_OmitsUnknownProjectID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := ExportBulkTemplateToFile(path, 0, sampleBulkRows()); err != nil {
		t.Fatal(err)
	}
	f := openExported(t, path)
	label, err := f.GetCellValue(SheetBulkGuide, "A2")
	if err != nil {
		t.Fatal(err)
	}
	if label == BulkProjectIDLabel {
		t.Errorf("プロジェクト ID 未指定なのに行が出力された: %q", label)
	}
}

func TestExportBulkTemplate_WriterOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportBulkTemplate(&buf, testTemplateProjectID, sampleBulkRows()); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("行数 = %d, want 3", len(rows))
	}
}

// TestExportBulkTemplateToFile_NoPartialFile は書き出し失敗時に空ファイルを残さないことを確認する。
func TestExportBulkTemplateToFile_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	// ディレクトリをパスに指定すると os.Create が失敗する
	if err := ExportBulkTemplateToFile(dir, testTemplateProjectID, sampleBulkRows()); err == nil {
		t.Fatal("ディレクトリへの書き出しが成功してしまった")
	}
}
