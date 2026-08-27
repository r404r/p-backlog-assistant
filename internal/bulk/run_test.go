package bulk

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/store"
)

// newRunFixture は「更新 1 行 + 新規追加 1 行」のジョブを作り、
// リモート側の課題(競合検知の再取得用)も用意する。
func newRunFixture(t *testing.T) (*store.Store, *fakeAPI, int64) {
	t.Helper()
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("取り込みエラー = %+v", res.Errors)
	}
	api := newFakeAPI()
	api.remote["EXA-1"] = backlogclient.Issue{
		ID: 101, IssueKey: "EXA-1", ProjectID: testProjectID, Updated: "2026-08-01T00:00:00Z",
	}
	return st, api, res.JobID
}

// newCreateRunFixture は「同じ内容の新規追加 2 行」だけのジョブを作る
// (再送前突合で同名行を取り違えないことの検証用。2 回目 高 1)。
func newCreateRunFixture(t *testing.T) (*store.Store, *fakeAPI, int64) {
	t.Helper()
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("取り込みエラー = %+v", res.Errors)
	}
	return st, newFakeAPI(), res.JobID
}

// jobCreatedShifted はジョブ作成時刻を d だけずらした RFC3339 文字列を返す
// (突合の時刻フィルタ検証用)。
func jobCreatedShifted(t *testing.T, st *store.Store, jobID int64, d time.Duration) string {
	t.Helper()
	job, err := st.GetJob(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	created, err := time.Parse(time.RFC3339, job.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return created.Add(d).UTC().Format(time.RFC3339)
}

// createdIssue は新規追加行(件名「新規課題」・種別 11・優先度 3)と
// 完全一致する、ジョブ作成後に作られた課題を返す。
func createdIssue(t *testing.T, st *store.Store, jobID, issueID int64) backlogclient.Issue {
	t.Helper()
	return backlogclient.Issue{
		ID: issueID, IssueKey: "EXA-" + strconv.FormatInt(issueID, 10),
		ProjectID: testProjectID, Summary: "新規課題",
		IssueTypeID: 11, PriorityID: 3,
		Created: jobCreatedShifted(t, st, jobID, time.Minute),
	}
}

// markRowSending は行を sending 状態にする(送信直後の異常終了の再現)。
func markRowSending(t *testing.T, st *store.Store, jobID int64, rowNo int) {
	t.Helper()
	if err := st.UpdateRowStatus(context.Background(), jobID, rowNo, store.RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
}

// testEngine は待機を記録する実行エンジン(中 4 のペーシング検証用)。
type testEngine struct {
	*Engine
	sleeps []time.Duration
}

// newTestEngine は待機を記録用フェイクへ差し替えた実行エンジンを返す(中 4)。
// 実待機(1 秒/リクエスト)でテストを遅くしないため、既定の sleep は使わない。
func newTestEngine(api API, st *store.Store) *testEngine {
	te := &testEngine{Engine: NewEngine(api, st)}
	te.Engine.sleep = func(d time.Duration) { te.sleeps = append(te.sleeps, d) }
	return te
}

// timedCall は仮想時計で記録した API 呼び出し(名前と時刻)。
type timedCall struct {
	name string
	at   time.Time
}

// timedAPI は API 呼び出しの名前と時刻を記録するラッパー。
// ペーシングは「呼び出しと呼び出しの間隔」であり、待機の回数だけでは
// どの呼び出しの間に空いたのかを確かめられないため、時刻で検証する。
type timedAPI struct {
	API
	now   func() time.Time
	calls []timedCall
}

func (a *timedAPI) record(name string) {
	a.calls = append(a.calls, timedCall{name: name, at: a.now()})
}

// names は記録した呼び出しの名前を順に返す(呼び出し順の検証用)。
func (a *timedAPI) names() []string {
	names := make([]string, 0, len(a.calls))
	for _, c := range a.calls {
		names = append(names, c.name)
	}
	return names
}

func (a *timedAPI) GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error) {
	a.record("GetIssue")
	return a.API.GetIssue(ctx, issueIDOrKey)
}

func (a *timedAPI) GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error) {
	a.record("GetIssues")
	return a.API.GetIssues(ctx, q)
}

func (a *timedAPI) CreateIssue(ctx context.Context, in backlogclient.IssueCreate) (*backlogclient.Issue, error) {
	a.record("CreateIssue")
	return a.API.CreateIssue(ctx, in)
}

func (a *timedAPI) UpdateIssue(ctx context.Context, issueIDOrKey string, in backlogclient.IssueUpdate) (*backlogclient.Issue, error) {
	a.record("UpdateIssue")
	return a.API.UpdateIssue(ctx, issueIDOrKey, in)
}

// pacingFixture は仮想時計で動く実行エンジンと、時刻付きの API 記録。
type pacingFixture struct {
	engine *Engine
	api    *timedAPI
	clock  time.Time
}

// newPacingFixture は待機で仮想時刻だけを進める実行エンジンを組み立てる。
// 実時間は経過しないため、1 秒間隔の検証を待たずに行える
// (時刻は仮想時計のみで進むので、間隔は待機によってのみ生まれる)。
func newPacingFixture(api API, st *store.Store) *pacingFixture {
	f := &pacingFixture{clock: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}
	f.api = &timedAPI{API: api, now: f.now}
	f.engine = NewEngine(f.api, st)
	f.engine.now = f.now
	f.engine.sleep = func(d time.Duration) { f.clock = f.clock.Add(d) }
	return f
}

func (f *pacingFixture) now() time.Time { return f.clock }

func rowStatuses(t *testing.T, st *store.Store, jobID int64) map[int]store.JobRow {
	t.Helper()
	rows, err := st.ListJobRows(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	m := map[int]store.JobRow{}
	for _, r := range rows {
		m[r.RowNo] = r
	}
	return m
}

func TestRun_UpdatesAndCreates(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()

	var progress []Progress
	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, nil,
		func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 2 || res.Failed != 0 || res.Conflict != 0 {
		t.Errorf("結果 = %+v", res)
	}
	if len(api.updates) != 1 || api.updates[0].key != "EXA-1" {
		t.Fatalf("更新呼び出し = %+v", api.updates)
	}
	if api.updates[0].in.Summary == nil || *api.updates[0].in.Summary != "新しい件名" {
		t.Errorf("更新内容 = %+v", api.updates[0].in)
	}
	if len(api.creates) != 1 || api.creates[0].Summary != "新規課題" ||
		api.creates[0].ProjectID != testProjectID ||
		api.creates[0].IssueTypeID != 11 || api.creates[0].PriorityID != 3 {
		t.Errorf("追加呼び出し = %+v", api.creates)
	}
	// 更新前にリモートの updated を再取得している(競合検知)
	if len(api.getCalls) != 1 || api.getCalls[0] != "EXA-1" {
		t.Errorf("GetIssue 呼び出し = %v", api.getCalls)
	}

	rows := rowStatuses(t, st, jobID)
	if rows[2].Status != store.RowStatusDone || rows[3].Status != store.RowStatusDone {
		t.Errorf("行状態 = %+v", rows)
	}
	// 新規追加は作成された課題 ID を記録する(再開時の二重作成確認に使う)
	if rows[3].ResultIssueID == 0 {
		t.Errorf("作成された課題 ID が記録されていない: %+v", rows[3])
	}
	// 課題キーも記録する(結果レポート・行明細で新規追加行のキーを表示するため)。
	// 書き込むのは done へ遷移する時だけ(送信中に書くと再開時に更新行と誤認される)
	if want := "EXA-" + strconv.FormatInt(rows[3].ResultIssueID, 10); rows[3].IssueKey != want {
		t.Errorf("課題キー = %q, want %q", rows[3].IssueKey, want)
	}
	if len(progress) == 0 || progress[len(progress)-1].Processed != 2 || progress[len(progress)-1].Total != 2 {
		t.Errorf("進捗 = %+v", progress)
	}
	job, err := st.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobStatusDone {
		t.Errorf("ジョブ状態 = %q", job.Status)
	}
}

// TestRun_ConflictSkipsRow はリモートが更新されている行を
// 送信せず conflict として記録することを確認する(設計書 5 節)。
func TestRun_ConflictSkipsRow(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	api.remote["EXA-1"] = backlogclient.Issue{
		ID: 101, IssueKey: "EXA-1", Updated: "2026-08-09T12:00:00Z", // 基準と異なる
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict != 1 || res.Done != 1 {
		t.Errorf("結果 = %+v", res)
	}
	if len(api.updates) != 0 {
		t.Errorf("競合行が送信された: %+v", api.updates)
	}
	rows := rowStatuses(t, st, jobID)
	if rows[2].Status != store.RowStatusConflict || rows[2].Error == "" {
		t.Errorf("行 2 = %+v", rows[2])
	}
}

// TestRun_ForceOverwritesConflict は force 指定で競合行を上書き実行できることを確認する。
func TestRun_ForceOverwritesConflict(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	api.remote["EXA-1"] = backlogclient.Issue{ID: 101, IssueKey: "EXA-1", Updated: "2026-08-09T12:00:00Z"}
	engine := newTestEngine(api, st)

	if _, err := engine.Run(ctx, jobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// conflict になった行を force で再実行する
	res, err := engine.Run(ctx, jobID, RunOptions{Force: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 || res.Conflict != 0 {
		t.Errorf("結果 = %+v", res)
	}
	if len(api.updates) != 1 {
		t.Errorf("更新呼び出し = %+v", api.updates)
	}
	if rowStatuses(t, st, jobID)[2].Status != store.RowStatusDone {
		t.Errorf("行 2 = %+v", rowStatuses(t, st, jobID)[2])
	}
}

// TestRun_DoesNotResendSendingRows は再開時に sending 行を
// 自動再送しないことを確認する(二重作成防止。設計書 5 節)。
func TestRun_DoesNotResendSendingRows(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	// 新規追加行が「送信中」のまま残っている状態を作る(送信直後の異常終了)
	if err := st.UpdateRowStatus(ctx, jobID, 3, store.RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Errorf("sending 行が自動再送された: %+v", api.creates)
	}
	if len(res.Warnings) == 0 {
		t.Error("sending 行の警告が返っていない")
	}
	if rowStatuses(t, st, jobID)[3].Status != store.RowStatusSending {
		t.Errorf("行 3 = %+v", rowStatuses(t, st, jobID)[3])
	}

	// 明示的な再送指示があれば送信する
	res2, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 1 {
		t.Errorf("再送指示で送信されなかった: %+v", api.creates)
	}
	if res2.Done != 1 {
		t.Errorf("結果 = %+v", res2)
	}
}

// TestRun_Cancel はキャンセル指示で行間の処理を止め、
// 未処理行を pending のまま残すことを確認する。
func TestRun_Cancel(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()

	var canceled atomic.Bool
	api.beforeCall = func() { canceled.Store(true) } // 1 件目の送信でキャンセル指示

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, canceled.Load, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done != 1 {
		t.Errorf("結果 = %+v", res)
	}
	if len(api.creates) != 0 {
		t.Errorf("キャンセル後に送信された: %+v", api.creates)
	}
	rows := rowStatuses(t, st, jobID)
	if rows[3].Status != store.RowStatusPending {
		t.Errorf("未処理行 = %+v, want pending", rows[3])
	}
	job, err := st.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != store.JobStatusCanceled {
		t.Errorf("ジョブ状態 = %q, want canceled", job.Status)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "中断") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_RecordsAPIError は送信失敗を error として行に記録することを確認する。
func TestRun_RecordsAPIError(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	api.updateErr = errors.New("Backlog API がエラーを返しました(HTTP 400)")

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Done != 1 {
		t.Errorf("結果 = %+v", res)
	}
	row := rowStatuses(t, st, jobID)[2]
	if row.Status != store.RowStatusError || !strings.Contains(row.Error, "HTTP 400") {
		t.Errorf("行 2 = %+v", row)
	}
}

// TestRun_SendsClearParameters はクリア指定(#CLEAR#)が
// 空値として API へ渡ることを確認する。
func TestRun_SendsClearParameters(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "", "", "", "", "", "", "", "#CLEAR#", "", "#CLEAR#", "#CLEAR#", "2026-08-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("取り込みエラー = %+v", res.Errors)
	}
	api := newFakeAPI()
	api.remote["EXA-1"] = backlogclient.Issue{ID: 101, IssueKey: "EXA-1", Updated: "2026-08-01T00:00:00Z"}

	if _, err := newTestEngine(api, st).Run(context.Background(), res.JobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 1 {
		t.Fatalf("更新呼び出し = %+v", api.updates)
	}
	in := api.updates[0].in
	if in.AssigneeID == nil || *in.AssigneeID != 0 {
		t.Errorf("担当者クリア = %+v", in.AssigneeID)
	}
	if in.DueDate == nil || *in.DueDate != "" {
		t.Errorf("期限クリア = %+v", in.DueDate)
	}
	if in.Description == nil || *in.Description != "" {
		t.Errorf("詳細クリア = %+v", in.Description)
	}
}

// TestRun_MissingRemoteIssueIsError はリモートに存在しない課題の更新を
// エラー行として記録することを確認する。
func TestRun_MissingRemoteIssueIsError(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	delete(api.remote, "EXA-1")

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Errorf("結果 = %+v", res)
	}
	if len(api.updates) != 0 {
		t.Errorf("存在しない課題へ送信された: %+v", api.updates)
	}
}

// TestRun_CountsSkippedRows は変更なし行を送信せず skip として数えることを確認する。
func TestRun_CountsSkippedRows(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-2", "画面崩れ", "", "", "", "", "", "", "", "", "", "", "2026-08-02T00:00:00Z"},
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("取り込みエラー = %+v", res.Errors)
	}
	api := newFakeAPI()
	runRes, err := newTestEngine(api, st).Run(context.Background(), res.JobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runRes.Skipped != 1 || runRes.Done != 1 {
		t.Errorf("結果 = %+v", runRes)
	}
	if len(api.getCalls) != 0 {
		t.Errorf("skip 行で API が呼ばれた: %v", api.getCalls)
	}
}

// TestRun_ResendSkipsAlreadyCreatedIssue は sending のまま残った新規追加行を
// 再送する前にリモートと突合し、作成済みなら POST しないことを確認する(高 3)。
func TestRun_ResendSkipsAlreadyCreatedIssue(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	// 新規追加行が「送信中」のまま残っている状態(送信直後の異常終了)
	markRowSending(t, st, jobID, 3)
	// リモートには送信内容と完全一致する課題が既に作成されている
	api.listed = []backlogclient.Issue{
		{ID: 2001, IssueKey: "EXA-2001", ProjectID: testProjectID, Summary: "別の課題"},
		createdIssue(t, st, jobID, 2002),
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Fatalf("作成済みなのに再送された: %+v", api.creates)
	}
	// 更新行(pending)1 件 + 突合で done にした新規追加行 1 件
	if res.Done != 2 {
		t.Errorf("結果 = %+v", res)
	}
	// 突合はジョブ作成日以降・対象プロジェクトに限定して取得する
	if len(api.listCalls) == 0 {
		t.Fatal("突合の課題一覧取得が行われていない")
	}
	q := api.listCalls[0]
	if len(q.ProjectIDs) != 1 || q.ProjectIDs[0] != testProjectID || q.CreatedSince == "" {
		t.Errorf("突合クエリ = %+v", q)
	}
	row := rowStatuses(t, st, jobID)[3]
	if row.Status != store.RowStatusDone || row.ResultIssueID != 2002 {
		t.Errorf("行 3 = %+v(done かつ作成済み課題 ID を期待)", row)
	}
	// 突合で見つけた課題のキーも記録する(送信して作成した場合と同じ扱い)
	if row.IssueKey != "EXA-2002" {
		t.Errorf("行 3 の課題キー = %q, want EXA-2002", row.IssueKey)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "作成済みを検出したため再送しませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_ResendCreatesWhenNoMatch は突合で一致が無い場合に再送することを確認する(高 3)。
func TestRun_ResendCreatesWhenNoMatch(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	if err := st.UpdateRowStatus(ctx, jobID, 3, store.RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	api.listed = []backlogclient.Issue{
		{ID: 2001, IssueKey: "EXA-2001", ProjectID: testProjectID, Summary: "別の課題"},
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 1 {
		t.Fatalf("一致が無いのに再送されなかった: %+v", api.creates)
	}
	if res.Done != 2 {
		t.Errorf("結果 = %+v", res)
	}
}

// TestRun_ResendKeepsSendingWhenMatchFails は突合に失敗した場合、
// 二重作成を避けるため送信せず sending のまま残すことを確認する(高 3)。
func TestRun_ResendKeepsSendingWhenMatchFails(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	if err := st.UpdateRowStatus(ctx, jobID, 3, store.RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}
	api.listErr = errors.New("課題一覧を取得できません")

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Errorf("突合できないのに送信された: %+v", api.creates)
	}
	if rowStatuses(t, st, jobID)[3].Status != store.RowStatusSending {
		t.Errorf("行 3 = %+v, want sending", rowStatuses(t, st, jobID)[3])
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "確認できなかったため再送しませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_ResendIgnoresIssueCreatedBeforeJob はジョブ作成前に作られた同名課題を
// 「作成済み」と誤認しないことを確認する(2 回目 高 1(a))。
// createdSince は日付単位でしか指定できないため、取得後に時刻で絞り込む。
func TestRun_ResendIgnoresIssueCreatedBeforeJob(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	markRowSending(t, st, jobID, 3)

	// 同じ内容だがジョブ作成前(同日)に作られた別課題
	old := createdIssue(t, st, jobID, 2001)
	old.Created = jobCreatedShifted(t, st, jobID, -time.Hour)
	api.listed = []backlogclient.Issue{old}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 1 {
		t.Fatalf("ジョブ前の同名課題を作成済みと誤認した: creates = %+v", api.creates)
	}
	row := rowStatuses(t, st, jobID)[3]
	if row.Status != store.RowStatusDone || row.ResultIssueID == 2001 {
		t.Errorf("行 3 = %+v(新規作成された課題 ID を期待)", row)
	}
	if res.Done != 2 {
		t.Errorf("結果 = %+v", res)
	}
}

// TestRun_ResendResolvesRowsToDifferentIssues は同一ジョブ内の同名 2 行が
// 別々の課題へ解決されることを確認する(2 回目 高 1(c))。
// 既に他行が claim した課題は候補から除外する。
func TestRun_ResendResolvesRowsToDifferentIssues(t *testing.T) {
	st, api, jobID := newCreateRunFixture(t)
	ctx := context.Background()

	// 行 2 は完了済み(課題 2002 を作成済みとして claim している)
	markRowSending(t, st, jobID, 2)
	if err := st.UpdateRowStatus(ctx, jobID, 2, store.RowStatusDone, 2002, ""); err != nil {
		t.Fatal(err)
	}
	// 行 3 は送信中のまま残っている
	markRowSending(t, st, jobID, 3)

	api.listed = []backlogclient.Issue{
		createdIssue(t, st, jobID, 2002),
		createdIssue(t, st, jobID, 2003),
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Fatalf("作成済みなのに再送された: %+v", api.creates)
	}
	row := rowStatuses(t, st, jobID)[3]
	if row.Status != store.RowStatusDone || row.ResultIssueID != 2003 {
		t.Errorf("行 3 = %+v(他行が claim していない課題 2003 を期待)", row)
	}
	if res.Done != 1 {
		t.Errorf("結果 = %+v", res)
	}
}

// TestRun_ResendKeepsSendingWhenAmbiguous は候補が複数残る場合に
// 再送せず sending のまま残すことを確認する(2 回目 高 1(c))。
func TestRun_ResendKeepsSendingWhenAmbiguous(t *testing.T) {
	st, api, jobID := newCreateRunFixture(t)
	ctx := context.Background()
	markRowSending(t, st, jobID, 2)
	markRowSending(t, st, jobID, 3)

	// どちらの行のものか特定できない同内容の課題が 2 件ある
	api.listed = []backlogclient.Issue{
		createdIssue(t, st, jobID, 2002),
		createdIssue(t, st, jobID, 2003),
	}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Fatalf("特定できないのに再送された: %+v", api.creates)
	}
	rows := rowStatuses(t, st, jobID)
	if rows[2].Status != store.RowStatusSending || rows[3].Status != store.RowStatusSending {
		t.Errorf("行状態 = %+v(sending のままを期待)", rows)
	}
	if res.Done != 0 || res.Failed != 0 {
		t.Errorf("結果 = %+v", res)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"),
		"作成済みの可能性がある課題を特定できませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_ResendKeepsSendingWhenPartialMatch は件名だけが一致する課題しか
// 見つからない場合に、再送も完了扱いもしないことを確認する(2 回目 高 1(b)(c))。
func TestRun_ResendKeepsSendingWhenPartialMatch(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	markRowSending(t, st, jobID, 3)

	partial := createdIssue(t, st, jobID, 2002)
	partial.PriorityID = 2 // 送信内容(優先度 3)と異なる
	api.listed = []backlogclient.Issue{partial}

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.creates) != 0 {
		t.Fatalf("部分一致だけなのに再送された: %+v", api.creates)
	}
	if rowStatuses(t, st, jobID)[3].Status != store.RowStatusSending {
		t.Errorf("行 3 = %+v, want sending", rowStatuses(t, st, jobID)[3])
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"),
		"Backlog 上で確認してから再送してください") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_ResendConflictWhenIssueDeleted は再送中の更新行で対象課題が
// 削除されていた場合に conflict として記録することを確認する(2 回目 高 3)。
func TestRun_ResendConflictWhenIssueDeleted(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	markRowSending(t, st, jobID, 2)
	delete(api.remote, "EXA-1") // 課題が削除された

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 0 {
		t.Errorf("削除済み課題へ送信された: %+v", api.updates)
	}
	row := rowStatuses(t, st, jobID)[2]
	if row.Status != store.RowStatusConflict || row.Error == "" {
		t.Errorf("行 2 = %+v, want conflict", row)
	}
	if res.Conflict != 1 || res.Failed != 0 {
		t.Errorf("結果 = %+v", res)
	}
}

// TestRun_ResendKeepsSendingWhenGetIssueFails は再送中の更新行で
// 競合検知の取得が一時的に失敗した場合、error に確定させず
// sending のまま残すことを確認する(2 回目 高 3)。
func TestRun_ResendKeepsSendingWhenGetIssueFails(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	markRowSending(t, st, jobID, 2)
	api.getErr = errors.New("一時的なネットワーク障害")

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("一時的な取得失敗が error として確定された: %+v", res)
	}
	if rowStatuses(t, st, jobID)[2].Status != store.RowStatusSending {
		t.Errorf("行 2 = %+v, want sending", rowStatuses(t, st, jobID)[2])
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "送信結果を確認できませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_FirstRunKeepsPendingWhenGetIssueFails は初回実行で競合検知の取得が
// 失敗した場合に、まだ何も送っていないため pending のまま残すことを
// 確認する(2 回目 高 3)。
func TestRun_FirstRunKeepsPendingWhenGetIssueFails(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	api.getErr = errors.New("一時的なネットワーク障害")

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("一時的な取得失敗が error として確定された: %+v", res)
	}
	if len(api.updates) != 0 {
		t.Errorf("状態を確認できないのに送信された: %+v", api.updates)
	}
	row := rowStatuses(t, st, jobID)[2]
	if row.Status != store.RowStatusPending {
		t.Errorf("行 2 = %+v, want pending", row)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "確認できなかったため送信しませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_WarnsUnprocessedCountOnCancel はキャンセル時に未処理の件数を
// 警告へ載せることを確認する(2 回目 中 2)。
func TestRun_WarnsUnprocessedCountOnCancel(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	var canceled atomic.Bool
	api.beforeCall = func() { canceled.Store(true) } // 1 件目の送信でキャンセル指示

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, canceled.Load, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "キャンセルされました(未処理 1 件") {
		t.Errorf("警告 = %v(件数付きのキャンセル通知を期待)", res.Warnings)
	}
}

// TestRun_ResendUpdatesUnconditionally は更新行(PATCH)は冪等のため
// 突合せず無条件に再送することを確認する(高 3)。
func TestRun_ResendUpdatesUnconditionally(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	ctx := context.Background()
	if err := st.UpdateRowStatus(ctx, jobID, 2, store.RowStatusSending, 0, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{ResendSending: true}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 1 {
		t.Errorf("更新行が再送されなかった: %+v", api.updates)
	}
	if len(api.listCalls) != 0 {
		t.Errorf("更新行で突合の取得が行われた: %+v", api.listCalls)
	}
}

// TestRun_UncertainErrorKeepsRowSending は成否不明のエラーを失敗として確定せず、
// sending のまま残して警告することを確認する(高 4)。
func TestRun_UncertainErrorKeepsRowSending(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	api.updateErr = &backlogclient.UncertainError{
		Op: "課題の更新", Err: errors.New("応答を受信できませんでした"),
	}

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("成否不明が失敗として確定された: %+v", res)
	}
	row := rowStatuses(t, st, jobID)[2]
	if row.Status != store.RowStatusSending {
		t.Errorf("行 2 = %+v, want sending", row)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "送信結果を確認できませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestRun_UnfinishedCreateRowKeepsEmptyIssueKey は、結末が確定していない
// 新規追加行に課題キーを書かないことを確認する。
//
// row.IssueKey == "" が「新規追加行」の目印であり、送信中(sending)・失敗(error)の
// 行にキーが入ると、再開時に更新行と誤認して再送前突合(二重作成防止)が
// 働かなくなる。キーを書き込むのは done へ遷移する時だけ。
func TestRun_UnfinishedCreateRowKeepsEmptyIssueKey(t *testing.T) {
	ctx := context.Background()
	// 成否不明(sending のまま残る)
	st, api, jobID := newRunFixture(t)
	api.createErr = &backlogclient.UncertainError{
		Op: "課題の追加", Err: errors.New("応答を受信できませんでした"),
	}
	if _, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if row := rowStatuses(t, st, jobID)[3]; row.Status != store.RowStatusSending || row.IssueKey != "" {
		t.Errorf("行 3 = %+v(sending かつ課題キーは空を期待)", row)
	}

	// 確定的拒否(error として確定する)
	st2, api2, jobID2 := newRunFixture(t)
	api2.createErr = &backlogclient.RejectedError{
		StatusCode: 400, Method: "POST", Path: "/api/v2/issues", Message: "summary は必須です",
	}
	if _, err := newTestEngine(api2, st2).Run(ctx, jobID2, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if row := rowStatuses(t, st2, jobID2)[3]; row.Status != store.RowStatusError || row.IssueKey != "" {
		t.Errorf("行 3 = %+v(error かつ課題キーは空を期待)", row)
	}
}

// TestRun_IncompleteCreateResponseKeepsSending は、作成の応答から課題を
// 特定できない(ID・課題キーのどちらかが欠ける)場合に完了扱いせず、
// sending のまま残して回復可能にすることを確認する(1 回目 中 2)。
//
// done にしてしまうと、その行は二度と再送・突合の対象にならないため、
// 「作成されたかもしれない課題」を追えなくなる。成否不明の書き込み(高 4)と
// 同じく、判断を先送りして利用者の再送指示に委ねる。
func TestRun_IncompleteCreateResponseKeepsSending(t *testing.T) {
	ctx := context.Background()
	// 送信して作成した経路: 応答に課題キーが無い
	st, api, jobID := newRunFixture(t)
	api.createNoIssueKey = true

	res, err := newTestEngine(api, st).Run(ctx, jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 完了は更新行の 1 件だけ(不完全な応答の行は数えない)
	if res.Done != 1 || res.Failed != 0 {
		t.Errorf("結果 = %+v", res)
	}
	row := rowStatuses(t, st, jobID)[3]
	if row.Status != store.RowStatusSending || row.IssueKey != "" || row.ResultIssueID != 0 {
		t.Errorf("行 3 = %+v(sending・課題キー空・結果 ID 0 を期待)", row)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "作成された課題を特定できませんでした") {
		t.Errorf("警告 = %v", res.Warnings)
	}

	// 再送前突合の経路: 突合で見つけた課題に課題キーが無い
	st2, api2, jobID2 := newRunFixture(t)
	markRowSending(t, st2, jobID2, 3)
	matched := createdIssue(t, st2, jobID2, 2002)
	matched.IssueKey = ""
	api2.listed = []backlogclient.Issue{matched}

	res2, err := newTestEngine(api2, st2).Run(ctx, jobID2, RunOptions{ResendSending: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(api2.creates) != 0 {
		t.Fatalf("作成済みなのに再送された: %+v", api2.creates)
	}
	if res2.Done != 1 {
		t.Errorf("結果 = %+v(完了は更新行の 1 件のみを期待)", res2)
	}
	row2 := rowStatuses(t, st2, jobID2)[3]
	if row2.Status != store.RowStatusSending || row2.IssueKey != "" || row2.ResultIssueID != 0 {
		t.Errorf("行 3 = %+v(sending・課題キー空・結果 ID 0 を期待)", row2)
	}
	if !strings.Contains(strings.Join(res2.Warnings, "\n"), "作成された課題を特定できませんでした") {
		t.Errorf("警告 = %v", res2.Warnings)
	}
}

// TestRun_RejectedErrorMarksRowError は確定的拒否(HTTP ステータス受信)を
// error として確定することを確認する(高 4)。
func TestRun_RejectedErrorMarksRowError(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	api.updateErr = &backlogclient.RejectedError{
		StatusCode: 400, Method: "PATCH", Path: "/api/v2/issues/EXA-1",
		Message: "priorityId は必須です",
	}

	res, err := newTestEngine(api, st).Run(context.Background(), jobID, RunOptions{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Errorf("結果 = %+v", res)
	}
	row := rowStatuses(t, st, jobID)[2]
	if row.Status != store.RowStatusError || !strings.Contains(row.Error, "priorityId は必須です") {
		t.Errorf("行 2 = %+v", row)
	}
}

// TestRun_PacesWriteRequests は書き込みの間に最低 1 秒の間隔を空けることを
// 確認する(中 4。2 行実行で 1 回以上待機する)。
func TestRun_PacesWriteRequests(t *testing.T) {
	st, api, jobID := newRunFixture(t)
	engine := newTestEngine(api, st)

	if _, err := engine.Run(context.Background(), jobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(engine.sleeps) < 1 {
		t.Fatalf("待機回数 = %d, want 1 以上", len(engine.sleeps))
	}
	for _, d := range engine.sleeps {
		if d > writeInterval {
			t.Errorf("待機時間 = %v, want %v 以下", d, writeInterval)
		}
	}
	if writeInterval < time.Second {
		t.Errorf("writeInterval = %v, want 1 秒以上", writeInterval)
	}
}

// TestRun_PacesEveryAPICall は「API 呼び出しは最低 1 秒間隔」を、
// 呼び出しと呼び出しの実間隔で確認する(中 4)。
//
// 特に更新行では、競合確認の取得(GetIssue)と書き込み(UpdateIssue)の間にも
// 間隔が空くことを求める。画面の所要時間見積り(frontend/src/lib/bulkEstimate.ts)
// と日英ユーザ文書は「新規は 1 呼び出し・更新は競合確認を含む 2 呼び出しで、
// 呼び出しの間は最低 1 秒」と案内しており、取得だけ間隔を空けないと
// 表示・文書と実際の挙動がずれる(レート消費も見積りより速くなる)。
//
// 待機の回数だけを見る TestRun_PacesWriteRequests では、書き込みの間の 1 回で
// 条件を満たしてしまいこの穴を検出できない。仮想時計で呼び出し時刻を記録し、
// 間隔そのものを検証する。
//
// 行の並びは「更新 → 新規追加 → 更新」にする。GetIssue がジョブ最初の呼び出しに
// なる並びだけでは、GetIssue の**前**の待機を落としても後続の markCall で
// 間隔が空いてしまい、取得前のペーシング欠落を検出できない
// (書き込み → 次の行の GetIssue の間隔が要る)。
func TestRun_PacesEveryAPICall(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
		{"EXA-3", "新しい件名 3", "", "", "", "", "", "", "", "", "", "", "2026-08-03T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("取り込みエラー = %+v", res.Errors)
	}
	api := newFakeAPI()
	api.remote["EXA-1"] = backlogclient.Issue{
		ID: 101, IssueKey: "EXA-1", ProjectID: testProjectID, Updated: "2026-08-01T00:00:00Z",
	}
	api.remote["EXA-3"] = backlogclient.Issue{
		ID: 103, IssueKey: "EXA-3", ProjectID: testProjectID, Updated: "2026-08-03T00:00:00Z",
	}
	f := newPacingFixture(api, st)

	if _, err := f.engine.Run(context.Background(), res.JobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	// 更新行(競合確認 → 更新)・新規追加行(追加)・更新行(競合確認 → 更新)
	want := "GetIssue,UpdateIssue,CreateIssue,GetIssue,UpdateIssue"
	if got := strings.Join(f.api.names(), ","); got != want {
		t.Fatalf("API 呼び出し = %v, want %v", got, want)
	}
	for i := 1; i < len(f.api.calls); i++ {
		prev, cur := f.api.calls[i-1], f.api.calls[i]
		if d := cur.at.Sub(prev.at); d < writeInterval {
			t.Errorf("%s → %s の間隔 = %v, want %v 以上", prev.name, cur.name, d, writeInterval)
		}
	}
}

// TestFetchMasterData はマスタ取得が 3 種類そろって返ることを確認する。
func TestFetchMasterData(t *testing.T) {
	api := newFakeAPI()
	master, err := FetchMasterData(context.Background(), api, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(master.IssueTypes) != 2 || len(master.Priorities) != 3 || len(master.Statuses) != 3 {
		t.Errorf("master = %+v", master)
	}
	if master.IssueTypes[0].Name != "タスク" {
		t.Errorf("種別 = %+v", master.IssueTypes)
	}
}

// TestFetchMasterData_CustomFields はカスタム属性定義もマスタに載ること、
// プロジェクトが ID の文字列表現で渡ることを確認する。
func TestFetchMasterData_CustomFields(t *testing.T) {
	api := newFakeAPI()
	master, err := FetchMasterData(context.Background(), api, testProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if got := api.customFieldCalls; len(got) != 1 || got[0] != strconv.FormatInt(testProjectID, 10) {
		t.Errorf("カスタム属性の取得引数 = %v", got)
	}
	if len(master.CustomFields) != 2 {
		t.Fatalf("カスタム属性 = %+v", master.CustomFields)
	}
	if master.CustomFields[0].Name != "顧客名" || master.CustomFields[0].TypeID != customfield.TypeText {
		t.Errorf("カスタム属性[0] = %+v", master.CustomFields[0])
	}
	if len(master.CustomFields[1].Items) != 2 {
		t.Errorf("カスタム属性[1].Items = %+v", master.CustomFields[1].Items)
	}
}

// TestFetchMasterData_CustomFieldsDegrades はカスタム属性を使えないスペース
// (プラン未対応 = 404 / 権限不足 = 403)でもマスタ取得全体は成功し、
// カスタム属性だけが空になることを確認する。
func TestFetchMasterData_CustomFieldsDegrades(t *testing.T) {
	for name, cause := range map[string]error{
		"404(プラン未対応)": backlogclient.ErrNotFound,
		"403(権限不足)":   backlogclient.ErrPermissionDenied,
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI()
			api.customFieldsErr = fmt.Errorf("%w: GET /api/v2/projects/1/customFields", cause)

			master, err := FetchMasterData(context.Background(), api, testProjectID)
			if err != nil {
				t.Fatalf("カスタム属性の失敗でマスタ取得全体が失敗した: %v", err)
			}
			if master.CustomFields == nil || len(master.CustomFields) != 0 {
				t.Errorf("カスタム属性 = %#v, want 空スライス", master.CustomFields)
			}
			// 他のマスタは従来どおり取得できる
			if len(master.IssueTypes) != 2 || len(master.Priorities) != 3 || len(master.Statuses) != 3 {
				t.Errorf("master = %+v", master)
			}
		})
	}
}

// TestFetchMasterData_CustomFieldsPropagatesOtherErrors は 404 / 403 以外の失敗
// (通信断・サーバエラー等)は握りつぶさずエラーにすることを確認する。
func TestFetchMasterData_CustomFieldsPropagatesOtherErrors(t *testing.T) {
	api := newFakeAPI()
	api.customFieldsErr = errors.New("HTTP リクエストに失敗しました")

	if _, err := FetchMasterData(context.Background(), api, testProjectID); err == nil {
		t.Fatal("カスタム属性の取得失敗がエラーにならなかった")
	} else if !strings.Contains(err.Error(), "カスタム属性") {
		t.Errorf("エラーメッセージ = %q", err.Error())
	}
}
