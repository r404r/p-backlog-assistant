package export

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// sampleUsers はテスト用のユーザデータ(実データ・実在ユーザは含めない)。
func sampleUsers() []UserExportRow {
	return []UserExportRow{
		{
			ID: 1, UserCode: "mock.taro", Name: "モック 太郎",
			MailAddress: "mock.taro@example.invalid",
			RoleType:    1, RoleName: "管理者",
			TeamNames:   []string{"開発チーム", "運用チーム"},
			ProjectKeys: []string{"SAMPLE", "DEMO"},
			// 管理者プロジェクトは 1 件
			AdminProjectKeys: []string{"SAMPLE"},
		},
		{
			ID: 2, UserCode: "mock.hanako", Name: "モック 花子",
			MailAddress: "mock.hanako@example.invalid",
			RoleType:    2, RoleName: "一般ユーザー",
			// 所属チーム・管理者プロジェクトが空のケース
			TeamNames:        nil,
			ProjectKeys:      []string{"TRIAL"},
			AdminProjectKeys: nil,
		},
	}
}

// containsString は列キー列に key が含まれるかを返す。
func containsString(values []string, key string) bool {
	for _, v := range values {
		if v == key {
			return true
		}
	}
	return false
}

// exportUsersToTempFile は一時ディレクトリへ xlsx を生成してパスを返す。
func exportUsersToTempFile(t *testing.T, rows []UserExportRow, opts UserOptions) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.xlsx")
	if err := ExportUsersToFile(path, rows, opts); err != nil {
		t.Fatalf("ExportUsersToFile: %v", err)
	}
	return path
}

func TestExportUsers_DefaultColumns(t *testing.T) {
	path := exportUsersToTempFile(t, sampleUsers(), UserOptions{})
	f := openExported(t, path)

	// シート構成: 1 枚目が「ユーザ」、2 枚目が「情報」
	sheets := f.GetSheetList()
	if len(sheets) != 2 || sheets[0] != SheetUsers || sheets[1] != SheetInfo {
		t.Fatalf("シート一覧 = %v, want [%s %s]", sheets, SheetUsers, SheetInfo)
	}

	rows, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3(ヘッダ + 2 行)", len(rows))
	}

	wantHeader := []string{
		"ユーザID", "名前", "メールアドレス", "ロール",
		"所属チーム", "参加プロジェクト", "管理者プロジェクト",
	}
	if !equalStrings(rows[0], wantHeader) {
		t.Errorf("ヘッダ = %v, want %v", rows[0], wantHeader)
	}

	want2 := []string{
		"mock.taro", "モック 太郎", "mock.taro@example.invalid", "管理者",
		"開発チーム, 運用チーム", "SAMPLE, DEMO", "SAMPLE",
	}
	if !equalStrings(rows[1], want2) {
		t.Errorf("2 行目 = %v, want %v", rows[1], want2)
	}
}

// TestExportUsers_RoleTypeColumn は roleType の数値列(ヘッダ「ロール値」)が
// 選択可能列として出力できることを確認する(中 4)。
// 未知の roleType でも数値そのものを Excel で確認できる。
func TestExportUsers_RoleTypeColumn(t *testing.T) {
	rows := []UserExportRow{
		{UserCode: "mock.taro", Name: "モック 太郎", RoleType: 1, RoleName: "管理者"},
		{UserCode: "mock.mirai", Name: "モック 未来", RoleType: 99, RoleName: "不明(99)"},
	}
	opts := UserOptions{Columns: []string{"roleName", "roleType"}}
	path := exportUsersToTempFile(t, rows, opts)
	f := openExported(t, path)

	got, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ロール", "ロール値"}; !equalStrings(got[0], want) {
		t.Fatalf("ヘッダ = %v, want %v", got[0], want)
	}
	if !equalStrings(got[1], []string{"管理者", "1"}) {
		t.Errorf("2 行目 = %v, want [管理者 1]", got[1])
	}
	if !equalStrings(got[2], []string{"不明(99)", "99"}) {
		t.Errorf("3 行目 = %v, want [不明(99) 99]", got[2])
	}
}

// TestExportUsers_RoleTypeNotInDefaultColumns は roleType 列が
// 選択可能だが既定列には含まれないことを確認する(中 4)。
func TestExportUsers_RoleTypeNotInDefaultColumns(t *testing.T) {
	if !containsString(AvailableUserColumns(), "roleType") {
		t.Error("roleType が選択可能列に含まれていない")
	}
	if containsString(DefaultUserColumns(), "roleType") {
		t.Error("roleType が既定列に含まれている")
	}
	h, ok := UserColumnHeader("roleType")
	if !ok || h != "ロール値" {
		t.Errorf("roleType のヘッダ = %q(ok=%v), want ロール値", h, ok)
	}
}

func TestExportUsers_SelectedColumns(t *testing.T) {
	// 指定順(既定の定義順とは異なる順)で出力されることも確認する
	opts := UserOptions{Columns: []string{"name", "userCode", "teamNames"}}
	path := exportUsersToTempFile(t, sampleUsers(), opts)
	f := openExported(t, path)

	rows, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"名前", "ユーザID", "所属チーム"}
	if !equalStrings(rows[0], wantHeader) {
		t.Fatalf("ヘッダ = %v, want %v", rows[0], wantHeader)
	}
	if got, want := rows[1][0], "モック 太郎"; got != want {
		t.Errorf("A2 = %q, want %q", got, want)
	}
	if got, want := rows[1][1], "mock.taro"; got != want {
		t.Errorf("B2 = %q, want %q", got, want)
	}
	// 選択しなかった列(メールアドレス)は出力されない
	for _, h := range rows[0] {
		if h == "メールアドレス" {
			t.Errorf("非選択列が出力されている: %v", rows[0])
		}
	}
}

func TestExportUsers_MultiValueJoin(t *testing.T) {
	rows := []UserExportRow{
		{
			UserCode: "mock.jiro", Name: "モック 次郎",
			TeamNames:        []string{"チームA", "チームB", "チームC"},
			ProjectKeys:      []string{},
			AdminProjectKeys: nil,
		},
	}
	opts := UserOptions{Columns: []string{"teamNames", "projectKeys", "adminProjectKeys"}}
	path := exportUsersToTempFile(t, rows, opts)
	f := openExported(t, path)

	// 複数値は半角カンマ + 空白で連結する(設計書 5 節)
	v, err := f.GetCellValue(SheetUsers, "A2")
	if err != nil {
		t.Fatal(err)
	}
	if want := "チームA, チームB, チームC"; v != want {
		t.Errorf("所属チーム = %q, want %q", v, want)
	}
	// 空スライス・nil はいずれも空セル
	for _, axis := range []string{"B2", "C2"} {
		got, err := f.GetCellValue(SheetUsers, axis)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("%s = %q, want \"\"", axis, got)
		}
	}
}

func TestExportUsers_UnknownColumn(t *testing.T) {
	var buf bytes.Buffer
	err := ExportUsers(&buf, sampleUsers(), UserOptions{Columns: []string{"name", "nosuchcolumn"}})
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

func TestExportUsersToFile_UnknownColumnLeavesNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.xlsx")
	err := ExportUsersToFile(path, sampleUsers(), UserOptions{Columns: []string{"nosuchcolumn"}})
	if !errors.Is(err, ErrUnknownColumn) {
		t.Fatalf("err = %v, want ErrUnknownColumn", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("エラー時に書きかけのファイルが残っている: %v", statErr)
	}
}

func TestExportUsers_EmptyRows(t *testing.T) {
	path := exportUsersToTempFile(t, nil, UserOptions{})
	f := openExported(t, path)

	rows, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("行数 = %d, want 1(ヘッダのみ)", len(rows))
	}
	if len(rows[0]) != len(DefaultUserColumns()) {
		t.Errorf("ヘッダ列数 = %d, want %d", len(rows[0]), len(DefaultUserColumns()))
	}

	// 情報シートの件数は 0
	v, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if v != "0" {
		t.Errorf("情報シートの件数 = %q, want \"0\"", v)
	}

	// 0 行でもオートフィルタはヘッダ行(既定 7 列 = A:G)に設定される
	if got := autoFilterRef(t, path); got != "A1:G1" {
		t.Errorf("0 行時のオートフィルタ範囲 = %q, want \"A1:G1\"", got)
	}
}

func TestExportUsers_AutoFilterAndFreezePane(t *testing.T) {
	path := exportUsersToTempFile(t, sampleUsers(), UserOptions{})

	if got := autoFilterRef(t, path); got != "A1:G1" {
		t.Errorf("オートフィルタ範囲 = %q, want \"A1:G1\"", got)
	}
	// ヘッダ行の固定(1 行目で分割・frozen)
	doc := readZipEntry(t, path, "xl/worksheets/sheet1.xml")
	if !strings.Contains(doc, `state="frozen"`) || !strings.Contains(doc, `ySplit="1"`) {
		t.Errorf("ヘッダ行が固定されていない: %.400s", doc)
	}
}

func TestExportUsers_WriterOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := ExportUsers(&buf, sampleUsers(), UserOptions{}); err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer f.Close()
	rows, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3", len(rows))
	}
	if got, want := rows[2][1], "モック 花子"; got != want {
		t.Errorf("B3 = %q, want %q", got, want)
	}
}

func TestExportUsers_InfoSheet(t *testing.T) {
	path := exportUsersToTempFile(t, sampleUsers(), UserOptions{})
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

	// スペース名等のメタは書かない(件数のみ)
	rows, err := f.GetRows(SheetInfo)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("情報シートの行数 = %d, want 2(見出し + 件数)", len(rows))
	}
}

func TestDefaultUserColumnsIsCopy(t *testing.T) {
	a := DefaultUserColumns()
	a[0] = "破壊"
	b := DefaultUserColumns()
	if b[0] == "破壊" {
		t.Errorf("DefaultUserColumns が内部スライスを共有している")
	}
}

func TestAvailableUserColumnsAllHaveHeaders(t *testing.T) {
	keys := AvailableUserColumns()
	if len(keys) != 8 {
		t.Fatalf("列キー数 = %d, want 8", len(keys))
	}
	// 既定は roleType(数値)以外のすべての列(中 4)
	wantDefault := []string{
		"userCode", "name", "mailAddress", "roleName",
		"teamNames", "projectKeys", "adminProjectKeys",
	}
	if !equalStrings(DefaultUserColumns(), wantDefault) {
		t.Errorf("既定列 = %v, want %v", DefaultUserColumns(), wantDefault)
	}
	for _, k := range keys {
		h, ok := UserColumnHeader(k)
		if !ok || h == "" {
			t.Errorf("列キー %q のヘッダが未定義", k)
		}
	}
	if _, ok := UserColumnHeader("nosuchcolumn"); ok {
		t.Errorf("未知の列キーが解決できてしまう")
	}
}

// TestExportUsers_ManyRows は多めの行数でも読み戻せることを確認する。
func TestExportUsers_ManyRows(t *testing.T) {
	const n = 1000
	rows := make([]UserExportRow, n)
	for i := range rows {
		rows[i] = UserExportRow{
			ID: int64(i + 1), UserCode: fmt.Sprintf("mock.user%d", i+1),
			Name: fmt.Sprintf("モック ユーザ%d", i+1), MailAddress: fmt.Sprintf("u%d@example.invalid", i+1),
			RoleType: 2, RoleName: "一般ユーザー",
			TeamNames: []string{"チームA"}, ProjectKeys: []string{"SAMPLE", "DEMO"},
		}
	}
	path := exportUsersToTempFile(t, rows, UserOptions{})
	f := openExported(t, path)

	got, err := f.GetRows(SheetUsers)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n+1 {
		t.Fatalf("行数 = %d, want %d", len(got), n+1)
	}
	if want := fmt.Sprintf("mock.user%d", n); got[n][0] != want {
		t.Errorf("最終行のユーザID = %q, want %q", got[n][0], want)
	}
	v, err := f.GetCellValue(SheetInfo, "B2")
	if err != nil {
		t.Fatal(err)
	}
	if v != fmt.Sprint(n) {
		t.Errorf("情報シートの件数 = %q, want %d", v, n)
	}
}
