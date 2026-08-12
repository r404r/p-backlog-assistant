package main

// app_export.go は課題・ユーザの Excel 出力と、画面の列選択に供給する
// 列メタデータのバインディング。
// 一括更新テンプレート・実行結果の出力は app_bulk.go にある
// (ここの出力上限・打ち切りの仕組みは、そちらからも使う)。

import (
	"errors"
	"log/slog"

	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// ExportResultDTO は Excel 出力結果(キャンセル時は path 空・rows 0)。
type ExportResultDTO struct {
	Path string `json:"path"`
	Rows int    `json:"rows"`
	// Unverifiable はカスタム属性条件を判定できず、出力対象から外れた課題の件数
	// (課題抽出の Excel 出力のみ。他の出力では常に 0)。
	// 出力ファイルは「条件に合う全件」ではないため、0 でなければ画面で警告する。
	Unverifiable int `json:"unverifiable"`
}

// exportSearchLimit は Excel 出力の件数上限。フロント契約では「条件一致全件」
// を出力するため、実質無制限の大きな値を使う。
const exportSearchLimit = 1_000_000

// errExportRowLimit は出力対象が exportSearchLimit を超えたことを表す番兵エラー。
//
// 課題は全件をメモリへ載せず 1 件ずつ書き出すため(R4)、総件数が判明するのは
// 走査を終えた時点になる。上限判定は走査の途中で行い、超えた行に達した時点で
// このエラーで打ち切る(残りを読み切ってから判定するより速く終わる)。
// 打ち切った出力は一時ファイルごと破棄されるため、出力先の既存ファイルは
// 変更されない(export.writeFileAtomic。R5)。
var errExportRowLimit = errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください")

// limitedIssueVisitor は「limit 件までは yield へ渡し、超えたら errExportRowLimit で
// 打ち切る」課題ビジターを返す(上限を超えた行は書き出さない)。
func limitedIssueVisitor(limit int, yield func(*store.Issue) error) store.IssueVisitor {
	n := 0
	return func(is *store.Issue) error {
		n++
		if n > limit {
			return errExportRowLimit
		}
		return yield(is)
	}
}

// hasColumn は列キー列に指定のキーが含まれるかを返す。
func hasColumn(columns []string, key string) bool {
	for _, c := range columns {
		if c == key {
			return true
		}
	}
	return false
}

// GetIssueExportColumns は課題抽出の列選択に出す固定列(列キー・表示ラベル・
// 既定選択)を返す(R14)。
//
// 以前はフロントエンドが同じ一覧をハードコードしており、Excel のヘッダと
// ずれていた(画面「作成日」/ Excel「作成日時」)。ラベルの正は Go 側の列定義とする。
//
// カスタム属性列(cf_{定義ID})は含めない。画面は絞り込み UI のために
// GetMasterData でカスタム属性の定義を取得しており、その定義から列キーと
// ラベル(定義名)を組み立てられるため、同じ定義をここで再取得しない
// (プロファイル ID・プロジェクト ID を引数に取らずに済み、
// プロジェクト切替時の追加の非同期取得も増えない)。
func (a *App) GetIssueExportColumns() ([]export.ColumnMeta, error) {
	return export.IssuePickerColumns(), nil
}

// GetUserExportColumns はユーザ抽出の列選択に出す列を返す(R14。GetIssueExportColumns と同じ趣旨)。
func (a *App) GetUserExportColumns() ([]export.ColumnMeta, error) {
	return export.UserPickerColumns(), nil
}

// ExportIssuesExcel は検索条件に一致する課題全件を Excel に出力する。
// 保存先は OS の保存ダイアログでユーザが選択する(キャンセル時は path 空)。
//
// 課題はローカル DB のカーソルから 1 件ずつ受け取り、そのまま StreamWriter へ
// 流す(R4)。以前は最大 100 万件を []store.Issue へ載せてから書き出しており、
// 生 JSON・詳細を含む大規模プロジェクトでメモリ枯渇の恐れがあった。
func (a *App) ExportIssuesExcel(profileID string, query store.IssueFilter, columns []string) (*ExportResultDTO, error) {
	lg := a.begin("ExportIssuesExcel",
		append(a.searchAttrs(profileID, query), slog.Int("columns", len(columns)))...)
	s, err := a.svc()
	if err != nil {
		return nil, lg.fail(err)
	}
	// 抽出条件の不備は走査を始める前に弾く。逐次出力では条件エラーが
	// 保存ダイアログの後まで表面化しないため(R4)。
	if err := store.ValidateIssueFilter(query); err != nil {
		return nil, lg.fail(err)
	}
	// カスタム属性列が選ばれている場合のみ、ヘッダ(定義名)と値の解決に必要な
	// 定義を取得する(選ばれていなければ API 呼び出しを増やさない)。
	// 保存先を尋ねる前に取得し、失敗した場合はダイアログを出さずにエラーを返す
	// (利用者が明示的に選んだ列を黙って空欄・欠落にしない)。
	opts := export.Options{Columns: columns}
	if export.HasCustomColumns(columns) {
		master, err := s.GetMasterData(a.ctx, profileID, query.ProjectID)
		if err != nil {
			return nil, lg.fail(err)
		}
		opts.CustomFields = master.CustomFields
	}
	// 親課題キー列が選ばれている場合のみ、親課題 ID → 課題キーの対応表を作る
	// (ローカル DB の走査を増やさない。引き当てられない親は ID:<数値> になる)
	if hasColumn(columns, export.ParentIssueKeyColumn) {
		keys, err := s.ListIssueKeysByID(a.ctx, profileID, query.ProjectID)
		if err != nil {
			return nil, lg.fail(err)
		}
		opts.ParentIssueKeys = keys
	}
	path, err := a.saveExcelDialog("Excel 出力先を選択", "backlog-issues.xlsx")
	if err != nil {
		return nil, lg.fail(err)
	}
	if path == "" { // ユーザがキャンセル
		lg.done(slog.Bool("canceled", true))
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	// 保存先・ファイル名はユーザが決めるため、ローカルユーザ名や顧客名を
	// 含みうる。パスもファイル名も記録せず、拡張子だけを残す(低 1)。
	lg.add(fileExtAttr(path))
	// ローカル DB のカーソル走査を Excel 出力のイテレータへ直結する。
	// 課題はこのクロージャを通り抜けるだけで、どこにも溜まらない。
	var res store.IssueIterateResult
	seq := func(yield func(*store.Issue) error) error {
		var err error
		res, err = s.IterateIssues(a.ctx, profileID, query,
			limitedIssueVisitor(exportSearchLimit, yield))
		return err
	}
	// 上限超過(errExportRowLimit)はそのまま画面へ返る。黙って部分出力しないのは
	// 従来どおりで、判定の時点だけが「取得後」から「書き出し中」に変わっている。
	if err := export.ExportIssuesToFile(path, seq, opts); err != nil {
		// 失敗時のエラーメッセージにも保存先のフルパスが含まれるため、
		// ログへ渡す前にプレースホルダへ置換する(高 2 / 2 回目 低 1)。
		// 画面へ返すエラーは、ユーザ自身が選んだパスなのでそのままにする。
		return nil, lg.failMasked(err, path)
	}
	lg.done(slog.Int("rows", res.Total), slog.Int("unverifiable", res.Unverifiable))
	// 判定できず出力から外れた件数も返す(黙って欠落させない)
	return &ExportResultDTO{Path: path, Rows: res.Total, Unverifiable: res.Unverifiable}, nil
}

// ExportUsersExcel は条件に一致するユーザ全件を Excel に出力する。
//
// 課題出力(R4)と違い、こちらは全件を一度にメモリへ載せたままにしている。
// ユーザはスペース全体でも数百〜数千件で、1 行あたりの情報量も小さく
// (生 JSON・詳細本文のような大きな列を持たない)、逐次化しても得られる
// メモリ削減より、所属チーム・参加プロジェクトを行へ畳み込む既存処理を
// 崩す方の危険が大きいため。件数が問題になる規模になったら
// store.IterateIssues と同じ形で逐次化する。
func (a *App) ExportUsersExcel(profileID string, query store.UserFilter, columns []string) (*ExportResultDTO, error) {
	lg := a.begin("ExportUsersExcel",
		append(userAttrs(profileID, query), slog.Int("columns", len(columns)))...)
	s, err := a.svc()
	if err != nil {
		return nil, lg.fail(err)
	}
	query.Limit = exportSearchLimit
	res, err := s.ListUsers(a.ctx, profileID, query)
	if err != nil {
		return nil, lg.fail(err)
	}
	if res.Truncated {
		return nil, lg.fail(errors.New("対象件数が上限(100 万件)を超えています。条件で絞り込んでください"))
	}
	path, err := a.saveExcelDialog("Excel 出力先を選択", "backlog-users.xlsx")
	if err != nil {
		return nil, lg.fail(err)
	}
	if path == "" { // ユーザがキャンセル
		lg.done(slog.Bool("canceled", true))
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	exportRows := make([]export.UserExportRow, 0, len(res.Users))
	for _, u := range res.Users {
		exportRows = append(exportRows, export.UserExportRow{
			ID:               u.ID,
			UserCode:         u.UserCode,
			Name:             u.Name,
			MailAddress:      u.MailAddress,
			RoleType:         u.RoleType,
			RoleName:         u.RoleName,
			TeamNames:        u.TeamNames,
			ProjectKeys:      u.ProjectKeys,
			AdminProjectKeys: u.AdminProjectKeys,
		})
	}
	lg.add(fileExtAttr(path))
	if err := export.ExportUsersToFile(path, exportRows, export.UserOptions{Columns: columns}); err != nil {
		return nil, lg.failMasked(err, path)
	}
	lg.done(slog.Int("rows", len(exportRows)))
	return &ExportResultDTO{Path: path, Rows: len(exportRows)}, nil
}
