package bulk

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/store"
)

// RunOptions は実行オプション(JSON で UI から渡せる形に保つ)。
type RunOptions struct {
	// Force は競合(base_updated 不一致)を無視して上書きする。
	// リモートの変更を黙って上書きしないため、既定は false。
	Force bool `json:"force"`
	// ResendSending は前回の実行で sending のまま残った行を再送する。
	// 二重作成の可能性があるため、ユーザが明示した場合のみ真にする。
	ResendSending bool `json:"resendSending"`
}

// Progress は実行進捗。
type Progress struct {
	Processed int `json:"processed"`
	Total     int `json:"total"`
}

// ProgressFunc は進捗コールバック(nil 可)。
type ProgressFunc func(Progress)

// RunResult は実行結果。
type RunResult struct {
	JobID    int64 `json:"jobId"`
	Done     int   `json:"done"`
	Failed   int   `json:"failed"`
	Conflict int   `json:"conflict"`
	// Skipped は取り込み時に「変更なし」と判定した行(skip)の件数。
	// キャンセル・中断で処理しなかった行は含まない(それらは pending /
	// sending のまま残り、件数は Warnings で知らせる。2 回目 中 2)。
	Skipped    int      `json:"skipped"`
	DurationMs int64    `json:"durationMs"`
	Warnings   []string `json:"warnings"`
}

func (r *RunResult) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// writeInterval は書き込みリクエスト(および再送前突合の取得)の間に空ける
// 最小間隔(中 4)。公式の推奨に従い、連続送信でも 1 秒以上の間隔を空ける。
const writeInterval = time.Second

// Engine は 1 プロファイル(API クライアント + ローカル DB)に対する実行エンジン。
type Engine struct {
	api API
	st  *store.Store
	now func() time.Time
	// sleep はペーシングの待機(テスト差し替え用)。
	sleep func(time.Duration)
	// lastCallAt は直近の API 呼び出し時刻(ペーシングの基準。中 4)。
	lastCallAt time.Time
}

// NewEngine は実行エンジンを生成する。
func NewEngine(api API, st *store.Store) *Engine {
	return &Engine{api: api, st: st, now: time.Now, sleep: time.Sleep}
}

// waitBeforeCall は直近の API 呼び出しから writeInterval 経過するまで待つ(中 4)。
// 初回呼び出しでは待たない(呼び出しと呼び出しの「間」を空けるのが目的)。
func (e *Engine) waitBeforeCall() {
	if e.lastCallAt.IsZero() {
		return
	}
	if d := writeInterval - e.now().Sub(e.lastCallAt); d > 0 {
		e.sleep(d)
	}
}

// markCall はペーシングの基準時刻を更新する。
func (e *Engine) markCall() { e.lastCallAt = e.now() }

// Run はジョブの未処理行を順に実行する(設計書 5 節)。
//
// 各行の手順:
//  1. 更新行は実行直前に GET /issues/:key でリモートの updated を再取得し、
//     Excel 出力時点の base_updated と異なれば conflict として送信しない
//     (Force 指定時のみ上書きする)。
//  2. 送信前に sending を記録し、送信後に done / error を記録する。
//     この順序により、送信直後の異常終了でも「送信済みかもしれない行」を
//     区別でき、再開時の二重作成を避けられる。
//  3. 行と行の間で canceled() を確認する(送信中の中断はしない)。
//
// canceled・onProgress は nil 可。
func (e *Engine) Run(ctx context.Context, jobID int64, opts RunOptions, canceled func() bool, onProgress ProgressFunc) (*RunResult, error) {
	start := e.now()
	job, err := e.st.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	summary, err := e.st.GetJobSummary(ctx, jobID)
	if err != nil {
		return nil, err
	}
	res := &RunResult{JobID: jobID, Skipped: summary.Skipped, Warnings: []string{}}

	targets, resend, err := e.collectTargets(ctx, jobID, opts, res)
	if err != nil {
		return nil, err
	}
	// 既に結果として記録済みの課題 ID(他行が作成した課題)を集める。
	// 再送前突合で候補から除外し、同名行どうしの取り違えを防ぐ(2 回目 高 1)。
	claimed, err := e.claimedIssueIDs(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if err := e.st.SetJobStatus(ctx, jobID, store.JobStatusRunning); err != nil {
		return nil, err
	}
	total := len(targets)
	if onProgress != nil {
		onProgress(Progress{Processed: 0, Total: total})
	}

	stopped := false
	processed := 0
	for _, row := range targets {
		if ctx.Err() != nil {
			stopped = true
			res.warn("処理を中断しました(%d 件処理済み)", processed)
			break
		}
		if canceled != nil && canceled() {
			stopped = true
			res.warn("処理を中断しました(%d 件処理済み)", processed)
			break
		}
		if err := e.processRow(ctx, job, row, opts, resend[row.RowNo], claimed, res); err != nil {
			// 行状態の記録に失敗した場合は、送信済みかどうかを追跡できなくなるため
			// ジョブ全体を止める(送信を続けると二重更新の危険がある)
			_ = e.st.SetJobStatus(ctx, jobID, store.JobStatusPending)
			return nil, err
		}
		processed++
		if onProgress != nil {
			onProgress(Progress{Processed: processed, Total: total})
		}
	}

	// 未処理のまま残った行を件数付きで知らせる(2 回目 中 2)
	e.warnUnprocessed(ctx, jobID, stopped, res)

	status := store.JobStatusDone
	if stopped {
		status = store.JobStatusCanceled
	}
	if err := e.st.SetJobStatus(ctx, jobID, status); err != nil {
		return nil, err
	}
	res.DurationMs = e.now().Sub(start).Milliseconds()
	return res, nil
}

// collectTargets は実行対象の行を行番号順で集める。
//
//   - pending: 常に対象
//   - sending: ResendSending が真の場合のみ対象(既定では自動再送しない)
//   - conflict: Force が真の場合のみ対象(競合を上書きする再実行)
//
// error 行は自動では再送しない(原因を確認してから Excel を取り込み直す運用)。
//
// 戻り値の resend は「送信中のまま残っていた行」の行番号集合。
// 新規追加行の再送前突合(高 3)で通常の未送信行と区別するために使う。
func (e *Engine) collectTargets(ctx context.Context, jobID int64, opts RunOptions, res *RunResult) ([]store.JobRow, map[int]bool, error) {
	pending, sending, err := e.st.ResumeTargets(ctx, jobID)
	if err != nil {
		return nil, nil, err
	}
	targets := append([]store.JobRow{}, pending...)
	resend := map[int]bool{}
	if len(sending) > 0 {
		if opts.ResendSending {
			targets = append(targets, sending...)
			for _, row := range sending {
				resend[row.RowNo] = true
			}
			res.warn("送信中のまま残っていた %d 行を明示指示により再送します(新規追加行は作成済みかを突合してから送信します)", len(sending))
		} else {
			res.warn("送信中のまま残っている行が %d 行あります。二重作成を防ぐため自動再送しません(内容を確認のうえ再送を指示してください)", len(sending))
		}
	}
	if opts.Force {
		conflicts, err := e.st.ListJobRowsByStatus(ctx, jobID, store.RowStatusConflict)
		if err != nil {
			return nil, nil, err
		}
		targets = append(targets, conflicts...)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].RowNo < targets[j].RowNo })
	return targets, resend, nil
}

// claimedIssueIDs はジョブ内の行が結果として既に記録した課題 ID を集める。
// 再送前突合(2 回目 高 1(c))で、他行が作成した課題を候補から除外するために使う。
func (e *Engine) claimedIssueIDs(ctx context.Context, jobID int64) (map[int64]bool, error) {
	rows, err := e.st.ListJobRows(ctx, jobID)
	if err != nil {
		return nil, err
	}
	claimed := map[int64]bool{}
	for _, r := range rows {
		if r.ResultIssueID > 0 {
			claimed[r.ResultIssueID] = true
		}
	}
	return claimed, nil
}

// warnUnprocessed は未処理のまま残った行(pending / sending)を件数付きで警告する
// (2 回目 中 2)。成功・失敗の件数だけでは「何件手つかずで残ったか」が
// 分からず、キャンセル・中断時の取りこぼしに気付けない。
func (e *Engine) warnUnprocessed(ctx context.Context, jobID int64, stopped bool, res *RunResult) {
	summary, err := e.st.GetJobSummary(ctx, jobID)
	if err != nil {
		return // 集計できない場合は警告を省く(実行結果自体は返す)
	}
	remain := summary.Pending + summary.Sending
	if remain == 0 {
		return
	}
	label := "未処理の行が残っています"
	if stopped {
		label = "キャンセルされました"
	}
	res.warn("%s(未処理 %d 件: 未送信 %d 件 / 送信中 %d 件)。ジョブ履歴から再開できます",
		label, remain, summary.Pending, summary.Sending)
}

// processRow は 1 行を処理する。戻り値のエラーは「DB へ状態を記録できない」等の
// 継続不能な障害のみ(API の失敗は error 行として記録し、処理は続行する)。
//
// isResend が真の行は「送信済みかもしれない行(sending)」の再送であり、
// 新規追加行は送信前にリモートと突合する(高 3)。更新(PATCH)は同じ内容を
// 再送しても結果が変わらない(冪等)ため、突合せず無条件に再送する。
//
// claimed は同一ジョブ内の他行が既に自分の結果として記録した課題 ID
// (再送前突合の候補から除外し、処理した行の結果を追加する)。
func (e *Engine) processRow(ctx context.Context, job *store.Job, row store.JobRow, opts RunOptions, isResend bool, claimed map[int64]bool, res *RunResult) error {
	payload, err := DecodePayload(row.Payload)
	if err != nil {
		res.Failed++
		return e.markRow(ctx, row, store.RowStatusError, 0, err.Error(), res)
	}

	if row.IssueKey != "" {
		// 実行直前の競合検知(リモートの変更を黙って上書きしない)
		remote, gerr := e.api.GetIssue(ctx, row.IssueKey)
		if gerr != nil {
			return e.handleGetIssueError(ctx, row, gerr, isResend, res)
		}
		switch {
		case row.BaseUpdated == "":
			res.warn("行 %d: 基準の更新日時(base_updated)が無いため競合検知をスキップしました", row.RowNo)
		case !sameTimestamp(row.BaseUpdated, remote.Updated) && !opts.Force:
			res.Conflict++
			return e.markRow(ctx, row, store.RowStatusConflict, 0,
				fmt.Sprintf("Excel 出力後にリモートで更新されています(基準 %s / 現在 %s)。上書きする場合は強制実行を指定してください",
					row.BaseUpdated, remote.Updated), res)
		}
	}

	// 新規追加行の再送は、送信前にリモートへ既に作成されていないかを突合する(高 3)。
	// sending 行は「送信したかどうかが分からない行」であり、確認せず再送すると
	// 課題を二重作成しうる。
	if row.IssueKey == "" && isResend {
		match, merr := e.findCreatedIssue(ctx, job, payload, claimed)
		if merr != nil {
			// 突合できない状態で送るのは二重作成の危険があるため送信しない。
			// 行は sending のまま残し、次回の再送指示で改めて突合する。
			res.warn("行 %d: 作成済みかどうかを確認できなかったため再送しませんでした(再実行してください): %s",
				row.RowNo, merr.Error())
			return nil
		}
		switch match.kind {
		case matchFound:
			recorded, merr := e.markRowCreated(ctx, row, match.issue, claimed, res)
			if merr != nil {
				return merr
			}
			if recorded {
				res.warn("行 %d: 作成済みを検出したため再送しませんでした(課題 %s)",
					row.RowNo, match.issue.IssueKey)
			}
			return nil
		case matchAmbiguous:
			// 候補が複数ある / 件名しか一致しない場合は、再送すれば二重作成、
			// 完了扱いにすれば取りこぼしになる。判断は利用者に委ねる(高 1(c))。
			res.warn("行 %d: 作成済みの可能性がある課題を特定できませんでした。"+
				"Backlog 上で確認してから再送してください", row.RowNo)
			return nil // sending のまま残す
		}
	}

	// 送信前に sending を記録する(再開時に「送信済みかもしれない行」を区別するため)
	if err := e.st.UpdateRowStatus(ctx, row.JobID, row.RowNo, store.RowStatusSending, 0, ""); err != nil {
		return err
	}

	if row.IssueKey == "" {
		e.waitBeforeCall() // 書き込み間隔を空ける(中 4)
		issue, cerr := e.api.CreateIssue(ctx, createParamsOf(job.ProjectID, payload))
		e.markCall()
		if cerr != nil {
			return e.handleWriteError(ctx, row, cerr, res)
		}
		_, cerr = e.markRowCreated(ctx, row, *issue, claimed, res)
		return cerr
	}
	e.waitBeforeCall() // 書き込み間隔を空ける(中 4)
	_, uerr := e.api.UpdateIssue(ctx, row.IssueKey, updateParamsOf(payload))
	e.markCall()
	if uerr != nil {
		return e.handleWriteError(ctx, row, uerr, res)
	}
	res.Done++
	return e.markRow(ctx, row, store.RowStatusDone, 0, "", res)
}

// handleGetIssueError は競合検知の再取得(実行直前の GetIssue)が失敗した場合の
// 扱いを決める(2 回目 高 3)。
//
//   - 課題が存在しない(404): 再送中(isResend)の行は既に送信済みかもしれず、
//     削除されたのか送信していないのかを判別できないため conflict として
//     利用者の確認を促す。初回実行の行は送信先が無いことが確定しているため error。
//   - それ以外の失敗: 一時的な障害の可能性があり、送信の可否を判断できない。
//     行の状態を変えずに残す(再送中は sending・初回は pending のまま)。
//     error に確定させると、実際には送信済みの行を失敗と誤報告してしまう。
func (e *Engine) handleGetIssueError(ctx context.Context, row store.JobRow, err error, isResend bool, res *RunResult) error {
	if errors.Is(err, backlogclient.ErrNotFound) {
		msg := fmt.Sprintf("課題 %s がリモートに存在しません(削除された可能性があります)", row.IssueKey)
		if isResend {
			res.Conflict++
			return e.markRow(ctx, row, store.RowStatusConflict, 0,
				msg+"。送信済みかどうかを Backlog 上で確認してください", res)
		}
		res.Failed++
		return e.markRow(ctx, row, store.RowStatusError, 0, msg, res)
	}
	if isResend {
		res.warn("行 %d: 送信結果を確認できませんでした(再開時に確認します): 課題 %s を取得できません(%s)",
			row.RowNo, row.IssueKey, err.Error())
		return nil // sending のまま残す(状態を変えない)
	}
	res.warn("行 %d: 課題 %s の状態を確認できなかったため送信しませんでした(未処理のまま残します): %s",
		row.RowNo, row.IssueKey, err.Error())
	return nil // pending のまま残す(まだ何も送っていない)
}

// handleWriteError は書き込みの失敗を 2 分類して記録する(高 4)。
//
//   - 確定的拒否(HTTP ステータスを受信した 4xx / 5xx): サーバは反映していない
//     ことが保証されるため error として確定する。
//   - 成否不明(応答欠落・解析失敗・ネットワークエラー): 反映済みの可能性が
//     あるため error にせず sending のまま残す。error にすると再送で二重更新に
//     なりうるうえ、実際には成功しているかもしれない行を失敗と誤報告する。
func (e *Engine) handleWriteError(ctx context.Context, row store.JobRow, err error, res *RunResult) error {
	if backlogclient.IsUncertain(err) {
		res.warn("行 %d: 送信結果を確認できませんでした(再開時に確認します): %s", row.RowNo, err.Error())
		return nil // sending のまま残す(状態を変えない)
	}
	res.Failed++
	return e.markRow(ctx, row, store.RowStatusError, 0, err.Error(), res)
}

// matchKind は再送前突合の判定結果。
type matchKind int

const (
	matchNone      matchKind = iota // 一致なし(未作成 → 再送してよい)
	matchFound                      // 送信内容と全項目一致する課題が 1 件(作成済み)
	matchAmbiguous                  // 作成済みかを特定できない(複数一致・件名のみ一致)
)

// issueMatch は突合結果(matchFound のときだけ issue が有効)。
type issueMatch struct {
	kind  matchKind
	issue backlogclient.Issue
}

// findCreatedIssue は再送対象の新規追加行が既に作成済みかを突合する
// (高 3 / 2 回目 高 1)。
//
// 判定は次の順に絞り込む:
//  1. ジョブ作成時刻(秒精度)以降に作られた課題のみを対象にする。
//     createdSince は日付単位でしか指定できないため、取得後に時刻で絞る(高 1(a))。
//  2. 同一ジョブの他行が既に自分の結果として記録した課題は除外する(高 1(c))。
//  3. 送信内容の全項目(件名・種別・優先度・担当者・期限・詳細)が一致する
//     課題だけを「作成済み」とみなす(高 1(b))。
//
// 全項目一致が 1 件だけなら matchFound、複数ある場合や件名しか一致しない
// 候補しか無い場合は matchAmbiguous(呼び出し側は再送しない)、
// 候補が 1 件も無ければ matchNone(再送してよい)を返す。
func (e *Engine) findCreatedIssue(ctx context.Context, job *store.Job, payload *Payload, claimed map[int64]bool) (issueMatch, error) {
	if payload.Summary == nil || *payload.Summary == "" {
		return issueMatch{}, errors.New("送信内容に件名がありません")
	}
	jobStart, err := time.Parse(time.RFC3339, job.CreatedAt)
	if err != nil {
		return issueMatch{}, fmt.Errorf("ジョブの作成日時を解釈できません(%q)", job.CreatedAt)
	}
	jobStart = jobStart.Truncate(time.Second)

	summary := *payload.Summary
	var exact, partial []backlogclient.Issue
	for offset := 0; offset < maxMatchIssues; offset += backlogclient.MaxPageSize {
		e.waitBeforeCall() // 突合の取得も間隔を空ける(中 4)
		issues, gerr := e.api.GetIssues(ctx, backlogclient.IssueQuery{
			ProjectIDs:   []int64{job.ProjectID},
			CreatedSince: createdSinceOf(jobStart),
			Sort:         "created",
			Order:        "asc",
			Count:        backlogclient.MaxPageSize,
			Offset:       offset,
		})
		e.markCall()
		if gerr != nil {
			return issueMatch{}, gerr
		}
		for i := range issues {
			issue := issues[i]
			if issue.Summary != summary || claimed[issue.ID] {
				continue // 別課題、または他行が既に結果として記録済み
			}
			created, cerr := time.Parse(time.RFC3339, issue.Created)
			switch {
			case cerr != nil:
				// 作成日時を解釈できない課題は、このジョブで作られたものか
				// 判断できない。除外すると二重作成しうるため候補として残す。
				partial = append(partial, issue)
				continue
			case created.Before(jobStart):
				continue // ジョブ実行前から存在する同名の別課題
			}
			if samePayload(payload, issue) {
				exact = append(exact, issue)
			} else {
				partial = append(partial, issue)
			}
		}
		if len(issues) < backlogclient.MaxPageSize {
			return decideMatch(exact, partial), nil // 最終ページまで確認した
		}
	}
	return issueMatch{}, errors.New("突合対象の課題が多すぎます")
}

// decideMatch は候補から突合結果を決める。
// 「全項目一致がちょうど 1 件」のときだけ作成済みと断定する。
func decideMatch(exact, partial []backlogclient.Issue) issueMatch {
	switch {
	case len(exact) == 1:
		return issueMatch{kind: matchFound, issue: exact[0]}
	case len(exact) > 1 || len(partial) > 0:
		return issueMatch{kind: matchAmbiguous}
	default:
		return issueMatch{kind: matchNone}
	}
}

// samePayload は課題が送信内容と全項目一致するかを返す(2 回目 高 1(b))。
//
// 件名だけの一致では、同じ件名の別課題を「作成済み」と誤判定して
// 必要な追加を取りこぼしうる。新規追加で送信する全項目を突き合わせる。
// 送信しなかった項目(nil)は、作成直後なら未設定のはずなので
// リモート側も空であることを求める。
func samePayload(p *Payload, issue backlogclient.Issue) bool {
	switch {
	case p.Summary == nil || *p.Summary != issue.Summary:
		return false
	case valueInt64(p.IssueTypeID) != issue.IssueTypeID:
		return false
	case valueInt64(p.PriorityID) != issue.PriorityID:
		return false
	case valueInt64(p.AssigneeID) != issue.AssigneeID:
		return false
	case normalizeStoredDate(valueString(p.DueDate)) != normalizeStoredDate(issue.DueDate):
		return false
	case normalizeNewlines(valueString(p.Description)) != normalizeNewlines(issue.Description):
		return false
	case valueInt64(p.ParentIssueID) != issue.ParentIssueID:
		return false
	case !sameCustomFields(p.CustomFields, issue):
		return false
	}
	return true
}

// sameCustomFields は送信したカスタム属性が課題へ反映されているかを返す。
//
// 送信していない属性は比較しない(必須属性の既定値など、こちらが指定しない値が
// 入っていても「別の課題」とは言えないため)。逆に、送信した属性を確認できない
// 応答(生 JSON が無い)は一致と断定しない。断定を誤って作成済み扱いにすると
// 必要な追加を取りこぼすため、判断は利用者へ委ねる(matchAmbiguous)。
func sameCustomFields(fields []customfield.InputValue, issue backlogclient.Issue) bool {
	if len(fields) == 0 {
		return true
	}
	if issue.RawJSON == "" {
		return false
	}
	values, err := customfield.ParseValues(issue.RawJSON)
	if err != nil {
		return false
	}
	current := map[int64]customfield.Value{}
	for _, v := range values {
		current[v.ID] = v
	}
	for _, f := range fields {
		cur, ok := current[f.ID]
		display := ""
		if ok {
			display = customfield.FormatValue(cur)
		}
		switch {
		case f.Clear:
			if display != "" {
				return false
			}
		case len(f.ItemIDs) > 0:
			if !sameInt64Set(f.ItemIDs, customfield.ItemIDs(cur)) {
				return false
			}
			// 「その他」の直接入力は送信非対応のため、こちらが作成した課題には
			// 残らないはず。残っている候補は別課題の可能性があり、作成済みと
			// 断定しない(取りこぼし方向へ倒す既存方針)
			if cur.OtherValue != "" {
				return false
			}
		default:
			if f.Text != display {
				return false
			}
		}
	}
	return true
}

// valueInt64 / valueString は nil を「未指定(ゼロ値)」として読む。
func valueInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func valueString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// normalizeNewlines は改行コードの違い(Excel 由来の CRLF)を吸収する。
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// maxMatchIssues は再送前突合で確認する課題数の上限(ページングの安全弁)。
// ジョブ作成時刻以降の課題のみを見るため、通常はこの上限に達しない。
const maxMatchIssues = 10000

// createdSinceOf はジョブ作成時刻から createdSince(yyyy-MM-dd)を作る。
//
// API は日付単位の指定しか受け付けず、スペースのタイムゾーンと UTC のずれで
// ジョブ当日分を取りこぼしうるため、1 日前から取得する。
// 実際の絞り込みは取得後に作成時刻(秒精度)で行う(2 回目 高 1(a))。
func createdSinceOf(jobStart time.Time) string {
	return jobStart.UTC().AddDate(0, 0, -1).Format("2006-01-02")
}

// markRow は行状態を記録する。
func (e *Engine) markRow(ctx context.Context, row store.JobRow, status string, resultIssueID int64, msg string, res *RunResult) error {
	if err := e.st.UpdateRowStatus(ctx, row.JobID, row.RowNo, status, resultIssueID, msg); err != nil {
		return fmt.Errorf("行 %d の状態を保存できませんでした: %w", row.RowNo, err)
	}
	return nil
}

// markRowCreated は新規追加が成立した行(送信して作成できた行・再送前突合で
// 作成済みを検出した行)を done にし、課題の ID とキーを記録する。
// 記録したときだけ完了件数を数え、同一ジョブ内の他行が同じ課題を拾わないよう
// claimed へ加える。戻り値の recorded は「done として記録したか」。
//
// 課題キーを書き込むのは、この「done へ遷移する 1 つの UPDATE」だけに限る
// (store.UpdateRowResult も done 以外での指定を拒否する)。
// job_rows.issue_key が空であることは新規追加行の目印であり、送信前(sending)に
// 書いてしまうと、再開時に更新行と誤認して再送前突合(二重作成防止)が
// 働かなくなるため。
//
// 応答から課題を特定できない(ID・課題キーのどちらかが欠ける)場合は done に
// せず、行を sending のまま残して警告する(2 回目 中 2)。不完全なまま done に
// すると、その行は二度と再送・突合の対象にならず、「作成されたかもしれない
// 課題」を追えなくなる。成否不明の書き込み(高 4)と同じく判断を先送りし、
// 次回の再送指示で改めて突合させる。
func (e *Engine) markRowCreated(ctx context.Context, row store.JobRow, issue backlogclient.Issue,
	claimed map[int64]bool, res *RunResult) (recorded bool, err error) {
	if issue.ID <= 0 || issue.IssueKey == "" {
		res.warn("行 %d: 応答から作成された課題を特定できませんでした(ID %d / キー %q)。"+
			"作成済みかを Backlog 上で確認のうえ、再送を指示してください", row.RowNo, issue.ID, issue.IssueKey)
		return false, nil // sending のまま残す(状態を変えない)
	}
	if err := e.st.UpdateRowResult(ctx, row.JobID, row.RowNo,
		store.RowStatusDone, issue.ID, issue.IssueKey, ""); err != nil {
		return false, fmt.Errorf("行 %d の状態を保存できませんでした: %w", row.RowNo, err)
	}
	claimed[issue.ID] = true
	res.Done++
	return true, nil
}

// createParamsOf は payload を課題追加のパラメータへ変換する。
// projectId はジョブ(テンプレート出力時のプロジェクト)を正とし、
// payload 側の値は使わない(取り込み後に別プロジェクトへ送られることを防ぐ)。
func createParamsOf(projectID int64, p *Payload) backlogclient.IssueCreate {
	in := backlogclient.IssueCreate{ProjectID: projectID}
	if p.Summary != nil {
		in.Summary = *p.Summary
	}
	if p.IssueTypeID != nil {
		in.IssueTypeID = *p.IssueTypeID
	}
	if p.PriorityID != nil {
		in.PriorityID = *p.PriorityID
	}
	in.Description = p.Description
	in.AssigneeID = p.AssigneeID
	in.DueDate = p.DueDate
	// 新規追加で親を解除することは無いため、設定のみを引き継ぐ(CF5)
	in.ParentIssueID = p.ParentIssueID
	in.CustomFields = p.CustomFields
	return in
}

// updateParamsOf は payload を課題更新のパラメータへ変換する
// (nil = 変更しない / 空値 = クリア の意味をそのまま引き継ぐ)。
func updateParamsOf(p *Payload) backlogclient.IssueUpdate {
	in := backlogclient.IssueUpdate{
		Summary:       p.Summary,
		Description:   p.Description,
		IssueTypeID:   p.IssueTypeID,
		PriorityID:    p.PriorityID,
		StatusID:      p.StatusID,
		AssigneeID:    p.AssigneeID,
		DueDate:       p.DueDate,
		ParentIssueID: p.ParentIssueID,
		CustomFields:  p.CustomFields,
	}
	// 親子関係の解除は 0 で表す(backlogclient 側で空文字パラメータになる。CF5)
	if p.ClearParentIssue {
		in.ParentIssueID = ptrInt64(0)
	}
	return in
}
