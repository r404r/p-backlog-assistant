package export

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/customfield"
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
	"期限", "詳細", "親課題キー", "base_updated",
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
			ParentIssueKey: "EX-9",
			BaseUpdated:    "2026-02-03T04:05:06Z",
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

// sampleBulkMasters はテスト用の選択候補(実データは含めない)。
func sampleBulkMasters() BulkTemplateMasters {
	return BulkTemplateMasters{
		IssueTypes: []NamedRef{{ID: 11, Name: "バグ"}, {ID: 12, Name: "タスク"}},
		Statuses:   []NamedRef{{ID: 1, Name: "未対応"}, {ID: 2, Name: "処理中"}, {ID: 4, Name: "完了"}},
		Priorities: []NamedRef{{ID: 2, Name: "高"}, {ID: 3, Name: "中"}},
		Assignees:  []NamedRef{{ID: 12345, Name: "テスト太郎"}, {ID: 12346, Name: "テスト花子"}},
	}
}

// exportBulkToTempFile は一時ディレクトリへテンプレートを生成してパスを返す。
func exportBulkToTempFile(t *testing.T, rows []BulkTemplateRow) string {
	t.Helper()
	return exportBulkToTempFileWith(t, rows, sampleBulkMasters())
}

// exportBulkToTempFileWith はマスタを指定してテンプレートを生成する。
func exportBulkToTempFileWith(t *testing.T, rows []BulkTemplateRow, masters BulkTemplateMasters) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := ExportBulkTemplateToFile(path, testTemplateProjectID, rows, masters); err != nil {
		t.Fatalf("ExportBulkTemplateToFile: %v", err)
	}
	return path
}

func TestExportBulkTemplate_Sheets(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	sheets := f.GetSheetList()
	want := []string{SheetBulkTemplate, SheetBulkGuide, SheetBulkMaster}
	if !equalStrings(sheets, want) {
		t.Fatalf("シート一覧 = %v, want %v", sheets, want)
	}
}

// TestExportBulkTemplate_MasterSheet は「マスタ」シートに選択候補が並ぶことを確認する。
// 利用者はここから名前で選ぶため、列の並び・見出し・担当者の表記を固定する。
func TestExportBulkTemplate_MasterSheet(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkMaster)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("マスタシートが空")
	}
	wantHeader := []string{"種別", "状態", "優先度", "担当者"}
	if !equalStrings(rows[0], wantHeader) {
		t.Fatalf("マスタ見出し = %v, want %v", rows[0], wantHeader)
	}

	// 列ごとの候補(担当者は同名対策で「表示名 (ID)」形式)
	wantCols := map[string][]string{
		"A": {"バグ", "タスク"},
		"B": {"未対応", "処理中", "完了"},
		"C": {"高", "中"},
		"D": {"テスト太郎 (12345)", "テスト花子 (12346)"},
	}
	for col, values := range wantCols {
		for i, want := range values {
			cell := col + strconv.Itoa(i+2)
			got, err := f.GetCellValue(SheetBulkMaster, cell)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("マスタ %s = %q, want %q", cell, got, want)
			}
		}
	}
}

// TestExportBulkTemplate_DropDowns は名前列にマスタシート参照のドロップダウン
// (データ入力規則)が設定されることを確認する。
func TestExportBulkTemplate_DropDowns(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())
	f := openExported(t, path)

	dvs, err := f.GetDataValidations(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, dv := range dvs {
		got[dv.Sqref] = dv.Formula1
	}
	// 種別名 = D 列 / 状態名 = F 列 / 優先度名 = H 列 / 担当者名 = J 列
	wants := map[string]string{
		"D2:D1001": "'" + SheetBulkMaster + "'!$A$2:$A$3",
		"F2:F1001": "'" + SheetBulkMaster + "'!$B$2:$B$4",
		"H2:H1001": "'" + SheetBulkMaster + "'!$C$2:$C$3",
		"J2:J1001": "'" + SheetBulkMaster + "'!$D$2:$D$3",
	}
	if len(dvs) != len(wants) {
		t.Errorf("データ入力規則の数 = %d, want %d(%+v)", len(dvs), len(wants), got)
	}
	for sqref, formula := range wants {
		if got[sqref] != formula {
			t.Errorf("%s の入力規則 = %q, want %q", sqref, got[sqref], formula)
		}
	}
	// #CLEAR# や ID の直接入力を妨げないよう、規則違反はエラーにしない
	for _, dv := range dvs {
		if dv.ShowErrorMessage {
			t.Errorf("入力規則が入力を拒否する設定になっている: %+v", dv)
		}
	}
}

// TestExportBulkTemplate_DropDownsSkippedWhenNoMaster は候補が無い場合に
// 入力規則を設定しない(不正な参照範囲を書かない)ことを確認する。
func TestExportBulkTemplate_DropDownsSkippedWhenNoMaster(t *testing.T) {
	path := exportBulkToTempFileWith(t, sampleBulkRows(), BulkTemplateMasters{})
	f := openExported(t, path)

	dvs, err := f.GetDataValidations(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if len(dvs) != 0 {
		t.Errorf("候補が無いのに入力規則が設定された: %+v", dvs)
	}
	rows, err := f.GetRows(SheetBulkMaster)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("マスタシートの行数 = %d, want 1(見出しのみ)", len(rows))
	}
}

// TestAssigneeLabel は担当者セルの表記「表示名 (ID)」の生成と解析を確認する。
// 同名ユーザを区別する唯一の手段のため、往復できることを固定する。
func TestAssigneeLabel(t *testing.T) {
	if got := AssigneeLabel("テスト太郎", 12345); got != "テスト太郎 (12345)" {
		t.Errorf("AssigneeLabel = %q", got)
	}
	if got := AssigneeLabel("テスト太郎", 0); got != "テスト太郎" {
		t.Errorf("ID 0 の AssigneeLabel = %q, want \"テスト太郎\"", got)
	}
	if got := AssigneeLabel("", 0); got != "" {
		t.Errorf("未設定の AssigneeLabel = %q, want \"\"", got)
	}
	if got := AssigneeLabel("", 12345); got != "(12345)" {
		t.Errorf("名前が無い場合の AssigneeLabel = %q", got)
	}

	cases := []struct {
		in     string
		id     int64
		parsed bool
	}{
		{"テスト太郎 (12345)", 12345, true},
		{"テスト太郎(12345)", 12345, true}, // 空白なし
		{"テスト太郎（12345）", 12345, true}, // 全角括弧(手入力対策)
		{"(12345)", 12345, true},      // 名前が無い場合
		{"テスト太郎", 0, false},           // 名前のみ
		{"重複 名前", 0, false},           // 名前のみ(空白入り)
		{"テスト太郎 (abc)", 0, false},     // ID が数値でない
		{"", 0, false},
	}
	for _, c := range cases {
		id, ok := ParseAssigneeLabel(c.in)
		if ok != c.parsed || id != c.id {
			t.Errorf("ParseAssigneeLabel(%q) = (%d, %v), want (%d, %v)", c.in, id, ok, c.id, c.parsed)
		}
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
	if !equalStrings(BulkTemplateHeaders(nil), wantBulkHeaders) {
		t.Errorf("BulkTemplateHeaders() = %v, want %v", BulkTemplateHeaders(nil), wantBulkHeaders)
	}
	// base_updated 列名は既存の抽出出力と共通の定数を使う
	if wantBulkHeaders[len(wantBulkHeaders)-1] != BaseUpdatedHeader {
		t.Errorf("最終列 = %q, want %q", wantBulkHeaders[len(wantBulkHeaders)-1], BaseUpdatedHeader)
	}
}

func TestBulkTemplateHeadersIsCopy(t *testing.T) {
	a := BulkTemplateHeaders(nil)
	a[0] = "破壊"
	if BulkTemplateHeaders(nil)[0] == "破壊" {
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
		// 担当者名は同名を区別できるよう「表示名 (ID)」形式で出力する
		"12345", "テスト太郎 (12345)",
		"2026-03-04", "詳細本文 1",
		// 親課題キー(CF5。ローカルに無い親は ID:<数値> 形式)
		"EX-9",
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
		// 親課題なしは空セル
		"",
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
	// 0 行でもオートフィルタはヘッダ行(14 列 = A:N)に設定される
	if got := autoFilterRef(t, path); got != "A1:N1" {
		t.Errorf("0 行時のオートフィルタ範囲 = %q, want \"A1:N1\"", got)
	}
}

func TestExportBulkTemplate_AutoFilterAndFreezePane(t *testing.T) {
	path := exportBulkToTempFile(t, sampleBulkRows())

	if got := autoFilterRef(t, path); got != "A1:N1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:N1\"", got)
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
		"種別名または種別ID のどちらか", // 新規行の必須項目(名前でもよいことを明示。文言の後退をテストで防ぐ)
		"種別", // 同上
		// 編集は名前列のドロップダウンで行う(ID 列は参考情報)
		SheetBulkMaster,
		"ドロップダウン",
		"名前列",
		"ID 列は参考情報です",
		"名前列が優先されます", // 名前列と ID 列が食い違う場合の扱い
		"食い違",
		"警告",
		"表示名 (ID)", // 担当者の表記
		// 親課題キー(CF5)の記法
		"親課題キー",
		"ID:",
		"新規追加行どうしを親子にすることはできません",
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
	if err := ExportBulkTemplateToFile(path, 0, sampleBulkRows(), sampleBulkMasters()); err != nil {
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
	if err := ExportBulkTemplate(&buf, testTemplateProjectID, sampleBulkRows(), sampleBulkMasters()); err != nil {
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

// TestExportBulkTemplateToFile_InvalidPath は書き出せないパスでエラーになることを
// 確認する(一時ファイルを残さないことは file_test.go で確認している)。
func TestExportBulkTemplateToFile_InvalidPath(t *testing.T) {
	dir := t.TempDir()
	// ディレクトリをパスに指定すると置換(リネーム)が失敗する
	if err := ExportBulkTemplateToFile(dir, testTemplateProjectID, sampleBulkRows(), sampleBulkMasters()); err == nil {
		t.Fatal("ディレクトリへの書き出しが成功してしまった")
	}
}

// --- カスタム属性(CF3)---------------------------------------------------

// sampleBulkCustomFields はテスト用のカスタム属性定義(実データではない)。
// 型ごとの扱い(ドロップダウンの有無・列幅・プリフィル)を網羅する。
func sampleBulkCustomFields() []customfield.Def {
	return []customfield.Def{
		{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"},
		{
			ID: 32, TypeID: customfield.TypeSingleList, Name: "重要度",
			Items: []customfield.Item{{ID: 321, Name: "高"}, {ID: 322, Name: "低"}},
		},
		{
			ID: 33, TypeID: customfield.TypeMultipleList, Name: "タグ",
			Items: []customfield.Item{{ID: 331, Name: "UI"}, {ID: 332, Name: "API"}, {ID: 333, Name: "DB"}},
		},
		{ID: 34, TypeID: customfield.TypeDate, Name: "開始日"},
	}
}

// sampleBulkMastersWithCustom はカスタム属性付きの選択候補。
func sampleBulkMastersWithCustom() BulkTemplateMasters {
	m := sampleBulkMasters()
	m.CustomFields = sampleBulkCustomFields()
	return m
}

// wantBulkCustomHeaders はカスタム属性列のヘッダ(固定 14 列の後ろに定義順で並ぶ)。
var wantBulkCustomHeaders = []string{"属性:顧客名", "属性:重要度", "属性:タグ", "属性:開始日"}

// TestExportBulkTemplate_CustomFieldHeaders は固定列の後ろに定義順で
// 「属性:{定義名}」列が並ぶことを確認する(取り込み契約)。
func TestExportBulkTemplate_CustomFieldHeaders(t *testing.T) {
	masters := sampleBulkMastersWithCustom()
	path := exportBulkToTempFileWith(t, sampleBulkRows(), masters)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]string{}, wantBulkHeaders...), wantBulkCustomHeaders...)
	if !equalStrings(rows[0], want) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], want)
	}
	if !equalStrings(BulkTemplateHeaders(masters.CustomFields), want) {
		t.Errorf("BulkTemplateHeaders = %v, want %v", BulkTemplateHeaders(masters.CustomFields), want)
	}
	// 接頭辞は取り込み側との契約
	if BulkCustomColumnPrefix != "属性:" {
		t.Errorf("BulkCustomColumnPrefix = %q", BulkCustomColumnPrefix)
	}
	if got := BulkCustomHeader("顧客名"); got != "属性:顧客名" {
		t.Errorf("BulkCustomHeader = %q", got)
	}
	// オートフィルタ・列幅も 18 列(A:R)に広がる
	if got := autoFilterRef(t, path); got != "A1:R1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:R1\"", got)
	}
}

// TestExportBulkTemplate_CustomFieldValues は既存課題行に現在値を
// プリフィルすることを確認する(利用者が現在値を見ながら編集できるようにする)。
func TestExportBulkTemplate_CustomFieldValues(t *testing.T) {
	rows := sampleBulkRows()
	rows[0].CustomFields = map[int64]string{
		31: "取引先 A",
		32: "高",
		33: "UI, DB",
		34: "2026-05-06",
	}
	path := exportBulkToTempFileWith(t, rows, sampleBulkMastersWithCustom())
	f := openExported(t, path)

	got, err := f.GetRows(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"取引先 A", "高", "UI, DB", "2026-05-06"}
	if !equalStrings(got[1][len(wantBulkHeaders):], want) {
		t.Errorf("2 行目のカスタム属性 = %v, want %v", got[1][len(wantBulkHeaders):], want)
	}
	// 値が無い行は空セル(空欄 = 変更しない)
	if len(got[2]) > len(wantBulkHeaders) {
		for _, v := range got[2][len(wantBulkHeaders):] {
			if v != "" {
				t.Errorf("3 行目のカスタム属性に値が入った: %v", got[2])
			}
		}
	}
}

// TestExportBulkTemplate_CustomFieldMasterAndDropDowns は
// 単一リスト・ラジオにだけ選択肢のドロップダウンを設定することを確認する
// (複数リスト・チェックボックスはカンマ区切りで複数選ぶためドロップダウンにできない)。
func TestExportBulkTemplate_CustomFieldMasterAndDropDowns(t *testing.T) {
	masters := sampleBulkMastersWithCustom()
	// ラジオも単一選択なのでドロップダウンの対象
	masters.CustomFields = append(masters.CustomFields, customfield.Def{
		ID: 35, TypeID: customfield.TypeRadio, Name: "区分",
		Items: []customfield.Item{{ID: 351, Name: "社内"}, {ID: 352, Name: "社外"}},
	})
	path := exportBulkToTempFileWith(t, sampleBulkRows(), masters)
	f := openExported(t, path)

	// マスタシート: 固定 4 列の後ろに単一選択のカスタム属性が並ぶ
	rows, err := f.GetRows(SheetBulkMaster)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"種別", "状態", "優先度", "担当者", "属性:重要度", "属性:区分"}
	if !equalStrings(rows[0], wantHeader) {
		t.Fatalf("マスタ見出し = %v, want %v", rows[0], wantHeader)
	}
	wantCols := map[string][]string{
		"E": {"高", "低"},
		"F": {"社内", "社外"},
	}
	for col, values := range wantCols {
		for i, want := range values {
			cell := col + strconv.Itoa(i+2)
			got, err := f.GetCellValue(SheetBulkMaster, cell)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("マスタ %s = %q, want %q", cell, got, want)
			}
		}
	}

	// データシート: 属性:重要度 = P 列 / 属性:区分 = S 列(属性:タグ・属性:開始日には設定しない)
	dvs, err := f.GetDataValidations(SheetBulkTemplate)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, dv := range dvs {
		got[dv.Sqref] = dv.Formula1
	}
	wants := map[string]string{
		"P2:P1001": "'" + SheetBulkMaster + "'!$E$2:$E$3",
		"S2:S1001": "'" + SheetBulkMaster + "'!$F$2:$F$3",
	}
	for sqref, formula := range wants {
		if got[sqref] != formula {
			t.Errorf("%s の入力規則 = %q, want %q(%+v)", sqref, got[sqref], formula, got)
		}
	}
	// 固定 4 列 + 単一選択 2 列 = 6 件(複数リスト・日付・文字列には設定しない)
	if len(dvs) != 6 {
		t.Errorf("データ入力規則の数 = %d, want 6(%+v)", len(dvs), got)
	}
}

// TestExportBulkTemplate_RejectsDuplicateCustomFieldName は定義名が重複する
// プロジェクトでテンプレートを出力しないことを確認する。
// 列ヘッダは定義名で解決するため、重複したまま出力すると取り込み時に
// どちらの定義か決められない。
func TestExportBulkTemplate_RejectsDuplicateCustomFieldName(t *testing.T) {
	masters := sampleBulkMasters()
	masters.CustomFields = []customfield.Def{
		{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"},
		{ID: 32, TypeID: customfield.TypeText, Name: "顧客名"},
	}
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	err := ExportBulkTemplateToFile(path, testTemplateProjectID, sampleBulkRows(), masters)
	if err == nil {
		t.Fatal("定義名が重複しているのに出力できた")
	}
	if !strings.Contains(err.Error(), "顧客名") {
		t.Errorf("メッセージ = %q", err.Error())
	}
}

// TestExportBulkTemplate_CustomFieldGuide は記入方法シートに
// カスタム属性の記法が載っていることを確認する(唯一の利用者向け仕様書のため)。
func TestExportBulkTemplate_CustomFieldGuide(t *testing.T) {
	path := exportBulkToTempFileWith(t, sampleBulkRows(), sampleBulkMastersWithCustom())
	f := openExported(t, path)

	rows, err := f.GetRows(SheetBulkGuide)
	if err != nil {
		t.Fatal(err)
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
		BulkCustomColumnPrefix, // 列ヘッダの記法
		"yyyy-MM-dd",           // 日付
		"カンマ",                  // 複数リスト・チェックボックスの区切り
		"その他",                  // 直接入力は未対応
		"未対応",
		"前後の空白",        // 取り込み時に落とすため前後空白だけの編集は反映できない
		"クリア指示と区別できない", // 文字列としての #CLEAR# は設定できない
	}
	for _, p := range wantPhrases {
		if !strings.Contains(text, p) {
			t.Errorf("記入方法シートに %q の説明が無い:\n%s", p, text)
		}
	}
}
