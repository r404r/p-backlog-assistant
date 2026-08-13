// Package sync は Backlog API とローカル SQLite キャッシュの同期エンジン
// (設計書 3 節)。フル同期・差分同期・削除検知を担う。
//
// 注意: パッケージ名が標準ライブラリの sync と衝突するため、
// 利用側は syncpkg 等のエイリアスで import すること。
package sync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// Mode は同期モード。
type Mode string

const (
	// ModeAuto は sync_state から自動判定する(DecideMode)。
	ModeAuto Mode = "auto"
	// ModeFull はプロジェクト全件のフル同期。
	ModeFull Mode = "full"
	// ModeIncremental は updatedSince による差分同期。
	ModeIncremental Mode = "incremental"
)

const (
	// pageSize は課題一覧・アクティビティの 1 ページ件数(API 上限)。
	pageSize = backlogclient.MaxPageSize
	// deleteConfirmLimit は削除候補を個別 GET で確認する上限(設計書 3 節)。
	// これ以上は誤削除・大量 API 呼び出しを避けて削除を保留する(警告のみ)。
	// 保留は自動では解除されない(候補がこの件数未満に減るまで、フル同期を
	// 繰り返しても同じ判断になる)ため、警告文で利用者の対処を案内する。
	deleteConfirmLimit = 100
	// maxPages はページングの安全上限(API 異常での無限ループ防止)。
	// pageSize=100 なので課題 500,000 件相当。
	maxPages = 5000
	// activityTypeIssueDeleted は「課題削除」のアクティビティ種別 ID。
	activityTypeIssueDeleted = 4
	// StaleThreshold はこれを超える未同期でフル同期へフォールバックする閾値。
	StaleThreshold = 14 * 24 * time.Hour
)

// API は同期エンジンが必要とする Backlog API 操作。
// 実体は *backlogclient.Client(テストではフェイクに差し替える)。
type API interface {
	GetProjects(ctx context.Context) ([]backlogclient.Project, error)
	GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error)
	GetIssuesCount(ctx context.Context, q backlogclient.IssueQuery) (int, error)
	GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error)
	// GetIssueComments は課題詳細の「最新の状態を取得」でのみ使う
	// (通常の同期はコメントを取得しない。refresh.go を参照)。
	GetIssueComments(ctx context.Context, issueIDOrKey string, q backlogclient.CommentQuery) ([]backlogclient.Comment, error)
	GetSpaceActivities(ctx context.Context, q backlogclient.ActivityQuery) ([]backlogclient.Activity, error)

	// ユーザ・チーム同期(SyncUsers)。GetUsersRaw / GetTeamsPaged は
	// 権限不足時に backlogclient.ErrPermissionDenied を返す。
	GetUsersRaw(ctx context.Context) ([]backlogclient.User, error)
	GetTeamsPaged(ctx context.Context, offset, count int) ([]backlogclient.Team, error)
	GetProjectUsers(ctx context.Context, projectID int64) ([]backlogclient.User, error)
	GetProjectAdministrators(ctx context.Context, projectID int64) ([]backlogclient.User, error)
	// GetProjectTeams は縮退パスでチーム情報を合成するために使う(高 1)。
	GetProjectTeams(ctx context.Context, projectID int64) ([]backlogclient.Team, error)
}

// コンパイル時チェック: *backlogclient.Client が API を満たすこと。
var _ API = (*backlogclient.Client)(nil)

// Phase は進捗の段階。
type Phase string

const (
	PhaseCount      Phase = "count"      // 総件数の取得
	PhaseFetch      Phase = "fetch"      // 課題の取得・保存
	PhaseDeleteScan Phase = "deleteScan" // 削除検知(削除候補確認 / アクティビティ消化)
	PhaseDone       Phase = "done"       // 完了
)

// Progress は進捗通知。
type Progress struct {
	Phase   Phase `json:"phase"`
	Fetched int   `json:"fetched"` // 取得済み件数
	Total   int   `json:"total"`   // 総件数(不明なら 0)
}

// ProgressFunc は進捗コールバック(nil 可)。
type ProgressFunc func(Progress)

// Result は同期結果。
type Result struct {
	Mode       Mode     `json:"mode"`
	Fetched    int      `json:"fetched"`  // API から取得した件数
	Upserted   int      `json:"upserted"` // 実際に DB へ書き込んだ件数
	Deleted    int      `json:"deleted"`  // 論理削除した件数
	Warnings   []string `json:"warnings"`
	DurationMs int64    `json:"durationMs"`
}

func (r *Result) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Engine は 1 つのプロファイル(API クライアント + ローカル DB)に対する同期エンジン。
type Engine struct {
	api API
	st  *store.Store

	// now はテスト差し替え用の現在時刻取得。
	now func() time.Time
	// applyDeletionsHook はテスト用のフック(トランザクション内で失敗を注入する)。
	applyDeletionsHook func() error
	// applyUsersStageHook はテスト用のフック(ユーザ同期の反映トランザクション内で、
	// 段階を指定して失敗を注入する。R7)。
	applyUsersStageHook func(stage string) error
}

// NewEngine は同期エンジンを生成する。
func NewEngine(api API, st *store.Store) *Engine {
	return &Engine{api: api, st: st, now: time.Now}
}

// DecideMode は sync_state から同期モードを判定する(設計書 3 節)。
// 状態が無い・カーソルが無い・最終同期時刻が不明/不正・長期未同期の場合は
// 連続性を保証できないためフル同期へフォールバックする。
func DecideMode(state *store.SyncState, now time.Time) Mode {
	if state == nil || state.ActivityCursor <= 0 || state.LastSyncedAt == "" {
		return ModeFull
	}
	last, err := time.Parse(time.RFC3339, state.LastSyncedAt)
	if err != nil {
		return ModeFull
	}
	elapsed := now.Sub(last)
	// 未来時刻(時計ずれ・DB 改変)も信頼できないためフル同期にする
	if elapsed < 0 || elapsed > StaleThreshold {
		return ModeFull
	}
	return ModeIncremental
}

// SyncIssues は 1 プロジェクトの課題を同期する。
// mode が ModeAuto の場合は DecideMode で判定する。
func (e *Engine) SyncIssues(ctx context.Context, projectID int64, mode Mode, onProgress ProgressFunc) (*Result, error) {
	if projectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	start := e.now()
	state, err := e.st.GetSyncState(ctx, store.DataKindIssues, projectID)
	if err != nil {
		return nil, err
	}
	if mode == "" || mode == ModeAuto {
		mode = DecideMode(state, start)
	}

	var res *Result
	switch mode {
	case ModeFull:
		res, err = e.fullSyncIssues(ctx, projectID, onProgress)
	case ModeIncremental:
		if m := DecideMode(state, start); m == ModeFull {
			return nil, errors.New("差分同期の前提(確定済みカーソルと最終同期時刻)が揃っていません。フル同期を実行してください")
		}
		res, err = e.incrementalSyncIssues(ctx, projectID, state, onProgress)
	default:
		return nil, fmt.Errorf("不明な同期モードです: %s", mode)
	}
	if err != nil {
		return nil, err
	}
	res.DurationMs = e.now().Sub(start).Milliseconds()
	report(onProgress, Progress{Phase: PhaseDone, Fetched: res.Fetched, Total: res.Fetched})
	return res, nil
}

func report(fn ProgressFunc, p Progress) {
	if fn != nil {
		fn(p)
	}
}

// fullSyncIssues はプロジェクト全件のフル同期(設計書 3 節「フル同期」)。
func (e *Engine) fullSyncIssues(ctx context.Context, projectID int64, onProgress ProgressFunc) (*Result, error) {
	res := &Result{Mode: ModeFull, Warnings: []string{}}

	// 1. 開始前に最新アクティビティ ID を activity_start_pending へ保存する。
	//    同期中に発生した削除を次回の差分同期で確実に拾うための境界。
	//    取得に失敗した場合は pending を立てず(= 完了時の昇格を行わず)、
	//    確定済みカーソルを据え置いて次回フル同期で回収する。
	if latest, err := e.latestActivityID(ctx); err != nil {
		res.warn("最新アクティビティ ID を取得できませんでした(削除検知の開始境界は更新しません): %v", err)
	} else if latest > 0 {
		if err := e.st.SetActivityStartPending(ctx, store.DataKindIssues, projectID, latest); err != nil {
			return nil, err
		}
	}

	// 2. 総件数(進捗表示用)。失敗しても同期自体は続行する。
	baseQuery := backlogclient.IssueQuery{ProjectIDs: []int64{projectID}}
	total, err := e.api.GetIssuesCount(ctx, baseQuery)
	if err != nil {
		res.warn("課題数を取得できませんでした(進捗は件数のみ表示します): %v", err)
		total = 0
	}
	report(onProgress, Progress{Phase: PhaseCount, Total: total})

	// 3. sort=created&order=asc で全ページ取得して UPSERT する。
	//    created は不変なので、offset ページング中に並行更新があっても
	//    行の並びが崩れない(sort=updated では取り逃しが起きる)。
	//    取得(API)と書き込み(DB)はパイプライン化して重ねる
	//    (ページ順・書き込み順は維持。fetchIssuePagesPipelined を参照)。
	seen := map[int64]bool{}
	fetchedAt := e.nowString()
	if err := e.fetchIssuePagesPipelined(ctx,
		func(page int) backlogclient.IssueQuery {
			q := baseQuery
			q.Sort, q.Order, q.Count, q.Offset = "created", "asc", pageSize, page*pageSize
			return q
		},
		func(_ int, issues []backlogclient.Issue) error {
			if len(issues) > 0 {
				rows := toStoreIssues(issues, fetchedAt)
				if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
					return store.UpsertIssues(ctx, tx, rows)
				}); err != nil {
					return err
				}
				for _, i := range issues {
					seen[i.ID] = true
				}
				res.Fetched += len(issues)
				res.Upserted += len(issues)
			}
			report(onProgress, Progress{Phase: PhaseFetch, Fetched: res.Fetched, Total: total})
			return nil
		}); err != nil {
		return nil, err
	}

	// 4. 一時集合に含まれないローカル行を「削除候補」とする。
	//    並行追加・削除で取り逃した課題を誤削除しないため、即座には消さない。
	report(onProgress, Progress{Phase: PhaseDeleteScan, Fetched: res.Fetched, Total: total})
	deletedIDs, recovered, err := e.confirmDeleteCandidates(ctx, projectID, seen, res)
	if err != nil {
		return nil, err
	}

	// 4b. 存在確認(200)で取得できた課題は、一覧から漏れただけの実在課題なので
	//     同じ同期内で UPSERT して最新化する(中 2)。古いローカル内容を残さない。
	if len(recovered) > 0 {
		rows := toStoreIssues(recovered, fetchedAt)
		if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
			return store.UpsertIssues(ctx, tx, rows)
		}); err != nil {
			return nil, err
		}
		// Fetched は一覧取得分の件数(進捗表示の分母と対応)なので加算しない
		res.Upserted += len(rows)
	}

	// 5. 削除確定・完了時刻の保存・pending → cursor の昇格を同一トランザクションで行う。
	now := e.now().UTC()
	if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.MarkIssuesDeleted(ctx, tx, projectID, deletedIDs); err != nil {
			return err
		}
		if err := store.SetSyncCompleted(ctx, tx, store.DataKindIssues, projectID,
			now.Format(time.RFC3339), now.Format("2006-01-02")); err != nil {
			return err
		}
		return store.PromotePendingCursor(ctx, tx, store.DataKindIssues, projectID)
	}); err != nil {
		return nil, err
	}
	res.Deleted = len(deletedIDs)
	return res, nil
}

// confirmDeleteCandidates は削除候補を GET /issues/:issueKey の 404 で確認し、
// 削除確定した課題 ID と、200 で取得できた(= 実在する)課題を返す。
// 候補が deleteConfirmLimit 以上の場合は確認せず警告のみ返し、削除は行わない
// (設計書 3 節。保留は自動解除されないため、警告文で対処を案内する)。
func (e *Engine) confirmDeleteCandidates(ctx context.Context, projectID int64, seen map[int64]bool, res *Result) ([]int64, []backlogclient.Issue, error) {
	refs, err := e.st.ListIssueRefs(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	var candidates []store.IssueRef
	for _, r := range refs {
		if !seen[r.ID] {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	if len(candidates) >= deleteConfirmLimit {
		// 保留は自動では解除されない(次のフル同期でも候補は同じだけ残る)。
		// 「次回確定します」と案内すると待てば直ると誤解させるため、
		// 実際に必要な対処まで書く。
		res.warn("削除候補が %d 件と多数のため、削除の反映を保留しました(誤削除の防止)。"+
			"候補が %d 件未満にならない限り、フル同期を繰り返しても保留は解除されません。"+
			"Backlog 側で実際に大量の課題を削除した場合は、アプリを終了してから"+
			"ローカル DB ファイル(保存先はアプリ情報画面に表示。同名の -wal・-shm ファイルも含む)を削除し、"+
			"再起動後にフル同期を実行してください", len(candidates), deleteConfirmLimit)
		return nil, nil, nil
	}
	var confirmed []int64
	var recovered []backlogclient.Issue
	for _, c := range candidates {
		key := c.IssueKey
		if key == "" {
			key = strconv.FormatInt(c.ID, 10)
		}
		issue, err := e.api.GetIssue(ctx, key)
		if err != nil {
			if errors.Is(err, backlogclient.ErrNotFound) {
				confirmed = append(confirmed, c.ID)
				continue
			}
			// 404 以外(通信エラー等)は削除確定できないため保留する
			res.warn("課題の存在確認に失敗したため削除を保留しました: %v", err)
			continue
		}
		// リモートに実在する = 一覧で取り逃しただけなので削除せず、
		// 取得できた内容で最新化する(中 2)。
		// 応答が期待どおりでない(ID 不明・別プロジェクト)場合は
		// 誤った行を作らないよう UPSERT しない。
		if issue == nil {
			continue
		}
		if issue.ID <= 0 || issue.ProjectID != projectID {
			res.warn("存在確認で取得した課題(%s)が想定と異なるため更新を見送りました", key)
			continue
		}
		recovered = append(recovered, *issue)
	}
	return confirmed, recovered, nil
}

// incrementalSyncIssues は差分同期(設計書 3 節「差分同期」)。
func (e *Engine) incrementalSyncIssues(ctx context.Context, projectID int64, state *store.SyncState, onProgress ProgressFunc) (*Result, error) {
	res := &Result{Mode: ModeIncremental, Warnings: []string{}}

	updatedSince, err := updatedSinceFor(state.LastSyncedAt)
	if err != nil {
		return nil, err
	}

	// 1. updatedSince で全ページ消化する(既定の 20 件で止めない)。
	localUpdated, err := e.st.GetIssueUpdatedMap(ctx, projectID)
	if err != nil {
		return nil, err
	}
	fetchedAt := e.nowString()
	// フル同期と同じく、取得と書き込みをオーバーラップさせる(順序は維持)。
	if err := e.fetchIssuePagesPipelined(ctx,
		func(page int) backlogclient.IssueQuery {
			return backlogclient.IssueQuery{
				ProjectIDs:   []int64{projectID},
				UpdatedSince: updatedSince,
				Sort:         "created", Order: "asc",
				Count: pageSize, Offset: page * pageSize,
			}
		},
		func(_ int, issues []backlogclient.Issue) error {
			res.Fetched += len(issues)

			// updated がローカルと同じ行は書き込まない(重複 DB 更新の防止)
			var changed []backlogclient.Issue
			for _, i := range issues {
				if prev, ok := localUpdated[i.ID]; ok && prev == i.Updated && i.Updated != "" {
					continue
				}
				changed = append(changed, i)
			}
			if len(changed) > 0 {
				rows := toStoreIssues(changed, fetchedAt)
				if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
					return store.UpsertIssues(ctx, tx, rows)
				}); err != nil {
					return err
				}
				for _, i := range changed {
					localUpdated[i.ID] = i.Updated
				}
				res.Upserted += len(changed)
			}
			report(onProgress, Progress{Phase: PhaseFetch, Fetched: res.Fetched})
			return nil
		}); err != nil {
		return nil, err
	}

	// 2. 削除検知(activities を全ページ消化)。
	report(onProgress, Progress{Phase: PhaseDeleteScan, Fetched: res.Fetched})
	deleted, err := e.consumeDeleteActivities(ctx, projectID, state.ActivityCursor, res)
	res.Deleted = deleted
	if err != nil {
		return nil, err
	}

	// 3. 完了時刻を保存する(カーソルは 2 で前進済み)。
	now := e.now().UTC()
	if err := e.st.SetSyncCompleted(ctx, store.DataKindIssues, projectID,
		now.Format(time.RFC3339), now.Format("2006-01-02")); err != nil {
		return nil, err
	}
	return res, nil
}

// issuePipelineBuffer は取得済みページを溜めるバッファ段数。
// 2 ページぶん先読みできれば「取得 1 ページ」と「書き込み 1 ページ」が
// 常に重なり、これ以上増やしても待ち時間は減らない(メモリだけ増える)。
const issuePipelineBuffer = 2

// fetchIssuePagesPipelined は課題ページの取得と処理をオーバーラップさせる。
//
// 並行設計の意図:
//   - プロデューサ(専用 goroutine)は queryFor(page) のクエリで API を叩き、
//     取得結果をバッファ付きチャネルへページ順に流す。
//   - コンシューマは呼び出し元 goroutine そのもので、受け取ったページを
//     ページ順に handle へ渡す。DB 書き込み・進捗通知・集計は
//     すべてこの 1 goroutine の中だけで起きるため、
//     既存のトランザクション整合・進捗順序・共有状態の扱いは変わらない。
//   - offset ページングの整合性のため、取得順・処理順はどちらも直列のまま。
//     重ねるのは「次ページの取得」と「今のページの書き込み」だけ。
//
// エラー・キャンセル:
//   - handle が失敗したら context をキャンセルしてプロデューサを確実に止め、
//     チャネルを空にしてから goroutine の終了を待つ(リーク防止)。
//   - 双方が失敗した場合はページ順で先に起きた側(= handle 側)を返す。
//     handle がページ i を処理できたということは、ページ i の取得は成功して
//     いるので、プロデューサの失敗は必ずページ i より後ろにあたる。
//   - 呼び出し元の context がキャンセルされた場合は、そのエラーを返す。
func (e *Engine) fetchIssuePagesPipelined(
	ctx context.Context,
	queryFor func(page int) backlogclient.IssueQuery,
	handle func(page int, issues []backlogclient.Issue) error,
) error {
	// 内部 context。コンシューマ側の失敗でプロデューサを止めるために使う。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type fetchedPage struct {
		index  int
		issues []backlogclient.Issue
	}
	ch := make(chan fetchedPage, issuePipelineBuffer)
	done := make(chan struct{})
	var producerErr error // done のクローズ後にのみ読む

	go func() {
		defer close(done)
		defer close(ch)
		for page := 0; ; page++ {
			if page >= maxPages {
				producerErr = fmt.Errorf("課題取得のページ数が上限(%d)を超えました", maxPages)
				return
			}
			q := queryFor(page)
			issues, err := e.api.GetIssues(ctx, q)
			if err != nil {
				producerErr = fmt.Errorf("課題一覧の取得に失敗しました(offset %d): %w", q.Offset, err)
				return
			}
			select {
			case ch <- fetchedPage{index: page, issues: issues}:
			case <-ctx.Done():
				// コンシューマの失敗・呼び出し元のキャンセルで送信先が消えた
				producerErr = ctx.Err()
				return
			}
			if len(issues) < pageSize {
				return
			}
		}
	}()

	var handleErr error
	for p := range ch {
		// 呼び出し元がキャンセルした場合は以降の書き込みを行わない
		if err := ctx.Err(); err != nil {
			handleErr = err
			break
		}
		if err := handle(p.index, p.issues); err != nil {
			handleErr = err
			break
		}
	}
	// プロデューサを止め、送信でブロックしている場合に備えて残りを捨てる。
	cancel()
	for range ch { //nolint:revive // チャネルを空にしてプロデューサを解放する
	}
	<-done // producerErr の読み取りを goroutine の終了後に揃える

	if handleErr != nil {
		return handleErr
	}
	return producerErr
}

// consumeDeleteActivities は activityTypeId=4 のアクティビティを minId から
// 全ページ消化し、課題を論理削除する。
// 各ページの削除反映と activity_cursor の前進は同一トランザクションで行う
// (カーソルだけ先行すると、異常終了時に未反映の削除イベントを永久に飛ばすため)。
func (e *Engine) consumeDeleteActivities(ctx context.Context, projectID, cursor int64, res *Result) (int, error) {
	deleted := 0
	for page := 0; ; page++ {
		if page >= maxPages {
			return deleted, fmt.Errorf("アクティビティ取得のページ数が上限(%d)を超えました", maxPages)
		}
		acts, err := e.api.GetSpaceActivities(ctx, backlogclient.ActivityQuery{
			ActivityTypeIDs: []int{activityTypeIssueDeleted},
			MinID:           cursor,
			Order:           "asc",
			Count:           pageSize,
		})
		if err != nil {
			return deleted, fmt.Errorf("アクティビティの取得に失敗しました(minId %d): %w", cursor, err)
		}
		if len(acts) == 0 {
			return deleted, nil
		}

		maxID := cursor
		var byID []int64
		var byKey []string
		unknown := 0
		noProject := 0
		for _, a := range acts {
			if a.ID > maxID {
				maxID = a.ID
			}
			// minId は下限(含む)のため、確定済みカーソル以下は処理済みとして飛ばす
			if a.ID <= cursor {
				continue
			}
			// project.id が特定できないイベントは削除しない(中 1)。
			// content.id だけを信用してプロジェクト条件なしで削除すると、
			// 別プロジェクトの同名 ID・同名キーの課題を巻き添えにしうるため、
			// 警告だけ残して次回のリコンシリエーションに委ねる。
			if a.ProjectID == 0 {
				noProject++
				continue
			}
			if a.ProjectID != projectID {
				continue // 他プロジェクトの削除は当該プロジェクトの同期対象外
			}
			id, key, ok := parseDeletedIssueRef(a)
			switch {
			case !ok:
				unknown++
			case id > 0:
				byID = append(byID, id)
			default:
				byKey = append(byKey, key)
			}
		}
		if unknown > 0 {
			res.warn("削除アクティビティ %d 件から課題を特定できませんでした(次回のリコンシリエーションで回収します)", unknown)
		}
		if noProject > 0 {
			res.warn("削除アクティビティ %d 件はプロジェクトを特定できないため削除を見送りました(次回のリコンシリエーションで回収します)", noProject)
		}

		pageDeleted := 0
		if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
			if e.applyDeletionsHook != nil {
				if err := e.applyDeletionsHook(); err != nil {
					return err
				}
			}
			// 論理削除は必ず projectID で限定する(中 1)
			for _, id := range byID {
				ok, err := store.MarkIssueDeletedByID(ctx, tx, projectID, id)
				if err != nil {
					return err
				}
				if ok {
					pageDeleted++
				}
			}
			for _, key := range byKey {
				ok, err := store.MarkIssueDeletedByKey(ctx, tx, projectID, key)
				if err != nil {
					return err
				}
				if ok {
					pageDeleted++
				}
			}
			// 削除反映と同一トランザクションでカーソルを前進させる
			return store.SetActivityCursor(ctx, tx, store.DataKindIssues, projectID, maxID)
		}); err != nil {
			return deleted, err
		}
		deleted += pageDeleted

		if maxID <= cursor {
			// 前進しない = これ以上消化できない(無限ループ防止)
			return deleted, nil
		}
		cursor = maxID
		if len(acts) < pageSize {
			return deleted, nil
		}
	}
}

// parseDeletedIssueRef は削除アクティビティの content から課題を特定する。
// content の構造は実機未確認のため防御的にパースし(verification.md 項目 2)、
// 課題 ID(content.id)か key_id + プロジェクトキーが取れた場合のみ ok=true を返す。
func parseDeletedIssueRef(a backlogclient.Activity) (issueID int64, issueKey string, ok bool) {
	if len(a.Content) == 0 {
		return 0, "", false
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(a.Content, &content); err != nil {
		return 0, "", false
	}
	if id, ok := numberField(content, "id"); ok && id > 0 {
		return id, "", true
	}
	// content.id が無い場合は key_id(課題番号)+ プロジェクトキーから課題キーを組む
	if keyID, ok := numberField(content, "key_id"); ok && keyID > 0 && a.ProjectKey != "" {
		return 0, fmt.Sprintf("%s-%d", a.ProjectKey, keyID), true
	}
	return 0, "", false
}

// numberField は JSON オブジェクトから数値フィールドを取り出す。
func numberField(m map[string]json.RawMessage, key string) (int64, bool) {
	raw, exists := m[key]
	if !exists {
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	v, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return v, true
}

// latestActivityID は最新のアクティビティ ID を返す(フル同期の開始境界)。
func (e *Engine) latestActivityID(ctx context.Context) (int64, error) {
	acts, err := e.api.GetSpaceActivities(ctx, backlogclient.ActivityQuery{Count: 1, Order: "desc"})
	if err != nil {
		return 0, err
	}
	if len(acts) == 0 {
		return 0, nil
	}
	return acts[0].ID, nil
}

// updatedSinceFor は差分同期の updatedSince(yyyy-MM-dd)を算出する。
// 規則: 前回同期時刻(UTC)の日付から 1 日引いた日付(verification.md 項目 1)。
// サーバがどの現実的な TZ(UTC−12〜UTC+14)で日付を解釈しても
// 前回同期時刻以降の更新が欠落しない。
func updatedSinceFor(lastSyncedAt string) (string, error) {
	t, err := time.Parse(time.RFC3339, lastSyncedAt)
	if err != nil {
		return "", fmt.Errorf("前回同期時刻を解釈できません(%q): %w", lastSyncedAt, err)
	}
	return t.UTC().AddDate(0, 0, -1).Format("2006-01-02"), nil
}

// SyncProjects は参加プロジェクト一覧を同期する。
// GET /projects は参加プロジェクトのみを返すため、返らなくなったプロジェクトは
// アクセス不能とみなし、課題等のキャッシュごと破棄する(設計書 2 節)。
func (e *Engine) SyncProjects(ctx context.Context) (*Result, error) {
	start := e.now()
	res := &Result{Mode: ModeFull, Warnings: []string{}}

	// 取得失敗はここで打ち切る。以降(= DeleteProjectsNotIn を呼ぶ経路)へは
	// 「API が正常応答した」場合しか到達しない。この前提により、空応答を
	// 取得失敗と取り違えて全キャッシュを破棄することはない(高 1)。
	projects, err := e.api.GetProjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("プロジェクト一覧の取得に失敗しました: %w", err)
	}
	res.Fetched = len(projects)

	fetchedAt := e.nowString()
	keep := make([]int64, 0, len(projects))
	now := e.now().UTC()
	if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
		for _, p := range projects {
			if err := store.UpsertProject(ctx, tx, &store.Project{
				ID: p.ID, ProjectKey: p.ProjectKey, Name: p.Name,
				Archived: p.Archived, RawJSON: p.RawJSON, FetchedAt: fetchedAt,
			}); err != nil {
				return err
			}
			keep = append(keep, p.ID)
			res.Upserted++
		}
		pruned, err := store.DeleteProjectsNotIn(ctx, tx, keep)
		if err != nil {
			return err
		}
		res.Deleted = pruned
		return store.SetSyncCompleted(ctx, tx, store.DataKindProjects, store.ProjectScopeAll,
			now.Format(time.RFC3339), now.Format("2006-01-02"))
	}); err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		res.warn("参加プロジェクトが 0 件でした(ローカルキャッシュをすべて破棄しました)")
	}
	res.DurationMs = e.now().Sub(start).Milliseconds()
	return res, nil
}

// toStoreIssues は API の課題を store の行へ変換する(raw_json を保持)。
func toStoreIssues(issues []backlogclient.Issue, fetchedAt string) []*store.Issue {
	rows := make([]*store.Issue, 0, len(issues))
	for _, i := range issues {
		rows = append(rows, &store.Issue{
			ID: i.ID, IssueKey: i.IssueKey, ProjectID: i.ProjectID,
			Summary: i.Summary, Description: i.Description,
			StatusID: i.StatusID, StatusName: i.StatusName,
			AssigneeID: i.AssigneeID, AssigneeName: i.AssigneeName,
			IssueTypeName: i.IssueTypeName, PriorityName: i.PriorityName,
			Created: i.Created, Updated: i.Updated, DueDate: i.DueDate,
			RawJSON: i.RawJSON, FetchedAt: fetchedAt,
		})
	}
	return rows
}

// nowString は fetched_at 用の現在時刻(UTC・RFC3339)を返す。
func (e *Engine) nowString() string {
	return e.now().UTC().Format(time.RFC3339)
}
