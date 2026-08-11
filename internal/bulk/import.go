package bulk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// descriptionPreviewLen は差分表示で詳細(本文)を切り詰める文字数。
// 本文全体を UI・ログへ流さないための上限でもある(設計書 7 節)。
const descriptionPreviewLen = 40

// RowError は取り込み時のエラー(行番号 + 日本語メッセージ)。
type RowError struct {
	RowNo   int    `json:"rowNo"`
	Message string `json:"message"`
}

// RowPreview は dry-run プレビューの 1 行。
type RowPreview struct {
	RowNo    int    `json:"rowNo"`
	Action   string `json:"action"` // create / update / skip
	IssueKey string `json:"issueKey"`
	Summary  string `json:"summary"`
	// Changes は「項目: 変更前 → 変更後」の表示文字列。
	Changes []string `json:"changes"`
	// ConflictWarning は base_updated とローカルの updated が食い違う場合に真
	//(実行時に conflict となる可能性が高い行の事前警告)。
	ConflictWarning bool `json:"conflictWarning"`
}

// ImportResult は取り込み結果(ジョブ + プレビュー)。
type ImportResult struct {
	JobID     int64        `json:"jobId"` // エラーがある場合は 0(ジョブを作らない)
	ProjectID int64        `json:"projectId"`
	TotalRows int          `json:"totalRows"`
	Creates   int          `json:"creates"`
	Updates   int          `json:"updates"`
	Skips     int          `json:"skips"`
	Valid     bool         `json:"valid"`
	Errors    []RowError   `json:"errors"`
	Previews  []RowPreview `json:"previews"`
	// Warnings は取り込みを止めないが利用者へ伝えるべき注意
	//(プロジェクト ID メタが無い旧テンプレート・担当者検証の縮退・
	// 名前列と食い違う ID 列を無視した行 等)。
	Warnings []string `json:"warnings"`
}

// ImportOptions は取り込みのパラメータ。
type ImportOptions struct {
	ProjectID int64
	FilePath  string
	// DefaultPriorityID は優先度未入力の新規追加行に適用する既定値
	//(取り込み時のダイアログでプロジェクト単位に指定する。設計書 5 節)。
	DefaultPriorityID int64
	// Master は種別・状態・優先度のマスタ(FetchMasterData の結果)。
	Master MasterData
	// API は親課題の状態確認(CF5)に使う Backlog API。
	//
	// ローカルに無い親を ID:<数値> で指定した行だけが使うため、nil でも
	// 取り込み自体は動く(その場合は当該行を「確認できない」として
	// 行エラーにし、検証できないまま送信しない)。
	API API
}

// Importer は Excel の取り込み(解析・検証・dry-run・ジョブ作成)を担う。
type Importer struct {
	st *store.Store
}

// NewImporter は Importer を生成する。
func NewImporter(st *store.Store) *Importer {
	return &Importer{st: st}
}

// Import は Excel を取り込み、検証・dry-run 差分を行って結果を返す。
//
// エラー行が 1 つでもある場合はジョブを作成しない(JobID = 0)。
// 実行できない入力を永続化しても再開の役に立たず、誤って一部だけ実行される
// リスクだけが残るため。
func (im *Importer) Import(ctx context.Context, opts ImportOptions) (*ImportResult, error) {
	if opts.ProjectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	data, err := parseWorkbook(opts.FilePath, opts.Master.CustomFields)
	if err != nil {
		return nil, err
	}
	// テンプレートに埋め込まれた対象プロジェクトと UI の選択が食い違う場合は、
	// 別プロジェクトへ書き込む事故になるため取り込み自体を拒否する(高 2)
	if data.projectID != 0 && data.projectID != opts.ProjectID {
		return nil, fmt.Errorf("このテンプレートはプロジェクト ID %d 用です(選択中のプロジェクト ID は %d)。対象プロジェクトを選び直すか、正しいテンプレートを指定してください",
			data.projectID, opts.ProjectID)
	}
	hash, err := fileHash(opts.FilePath)
	if err != nil {
		return nil, err
	}

	res := &ImportResult{
		ProjectID: opts.ProjectID,
		Errors:    []RowError{},
		Previews:  []RowPreview{},
		Warnings:  []string{},
	}
	if data.projectID == 0 {
		// メタ情報が無い旧テンプレート。誤ったプロジェクトへの取り込みを
		// 自動では検知できないため、続行しつつ利用者へ確認を促す(高 2)
		res.Warnings = append(res.Warnings,
			"このファイルには対象プロジェクトの情報がありません(旧テンプレートの可能性があります)。選択中のプロジェクトが正しいか確認してください")
	}

	users, err := im.assigneeCandidates(ctx, opts.ProjectID, res)
	if err != nil {
		return nil, err
	}
	v := &validator{
		st:                im.st,
		api:               opts.API,
		projectID:         opts.ProjectID,
		defaultPriorityID: opts.DefaultPriorityID,
		idx:               newIndex(opts.Master, users),
	}
	// 親課題キー列があるファイルだけ、プロジェクト内の親子関係を 1 回走査して
	// 索引化する(「子を持つか」の判定に全課題が要るため。CF5)
	if data.columns[colParentIssueKey] {
		parents, perr := im.st.ListIssueParents(ctx, opts.ProjectID)
		if perr != nil {
			return nil, perr
		}
		v.parents = newParentIndex(parents)
	}

	plans := make([]*rowPlan, 0, len(data.rows))
	seenKeys := map[string]int{} // 課題キー → 最初に現れた行番号
	for _, row := range data.rows {
		res.TotalRows++
		if key := row.cell(colIssueKey); key != "" {
			if first, dup := seenKeys[key]; dup {
				res.Errors = append(res.Errors, RowError{
					RowNo:   row.rowNo,
					Message: fmt.Sprintf("課題 %s が %d 行目にも指定されています(同じ課題を複数行で更新できません)", key, first),
				})
				continue
			}
			seenKeys[key] = row.rowNo
		}
		plan, err := v.plan(ctx, row)
		if err != nil {
			// 行の内容が原因ではない失敗(親の状態確認での認証・レート制限・
			// 通信障害・中断)は行エラーにせず、取り込み全体を止める(CF5)
			if isFatal(err) {
				return nil, err
			}
			res.Errors = append(res.Errors, RowError{RowNo: row.rowNo, Message: err.Error()})
			continue
		}
		plans = append(plans, plan)
	}

	// 1 階層制約のうち「同一バッチ内の組み合わせ」は全行を見ないと判定できない
	// ため、行ごとの検証を終えてからまとめて確認する(CF5 の (d))。
	if v.parents != nil {
		if batchErrs := v.parents.validateBatch(plans); len(batchErrs) > 0 {
			res.Errors = append(res.Errors, batchErrs...)
			plans = dropRows(plans, batchErrs)
		}
	}
	// エラー行を除いた行だけを集計・プレビューへ載せる。
	// 行の警告(名前列と食い違う ID 列を無視した等)も受理された行のみ報告する
	// (エラー行の警告は利用者の注意を分散させるだけ)。
	for _, plan := range plans {
		res.Warnings = append(res.Warnings, plan.warnings...)
		switch plan.action {
		case ActionCreate:
			res.Creates++
		case ActionUpdate:
			res.Updates++
		default:
			res.Skips++
		}
		res.Previews = append(res.Previews, RowPreview{
			RowNo:           plan.rowNo,
			Action:          plan.action,
			IssueKey:        plan.issueKey,
			Summary:         plan.summary,
			Changes:         plan.changes,
			ConflictWarning: plan.conflictWarning,
		})
	}
	// 行番号順に並べ直す(バッチ検証のエラーは後から足されるため)
	sort.SliceStable(res.Errors, func(i, j int) bool { return res.Errors[i].RowNo < res.Errors[j].RowNo })

	res.Valid = len(res.Errors) == 0
	if !res.Valid {
		return res, nil
	}
	jobID, err := im.st.CreateJob(ctx, jobKind(res.Creates, res.Updates),
		opts.ProjectID, opts.FilePath, hash, jobRowsOf(plans))
	if err != nil {
		return nil, err
	}
	res.JobID = jobID
	return res, nil
}

// dropRows はエラーになった行番号の plan を取り除く
// (エラー行は送信対象にしないため、集計・プレビューにも載せない)。
func dropRows(plans []*rowPlan, errs []RowError) []*rowPlan {
	failed := make(map[int]bool, len(errs))
	for _, e := range errs {
		failed[e.RowNo] = true
	}
	out := plans[:0]
	for _, p := range plans {
		if !failed[p.rowNo] {
			out = append(out, p)
		}
	}
	return out
}

// AssigneeCandidates は担当者として指定できるユーザを返す(中 1)。
//
// 候補は対象プロジェクトの参加者に限定する(参加していないユーザを担当者に
// 指定しても API が拒否するため、送信前に弾いたほうが早く気づける)。
// ユーザ同期が未実施で参加者が 0 件の場合は、担当者名の解決が一切できなく
// なってしまうためスペース全体のユーザへフォールバックする
// (第 2 戻り値がフォールバックしたかどうか)。
//
// 取り込み時の検証とテンプレート出力の候補一覧で同じ集合を使うため公開している。
func AssigneeCandidates(ctx context.Context, st *store.Store, projectID int64) ([]store.UserRef, bool, error) {
	members, err := st.ListProjectUserRefs(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	if len(members) > 0 {
		return members, false, nil
	}
	all, err := st.ListUserRefs(ctx)
	if err != nil {
		return nil, false, err
	}
	return all, true, nil
}

// assigneeCandidates は担当者検証に使うユーザ候補を返し、
// スペース全体へ縮退した場合は警告を残す。
func (im *Importer) assigneeCandidates(ctx context.Context, projectID int64, res *ImportResult) ([]store.UserRef, error) {
	users, fellBack, err := AssigneeCandidates(ctx, im.st, projectID)
	if err != nil {
		return nil, err
	}
	if fellBack {
		res.Warnings = append(res.Warnings,
			"ユーザ同期が未実施のため担当者検証はスペース全体で行いました(プロジェクトに参加していない担当者は実行時に失敗する可能性があります)")
	}
	return users, nil
}

// jobKind は行の内訳からジョブ種別を決める。
func jobKind(creates, updates int) string {
	switch {
	case creates > 0 && updates > 0:
		return store.JobKindMixed
	case creates > 0:
		return store.JobKindCreate
	default:
		return store.JobKindUpdate
	}
}

// jobRowsOf は検証済みの行を job_rows の行へ変換する。
// 変更が無い行(skip)も記録し、行番号と結果レポートの対応を保つ。
func jobRowsOf(plans []*rowPlan) []store.JobRow {
	rows := make([]store.JobRow, 0, len(plans))
	for _, p := range plans {
		payload, err := EncodePayload(p.payload)
		if err != nil {
			// Payload は自前で組み立てた構造体のみで、JSON 化に失敗する要素は無い
			payload = "{}"
		}
		status := store.RowStatusPending
		if p.action == ActionSkip {
			status = store.RowStatusSkip
		}
		rows = append(rows, store.JobRow{
			RowNo:       p.rowNo,
			IssueKey:    p.issueKey,
			Payload:     payload,
			BaseUpdated: p.baseUpdated,
			Status:      status,
		})
	}
	return rows
}

// fileHash は入力ファイルの SHA-256(hex)を返す(再開時の同一性確認用)。
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("ファイルを開けません: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("ファイルを読み取れません: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// rowPlan は 1 行の検証・差分結果。
type rowPlan struct {
	rowNo           int
	action          string
	issueKey        string
	summary         string
	baseUpdated     string
	payload         Payload
	changes         []string
	conflictWarning bool
	// warnings は取り込みを止めないが利用者へ伝えるべき注意
	//(名前列と食い違う ID 列を無視した 等)。ImportResult.Warnings へ集約する。
	warnings []string
	// parent は親課題の変更内容(nil = この行は親を変更しない)。
	// バッチ全体の 1 階層検証(CF5 の (d))で使う。
	parent *parentChange
}

// index は ID・名前の解決表。
type index struct {
	issueTypeByID   map[int64]string
	issueTypeByName map[string][]int64
	priorityByID    map[int64]string
	priorityByName  map[string][]int64
	statusByID      map[int64]string
	statusByName    map[string][]int64
	userByID        map[int64]string
	userByName      map[string][]int64
	userByCode      map[string][]int64
	// customDefs はカスタム属性の定義(定義順。列の並び・差分の並びに使う)。
	customDefs []customfield.Def
	// customItems は定義 ID → 選択肢索引(リスト系のみ意味を持つ)。
	customItems map[int64]*customItems
}

func newIndex(master MasterData, users []store.UserRef) *index {
	idx := &index{
		issueTypeByID: map[int64]string{}, issueTypeByName: map[string][]int64{},
		priorityByID: map[int64]string{}, priorityByName: map[string][]int64{},
		statusByID: map[int64]string{}, statusByName: map[string][]int64{},
		userByID: map[int64]string{}, userByName: map[string][]int64{},
		userByCode:  map[string][]int64{},
		customDefs:  master.CustomFields,
		customItems: map[int64]*customItems{},
	}
	for _, def := range master.CustomFields {
		idx.customItems[def.ID] = newCustomItems(def)
	}
	add := func(byID map[int64]string, byName map[string][]int64, id int64, name string) {
		byID[id] = name
		key := normalizeHeader(name)
		if key != "" {
			byName[key] = append(byName[key], id)
		}
	}
	for _, t := range master.IssueTypes {
		add(idx.issueTypeByID, idx.issueTypeByName, t.ID, t.Name)
	}
	for _, p := range master.Priorities {
		add(idx.priorityByID, idx.priorityByName, p.ID, p.Name)
	}
	for _, s := range master.Statuses {
		add(idx.statusByID, idx.statusByName, s.ID, s.Name)
	}
	for _, u := range users {
		add(idx.userByID, idx.userByName, u.ID, u.Name)
		if code := normalizeHeader(u.UserCode); code != "" {
			idx.userByCode[code] = append(idx.userByCode[code], u.ID)
		}
	}
	return idx
}

// validator は 1 行ずつの検証・差分生成を行う。
type validator struct {
	st *store.Store
	// api は親課題の状態確認(CF5)にだけ使う。nil 可(ImportOptions.API 参照)。
	api               API
	projectID         int64
	defaultPriorityID int64
	idx               *index
	// parents はプロジェクト内の親子関係の索引(CF5)。
	// 親課題キー列が無いファイルでは nil(検証も走らない)。
	parents *parentIndex
	// rowWarnings は処理中の行で発生した警告(plan の入口で初期化する)。
	rowWarnings []string
}

// plan は 1 行を検証し、送信内容と差分を決める。
// エラーは行ごとに 1 件目で打ち切る(メッセージを読みやすく保つ)。
func (v *validator) plan(ctx context.Context, r rawRow) (*rowPlan, error) {
	v.rowWarnings = nil
	var (
		plan *rowPlan
		err  error
	)
	if r.cell(colIssueKey) == "" {
		plan, err = v.planCreate(ctx, r)
	} else {
		plan, err = v.planUpdate(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	plan.warnings = v.rowWarnings
	return plan, nil
}

// planCreate は新規追加行(issueKey が空)を検証する。
func (v *validator) planCreate(ctx context.Context, r rawRow) (*rowPlan, error) {
	// 新規追加ではクリア指定を使えない(クリアすべき既存値が無い)
	for _, key := range []string{
		colSummary, colDescription, colDueDate, colAssigneeID, colAssigneeName,
		colIssueTypeID, colIssueTypeName, colStatusID, colStatusName,
		colPriorityID, colPriorityName, colParentIssueKey,
	} {
		if r.cell(key) == ClearToken {
			return nil, errors.New("新規追加行では " + ClearToken + " を指定できません")
		}
	}
	if r.has(colStatusID) || r.has(colStatusName) {
		return nil, errors.New("新規追加行に状態は指定できません(状態は追加後の更新で変更してください)")
	}
	summary := r.cell(colSummary)
	if summary == "" {
		return nil, errors.New("件名が入力されていません(新規追加には必須です)")
	}
	// 新規追加行でも名前列を優先する(ID 列との食い違いは警告して無視する)
	issueTypeID, _, err := v.resolveNamed(r, colIssueTypeID, colIssueTypeName, "種別",
		v.idx.issueTypeByID, v.idx.issueTypeByName, false)
	if err != nil {
		return nil, err
	}
	if issueTypeID == nil {
		return nil, errors.New("種別が入力されていません(新規追加には必須です)")
	}
	priorityID, _, err := v.resolveNamed(r, colPriorityID, colPriorityName, "優先度",
		v.idx.priorityByID, v.idx.priorityByName, false)
	if err != nil {
		return nil, err
	}
	if priorityID == nil {
		if v.defaultPriorityID <= 0 {
			return nil, errors.New("優先度が入力されていません(既定の優先度も指定されていません)")
		}
		if _, ok := v.idx.priorityByID[v.defaultPriorityID]; !ok {
			return nil, fmt.Errorf("既定の優先度 ID %d は存在しません", v.defaultPriorityID)
		}
		priorityID = ptrInt64(v.defaultPriorityID)
	}
	assigneeID, _, err := v.resolveAssignee(r, false)
	if err != nil {
		return nil, err
	}

	plan := &rowPlan{rowNo: r.rowNo, action: ActionCreate, summary: summary}
	plan.payload = Payload{
		Action:      ActionCreate,
		ProjectID:   v.projectID,
		Summary:     ptrString(summary),
		IssueTypeID: issueTypeID,
		PriorityID:  priorityID,
		AssigneeID:  assigneeID,
	}
	plan.changes = append(plan.changes, fmt.Sprintf("件名: %s", summary))
	plan.changes = append(plan.changes, fmt.Sprintf("種別: %s", v.idx.issueTypeByID[*issueTypeID]))
	plan.changes = append(plan.changes, fmt.Sprintf("優先度: %s", v.idx.priorityByID[*priorityID]))
	if assigneeID != nil {
		plan.changes = append(plan.changes, fmt.Sprintf("担当者: %s", v.idx.userByID[*assigneeID]))
	}
	if due := r.cell(colDueDate); due != "" {
		normalized, err := parseDueDate(due)
		if err != nil {
			return nil, err
		}
		plan.payload.DueDate = ptrString(normalized)
		plan.changes = append(plan.changes, fmt.Sprintf("期限: %s", normalized))
	}
	if desc := r.cell(colDescription); desc != "" {
		plan.payload.Description = ptrString(desc)
		plan.changes = append(plan.changes, fmt.Sprintf("詳細: %s", summarize(desc)))
	}
	if err := v.planParent(ctx, r, nil, plan); err != nil {
		return nil, err
	}
	if err := v.planCustomFieldsCreate(r, *issueTypeID, plan); err != nil {
		return nil, err
	}
	plan.payload.Changes = plan.changes
	return plan, nil
}

// planUpdate は更新行(issueKey に値あり)を検証し、ローカルの現在値との差分を作る。
func (v *validator) planUpdate(ctx context.Context, r rawRow) (*rowPlan, error) {
	issueKey := r.cell(colIssueKey)
	cur, err := v.st.GetIssueByKey(ctx, v.projectID, issueKey)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("課題 %s がローカルデータに見つかりません(対象プロジェクトを同期してから再実行してください)", issueKey)
	}
	// 更新行は base_updated を必須にする(高 1)。
	// 欠落を許すと実行時の競合検知(リモートの更新有無の確認)を素通りしてしまい、
	// 他者の変更を黙って上書きしうる。列ごと消した Excel も同じ理由で拒否する。
	if err := validateBaseUpdated(r.cell(colBaseUpdated)); err != nil {
		return nil, err
	}

	plan := &rowPlan{
		rowNo:       r.rowNo,
		issueKey:    issueKey,
		summary:     cur.Summary,
		baseUpdated: r.cell(colBaseUpdated),
	}
	plan.payload = Payload{Action: ActionUpdate}
	// base_updated とローカルの updated が食い違う行は、実行時に conflict に
	// なる可能性が高い(Excel 出力後に同期でリモートの変更を取り込んだ場合)
	if plan.baseUpdated != "" && !sameTimestamp(plan.baseUpdated, cur.Updated) {
		plan.conflictWarning = true
	}

	// 件名
	if s := r.cell(colSummary); s != "" {
		if s == ClearToken {
			return nil, clearNotAllowed("件名")
		}
		if s != cur.Summary {
			plan.payload.Summary = ptrString(s)
			plan.summary = s
			plan.changes = append(plan.changes, change("件名", cur.Summary, s))
		}
	}
	// 種別(ローカルには名前しか無いため名前で比較する)
	issueTypeID, _, err := v.resolveNamed(r, colIssueTypeID, colIssueTypeName, "種別",
		v.idx.issueTypeByID, v.idx.issueTypeByName, false)
	if err != nil {
		return nil, err
	}
	if issueTypeID != nil {
		name := v.idx.issueTypeByID[*issueTypeID]
		if name != cur.IssueTypeName {
			plan.payload.IssueTypeID = issueTypeID
			plan.changes = append(plan.changes, change("種別", cur.IssueTypeName, name))
		}
	}
	// 状態
	statusID, _, err := v.resolveNamed(r, colStatusID, colStatusName, "状態",
		v.idx.statusByID, v.idx.statusByName, false)
	if err != nil {
		return nil, err
	}
	if statusID != nil {
		name := v.idx.statusByID[*statusID]
		changed := *statusID != cur.StatusID
		if cur.StatusID == 0 { // 旧データで status_id を持たない場合は名前で比較する
			changed = name != cur.StatusName
		}
		if changed {
			plan.payload.StatusID = statusID
			plan.changes = append(plan.changes, change("状態", cur.StatusName, name))
		}
	}
	// 優先度(ローカルには名前しか無いため名前で比較する)
	priorityID, _, err := v.resolveNamed(r, colPriorityID, colPriorityName, "優先度",
		v.idx.priorityByID, v.idx.priorityByName, false)
	if err != nil {
		return nil, err
	}
	if priorityID != nil {
		name := v.idx.priorityByID[*priorityID]
		if name != cur.PriorityName {
			plan.payload.PriorityID = priorityID
			plan.changes = append(plan.changes, change("優先度", cur.PriorityName, name))
		}
	}
	// 担当者(クリア可)
	assigneeID, assigneeCleared, err := v.resolveAssignee(r, true)
	if err != nil {
		return nil, err
	}
	switch {
	case assigneeCleared:
		if cur.AssigneeID != 0 {
			plan.payload.AssigneeID = ptrInt64(0)
			plan.changes = append(plan.changes, change("担当者", cur.AssigneeName, "(クリア)"))
		}
	case assigneeID != nil:
		if *assigneeID != cur.AssigneeID {
			plan.payload.AssigneeID = assigneeID
			plan.changes = append(plan.changes, change("担当者", cur.AssigneeName, v.idx.userByID[*assigneeID]))
		}
	}
	// 期限(クリア可)
	if due := r.cell(colDueDate); due != "" {
		curDue := normalizeStoredDate(cur.DueDate)
		if due == ClearToken {
			if curDue != "" {
				plan.payload.DueDate = ptrString("")
				plan.changes = append(plan.changes, change("期限", curDue, "(クリア)"))
			}
		} else {
			normalized, err := parseDueDate(due)
			if err != nil {
				return nil, err
			}
			if normalized != curDue {
				plan.payload.DueDate = ptrString(normalized)
				plan.changes = append(plan.changes, change("期限", curDue, normalized))
			}
		}
	}
	// 詳細(クリア可)
	if desc := r.cell(colDescription); desc != "" {
		if desc == ClearToken {
			if cur.Description != "" {
				plan.payload.Description = ptrString("")
				plan.changes = append(plan.changes, change("詳細", summarize(cur.Description), "(クリア)"))
			}
		} else if desc != cur.Description {
			plan.payload.Description = ptrString(desc)
			plan.changes = append(plan.changes, change("詳細", summarize(cur.Description), summarize(desc)))
		}
	}
	// 親課題(空欄 = 変更しない / #CLEAR# = 親子関係の解除。CF5)
	if err := v.planParent(ctx, r, cur, plan); err != nil {
		return nil, err
	}
	// カスタム属性(定義順。空欄 = 変更しない / #CLEAR# = クリア)
	if err := v.planCustomFieldsUpdate(r, cur, plan); err != nil {
		return nil, err
	}

	if len(plan.changes) == 0 {
		// 変更が 1 つも無い行は送信しない(空の PATCH は API がエラーにする)
		plan.action = ActionSkip
		return plan, nil
	}
	plan.action = ActionUpdate
	plan.payload.Changes = plan.changes
	return plan, nil
}

// resolveNamed は名前列・ID 列からマスタの ID を解決する。
//
// 名前列を常に優先する(利用者はテンプレートの「マスタ」シートのドロップダウンから
// 名前で選ぶため。ID 列は参考情報)。名前列に値があれば名前で解決し、解決できない
// 場合・曖昧な場合はエラーにする。ID 列を使うのは名前列が空の行だけ。
// 両方に値があり指す先が食い違う場合は名前列を採用し、ID 列を無視した旨を
// 行の警告として残す(高 1)。
//
// 「どちらの列が編集されたか」を現在値から推測していた頃は、出力後にリモートが
// 変化した行で古い ID 列を採用する誤更新が起きていた。推測はやめ、正となる列を
// 名前列 1 つに固定している。
//
// 戻り値の cleared は #CLEAR# が指定されたことを表す(allowClear が真のときのみ)。
func (v *validator) resolveNamed(r rawRow, idCol, nameCol, label string,
	byID map[int64]string, byName map[string][]int64, allowClear bool) (id *int64, cleared bool, err error) {

	idVal, nameVal := r.cell(idCol), r.cell(nameCol)
	// 名前列が正のため、#CLEAR# の判定も名前列を優先する(2 回目レビュー高 1)。
	// ID 列の #CLEAR# が効くのは名前列が空の行だけ。
	if nameVal == ClearToken {
		if !allowClear {
			return nil, false, clearNotAllowed(label)
		}
		v.warnIgnoredIDRaw(r.rowNo, idCol, idVal, ClearToken)
		return nil, true, nil
	}

	if nameVal != "" {
		ids := byName[normalizeHeader(nameVal)]
		switch len(ids) {
		case 0:
			return nil, false, fmt.Errorf("%s「%s」が見つかりません(「%s」シートの候補から選んでください)",
				label, nameVal, export.SheetBulkMaster)
		case 1:
			v.warnIgnoredID(r.rowNo, idCol, idVal, ids[0])
			return ptrInt64(ids[0]), false, nil
		default:
			// 名前列を正とするため、ID 列を足すだけでは解決しない(名前列を空にする必要がある)
			return nil, false, fmt.Errorf("%s「%s」は複数あり一意に決められません(%s を空にして %s を指定してください)",
				label, nameVal, nameColumnLabels[nameCol], idColumnLabels[idCol])
		}
	}
	if idVal == ClearToken { // 名前列が空の行のみ ID 列の #CLEAR# を受理する
		if !allowClear {
			return nil, false, clearNotAllowed(label)
		}
		return nil, true, nil
	}
	if idVal == "" {
		return nil, false, nil // 未指定 = 変更しない
	}
	n, perr := strconv.ParseInt(idVal, 10, 64)
	if perr != nil {
		return nil, false, fmt.Errorf("%sID は数値で入力してください(%q)", label, idVal)
	}
	if _, ok := byID[n]; !ok {
		return nil, false, fmt.Errorf("%sID %d は存在しません", label, n)
	}
	return ptrInt64(n), false, nil
}

// warnIgnoredID は名前列で解決した ID と ID 列の値が食い違う場合に行の警告を残す。
// ID 列が空、または名前列と同じ値を指す場合は何もしない
// (テンプレートは両方を出力するため、一致は通常の状態)。
// 数値として読めない ID 列も「食い違い」として無視する(名前列が正のため、
// 参考情報の書式エラーで取り込み全体を止めない)。
func (v *validator) warnIgnoredID(rowNo int, idCol, idVal string, resolved int64) {
	if n, err := strconv.ParseInt(idVal, 10, 64); err == nil && n == resolved {
		return
	}
	v.warnIgnoredIDRaw(rowNo, idCol, idVal, "")
}

// warnIgnoredIDRaw は ID 列が無視された旨の警告を残す(agree に一致する値は警告しない)。
// 名前列の #CLEAR# を採用した場合など、解決 ID との数値比較ができない経路でも使う。
func (v *validator) warnIgnoredIDRaw(rowNo int, idCol, idVal, agree string) {
	if idVal == "" || idVal == agree {
		return
	}
	v.rowWarnings = append(v.rowWarnings,
		fmt.Sprintf("%d 行目: %s 列は名前列と食い違うため無視しました", rowNo, idColumnLabels[idCol]))
}

// resolveAssignee は担当者を解決する。resolveNamed と同じく名前列を常に優先し、
// テンプレートが出力する「表示名 (ID)」形式なら括弧内の ID を使う
// (同名ユーザを区別できる唯一の手段)。
// 名前だけの場合はローカル users の名前 → ログイン ID の順に一意一致を探す
// (同名ユーザの誤選択を防ぐため、複数一致は必ずエラーにする)。
// 担当者ID 列を使うのは名前列が空の行だけで、食い違う ID 列は警告して無視する。
func (v *validator) resolveAssignee(r rawRow, allowClear bool) (id *int64, cleared bool, err error) {
	idVal, nameVal := r.cell(colAssigneeID), r.cell(colAssigneeName)
	// resolveNamed と同じく #CLEAR# も名前列を優先する(2 回目レビュー高 1)
	if nameVal == ClearToken {
		if !allowClear {
			return nil, false, clearNotAllowed("担当者")
		}
		v.warnIgnoredIDRaw(r.rowNo, colAssigneeID, idVal, ClearToken)
		return nil, true, nil
	}

	if nameVal != "" {
		resolved, rerr := v.resolveAssigneeName(nameVal)
		if rerr != nil {
			return nil, false, rerr
		}
		v.warnIgnoredID(r.rowNo, colAssigneeID, idVal, resolved)
		return ptrInt64(resolved), false, nil
	}
	if idVal == ClearToken { // 名前列が空の行のみ ID 列の #CLEAR# を受理する
		if !allowClear {
			return nil, false, clearNotAllowed("担当者")
		}
		return nil, true, nil
	}
	if idVal == "" {
		return nil, false, nil // 未指定 = 変更しない
	}
	n, perr := strconv.ParseInt(idVal, 10, 64)
	if perr != nil {
		return nil, false, fmt.Errorf("担当者ID は数値で入力してください(%q)", idVal)
	}
	if _, ok := v.idx.userByID[n]; !ok {
		return nil, false, fmt.Errorf("担当者ID %d は存在しません(ユーザ情報を同期してから再実行してください)", n)
	}
	return ptrInt64(n), false, nil
}

// resolveAssigneeName は担当者名(「表示名 (ID)」形式・表示名・ログイン ID)を
// ユーザ ID へ解決する。
func (v *validator) resolveAssigneeName(nameVal string) (int64, error) {
	if labelID, ok := export.ParseAssigneeLabel(nameVal); ok {
		if _, exists := v.idx.userByID[labelID]; !exists {
			return 0, fmt.Errorf("担当者「%s」が見つかりません(ユーザ情報を同期してから再実行してください)", nameVal)
		}
		return labelID, nil
	}
	key := normalizeHeader(nameVal)
	ids := v.idx.userByName[key]
	if len(ids) == 0 {
		ids = v.idx.userByCode[key] // 名前で見つからない場合はログイン ID として照合する
	}
	switch len(ids) {
	case 0:
		return 0, fmt.Errorf("担当者「%s」が見つかりません(「%s」シートの候補から選んでください)",
			nameVal, export.SheetBulkMaster)
	case 1:
		return ids[0], nil
	default:
		return 0, fmt.Errorf("担当者「%s」は複数あり一意に決められません(「%s」シートの候補から選んでください)",
			nameVal, export.SheetBulkMaster)
	}
}

// validateBaseUpdated は更新行の base_updated を検証する(高 1)。
// 未入力・RFC3339 として解釈できない値はエラーにする。
func validateBaseUpdated(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("base_updated がありません。テンプレートから出力した Excel を使用してください")
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return fmt.Errorf("base_updated が不正です(%q)。テンプレートから出力した Excel を使用してください", v)
	}
	return nil
}

// clearNotAllowed はクリア不可フィールドへの #CLEAR# 指定エラーを返す。
func clearNotAllowed(label string) error {
	return fmt.Errorf("%s に %s は指定できません(クリアできるのは担当者・期限・詳細のみです)", label, ClearToken)
}

// change は差分の表示文字列「項目: 変更前 → 変更後」を作る。
func change(label, before, after string) string {
	if before == "" {
		before = "(未設定)"
	}
	if after == "" {
		after = "(未設定)"
	}
	return fmt.Sprintf("%s: %s → %s", label, before, after)
}

// summarize は本文を差分表示用に切り詰める。
func summarize(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
	r := []rune(s)
	if len(r) <= descriptionPreviewLen {
		return s
	}
	return string(r[:descriptionPreviewLen]) + "…"
}

// dueDateLayouts は期限として受け付ける書式。
var dueDateLayouts = []string{
	"2006-1-2", "2006/1/2",
	"2006-1-2 15:04:05", "2006/1/2 15:04:05",
	time.RFC3339,
}

// parseDueDate は期限を yyyy-MM-dd へ正規化する(API の送信書式)。
func parseDueDate(v string) (string, error) {
	normalized, ok := parseDateValue(v)
	if !ok {
		return "", fmt.Errorf("期限の書式が不正です(%q)。yyyy-MM-dd 形式で入力してください", strings.TrimSpace(v))
	}
	return normalized, nil
}

// parseDateValue は日付セルを yyyy-MM-dd へ正規化する(期限・日付型のカスタム属性で共用)。
// Excel が日付として保持しているセル(シリアル値)にも対応する。
func parseDateValue(v string) (string, bool) {
	v = strings.TrimSpace(v)
	for _, layout := range dueDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	// Excel のシリアル値(日付書式が「標準」のまま入力された場合)
	if serial, err := strconv.ParseFloat(v, 64); err == nil && serial > 0 && serial < 100000 {
		if t, terr := excelize.ExcelDateToTime(serial, false); terr == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// normalizeStoredDate はローカル DB の期限(ISO8601 または日付)を yyyy-MM-dd にする。
func normalizeStoredDate(v string) string {
	if v == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	if len(v) >= 10 {
		if t, err := time.Parse("2006-01-02", v[:10]); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return v
}

// sameTimestamp は 2 つの更新日時を比較する(表記ゆれを吸収する)。
func sameTimestamp(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	ta, aerr := time.Parse(time.RFC3339, a)
	tb, berr := time.Parse(time.RFC3339, b)
	if aerr != nil || berr != nil {
		return false
	}
	return ta.Equal(tb)
}
