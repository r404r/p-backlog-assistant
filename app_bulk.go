package main

// app_bulk.go は一括更新・追加(画面 3)のバインディング
// (テンプレート出力 → 取り込み・dry-run → 実行・中断 → 履歴・結果出力)と、
// そこで使うマスタデータの受け渡し。
//
// 生 JSON からの補完(rawIssueIDs・parentIssueKeyOf・bulkCustomFieldValues)と
// DTO への詰め替えは、いずれも export / フロント契約の型へ寄せる処理なので
// service 層へは移していない(移すと service が export に依存してしまう。R13)。
// 生 JSON の重複パース自体の解消は別課題(R11)。

import (
	"encoding/json"
	"log/slog"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
)

// rawIssueIDs は課題の raw_json から種別 ID・優先度 ID を取り出す
// (store.Issue は名前のみ保持しているため、テンプレートの ID 列はここで補完する)。
func rawIssueIDs(rawJSON string) (issueTypeID, priorityID int64) {
	var v struct {
		IssueType struct {
			ID int64 `json:"id"`
		} `json:"issueType"`
		Priority struct {
			ID int64 `json:"id"`
		} `json:"priority"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &v); err != nil {
		return 0, 0
	}
	return v.IssueType.ID, v.Priority.ID
}

// parentIssueKeyOf は課題の raw_json から親課題の表記(CF5)を作る。
//
// 同一プロジェクトの親は課題キー、ローカルに無い親(未同期・別プロジェクト)は
// ID:<数値>、親なし・生 JSON が読めない課題は空文字になる。
// 課題抽出・テンプレート出力の両方で同じ表記を使い、往復できるようにする。
func parentIssueKeyOf(rawJSON string, keys map[int64]string) string {
	return export.FormatParentIssueRef(store.ParentIssueID(rawJSON), keys)
}

// bulkCustomFieldValues は課題の raw_json からカスタム属性の現在値を
// 「定義 ID → 表示文字列」で取り出す(テンプレートのプリフィル用。CF3)。
// 課題抽出のプレビュー(issueRowDTOOf)も同じ解釈を使う。
//
// 解釈できない生 JSON は空として扱い、テンプレート出力全体を止めない
// (課題出力と同じ流儀。異常の検知は同期・customfield 側の責務)。
func bulkCustomFieldValues(rawJSON string) map[int64]string {
	if rawJSON == "" {
		return nil
	}
	values, err := customfield.ParseValues(rawJSON)
	if err != nil {
		return nil
	}
	out := make(map[int64]string, len(values))
	for _, v := range values {
		out[v.ID] = customfield.FormatValue(v)
	}
	return out
}

// bulkTemplateMasters はテンプレートの「マスタ」シートに載せる選択候補を集める。
//
// 種別・状態・優先度は API のマスタ(取り込み時の検証と同じ内容)、
// 担当者はローカルのプロジェクト参加者(未同期ならスペース全体)を使う。
// export へ渡す型に詰め替えることで、export が bulk・store に依存しないようにする。
//
// この詰め替えを service 層へ移さないのは、export の型(表示・出力の都合で決まる形)
// へ寄せる処理であり、service を export に依存させてしまうため(R13)。
func (a *App) bulkTemplateMasters(s *service.ProfileService, profileID string, projectID int64) (export.BulkTemplateMasters, error) {
	var out export.BulkTemplateMasters
	master, err := s.GetMasterData(a.ctx, profileID, projectID)
	if err != nil {
		return out, err
	}
	out.IssueTypes = namedRefsOf(master.IssueTypes)
	out.Statuses = namedRefsOf(master.Statuses)
	out.Priorities = namedRefsOf(master.Priorities)
	// カスタム属性は列の生成・選択肢のドロップダウンに使う(CF3)
	out.CustomFields = master.CustomFields

	users, err := s.ListAssigneeCandidates(a.ctx, profileID, projectID)
	if err != nil {
		return out, err
	}
	out.Assignees = make([]export.NamedRef, 0, len(users))
	for _, u := range users {
		out.Assignees = append(out.Assignees, export.NamedRef{ID: u.ID, Name: u.Name})
	}
	return out, nil
}

// namedRefsOf はマスタ(bulk.NamedID)を export の候補型へ詰め替える。
func namedRefsOf(items []bulk.NamedID) []export.NamedRef {
	out := make([]export.NamedRef, 0, len(items))
	for _, it := range items {
		out = append(out, export.NamedRef{ID: it.ID, Name: it.Name})
	}
	return out
}

// bulkTemplateRowOf は課題 1 件をテンプレート行へ詰め替える。
//
// 逐次書き出し(R4)では課題を保持しないため、変換は 1 件のみで完結させる
// (種別 ID・優先度 ID・親課題・カスタム属性はいずれも当該課題の生 JSON と、
// 事前に用意した課題キーの対応表だけで決まる)。
func bulkTemplateRowOf(is *store.Issue, parentKeys map[int64]string) export.BulkTemplateRow {
	typeID, priorityID := rawIssueIDs(is.RawJSON)
	return export.BulkTemplateRow{
		IssueKey:       is.IssueKey,
		Summary:        is.Summary,
		IssueTypeID:    typeID,
		IssueTypeName:  is.IssueTypeName,
		StatusID:       is.StatusID,
		StatusName:     is.StatusName,
		PriorityID:     priorityID,
		PriorityName:   is.PriorityName,
		AssigneeID:     is.AssigneeID,
		AssigneeName:   is.AssigneeName,
		DueDate:        is.DueDate,
		Description:    is.Description,
		ParentIssueKey: parentIssueKeyOf(is.RawJSON, parentKeys),
		BaseUpdated:    is.Updated,
		CustomFields:   bulkCustomFieldValues(is.RawJSON),
	}
}

// ExportBulkTemplate は一括更新テンプレート(既存課題 + base_updated)を Excel 出力する。
//
// 課題抽出の Excel 出力と同じく、ローカル DB のカーソルから 1 件ずつ受け取って
// StreamWriter へ流す(R4)。
func (a *App) ExportBulkTemplate(profileID string, projectID int64, query store.IssueFilter) (*ExportResultDTO, error) {
	lg := a.begin("ExportBulkTemplate",
		slog.String("profileId", profileID), slog.Int64("projectId", projectID))
	s, err := a.svc()
	if err != nil {
		return nil, lg.fail(err)
	}
	query.ProjectID = projectID
	// 抽出条件の不備は保存ダイアログを出す前に弾く(課題抽出の出力と同じ理由)。
	if err := store.ValidateIssueFilter(query); err != nil {
		return nil, lg.fail(err)
	}
	// 名前で編集できるようにするため、テンプレートへ選択候補(種別・状態・優先度・担当者)を載せる。
	// 保存先を尋ねる前に取得し、失敗した場合はダイアログを出さずに終わる。
	masters, err := a.bulkTemplateMasters(s, profileID, projectID)
	if err != nil {
		return nil, lg.fail(err)
	}
	// 親課題キーのプリフィル(CF5)に使う「課題 ID → 課題キー」。
	// テンプレートには常に親課題キー列が付くため、こちらは常に取得する。
	parentKeys, err := s.ListIssueKeysByID(a.ctx, profileID, projectID)
	if err != nil {
		return nil, lg.fail(err)
	}
	path, err := a.saveExcelDialog("テンプレートの出力先を選択", "backlog-bulk-template.xlsx")
	if err != nil {
		return nil, lg.fail(err)
	}
	if path == "" { // ユーザがキャンセル
		lg.done(slog.Bool("canceled", true))
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	lg.add(fileExtAttr(path))
	// 課題 → テンプレート行の変換をカーソル走査の中で行い、行は書き出したら捨てる。
	var res store.IssueIterateResult
	seq := func(yield func(*export.BulkTemplateRow) error) error {
		var err error
		res, err = s.IterateIssues(a.ctx, profileID, query,
			limitedIssueVisitor(exportSearchLimit, func(is *store.Issue) error {
				row := bulkTemplateRowOf(is, parentKeys)
				return yield(&row)
			}))
		return err
	}
	// 上限超過(errExportRowLimit)は課題抽出と同じ扱い(部分出力せずエラー)。
	if err := export.ExportBulkTemplateToFile(path, projectID, seq, masters); err != nil {
		return nil, lg.failMasked(err, path)
	}
	lg.done(slog.Int("rows", res.Total))
	return &ExportResultDTO{Path: path, Rows: res.Total}, nil
}

// ImportBulkFile は記入済み Excel を選択して取り込み、検証 + dry-run プレビューを返す。
// ファイル選択キャンセル時は jobId=0 かつ totalRows=0 を返す(フロント契約)。
func (a *App) ImportBulkFile(profileID string, projectID int64, defaultPriorityID int64) (*bulk.ImportResult, error) {
	lg := a.begin("ImportBulkFile",
		slog.String("profileId", profileID),
		slog.Int64("projectId", projectID),
		slog.Int64("defaultPriorityId", defaultPriorityID))
	s, err := a.svc()
	if err != nil {
		return nil, lg.fail(err)
	}
	path, err := a.openExcelDialog("記入済みの Excel ファイルを選択")
	if err != nil {
		return nil, lg.fail(err)
	}
	if path == "" { // ユーザがキャンセル
		lg.done(slog.Bool("canceled", true))
		return &bulk.ImportResult{
			ProjectID: projectID,
			Errors:    []bulk.RowError{},
			Previews:  []bulk.RowPreview{},
			Warnings:  []string{},
		}, nil
	}
	lg.add(fileExtAttr(path))
	res, err := s.ImportBulkFile(a.ctx, profileID, projectID, path, defaultPriorityID)
	if err != nil {
		return nil, lg.failMasked(err, path)
	}
	if res.Errors == nil {
		res.Errors = []bulk.RowError{}
	}
	if res.Previews == nil {
		res.Previews = []bulk.RowPreview{}
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	// 警告本文はプロジェクト情報等を含みうるため件数のみ記録する
	lg.done(
		slog.Int64("jobId", res.JobID),
		slog.Int("totalRows", res.TotalRows),
		slog.Int("creates", res.Creates),
		slog.Int("updates", res.Updates),
		slog.Int("errors", len(res.Errors)),
		slog.Int("warnings", len(res.Warnings)),
		slog.Bool("valid", res.Valid))
	return res, nil
}

// RunBulkJob は取り込み済みジョブを実行する(1 件ずつ POST/PATCH。進捗は
// Wails イベント 'bulk:progress' {jobId, processed, total} で通知)。
func (a *App) RunBulkJob(profileID string, jobID int64, force, resendSending bool) (*bulk.RunResult, error) {
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("jobId", jobID),
		slog.Bool("force", force),
		slog.Bool("resendSending", resendSending),
	}
	return appOp(a, "RunBulkJob", attrs,
		func(s *service.ProfileService) (*bulk.RunResult, []slog.Attr, error) {
			onProgress := func(p bulk.Progress) {
				wailsruntime.EventsEmit(a.ctx, "bulk:progress", map[string]any{
					"jobId":     jobID,
					"processed": p.Processed,
					"total":     p.Total,
				})
			}
			res, err := s.RunBulkJob(a.ctx, profileID, jobID,
				bulk.RunOptions{Force: force, ResendSending: resendSending}, onProgress)
			if err != nil {
				return nil, nil, err
			}
			if res.Warnings == nil {
				res.Warnings = []string{}
			}
			// 警告本文は課題キー等を含みうるため件数のみ記録する
			return res, []slog.Attr{
				slog.Int("done", res.Done),
				slog.Int("failed", res.Failed),
				slog.Int("conflict", res.Conflict),
				slog.Int("skipped", res.Skipped),
				slog.Int("warnings", len(res.Warnings)),
				slog.Int64("durationMs", res.DurationMs),
			}, nil
		})
}

// CancelBulkRun は実行中の一括ジョブへキャンセルを要求する(行間で反映される)。
// ジョブ ID はプロファイルごとの採番のため、プロファイル ID と併せて指定する(中 2)。
func (a *App) CancelBulkRun(profileID string, jobID int64) error {
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("jobId", jobID)}
	return appOpErr(a, "CancelBulkRun", attrs,
		func(s *service.ProfileService) ([]slog.Attr, error) {
			s.CancelBulkRun(profileID, jobID)
			return nil, nil
		})
}

// ListBulkJobs は一括ジョブの履歴(行数集計付き)を返す。
func (a *App) ListBulkJobs(profileID string) ([]store.JobSummary, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "ListBulkJobs", attrs,
		func(s *service.ProfileService) ([]store.JobSummary, []slog.Attr, error) {
			jobs, err := s.ListBulkJobs(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			if jobs == nil {
				jobs = []store.JobSummary{}
			}
			return jobs, []slog.Attr{slog.Int("jobs", len(jobs))}, nil
		})
}

// BulkJobRowDTO は一括ジョブの行明細 1 行(フロント契約)。
//
// payload(送信内容)・baseUpdated は返さない。課題本文・件名を含みうるうえ、
// 画面での結果確認には不要なため(設計書 7 節)。
type BulkJobRowDTO struct {
	RowNo    int    `json:"rowNo"`
	IssueKey string `json:"issueKey"`
	Status   string `json:"status"`
	// StatusLabel は Status の表示名(R14)。画面が独自の対応表を持たずに済むよう、
	// 結果 Excel と同じ bulkRowStatusLabel の値をそのまま渡す。
	StatusLabel   string `json:"statusLabel"`
	ResultIssueID int64  `json:"resultIssueId"`
	Error         string `json:"error"`
}

// GetBulkJobRows はジョブの行明細を行番号順で返す(実行結果の確認用)。
func (a *App) GetBulkJobRows(profileID string, jobID int64) ([]BulkJobRowDTO, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("jobId", jobID)}
	return appOp(a, "GetBulkJobRows", attrs,
		func(s *service.ProfileService) ([]BulkJobRowDTO, []slog.Attr, error) {
			rows, err := s.GetBulkJobRows(a.ctx, profileID, jobID)
			if err != nil {
				return nil, nil, err
			}
			out := make([]BulkJobRowDTO, 0, len(rows))
			for _, r := range rows {
				out = append(out, BulkJobRowDTO{
					RowNo:         r.RowNo,
					IssueKey:      r.IssueKey,
					Status:        r.Status,
					StatusLabel:   bulkRowStatusLabel(r.Status),
					ResultIssueID: r.ResultIssueID,
					Error:         r.Error,
				})
			}
			return out, []slog.Attr{slog.Int("rows", len(out))}, nil
		})
}

// bulkRowAction は行の処理区分の表示名を返す。
// payload を解析せず、行状態と課題キー・作成された課題 ID の有無だけで判断する
// (送信内容を画面・Excel へ持ち出さないため)。
// 表示名は取り込み時のプレビューと同じ bulk.ActionLabel を使う(R14)。
//
// 作成された課題 ID(ResultIssueID)が入るのは新規追加が成立した行だけで、
// その行には作成された課題のキーも記録される。課題キーの有無より先に
// 判定しないと、成功した新規追加行が「更新」と表示されてしまう。
func bulkRowAction(row store.JobRow) string {
	switch {
	case row.Status == store.RowStatusSkip:
		return bulk.ActionLabel(bulk.ActionSkip)
	case row.ResultIssueID > 0 || row.IssueKey == "":
		return bulk.ActionLabel(bulk.ActionCreate)
	default:
		return bulk.ActionLabel(bulk.ActionUpdate)
	}
}

// bulkRowStatusLabels は行状態の表示名(画面の行明細と結果 Excel で共通。R14)。
//
// 以前は画面が独自の対応表を持っており、同じ状態が別の名前で表示されていた
// (skip が画面「対象外」/ Excel「変更なし」、sending が画面「送信中」/
// Excel「送信中(結果未確認)」)。より正確な方へ統一し、定義はここだけに置く。
var bulkRowStatusLabels = map[string]string{
	store.RowStatusPending:  "未処理",
	store.RowStatusSending:  "送信中(結果未確認)",
	store.RowStatusDone:     "完了",
	store.RowStatusError:    "失敗",
	store.RowStatusConflict: "競合",
	store.RowStatusSkip:     "変更なし",
}

// bulkRowStatusLabel は行状態の表示名を返す(未知の値はそのまま返す)。
func bulkRowStatusLabel(status string) string {
	if label, ok := bulkRowStatusLabels[status]; ok {
		return label
	}
	return status
}

// ExportBulkResultExcel はジョブの実行結果を Excel に出力する(高 5)。
// 保存先は OS の保存ダイアログでユーザが選択する(キャンセル時は path 空)。
func (a *App) ExportBulkResultExcel(profileID string, jobID int64) (*ExportResultDTO, error) {
	lg := a.begin("ExportBulkResultExcel",
		slog.String("profileId", profileID), slog.Int64("jobId", jobID))
	s, err := a.svc()
	if err != nil {
		return nil, lg.fail(err)
	}
	rows, err := s.GetBulkJobRows(a.ctx, profileID, jobID)
	if err != nil {
		return nil, lg.fail(err)
	}
	path, err := a.saveExcelDialog("実行結果の出力先を選択", "backlog-bulk-result.xlsx")
	if err != nil {
		return nil, lg.fail(err)
	}
	if path == "" { // ユーザがキャンセル
		lg.done(slog.Bool("canceled", true))
		return &ExportResultDTO{Path: "", Rows: 0}, nil
	}
	exportRows := make([]export.BulkResultRow, 0, len(rows))
	for _, r := range rows {
		exportRows = append(exportRows, export.BulkResultRow{
			RowNo:         r.RowNo,
			Action:        bulkRowAction(r),
			IssueKey:      r.IssueKey,
			ResultIssueID: r.ResultIssueID,
			Status:        bulkRowStatusLabel(r.Status),
			ErrorMessage:  r.Error,
		})
	}
	lg.add(fileExtAttr(path))
	if err := export.ExportBulkResultToFile(path, exportRows); err != nil {
		return nil, lg.failMasked(err, path)
	}
	lg.done(slog.Int("rows", len(exportRows)))
	return &ExportResultDTO{Path: path, Rows: len(exportRows)}, nil
}

// CustomFieldItemDTO はリスト系カスタム属性の選択肢
// (frontend/src/lib/backend.ts の CustomFieldItem と対)。
type CustomFieldItemDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// CustomFieldDefDTO はカスタム属性の定義
// (frontend/src/lib/backend.ts の CustomFieldDef と対)。
//
// typeName は画面での型判定・表示に使うため Go 側で解決して渡す
// (型 ID の対応表をフロントへ二重に持たせない)。
type CustomFieldDefDTO struct {
	ID          int64  `json:"id"`
	TypeID      int    `json:"typeId"`
	TypeName    string `json:"typeName"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	// ApplicableIssueTypes は適用対象の課題種別 ID(空 = 全課題種別)。
	ApplicableIssueTypes []int64              `json:"applicableIssueTypes"`
	AllowInput           bool                 `json:"allowInput"`
	AllowAddItem         bool                 `json:"allowAddItem"`
	Items                []CustomFieldItemDTO `json:"items"`
}

// MasterDataDTO は種別・優先度・状態・カスタム属性のマスタ
// (frontend/src/lib/backend.ts の MasterData と対。各配列は null を返さない)。
type MasterDataDTO struct {
	IssueTypes   []bulk.NamedID      `json:"issueTypes"`
	Priorities   []bulk.NamedID      `json:"priorities"`
	Statuses     []bulk.NamedID      `json:"statuses"`
	CustomFields []CustomFieldDefDTO `json:"customFields"`
}

// newMasterDataDTO はマスタを DTO へ写す(nil スライスは空スライスへ正規化)。
func newMasterDataDTO(md *bulk.MasterData) *MasterDataDTO {
	dto := &MasterDataDTO{
		IssueTypes:   md.IssueTypes,
		Priorities:   md.Priorities,
		Statuses:     md.Statuses,
		CustomFields: make([]CustomFieldDefDTO, 0, len(md.CustomFields)),
	}
	if dto.IssueTypes == nil {
		dto.IssueTypes = []bulk.NamedID{}
	}
	if dto.Priorities == nil {
		dto.Priorities = []bulk.NamedID{}
	}
	if dto.Statuses == nil {
		dto.Statuses = []bulk.NamedID{}
	}
	for _, def := range md.CustomFields {
		d := CustomFieldDefDTO{
			ID:                   def.ID,
			TypeID:               def.TypeID,
			TypeName:             customfield.TypeName(def.TypeID),
			Name:                 def.Name,
			Description:          def.Description,
			Required:             def.Required,
			ApplicableIssueTypes: def.ApplicableIssueTypes,
			AllowInput:           def.AllowInput,
			AllowAddItem:         def.AllowAddItem,
			Items:                make([]CustomFieldItemDTO, 0, len(def.Items)),
		}
		if d.ApplicableIssueTypes == nil {
			d.ApplicableIssueTypes = []int64{}
		}
		for _, it := range def.Items {
			d.Items = append(d.Items, CustomFieldItemDTO{ID: it.ID, Name: it.Name})
		}
		dto.CustomFields = append(dto.CustomFields, d)
	}
	return dto
}

// GetMasterData は種別・優先度・状態・カスタム属性のマスタを返す
// (取り込みの既定優先度選択などに使用)。
func (a *App) GetMasterData(profileID string, projectID int64) (*MasterDataDTO, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	return appOp(a, "GetMasterData", attrs,
		func(s *service.ProfileService) (*MasterDataDTO, []slog.Attr, error) {
			md, err := s.GetMasterData(a.ctx, profileID, projectID)
			if err != nil {
				return nil, nil, err
			}
			dto := newMasterDataDTO(md)
			return dto, []slog.Attr{
				slog.Int("issueTypes", len(dto.IssueTypes)),
				slog.Int("priorities", len(dto.Priorities)),
				slog.Int("statuses", len(dto.Statuses)),
				slog.Int("customFields", len(dto.CustomFields)),
			}, nil
		})
}
