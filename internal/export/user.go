package export

// user.go はユーザ一覧の Excel(xlsx)出力を担う。
//
// 設計書 5 節「Excel 入出力仕様」準拠:
//   - 既定の出力列: ユーザ ID / 名前 / メール / ロール / 所属チーム / 参加プロジェクト / 管理者プロジェクト。
//     ロールの数値(roleType)は選択可能列として別に用意する。
//   - 複数値(チーム・プロジェクト)は 1 セルにカンマ区切りで連結して出力する。
//
// 課題出力(issue.go)と同じ流儀で StreamWriter・ヘッダ太字・オートフィルタ・ヘッダ行固定を行う。
// 情報シートには件数のみを書き、スペース名等の環境情報は書き出さない(設計書 7 節)。

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// SheetUsers はユーザデータを書き出すシート。
const SheetUsers = "ユーザ"

// multiValueSeparator は複数値(所属チーム等)を 1 セルに連結する際の区切り。
// 設計書 5 節の「カンマ区切り」に合わせ、半角カンマ + 空白を使う(低 1)。
const multiValueSeparator = ", "

// UserExportRow は出力する 1 ユーザ分のデータ。
//
// store のユーザ行と同形だが、export パッケージは表示・整形のみを責務とするため
// 独自に定義する(store への依存を持たない)。
type UserExportRow struct {
	// ID は Backlog のユーザ ID(数値)。
	ID int64
	// UserCode はログイン用のユーザ ID(Backlog API の userId)。
	UserCode string
	// Name は表示名。
	Name string
	// MailAddress はメールアドレス。
	MailAddress string
	// RoleType はスペース全体のロール種別(API 実値。1=管理者 〜 6=ゲスト閲覧者)。
	RoleType int
	// RoleName は RoleType の日本語表記(呼び出し側が解決済みの値を渡す。
	// 未知の値は「不明(N)」形式で数値を含む)。
	RoleName string
	// TeamNames は所属チーム名。
	TeamNames []string
	// ProjectKeys は参加プロジェクトのキー。
	ProjectKeys []string
	// AdminProjectKeys は管理者として登録されているプロジェクトのキー。
	AdminProjectKeys []string
}

// userColumn は 1 出力列の定義。
type userColumn struct {
	key    string                      // 呼び出し側が指定する列キー
	header string                      // 1 行目に出力する日本語ヘッダ
	value  func(*UserExportRow) string // セル値の生成
	width  float64                     // 列幅(文字数目安)
	// optional が真の列は選択可能だが既定列には含めない。
	optional bool
}

// userColumns は出力可能な列の定義(表示順の既定でもある)。
var userColumns = []userColumn{
	{"userCode", "ユーザID", func(u *UserExportRow) string { return u.UserCode }, 18, false},
	{"name", "名前", func(u *UserExportRow) string { return u.Name }, 20, false},
	{"mailAddress", "メールアドレス", func(u *UserExportRow) string { return u.MailAddress }, 30, false},
	{"roleName", "ロール", func(u *UserExportRow) string { return u.RoleName }, 14, false},
	// roleType の数値列。既知値は「ロール」列で読めるため既定では出さないが、
	// 未知のロールを識別できるよう選択可能な列として用意する(中 4)。
	{"roleType", "ロール値", func(u *UserExportRow) string { return strconv.Itoa(u.RoleType) }, 10, true},
	{"teamNames", "所属チーム", func(u *UserExportRow) string { return joinValues(u.TeamNames) }, 28, false},
	{"projectKeys", "参加プロジェクト", func(u *UserExportRow) string { return joinValues(u.ProjectKeys) }, 32, false},
	{"adminProjectKeys", "管理者プロジェクト", func(u *UserExportRow) string { return joinValues(u.AdminProjectKeys) }, 32, false},
}

// UserOptions はユーザ Excel 出力のオプション。
type UserOptions struct {
	// Columns は出力する列キーを表示順に指定する。空なら DefaultUserColumns を使う。
	Columns []string
}

// DefaultUserColumns は既定の出力列キーを返す
// (選択可能列のうち optional でないもの。roleType の数値列は含まない)。
// 呼び出し側が返り値を書き換えても内部定義には影響しない。
func DefaultUserColumns() []string {
	out := make([]string, 0, len(userColumns))
	for _, c := range userColumns {
		if !c.optional {
			out = append(out, c.key)
		}
	}
	return out
}

// AvailableUserColumns は指定可能な列キーを定義順に返す。
func AvailableUserColumns() []string {
	out := make([]string, len(userColumns))
	for i, c := range userColumns {
		out[i] = c.key
	}
	return out
}

// UserColumnHeader は列キーに対応する日本語ヘッダを返す。未知のキーなら ok=false。
func UserColumnHeader(key string) (string, bool) {
	for _, c := range userColumns {
		if c.key == key {
			return c.header, true
		}
	}
	return "", false
}

// joinValues は複数値を 1 セル用の文字列に連結する。空要素は落とす。
func joinValues(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, multiValueSeparator)
}

// resolveUserColumns は列キー列を定義に解決する。未知のキーは ErrUnknownColumn。
func resolveUserColumns(keys []string) ([]userColumn, error) {
	if len(keys) == 0 {
		keys = DefaultUserColumns()
	}
	out := make([]userColumn, 0, len(keys))
	for _, k := range keys {
		var found bool
		for _, c := range userColumns {
			if c.key == k {
				out = append(out, c)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", ErrUnknownColumn, k)
		}
	}
	return out, nil
}

// ExportUsersToFile はユーザ一覧を xlsx として path に書き出す。
// 一時ファイルへ書き切ってから置換するため、失敗しても出力先の既存ファイルは
// そのまま残り、書きかけのファイルも残らない(writeFileAtomic。R5)。
func ExportUsersToFile(path string, rows []UserExportRow, opts UserOptions) error {
	// 列指定の検証はファイル生成前に済ませ、不正指定で空ファイルを作らない。
	if _, err := resolveUserColumns(opts.Columns); err != nil {
		return err
	}
	return writeFileAtomic(path, func(w io.Writer) error {
		return ExportUsers(w, rows, opts)
	})
}

// ExportUsers はユーザ一覧を xlsx として w に書き出す。
// opts.Columns が空なら DefaultUserColumns を使う。未知の列キーは ErrUnknownColumn を返す。
func ExportUsers(w io.Writer, rows []UserExportRow, opts UserOptions) error {
	cols, err := resolveUserColumns(opts.Columns)
	if err != nil {
		return err
	}

	f := excelize.NewFile()
	defer f.Close()

	// 既定シートをユーザシートにリネームし、情報シートを 2 枚目として追加する。
	if err := f.SetSheetName(f.GetSheetName(0), SheetUsers); err != nil {
		return err
	}
	if err := writeInfoSheet(f, len(rows)); err != nil {
		return err
	}

	if err := writeUserSheet(f, cols, rows); err != nil {
		return err
	}
	return f.Write(w)
}

// writeUserSheet はユーザシートを StreamWriter で書き出す。
func writeUserSheet(f *excelize.File, cols []userColumn, rows []UserExportRow) error {
	colCount := len(cols)
	specs := make([]streamSheetColumn, 0, colCount)
	for _, c := range cols {
		specs = append(specs, streamSheetColumn{header: c.header, width: c.width})
	}
	sheet, err := newStreamDataSheet(f, SheetUsers, specs)
	if err != nil {
		return err
	}
	sw := sheet.writer

	// 2 行目以降: ユーザデータ。
	values := make([]any, colCount)
	for n := range rows {
		user := &rows[n]
		for i, c := range cols {
			values[i] = c.value(user)
		}
		cell, err := excelize.CoordinatesToCellName(1, n+2)
		if err != nil {
			return err
		}
		if err := sw.SetRow(cell, values); err != nil {
			return err
		}
	}

	return sheet.Finish(nil)
}
