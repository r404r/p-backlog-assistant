package export

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backlog-assistant/internal/store"

	"github.com/xuri/excelize/v2"
)

// sampleIssues はテスト用の課題データ(実データ・実在 URL は含めない)。
func sampleIssues() []store.Issue {
	return []store.Issue{
		{
			ID: 1, IssueKey: "EX-1", ProjectID: 1,
			Summary: "ログイン画面の不具合", Description: "詳細本文 1",
			StatusName: "未対応", AssigneeName: "テスト太郎",
			IssueTypeName: "バグ", PriorityName: "高",
			Created: "2026-01-02T03:04:05Z", Updated: "2026-02-03T04:05:06Z",
			DueDate: "2026-03-04T00:00:00Z",
		},
		{
			ID: 2, IssueKey: "EX-2", ProjectID: 1,
			Summary: "検索の改善", Description: "詳細本文 2",
			StatusName: "処理中", AssigneeName: "",
			IssueTypeName: "タスク", PriorityName: "中",
			Created: "2026-01-05T00:00:00Z", Updated: "不正な日時",
			DueDate: "",
		},
	}
}

// exportToTempFile は一時ディレクトリへ xlsx を生成してパスを返す。
func exportToTempFile(t *testing.T, rows []store.Issue, opts Options) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issues.xlsx")
	if err := ExportIssuesToFile(path, rows, opts); err != nil {
		t.Fatalf("ExportIssuesToFile: %v", err)
	}
	return path
}

// openExported は生成した xlsx を excelize で開き直す。
func openExported(t *testing.T, path string) *excelize.File {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// readZipEntry は xlsx(zip)内の指定パートを生 XML として読む。
// オートフィルタ・固定行は excelize の公開 API に getter が無いため XML で検証する。
func readZipEntry(t *testing.T, path, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("zip.OpenReader: %v", err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		if e.Name == name {
			rc, err := e.Open()
			if err != nil {
				t.Fatalf("zip entry open: %v", err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("zip entry read: %v", err)
			}
			return string(b)
		}
	}
	t.Fatalf("zip エントリが見つからない: %s", name)
	return ""
}

// autoFilterRef はワークシート XML から autoFilter の ref を取り出し、
// 絶対参照記号($)を除いて返す(excelize は絶対参照で書き出す)。
func autoFilterRef(t *testing.T, path string) string {
	t.Helper()
	doc := readZipEntry(t, path, "xl/worksheets/sheet1.xml")
	const key = `<autoFilter ref="`
	i := strings.Index(doc, key)
	if i < 0 {
		return ""
	}
	rest := doc[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return strings.ReplaceAll(rest[:j], "$", "")
}

// setLocalLocation は日時整形に使うタイムゾーンを差し替える(テスト用)。
func setLocalLocation(loc *time.Location) func() {
	prev := localLocation
	localLocation = loc
	return func() { localLocation = prev }
}

func TestExportIssues_DefaultColumns(t *testing.T) {
	path := exportToTempFile(t, sampleIssues(), Options{})
	f := openExported(t, path)

	// シート構成: 1 枚目が「課題」、2 枚目が「情報」
	sheets := f.GetSheetList()
	if len(sheets) != 2 || sheets[0] != SheetIssues || sheets[1] != SheetInfo {
		t.Fatalf("シート一覧 = %v, want [%s %s]", sheets, SheetIssues, SheetInfo)
	}

	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3(ヘッダ + 2 行)", len(rows))
	}

	wantHeader := []string{"キー", "件名", "状態", "担当者", "種別", "優先度", "作成日時", "更新日時", "期限"}
	if !equalStrings(rows[0], wantHeader) {
		t.Errorf("ヘッダ = %v, want %v", rows[0], wantHeader)
	}
	// 既定列に詳細(description)は含まれない
	for _, h := range rows[0] {
		if h == "詳細" {
			t.Errorf("既定列に詳細が含まれている: %v", rows[0])
		}
	}

	if got, want := rows[1][0], "EX-1"; got != want {
		t.Errorf("A2 = %q, want %q", got, want)
	}
	if got, want := rows[1][1], "ログイン画面の不具合"; got != want {
		t.Errorf("B2 = %q, want %q", got, want)
	}
	if got, want := rows[1][2], "未対応"; got != want {
		t.Errorf("C2 = %q, want %q", got, want)
	}
	// 未割当の担当者は空文字
	if got := rows[2][3]; got != "" {
		t.Errorf("担当者未設定のセル = %q, want \"\"", got)
	}
}

func TestExportIssues_SelectedColumns(t *testing.T) {
	opts := Options{Columns: []string{"issueKey", "summary", "description"}}
	path := exportToTempFile(t, sampleIssues(), opts)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"キー", "件名", "詳細"}
	if !equalStrings(rows[0], wantHeader) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], wantHeader)
	}
	if got, want := rows[1][2], "詳細本文 1"; got != want {
		t.Errorf("C2 = %q, want %q", got, want)
	}
	// 指定順が保持される
	if got, want := rows[2][0], "EX-2"; got != want {
		t.Errorf("A3 = %q, want %q", got, want)
	}
}

func TestExportIssues_UnknownColumn(t *testing.T) {
	var buf bytes.Buffer
	err := ExportIssues(&buf, sampleIssues(), Options{Columns: []string{"issueKey", "nosuchcolumn"}})
	if !errors.Is(err, ErrUnknownColumn) {
		t.Fatalf("err = %v, want ErrUnknownColumn", err)
	}
	if !strings.Contains(err.Error(), "nosuchcolumn") {
		t.Errorf("エラーメッセージに列キーが含まれない: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("エラー時に出力が書かれている(%d バイト)", buf.Len())
	}
}

func TestExportIssues_EmptyRows(t *testing.T) {
	path := exportToTempFile(t, nil, Options{})
	f := openExported(t, path)

	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数 = %d, want 1(ヘッダのみ)", len(rows))
	}
	if len(rows[0]) != len(DefaultColumns()) {
		t.Errorf("ヘッダ列数 = %d, want %d", len(rows[0]), len(DefaultColumns()))
	}

	// 情報シートの件数は 0
	v, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if v != "0" {
		t.Errorf("情報シートの件数 = %q, want \"0\"", v)
	}

	// 0 行でもオートフィルタはヘッダ行に設定される
	if got := autoFilterRef(t, path); got != "A1:I1" {
		t.Errorf("0 行時のオートフィルタ範囲 = %q, want \"A1:I1\"", got)
	}
}

func TestExportIssues_TimeFormatting(t *testing.T) {
	// 固定オフセット(+09:00)のローカルタイムゾーンで整形結果を検証する
	loc := time.FixedZone("TEST+09", 9*3600)
	restore := setLocalLocation(loc)
	defer restore()

	rows := []store.Issue{{
		IssueKey: "EX-1", Summary: "s",
		Created: "2026-01-02T03:04:05Z",      // UTC → +09:00 で 12:04
		Updated: "2026-01-02T20:00:00+09:00", // 既に +09:00
		DueDate: "2026-03-04T00:00:00Z",
	}, {
		IssueKey: "EX-2", Summary: "s",
		Created: "パース不能",
		Updated: "",
		DueDate: "2026-03-05", // 日付のみの入力
	}}
	path := exportToTempFile(t, rows, Options{Columns: []string{"created", "updated", "dueDate"}})
	f := openExported(t, path)

	got, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"作成日時", "更新日時", "期限"},
		{"2026-01-02 12:04", "2026-01-02 20:00", "2026-03-04"},
		// パース不能な値はそのまま、空文字は空セル
		{"パース不能", "", "2026-03-05"},
	}
	for i := range want {
		if !equalStrings(got[i], want[i]) {
			t.Errorf("行 %d = %v, want %v", i+1, got[i], want[i])
		}
	}
	// 日付のみ入力はそのまま日付として出る
	v, err := f.GetCellValue(SheetIssues, "C3")
	if err != nil {
		t.Fatal(err)
	}
	if v != "2026-03-05" {
		t.Errorf("C3 = %q, want \"2026-03-05\"", v)
	}
}

func TestExportIssues_WithBaseUpdated(t *testing.T) {
	opts := Options{Columns: []string{"issueKey", "summary"}, WithBaseUpdated: true}
	path := exportToTempFile(t, sampleIssues(), opts)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"キー", "件名", BaseUpdatedHeader}
	if !equalStrings(rows[0], wantHeader) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], wantHeader)
	}
	// base_updated は整形せず RFC3339 の生値
	if got, want := rows[1][2], "2026-02-03T04:05:06Z"; got != want {
		t.Errorf("base_updated = %q, want %q", got, want)
	}
	if got, want := rows[2][2], "不正な日時"; got != want {
		t.Errorf("base_updated(生値) = %q, want %q", got, want)
	}

	// オートフィルタ範囲に base_updated 列まで含まれる(3 列 = A:C)
	if got := autoFilterRef(t, path); got != "A1:C1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:C1\"", got)
	}
}

func TestExportIssues_AutoFilterAndFreezePane(t *testing.T) {
	path := exportToTempFile(t, sampleIssues(), Options{})

	// 既定 9 列 → A1:I1
	if got := autoFilterRef(t, path); got != "A1:I1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:I1\"", got)
	}
	// ヘッダ行の固定(1 行目で分割・frozen)
	doc := readZipEntry(t, path, "xl/worksheets/sheet1.xml")
	if !strings.Contains(doc, `state="frozen"`) || !strings.Contains(doc, `ySplit="1"`) {
		t.Errorf("ヘッダ行が固定されていない: %.400s", doc)
	}
}

func TestExportIssues_WriterOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportIssues(&buf, sampleIssues(), Options{}); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	rows, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("行数 = %d, want 3", len(rows))
	}
}

func TestExportIssues_InfoSheet(t *testing.T) {
	path := exportToTempFile(t, sampleIssues(), Options{})
	f := openExported(t, path)

	label, err := f.GetCellValue(SheetInfo, "A2")
	if err != nil {
		t.Fatal(err)
	}
	if label != "件数" {
		t.Errorf("情報シート A2 = %q, want \"件数\"", label)
	}
	count, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if count != "2" {
		t.Errorf("情報シート B2 = %q, want \"2\"", count)
	}

	// スペース名・プロジェクト名等のメタは書かない(件数のみ)
	rows, err := f.GetRows(SheetInfo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("情報シートの行数 = %d, want 2(見出し + 件数)", len(rows))
	}
}

func TestDefaultColumnsIsCopy(t *testing.T) {
	a := DefaultColumns()
	a[0] = "破壊"
	b := DefaultColumns()
	if b[0] == "破壊" {
		t.Errorf("DefaultColumns が内部スライスを共有している")
	}
}

func TestAvailableColumnsAllHaveHeaders(t *testing.T) {
	keys := AvailableColumns()
	if len(keys) != 10 {
		t.Fatalf("列キー数 = %d, want 10", len(keys))
	}
	for _, k := range keys {
		h, ok := ColumnHeader(k)
		if !ok || h == "" {
			t.Errorf("列キー %q のヘッダが未定義", k)
		}
	}
	if _, ok := ColumnHeader("nosuchcolumn"); ok {
		t.Errorf("未知の列キーが解決できてしまう")
	}
}

// TestExportIssues_LargeRows は 5,000 行の生成が現実的な時間で終わることを確認する。
func TestExportIssues_LargeRows(t *testing.T) {
	const n = 5000
	rows := make([]store.Issue, n)
	for i := range rows {
		rows[i] = store.Issue{
			ID: int64(i + 1), IssueKey: fmt.Sprintf("EX-%d", i+1), ProjectID: 1,
			Summary: fmt.Sprintf("課題 %d", i+1), Description: "詳細",
			StatusName: "未対応", AssigneeName: "テスト太郎",
			IssueTypeName: "バグ", PriorityName: "中",
			Created: "2026-01-02T03:04:05Z", Updated: "2026-02-03T04:05:06Z",
			DueDate: "2026-03-04T00:00:00Z",
		}
	}
	path := filepath.Join(t.TempDir(), "large.xlsx")
	start := time.Now()
	if err := ExportIssuesToFile(path, rows, Options{WithBaseUpdated: true}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("%d 行の生成時間: %s", n, elapsed)
	if elapsed > 30*time.Second {
		t.Errorf("%d 行の生成に %s かかった(想定を大きく超過)", n, elapsed)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Fatal("生成ファイルが空")
	}

	f := openExported(t, path)
	got, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n+1 {
		t.Fatalf("行数 = %d, want %d", len(got), n+1)
	}
	if got[n][0] != fmt.Sprintf("EX-%d", n) {
		t.Errorf("最終行のキー = %q", got[n][0])
	}
	v, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if v != fmt.Sprint(n) {
		t.Errorf("情報シートの件数 = %q, want %d", v, n)
	}
}

// equalStrings は文字列スライスの一致を判定する。
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
