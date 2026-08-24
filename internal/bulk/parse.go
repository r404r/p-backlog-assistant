package bulk

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// テンプレートの列キー(内部表現)。ヘッダ名から解決するため、
// 列の並び順には依存しない(設計書 5 節)。
const (
	colIssueKey       = "issueKey"
	colSummary        = "summary"
	colIssueTypeID    = "issueTypeId"
	colIssueTypeName  = "issueTypeName"
	colStatusID       = "statusId"
	colStatusName     = "statusName"
	colPriorityID     = "priorityId"
	colPriorityName   = "priorityName"
	colAssigneeID     = "assigneeId"
	colAssigneeName   = "assigneeName"
	colDueDate        = "dueDate"
	colDescription    = "description"
	colParentIssueKey = "parentIssueKey"
	colBaseUpdated    = "baseUpdated"
)

// fixedColumnCount は上の固定列の数(列数上限の見積もりに使う)。
// headerAliases が解決する列キーの数と一致することを parse_test.go で確認する。
const fixedColumnCount = 14

// headerAliases はヘッダ名(正規化済み)→ 列キー。
// テンプレートの正式名に加え、課題抽出 Excel のヘッダ(キー・状態・担当者 等)も
// 受け付ける(抽出結果へ base_updated を付けたファイルをそのまま取り込めるようにする)。
// 正規化は normalizeHeader(NFKC + ケースフォールド)で行うため、
// ここのキーは小文字・半角で書く。
var headerAliases = map[string]string{
	"issuekey": colIssueKey, "キー": colIssueKey, "課題キー": colIssueKey,
	"件名": colSummary, "summary": colSummary,
	"種別id": colIssueTypeID, "種別名": colIssueTypeName, "種別": colIssueTypeName,
	"状態id": colStatusID, "状態名": colStatusName, "状態": colStatusName,
	"優先度id": colPriorityID, "優先度名": colPriorityName, "優先度": colPriorityName,
	"担当者id": colAssigneeID, "担当者名": colAssigneeName, "担当者": colAssigneeName,
	"期限": colDueDate, "詳細": colDescription,
	// 親課題(CF5)。抽出出力・テンプレートとも「親課題キー」を使う
	"親課題キー": colParentIssueKey, "親課題": colParentIssueKey,
	"base_updated": colBaseUpdated,
}

// idColumnLabels / nameColumnLabels は列キー → テンプレート上の見出し
// (エラーメッセージでどのセルを直せばよいかを示すため)。
var idColumnLabels = map[string]string{
	colIssueTypeID: "種別ID",
	colStatusID:    "状態ID",
	colPriorityID:  "優先度ID",
	colAssigneeID:  "担当者ID",
}

var nameColumnLabels = map[string]string{
	colIssueTypeName: "種別名",
	colStatusName:    "状態名",
	colPriorityName:  "優先度名",
	colAssigneeName:  "担当者名",
}

// customHeaderPrefix は正規化済みのカスタム属性ヘッダ接頭辞
// (ヘッダ側も normalizeHeader で正規化してから比較する)。
var customHeaderPrefix = normalizeHeader(export.BulkCustomColumnPrefix)

// customColKey はカスタム属性の列キー(定義 ID から作る内部表現)。
// 固定列のキー(issueKey 等)と衝突しないよう接頭辞を付ける。
func customColKey(defID int64) string {
	return "customField:" + strconv.FormatInt(defID, 10)
}

// rawRow は Excel の 1 行(列キー → トリム済みセル値)。
type rawRow struct {
	rowNo int // Excel の行番号(ヘッダが 1 行目、データは 2 行目から)
	cells map[string]string
}

// cell は列の値を返す(列が無ければ空文字)。
func (r rawRow) cell(key string) string { return r.cells[key] }

// has は値が入っているかを返す。
func (r rawRow) has(key string) bool { return r.cells[key] != "" }

// sheetData は取り込んだシートの内容。
type sheetData struct {
	rows    []rawRow
	columns map[string]bool // 存在した列キー
	// projectID は「記入方法」シートに埋め込まれた対象プロジェクト ID(高 2)。
	// 0 はメタ情報が無い(旧テンプレート・手作りファイル)ことを表す。
	projectID int64
}

// normalizeHeader はヘッダ名を比較用に正規化する
// (全角・半角、大文字・小文字の違いを吸収する)。
func normalizeHeader(s string) string {
	return store.NormalizeSearchText(strings.TrimSpace(s))
}

// parseLimits は取り込む Excel の実用上限(R6)。
// テストで小さい値を注入できるよう、定数ではなく構造体で持ち回す。
type parseLimits struct {
	// maxFileSize は xlsx ファイル自体(圧縮後)のサイズ上限(バイト)。
	maxFileSize int64
	// maxDataRows はデータ行数の上限。カウント対象はヘッダ行を除く
	// 「物理的に空でない行」(既知列が空でも、どこかに値があれば数える)。
	maxDataRows int
	// extraColumns は「固定列 + カスタム属性定義数」に上乗せする余裕列数。
	extraColumns int
	// unzipSizeLimit / unzipXMLSizeLimit は excelize の展開上限(バイト)。
	unzipSizeLimit    int64
	unzipXMLSizeLimit int64
}

// defaultParseLimits は実運用の上限を返す。
//
// 上限を設ける理由: excelize の既定の展開上限は 16GB で実質無制限に近く、
// 高圧縮率の xlsx(数 MB のファイルが GB 単位に展開される)を読ませると
// メモリを使い果たしてアプリごと落ちる。
func defaultParseLimits() parseLimits {
	return parseLimits{
		// 100MB: 50,000 行 × 30 列程度のテンプレートでも xlsx は数 MB に収まる。
		// 実運用のファイルは確実に通り、明らかに異常なファイルだけを弾ける水準。
		maxFileSize: 100 << 20,
		// 50,000 行: 「記入方法」シートの案内どおり 1 件ずつ最低 1 秒間隔で
		// 送信し、更新では競合確認も行う。推奨値ではなく「これ以上は
		// 現実的に実行できない・メモリも危ない」という安全弁として置く。
		maxDataRows: 50_000,
		// 64 列: 課題抽出 Excel をそのまま取り込む場合、取り込みでは使わない列
		// (作成日時・更新日時 等)が混ざるため、その分の余裕を見込む。
		extraColumns: 64,
		// 1GB: zip 展開後の合計サイズ上限(excelize 既定の 16GB を絞る)。
		unzipSizeLimit: 1 << 30,
		// 16MB: ワークシート XML をメモリへ載せる上限(excelize 既定と同じ)。
		// これを超える XML は excelize が一時ディレクトリへ書き出すため、
		// 値を大きくするとかえってメモリを消費する。あえて既定値のまま明示する。
		unzipXMLSizeLimit: 16 << 20,
	}
}

// parseWorkbook は xlsx を読み、テンプレート列を解決した行を返す。
//
// シートは「issueKey 列を持つ最初のシート」を対象にする
// (抽出 Excel の「情報」シートのような付随シートを自然に読み飛ばせる)。
// defs はプロジェクトのカスタム属性定義。「属性:{定義名}」列の解決に使う。
func parseWorkbook(path string, defs []customfield.Def) (*sheetData, error) {
	return parseWorkbookWithLimits(path, defs, defaultParseLimits())
}

// parseWorkbookWithLimits は上限を指定して xlsx を読む(テストから上限を注入する)。
func parseWorkbookWithLimits(path string, defs []customfield.Def, lim parseLimits) (*sheetData, error) {
	// 開く前にファイルサイズを確認する(展開してからでは手遅れなため)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("Excel ファイルを開けません: %w", err)
	}
	if info.Size() > lim.maxFileSize {
		return nil, fmt.Errorf("Excel ファイルが大きすぎます(%s。上限は %s です)。行を分けて取り込んでください",
			formatFileSize(info.Size()), formatFileSize(lim.maxFileSize))
	}

	f, err := excelize.OpenFile(path, excelize.Options{
		UnzipSizeLimit:    lim.unzipSizeLimit,
		UnzipXMLSizeLimit: lim.unzipXMLSizeLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("Excel ファイルを開けません: %w", err)
	}
	defer func() { _ = f.Close() }()

	// テンプレートに埋め込まれた対象プロジェクト ID(高 2)。
	// 取得できない場合は 0(メタ情報無し)として続行し、判断は呼び出し側に委ねる。
	projectID, err := readTemplateProjectID(f)
	if err != nil {
		return nil, err
	}
	// カスタム属性列は定義名で解決するため、名前の索引を先に作る
	// (定義名が空・重複しているプロジェクトはここで取り込みを止める)。
	// 正規化はヘッダ照合と同じ normalizeHeader を使う。
	customByName, err := customfield.DefsByName(defs, normalizeHeader)
	if err != nil {
		return nil, err
	}
	// 列数上限は「固定列 + カスタム属性定義数 + 余裕」。プロジェクトごとに
	// 妥当な幅が変わるため、固定値ではなく定義数から求める。
	maxColumns := fixedColumnCount + len(defs) + lim.extraColumns

	for _, name := range f.GetSheetList() {
		data, err := parseSheet(f, name, customByName, maxColumns, lim.maxDataRows)
		if err != nil {
			return nil, err
		}
		if data == nil {
			continue // 対象シートではない
		}
		data.projectID = projectID
		return data, nil
	}
	return nil, errors.New("issueKey 列が見つかりません(一括更新テンプレートの Excel を指定してください)")
}

// parseSheet は 1 シートを読み、テンプレート列を解決した行を返す。
// 対象シート(issueKey 列を持つシート)でない場合は (nil, nil) を返す。
//
// 行はストリーム(excelize の Rows)で読む。GetRows は全行を [][]string へ
// 展開してから返すため、上限超過を判定する前にメモリを食い尽くしてしまう。
// Rows なら上限に達した時点で打ち切れる(R6)。
//
// なお Rows は Excel の行番号どおりに 1 行ずつ進み(データの無い行は
// 空スライスを返す)、GetRows と同じ行番号になる。
func parseSheet(f *excelize.File, name string, customByName map[string]customfield.Def,
	maxColumns, maxDataRows int) (*sheetData, error) {
	rows, err := f.Rows(name)
	if err != nil {
		return nil, fmt.Errorf("シート %q を読み取れません: %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		return nil, nil // 空シート
	}
	header, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("シート %q を読み取れません: %w", name, err)
	}
	colOf, columns, err := mapHeaders(header, customByName)
	if err != nil {
		return nil, err
	}
	if !columns[colIssueKey] {
		return nil, nil // 対象シートではない
	}
	// 列数の確認は対象シートに限る(付随シートの「マスタ」等は列幅の事情が異なる)
	if len(header) > maxColumns {
		return nil, fmt.Errorf("列が多すぎます(シート %q のヘッダ行に %d 列。上限は %d 列です)。テンプレートを出力し直して使用してください",
			name, len(header), maxColumns)
	}

	data := &sheetData{columns: columns}
	rowNo := 1    // ヘッダ行を読み終えた時点の行番号
	nonEmpty := 0 // 物理的に空でない行の数(行数上限のカウント対象)
	for rows.Next() {
		rowNo++
		row, err := rows.Columns()
		if err != nil {
			return nil, fmt.Errorf("シート %q を読み取れません: %w", name, err)
		}
		// ヘッダより広い行(既知列の外へ大量のセルを置いたファイル)も弾く
		if len(row) > maxColumns {
			return nil, fmt.Errorf("列が多すぎます(シート %q の %d 行目に %d 列。上限は %d 列です)。テンプレートを出力し直して使用してください",
				name, rowNo, len(row), maxColumns)
		}
		// 行数上限は「取り込む行」ではなく「物理的に空でない行」に対して適用する。
		// 既知列が空でも読み取りの負荷は同じで、未知列だけを埋めた巨大ファイルを
		// 素通しさせないため(中 4)。判定はセルの内容ではなく「セルが 1 つでも
		// 存在するか」(len > 0)で行う。空白のみのセルや空文字に評価される数式も
		// 読み取り負荷は同じであり、内容で判定すると上限を回避できてしまう。
		if len(row) == 0 {
			continue // セルを 1 つも持たない完全な空行だけを無視する
		}
		nonEmpty++
		if nonEmpty > maxDataRows {
			return nil, fmt.Errorf("データ行が多すぎます(上限は %s 行です)。ファイルを分けて取り込んでください",
				formatThousands(maxDataRows))
		}
		r := rawRow{rowNo: rowNo, cells: map[string]string{}}
		empty := true
		for idx, key := range colOf {
			if idx >= len(row) {
				continue
			}
			v := strings.TrimSpace(row[idx])
			if v == "" {
				continue
			}
			r.cells[key] = v
			empty = false
		}
		if empty {
			continue // 既知列に値が無い行は取り込み対象外
		}
		data.rows = append(data.rows, r)
	}
	if err := rows.Error(); err != nil {
		return nil, fmt.Errorf("シート %q を読み取れません: %w", name, err)
	}
	if len(data.rows) == 0 {
		return nil, errors.New("データ行がありません(ヘッダ行のみのファイルです)")
	}
	return data, nil
}

// formatFileSize はファイルサイズを利用者向けに整形する(1MB 未満はバイト表記)。
func formatFileSize(n int64) string {
	const mb = 1 << 20
	if n >= mb {
		return fmt.Sprintf("%.1fMB", float64(n)/mb)
	}
	return fmt.Sprintf("%d バイト", n)
}

// formatThousands は件数を 3 桁区切りで整形する(「記入方法」シートの表記に合わせる)。
func formatThousands(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// readTemplateProjectID は「記入方法」シートに埋め込まれた対象プロジェクト ID を読む(高 2)。
//
// 見出し(export.BulkProjectIDLabel)が A 列にある行の B 列を数値として解釈する。
// シートや行が無い場合は 0 を返す(旧テンプレート・抽出結果を加工したファイル)。
// 行はあるのに値が数値でない場合は、誤ったプロジェクトへの書き込みを防ぐため
// エラーにする(黙って「メタ情報無し」として扱わない)。
func readTemplateProjectID(f *excelize.File) (int64, error) {
	for _, name := range f.GetSheetList() {
		if name != export.SheetBulkGuide {
			continue
		}
		// 行はストリームで読む(全行の展開を避ける。R6)
		rows, err := f.Rows(name)
		if err != nil {
			return 0, fmt.Errorf("シート %q を読み取れません: %w", name, err)
		}
		id, err := scanTemplateProjectID(rows, name)
		_ = rows.Close()
		if err != nil {
			return 0, err
		}
		return id, nil
	}
	return 0, nil
}

// maxGuideScanRows は「記入方法」シートでプロジェクト ID の見出しを探す行数の上限。
//
// 出力側(export.ExportBulkTemplate)は 1 行目を見出し(項目・説明)、
// 2 行目をプロジェクト ID 行として書き、以降は固定の説明行(20 行程度)しか
// 続かない。したがって先頭 100 行まで見れば十分で、これを超えて探すのは
// 細工されたシートを延々と走査するだけになる(中 4)。
const maxGuideScanRows = 100

// scanTemplateProjectID は「記入方法」シートの先頭 maxGuideScanRows 行から
// 対象プロジェクト ID を探す。見つからなければ 0(メタ情報無し)を返す。
func scanTemplateProjectID(rows *excelize.Rows, name string) (int64, error) {
	scanned := 0
	for rows.Next() {
		scanned++
		if scanned > maxGuideScanRows {
			return 0, nil // 走査上限。見出し無し(旧テンプレート等)として扱う
		}
		row, err := rows.Columns()
		if err != nil {
			return 0, fmt.Errorf("シート %q を読み取れません: %w", name, err)
		}
		if len(row) == 0 || normalizeHeader(row[0]) != normalizeHeader(export.BulkProjectIDLabel) {
			continue
		}
		if len(row) < 2 || strings.TrimSpace(row[1]) == "" {
			return 0, fmt.Errorf("「%s」シートの %s に値がありません。テンプレートから出力した Excel を使用してください",
				export.SheetBulkGuide, export.BulkProjectIDLabel)
		}
		id, perr := strconv.ParseInt(strings.TrimSpace(row[1]), 10, 64)
		if perr != nil || id <= 0 {
			return 0, fmt.Errorf("「%s」シートの %s が不正です(%q)。テンプレートから出力した Excel を使用してください",
				export.SheetBulkGuide, export.BulkProjectIDLabel, strings.TrimSpace(row[1]))
		}
		return id, nil
	}
	if err := rows.Error(); err != nil {
		return 0, fmt.Errorf("シート %q を読み取れません: %w", name, err)
	}
	return 0, nil
}

// mapHeaders はヘッダ行から「列インデックス → 列キー」と存在する列の集合を作る。
// 同じ列キーに対応するヘッダが複数あるファイルは、どちらを使うか決められないためエラーにする。
//
// 「属性:{定義名}」のヘッダはカスタム属性列として定義名で解決する。
// 定義に無い名前はエラーにする(黙って無視すると、記入した内容が反映されない
// まま実行され、利用者は更新されたと誤解する)。
func mapHeaders(header []string, customByName map[string]customfield.Def) (map[int]string, map[string]bool, error) {
	colOf := map[int]string{}
	columns := map[string]bool{}
	seen := map[string]string{} // 列キー → 最初に見つかったヘッダ名
	for i, h := range header {
		key, err := headerColumnKey(h, customByName)
		if err != nil {
			return nil, nil, err
		}
		if key == "" {
			continue // 未知の列は無視する(作成日時 等)
		}
		if first, dup := seen[key]; dup {
			return nil, nil, fmt.Errorf("同じ意味の列が重複しています(%q と %q)", first, strings.TrimSpace(h))
		}
		seen[key] = strings.TrimSpace(h)
		colOf[i] = key
		columns[key] = true
	}
	return colOf, columns, nil
}

// headerColumnKey はヘッダ 1 つを列キーへ解決する(該当しない列は空文字)。
func headerColumnKey(header string, customByName map[string]customfield.Def) (string, error) {
	norm := normalizeHeader(header)
	if name, ok := strings.CutPrefix(norm, customHeaderPrefix); ok {
		def, found := customByName[strings.TrimSpace(name)]
		if !found {
			return "", fmt.Errorf("カスタム属性「%s」の定義が見つかりません(テンプレートを出力し直してください)",
				strings.TrimPrefix(strings.TrimSpace(header), export.BulkCustomColumnPrefix))
		}
		return customColKey(def.ID), nil
	}
	return headerAliases[norm], nil
}
