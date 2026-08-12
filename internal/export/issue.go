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
	value  func(*store.Issue) string // セル値の生成(カスタム属性列・親課題キー列は nil)
	width  float64                   // 列幅(文字数目安)
	// customFieldID はカスタム属性列の定義 ID(固定列は 0)。
	// カスタム属性の値は行ごとに一括で解析した結果から引くため(列ごとに
	// 生 JSON を解析し直さないため)、value ではなくこの ID で解決する。
	customFieldID int64
	// parentIssue は親課題キー列であることを表す(CF5)。
	// 値の解決には課題自身の生 JSON に加えて Options.ParentIssueKeys が要り、
	// value(課題 1 件だけを見る関数)では組み立てられないため区別する。
	parentIssue bool
	// pickerHidden が真の列は、出力では指定できるが画面の列選択には出さない
	// (IssuePickerColumns が除外する。R14)。
	pickerHidden bool
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
	// 詳細は本文が長く、既定でも列選択でも出していない(画面の列選択には出さない)。
	// 出力の仕組み自体は従来どおり残してあるため、列選択に出したくなったら
	// pickerHidden を外すだけでよい(R14 では画面の見た目を変えないため据え置き)。
	{key: "description", header: "詳細", value: func(i *store.Issue) string { return i.Description }, width: 60, pickerHidden: true},
	// 親課題キー(CF5)。値は生 JSON の parentIssueId を Options.ParentIssueKeys で
	// 課題キーへ引き当てる(引き当てられない親は ID:<数値>)。
	{key: ParentIssueKeyColumn, header: ParentIssueKeyHeader, width: 14, parentIssue: true},
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
	// ParentIssueKeys は親課題 ID → 課題キーの対応表(CF5)。
	// parentIssueKey 列を出力するときに呼び出し側が渡す(対象プロジェクトの
	// 未削除課題から作る)。ここに無い親課題 ID は ID:<数値> 形式で出力される。
	ParentIssueKeys map[int64]string
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

// CustomColumnIDs は列キー列からカスタム属性の定義 ID を指定順に取り出す
// (固定列・カスタム属性以外の列は無視。同じ ID は 1 度だけ)。
//
// 画面プレビュー(App.SearchIssues)が「どのカスタム属性の値を返すか」を
// 決めるために使う。cf_{定義ID} という列キーの規約をこのパッケージに閉じたまま、
// 呼び出し側が Excel 出力と同じ列指定を使い回せるようにする。
func CustomColumnIDs(keys []string) []int64 {
	var out []int64
	seen := make(map[int64]bool, len(keys))
	for _, k := range keys {
		id, ok := parseCustomColumnKey(k)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
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

// IssueSeq は課題を 1 件ずつ供給するイテレータ(R4)。
//
// 出力側は yield へ渡された課題をその場でセルへ変換し、保持しない。
// そのため供給側は 1 件ぶんの構造体を使い回してよく、100 万件規模でも
// メモリ使用量は行数に比例しない。
//
// 約束: yield が返したエラーは加工せずそのまま返すこと。呼び出し側は
// errors.Is で自分の打ち切り理由(件数上限等)を判定する。
type IssueSeq func(yield func(*store.Issue) error) error

// IssueSlice は []store.Issue を IssueSeq に変換する。
// 既に全件がメモリにある小規模な呼び出し(テスト等)のための互換手段で、
// 大量データの経路では store のカーソル走査から直接供給すること。
func IssueSlice(rows []store.Issue) IssueSeq {
	return func(yield func(*store.Issue) error) error {
		for i := range rows {
			if err := yield(&rows[i]); err != nil {
				return err
			}
		}
		return nil
	}
}

// ExportIssuesToFile は課題一覧を xlsx として path に書き出す。
// 一時ファイルへ書き切ってから置換するため、失敗しても出力先の既存ファイルは
// そのまま残り、書きかけのファイルも残らない(writeFileAtomic。R5)。
// rows が途中でエラーを返した場合も同様で、出力先には手を付けない。
func ExportIssuesToFile(path string, rows IssueSeq, opts Options) error {
	// 列指定の検証はファイル生成前に済ませ、不正指定で空ファイルを作らない。
	if _, err := resolveColumns(opts.Columns, opts.CustomFields); err != nil {
		return err
	}
	return writeFileAtomic(path, func(w io.Writer) error {
		return ExportIssues(w, rows, opts)
	})
}

// ExportIssues は課題一覧を xlsx として w に書き出す。
// columns が空なら DefaultColumns を使う。未知の列キーは ErrUnknownColumn を返す。
// rows が nil なら課題 0 件(ヘッダのみ)として出力する。
func ExportIssues(w io.Writer, rows IssueSeq, opts Options) error {
	cols, err := resolveColumns(opts.Columns, opts.CustomFields)
	if err != nil {
		return err
	}

	f := excelize.NewFile()
	defer f.Close()

	// 既定シートを課題シートにリネームし、情報シートを 2 枚目として追加する。
	// 件数は逐次書き出しが終わるまで分からないため、ここでは枠だけ作り、
	// 値は書き出し後に埋める(シートの並び順を保つための順序)。
	if err := f.SetSheetName(f.GetSheetName(0), SheetIssues); err != nil {
		return err
	}
	if err := newInfoSheet(f); err != nil {
		return err
	}

	count, err := writeIssueSheet(f, cols, rows, opts)
	if err != nil {
		return err
	}
	if err := setInfoCount(f, count); err != nil {
		return err
	}
	return f.Write(w)
}

// writeInfoSheet は生成メタを情報シートに書き出す。
// 実データ(スペース名・プロジェクト名等)は書かない方針のため、件数のみを記載する。
// 件数が先に分かっている出力(ユーザ・一括結果)から使う。
func writeInfoSheet(f *excelize.File, count int) error {
	if err := newInfoSheet(f); err != nil {
		return err
	}
	return setInfoCount(f, count)
}

// newInfoSheet は情報シートを作り、見出しだけを書く(件数は setInfoCount で埋める)。
func newInfoSheet(f *excelize.File) error {
	if _, err := f.NewSheet(SheetInfo); err != nil {
		return err
	}
	if err := f.SetColWidth(SheetInfo, "A", "A", 16); err != nil {
		return err
	}
	for _, c := range []struct{ axis, value string }{
		{"A1", "項目"}, {"B1", "値"}, {"A2", "件数"},
	} {
		if err := f.SetCellValue(SheetInfo, c.axis, c.value); err != nil {
			return err
		}
	}
	return nil
}

// setInfoCount は情報シートの件数を書き込む(逐次書き出し後に確定する)。
func setInfoCount(f *excelize.File, count int) error {
	return f.SetCellValue(SheetInfo, "B2", count)
}

// writeIssueSheet は課題シートを StreamWriter で書き出し、書き出した行数を返す。
// 課題は rows から 1 件ずつ受け取り、セルへ変換したらすぐ捨てる(R4)。
func writeIssueSheet(f *excelize.File, cols []column, rows IssueSeq, opts Options) (int, error) {
	sw, err := f.NewStreamWriter(SheetIssues)
	if err != nil {
		return 0, err
	}

	withBaseUpdated := opts.WithBaseUpdated
	colCount := len(cols)
	if withBaseUpdated {
		colCount++
	}

	// 列幅・固定行は最初の SetRow より前に設定する(StreamWriter の制約)。
	for i, c := range cols {
		if err := sw.SetColWidth(i+1, i+1, c.width); err != nil {
			return 0, err
		}
	}
	if withBaseUpdated {
		if err := sw.SetColWidth(colCount, colCount, 22); err != nil {
			return 0, err
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
		return 0, err
	}

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#DDEBF7"}},
	})
	if err != nil {
		return 0, err
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
		return 0, err
	}

	// 2 行目以降: 課題データ。
	// カスタム属性列があるときだけ、行ごとに生 JSON を 1 回解析して値を引く
	// (固定列だけの出力に解析コストを掛けない)。
	// セル値のスライスは 1 本を使い回し、課題も受け取った 1 件だけを見る。
	hasCustom := containsCustomColumn(cols)
	values := make([]any, colCount)
	var custom map[int64]string
	count := 0
	if rows != nil {
		err = rows(func(issue *store.Issue) error {
			if hasCustom {
				custom = customValuesOf(issue)
			}
			for i, c := range cols {
				switch {
				case c.customFieldID != 0:
					// 値を持たない課題・解析できない課題では空欄になる
					values[i] = custom[c.customFieldID]
				case c.parentIssue:
					// 生 JSON が無い・壊れている課題は親なし(空欄)へ縮退する
					values[i] = FormatParentIssueRef(store.ParentIssueID(issue.RawJSON), opts.ParentIssueKeys)
				default:
					values[i] = c.value(issue)
				}
			}
			if withBaseUpdated {
				// 競合検知の基準値は整形せず、取得時の生値をそのまま埋め込む。
				values[colCount-1] = issue.Updated
			}
			count++
			cell, err := excelize.CoordinatesToCellName(1, count+1)
			if err != nil {
				return err
			}
			return sw.SetRow(cell, values)
		})
		if err != nil {
			return 0, err
		}
	}

	// オートフィルタはヘッダ行に設定する(Flush 前に設定する必要がある)。
	lastCol, err := excelize.ColumnNumberToName(colCount)
	if err != nil {
		return 0, err
	}
	if err := f.AutoFilter(SheetIssues, fmt.Sprintf("A1:%s1", lastCol), nil); err != nil {
		return 0, err
	}
	if err := sw.Flush(); err != nil {
		return 0, err
	}
	return count, nil
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
