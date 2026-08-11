// Package export は抽出結果の Excel(xlsx)出力を担う。
//
// 設計書 5 節「Excel 入出力仕様」準拠:
//   - excelize の StreamWriter を使用し、数万行の課題でもメモリを抑えて出力する。
//   - 1 行目は日本語ヘッダ。オートフィルタを設定し、ヘッダ行を固定表示にする。
//   - 出力列は呼び出し側が選択できる(列キー → 日本語ヘッダの対応表を本パッケージが持つ)。
//
// このパッケージは呼び出し側から渡された課題データのみを書き出す。
// スペース名・プロジェクト名・接続先 URL 等の環境情報は書き出さない(設計書 7 節)。
package export

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/store"

	"github.com/xuri/excelize/v2"
)

// シート名。
const (
	// SheetIssues は課題データを書き出すシート。
	SheetIssues = "課題"
	// SheetInfo は生成メタ(件数)を書き出すシート。
	SheetInfo = "情報"
)

// BaseUpdatedHeader は一括更新テンプレートの競合検知用列のヘッダ。
// 機械可読な列のため日本語化せず、設計書 5 節の名称をそのまま使う。
const BaseUpdatedHeader = "base_updated"

// 出力する日時の書式。
const (
	dateTimeLayout = "2006-01-02 15:04"
	dateLayout     = "2006-01-02"
)

// ErrUnknownColumn は未知の列キーが指定されたことを表す。
var ErrUnknownColumn = errors.New("未知の列キーです")

// localLocation は日時整形に使うタイムゾーン。テストから差し替える。
var localLocation = time.Local

// customColumnPrefix はカスタム属性列の列キーの接頭辞。
// 列キーは cf_{定義ID}(例: cf_123)で、ヘッダには定義名を使う。
const customColumnPrefix = "cf_"

// カスタム属性列の列幅(文字数目安)。値の長さは定義の型でしか見当が付かないため、
// 数値・日付は固定列の期限・数値系に合わせて狭く、テキスト系・リスト系は広めにする。
const (
	customColumnWidthNarrow = 14
	customColumnWidthWide   = 30
)

// column は 1 出力列の定義。
type column struct {
	key    string                    // 呼び出し側が指定する列キー
	header string                    // 1 行目に出力する日本語ヘッダ
	value  func(*store.Issue) string // セル値の生成(カスタム属性列は nil)
	width  float64                   // 列幅(文字数目安)
	// customFieldID はカスタム属性列の定義 ID(固定列は 0)。
	// カスタム属性の値は行ごとに一括で解析した結果から引くため(列ごとに
	// 生 JSON を解析し直さないため)、value ではなくこの ID で解決する。
	customFieldID int64
}

// columns は出力可能な列の定義(表示順の既定でもある)。
var columns = []column{
	{key: "issueKey", header: "キー", value: func(i *store.Issue) string { return i.IssueKey }, width: 14},
	{key: "summary", header: "件名", value: func(i *store.Issue) string { return i.Summary }, width: 48},
	{key: "statusName", header: "状態", value: func(i *store.Issue) string { return i.StatusName }, width: 12},
	{key: "assigneeName", header: "担当者", value: func(i *store.Issue) string { return i.AssigneeName }, width: 16},
	{key: "issueTypeName", header: "種別", value: func(i *store.Issue) string { return i.IssueTypeName }, width: 14},
	{key: "priorityName", header: "優先度", value: func(i *store.Issue) string { return i.PriorityName }, width: 10},
	{key: "created", header: "作成日時", value: func(i *store.Issue) string { return formatDateTime(i.Created) }, width: 18},
	{key: "updated", header: "更新日時", value: func(i *store.Issue) string { return formatDateTime(i.Updated) }, width: 18},
	{key: "dueDate", header: "期限", value: func(i *store.Issue) string { return formatDate(i.DueDate) }, width: 12},
	{key: "description", header: "詳細", value: func(i *store.Issue) string { return i.Description }, width: 60},
}

// defaultColumnKeys は既定の出力列(詳細は本文が長いため既定では出さない)。
var defaultColumnKeys = []string{
	"issueKey", "summary", "statusName", "assigneeName",
	"issueTypeName", "priorityName", "created", "updated", "dueDate",
}

// Options は課題 Excel 出力のオプション。
type Options struct {
	// Columns は出力する列キーを表示順に指定する。空なら DefaultColumns を使う。
	Columns []string
	// CustomFields は出力対象プロジェクトのカスタム属性定義。
	// cf_{定義ID} 形式の列キーはここに載っている定義だけが指定できる
	// (定義に無いキーは ErrUnknownColumn)。カスタム属性列を使わない
	// 呼び出しでは空のままでよい。
	CustomFields []customfield.Def
	// WithBaseUpdated が true のとき、末尾に base_updated 列(更新日時の RFC3339 生値)
	// を追加する。一括更新テンプレートの競合検知(設計書 5 節)で使う。
	WithBaseUpdated bool
}

// DefaultColumns は既定の出力列キーを返す(呼び出し側が書き換えても内部に影響しない)。
// カスタム属性列は既定に含めない(利用者が明示的に選んだときだけ出力する)。
func DefaultColumns() []string {
	out := make([]string, len(defaultColumnKeys))
	copy(out, defaultColumnKeys)
	return out
}

// AvailableColumns は指定可能な列キーを返す。
// 固定列(定義順)の後に、渡されたカスタム属性の定義順で cf_{定義ID} が並ぶ。
func AvailableColumns(defs []customfield.Def) []string {
	out := make([]string, 0, len(columns)+len(defs))
	for _, c := range columns {
		out = append(out, c.key)
	}
	for _, d := range defs {
		out = append(out, CustomColumnKey(d.ID))
	}
	return out
}

// ColumnHeader は列キーに対応する日本語ヘッダを返す。未知のキーなら ok=false。
// カスタム属性列(cf_{定義ID})のヘッダは定義名になる。
func ColumnHeader(key string, defs []customfield.Def) (string, bool) {
	c, ok := findColumn(key, defs)
	if !ok {
		return "", false
	}
	return c.header, true
}

// CustomColumnKey はカスタム属性の定義 ID に対応する列キーを返す。
func CustomColumnKey(defID int64) string {
	return customColumnPrefix + strconv.FormatInt(defID, 10)
}

// HasCustomColumns は列キー列にカスタム属性列が含まれるかを返す。
// 呼び出し側が「カスタム属性の定義取得(API 呼び出し)が要るか」を
// 判断するために使う(選ばれていなければ取得を増やさない)。
func HasCustomColumns(keys []string) bool {
	for _, k := range keys {
		if strings.HasPrefix(k, customColumnPrefix) {
			return true
		}
	}
	return false
}

// parseCustomColumnKey は列キーからカスタム属性の定義 ID を取り出す。
// 接頭辞が無い・ID が数値でない場合は ok=false(固定列として扱われ、
// 最終的に ErrUnknownColumn になる)。
func parseCustomColumnKey(key string) (int64, bool) {
	if !strings.HasPrefix(key, customColumnPrefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(key, customColumnPrefix), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// customColumn はカスタム属性の定義から出力列を作る。
func customColumn(def customfield.Def) column {
	width := float64(customColumnWidthWide)
	switch def.TypeID {
	case customfield.TypeNumeric, customfield.TypeDate:
		width = customColumnWidthNarrow
	}
	return column{
		key:           CustomColumnKey(def.ID),
		header:        def.Name,
		width:         width,
		customFieldID: def.ID,
	}
}

// findColumn は列キーを固定列またはカスタム属性列の定義に解決する。
func findColumn(key string, defs []customfield.Def) (column, bool) {
	for _, c := range columns {
		if c.key == key {
			return c, true
		}
	}
	id, ok := parseCustomColumnKey(key)
	if !ok {
		return column{}, false
	}
	for _, d := range defs {
		if d.ID == id {
			return customColumn(d), true
		}
	}
	return column{}, false
}

// resolveColumns は列キー列を定義に解決する。未知のキーは ErrUnknownColumn。
func resolveColumns(keys []string, defs []customfield.Def) ([]column, error) {
	if len(keys) == 0 {
		keys = defaultColumnKeys
	}
	out := make([]column, 0, len(keys))
	for _, k := range keys {
		c, ok := findColumn(k, defs)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownColumn, k)
		}
		out = append(out, c)
	}
	return out, nil
}

// ExportIssuesToFile は課題一覧を xlsx として path に書き出す。
// 書き出しに失敗した場合、書きかけのファイルは残さない。
func ExportIssuesToFile(path string, rows []store.Issue, opts Options) error {
	// 列指定の検証はファイル生成前に済ませ、不正指定で空ファイルを作らない。
	if _, err := resolveColumns(opts.Columns, opts.CustomFields); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := ExportIssues(f, rows, opts); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// ExportIssues は課題一覧を xlsx として w に書き出す。
// columns が空なら DefaultColumns を使う。未知の列キーは ErrUnknownColumn を返す。
func ExportIssues(w io.Writer, rows []store.Issue, opts Options) error {
	cols, err := resolveColumns(opts.Columns, opts.CustomFields)
	if err != nil {
		return err
	}

	f := excelize.NewFile()
	defer f.Close()

	// 既定シートを課題シートにリネームし、情報シートを 2 枚目として追加する。
	if err := f.SetSheetName(f.GetSheetName(0), SheetIssues); err != nil {
		return err
	}
	if err := writeInfoSheet(f, len(rows)); err != nil {
		return err
	}

	if err := writeIssueSheet(f, cols, rows, opts.WithBaseUpdated); err != nil {
		return err
	}
	return f.Write(w)
}

// writeInfoSheet は生成メタを情報シートに書き出す。
// 実データ(スペース名・プロジェクト名等)は書かない方針のため、件数のみを記載する。
func writeInfoSheet(f *excelize.File, count int) error {
	if _, err := f.NewSheet(SheetInfo); err != nil {
		return err
	}
	if err := f.SetColWidth(SheetInfo, "A", "A", 16); err != nil {
		return err
	}
	cells := []struct {
		axis  string
		value any
	}{
		{"A1", "項目"}, {"B1", "値"},
		{"A2", "件数"}, {"B2", count},
	}
	for _, c := range cells {
		if err := f.SetCellValue(SheetInfo, c.axis, c.value); err != nil {
			return err
		}
	}
	return nil
}

// writeIssueSheet は課題シートを StreamWriter で書き出す。
func writeIssueSheet(f *excelize.File, cols []column, rows []store.Issue, withBaseUpdated bool) error {
	sw, err := f.NewStreamWriter(SheetIssues)
	if err != nil {
		return err
	}

	colCount := len(cols)
	if withBaseUpdated {
		colCount++
	}

	// 列幅・固定行は最初の SetRow より前に設定する(StreamWriter の制約)。
	for i, c := range cols {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return err
		}
	}
	if withBaseUpdated {
		if err := sw.SetColWidth(colCount, colCount, 22); err != nil {
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

	// 1 行目: 日本語ヘッダ。
	header := make([]any, 0, colCount)
	for _, c := range cols {
		header = append(header, excelize.Cell{StyleID: headerStyle, Value: c.header})
	}
	if withBaseUpdated {
		header = append(header, excelize.Cell{StyleID: headerStyle, Value: BaseUpdatedHeader})
	}
	if err := sw.SetRow("A1", header); err != nil {
		return err
	}

	// 2 行目以降: 課題データ。
	// カスタム属性列があるときだけ、行ごとに生 JSON を 1 回解析して値を引く
	// (固定列だけの出力に解析コストを掛けない)。
	hasCustom := containsCustomColumn(cols)
	values := make([]any, colCount)
	var custom map[int64]string
	for n := range rows {
		issue := &rows[n]
		if hasCustom {
			custom = customValuesOf(issue)
		}
		for i, c := range cols {
			if c.customFieldID != 0 {
				// 値を持たない課題・解析できない課題では空欄になる
				values[i] = custom[c.customFieldID]
				continue
			}
			values[i] = c.value(issue)
		}
		if withBaseUpdated {
			// 競合検知の基準値は整形せず、取得時の生値をそのまま埋め込む。
			values[colCount-1] = issue.Updated
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
	if err := f.AutoFilter(SheetIssues, fmt.Sprintf("A1:%s1", lastCol), nil); err != nil {
		return err
	}
	return sw.Flush()
}

// containsCustomColumn は解決済みの列にカスタム属性列が含まれるかを返す
// (列キーを見る HasCustomColumns と対で、こちらは解決後の列を見る)。
func containsCustomColumn(cols []column) bool {
	for _, c := range cols {
		if c.customFieldID != 0 {
			return true
		}
	}
	return false
}

// customValuesOf は課題 1 件のカスタム属性を「定義 ID → 表示文字列」にまとめる。
//
// 生 JSON の解析は行ごとに 1 回だけ行い、複数のカスタム属性列で使い回す。
// 解析できない場合(生 JSON が空 / 壊れている / customFields が配列でない)は
// nil を返し、その行のカスタム属性列だけを空欄へ縮退させる。行や出力全体を
// 失敗させないのは、Excel 出力が「今ローカルにある情報の書き出し」であり、
// 1 件のデータ不備で全件の出力を失う方が損失が大きいため
// (生 JSON が空になるのは旧バージョンで同期した課題で起こりうる)。
// 異常の検知は同期側・customfield 側の責務とし、ここでは通知しない。
func customValuesOf(issue *store.Issue) map[int64]string {
	if issue.RawJSON == "" {
		return nil
	}
	values, err := customfield.ParseValues(issue.RawJSON)
	if err != nil {
		return nil
	}
	out := make(map[int64]string, len(values))
	for _, v := range values {
		out[v.ID] = customfield.FormatValue(v)
	}
	return out
}

// formatDateTime は RFC3339 の日時をローカル時刻の "YYYY-MM-DD HH:MM" に整形する。
// 空文字は空文字のまま、パースできない値はそのまま出力する(情報を失わせない)。
func formatDateTime(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.In(localLocation).Format(dateTimeLayout)
}

// formatDate は期限を "YYYY-MM-DD" に整形する。
// 期限は時刻を持たない日付なので、タイムゾーン変換で日付がずれないよう UTC のまま扱う。
func formatDate(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(dateLayout)
	}
	// "YYYY-MM-DD" 形式で渡された場合はそのまま使う。
	if _, err := time.Parse(dateLayout, s); err == nil {
		return s
	}
	// 想定外の形式は情報を落とさずそのまま出す。
	return strings.TrimSpace(s)
}
