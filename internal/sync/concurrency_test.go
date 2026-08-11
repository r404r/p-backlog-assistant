package sync

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// --- 共通ヘルパー ---------------------------------------------------------

// seedOrderedIssues は created 昇順が ID 昇順と一致する課題を n 件仕込む
// (ページ境界と書き込み順の検証に使う)。
func seedOrderedIssues(api *fakeAPI, projectID int64, n int) {
	for i := 1; i <= n; i++ {
		api.addIssue(int64(i), fmt.Sprintf("EXA-%d", i), projectID, "件名",
			fmt.Sprintf("2026-01-01T%02d:%02d:00Z", i/60, i%60), "2026-08-08T00:00:00Z")
	}
}

// pipelineQuery はパイプライン検証用のページクエリを組み立てる。
func pipelineQuery(projectID int64) func(int) backlogclient.IssueQuery {
	return func(page int) backlogclient.IssueQuery {
		return backlogclient.IssueQuery{
			ProjectIDs: []int64{projectID},
			Sort:       "created", Order: "asc",
			Count: pageSize, Offset: page * pageSize,
		}
	}
}

// assertNoGoroutineLeak は before 時点より goroutine が増えていないことを確認する
// (パイプライン・ワーカーの取り残しがないこと)。
func assertNoGoroutineLeak(t *testing.T, before int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("goroutine が残存しています: before = %d, after = %d", before, runtime.NumGoroutine())
}

// --- 課題取得パイプライン -------------------------------------------------

// TestFetchIssuePagesPipelined_PreservesPageOrder は取得と書き込みを重ねても
// ページの処理順(= DB 書き込み順)が取得順どおりであることを確認する。
func TestFetchIssuePagesPipelined_PreservesPageOrder(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 250)
	e := NewEngine(api, nil)

	var gotPages []int
	var gotFirstIDs []int64
	err := e.fetchIssuePagesPipelined(context.Background(), pipelineQuery(1),
		func(page int, issues []backlogclient.Issue) error {
			gotPages = append(gotPages, page)
			if len(issues) > 0 {
				gotFirstIDs = append(gotFirstIDs, issues[0].ID)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gotPages) != "[0 1 2]" {
		t.Errorf("処理したページ = %v, want [0 1 2]", gotPages)
	}
	if fmt.Sprint(gotFirstIDs) != "[1 101 201]" {
		t.Errorf("各ページ先頭の課題 ID = %v, want [1 101 201]", gotFirstIDs)
	}
	// 取得側も offset 昇順のまま(offset ページングの整合性)
	var offsets []int
	for _, q := range api.recordedIssueQueries() {
		offsets = append(offsets, q.Offset)
	}
	if fmt.Sprint(offsets) != "[0 100 200]" {
		t.Errorf("取得 offset = %v, want [0 100 200]", offsets)
	}
}

// TestFetchIssuePagesPipelined_HandleErrorStopsProducer は書き込み側の失敗で
// 取得側 goroutine が確実に停止し(全ページを取り切らない)、
// goroutine が残らないことを確認する。
func TestFetchIssuePagesPipelined_HandleErrorStopsProducer(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 1000) // 10 ページ
	api.issuesDelay = 5 * time.Millisecond
	e := NewEngine(api, nil)

	wantErr := errors.New("フェイク: 書き込みに失敗")
	before := runtime.NumGoroutine()
	err := e.fetchIssuePagesPipelined(context.Background(), pipelineQuery(1),
		func(page int, issues []backlogclient.Issue) error {
			if page == 1 {
				return wantErr
			}
			return nil
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("エラー = %v, want %v", err, wantErr)
	}
	if n := len(api.recordedIssueQueries()); n >= 10 {
		t.Errorf("取得ページ数 = %d, want 10 未満(取得側が停止していない)", n)
	}
	assertNoGoroutineLeak(t, before)
}

// TestFetchIssuePagesPipelined_ProducerErrorReturned は取得側の失敗が
// そのまま返ることを確認する(offset 付きのメッセージを維持)。
func TestFetchIssuePagesPipelined_ProducerErrorReturned(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 300)
	api.failIssuesAtOffset = 100
	e := NewEngine(api, nil)

	before := runtime.NumGoroutine()
	err := e.fetchIssuePagesPipelined(context.Background(), pipelineQuery(1),
		func(page int, issues []backlogclient.Issue) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "offset 100") {
		t.Fatalf("エラー = %v, want offset 100 の取得失敗", err)
	}
	assertNoGoroutineLeak(t, before)
}

// TestFetchIssuePagesPipelined_ReturnsFirstErrorInPageOrder は取得側・書き込み側の
// 両方が失敗した場合、ページ順で先に起きたエラー(この場合は書き込み側)を返すことを
// 確認する。
func TestFetchIssuePagesPipelined_ReturnsFirstErrorInPageOrder(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 500)
	api.failIssuesAtOffset = 200 // ページ 2 の取得が失敗
	e := NewEngine(api, nil)

	wantErr := errors.New("フェイク: ページ 1 の書き込みに失敗")
	err := e.fetchIssuePagesPipelined(context.Background(), pipelineQuery(1),
		func(page int, issues []backlogclient.Issue) error {
			if page == 1 {
				return wantErr
			}
			return nil
		})
	if !errors.Is(err, wantErr) {
		t.Fatalf("エラー = %v, want ページ 1 の書き込み失敗(ページ順で先)", err)
	}
}

// TestFetchIssuePagesPipelined_ContextCancelPropagates は呼び出し元の
// キャンセルが取得側・書き込み側の双方へ伝播し、キャンセルを返すことを確認する。
func TestFetchIssuePagesPipelined_ContextCancelPropagates(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 1000) // 10 ページ(バッファに収まらない)
	e := NewEngine(api, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := runtime.NumGoroutine()
	handled := 0
	err := e.fetchIssuePagesPipelined(ctx, pipelineQuery(1),
		func(page int, issues []backlogclient.Issue) error {
			handled++
			cancel() // 1 ページ処理した時点で中断
			return nil
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("エラー = %v, want context.Canceled", err)
	}
	if handled >= 10 {
		t.Errorf("処理ページ数 = %d, want 10 未満(キャンセルが効いていない)", handled)
	}
	assertNoGoroutineLeak(t, before)
}

// TestFetchIssuePagesPipelined_OverlapsFetchAndWrite はページ取得と書き込みが
// 重なり、直列実行より短時間で完了することを確認する。
func TestFetchIssuePagesPipelined_OverlapsFetchAndWrite(t *testing.T) {
	const pages = 6
	const unit = 25 * time.Millisecond

	api := newFakeAPI()
	seedOrderedIssues(api, 1, pages*pageSize)
	api.issuesDelay = unit
	e := NewEngine(api, nil)
	ctx := context.Background()
	q := pipelineQuery(1)

	// 直列基準: 取得 → 書き込み → 次ページ取得 を順に行った場合の所要時間。
	serialStart := time.Now()
	for page := 0; page < pages; page++ {
		if _, err := api.GetIssues(ctx, q(page)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(unit)
	}
	serial := time.Since(serialStart)

	pipeStart := time.Now()
	err := e.fetchIssuePagesPipelined(ctx, q, func(page int, issues []backlogclient.Issue) error {
		time.Sleep(unit)
		return nil
	})
	pipelined := time.Since(pipeStart)
	if err != nil {
		t.Fatal(err)
	}
	// 理論値は直列の約 58%(取得と書き込みが 1 ページぶんずれて重なる)。
	if pipelined >= serial*85/100 {
		t.Errorf("パイプライン = %v, 直列 = %v(オーバーラップしていない)", pipelined, serial)
	}
	t.Logf("直列 = %v, パイプライン = %v", serial, pipelined)
}

// TestFullSync_ProgressOrderIsPreserved はパイプライン化後もフル同期の
// 進捗通知が取得順どおりであることを確認する。
func TestFullSync_ProgressOrderIsPreserved(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 250)
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})

	s := openTempStore(t)
	e := newTestEngine(t, api, s)

	var progress []Progress
	res, err := e.SyncIssues(context.Background(), 1, ModeFull, func(p Progress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 250 || res.Upserted != 250 {
		t.Fatalf("fetched = %d / upserted = %d, want 250", res.Fetched, res.Upserted)
	}
	var fetchSeq []int
	for _, p := range progress {
		if p.Phase == PhaseFetch {
			fetchSeq = append(fetchSeq, p.Fetched)
		}
	}
	if fmt.Sprint(fetchSeq) != "[100 200 250]" {
		t.Errorf("fetch 進捗 = %v, want [100 200 250]", fetchSeq)
	}
	ids, err := s.ListIssueIDs(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 250 {
		t.Errorf("DB の課題数 = %d, want 250", len(ids))
	}
}

// TestIncrementalSync_ProgressOrderIsPreserved は差分同期のパイプライン化でも
// 進捗の順序と件数が保たれることを確認する。
func TestIncrementalSync_ProgressOrderIsPreserved(t *testing.T) {
	api := newFakeAPI()
	seedOrderedIssues(api, 1, 250)

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	var fetchSeq []int
	res, err := e.SyncIssues(ctx, 1, ModeIncremental, func(p Progress) {
		if p.Phase == PhaseFetch {
			fetchSeq = append(fetchSeq, p.Fetched)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 250 {
		t.Errorf("fetched = %d, want 250", res.Fetched)
	}
	if fmt.Sprint(fetchSeq) != "[100 200 250]" {
		t.Errorf("fetch 進捗 = %v, want [100 200 250]", fetchSeq)
	}
}

// --- プロジェクト単位取得の有界並列 ---------------------------------------

// TestFetchProjectMembers_PreservesOrderAndWarnings は並列取得でも
// 結果の並び・警告の順序・失敗件数が元のプロジェクト順どおりであることを確認する。
func TestFetchProjectMembers_PreservesOrderAndWarnings(t *testing.T) {
	api := newFakeAPI()
	var projects []store.Project
	for i := int64(1); i <= 8; i++ {
		key := fmt.Sprintf("EX%02d", i)
		projects = append(projects, store.Project{ID: i, ProjectKey: key})
		api.projectUsers[i] = []backlogclient.User{fakeUser(i, fmt.Sprintf("u%d", i), "利用者", 2)}
		api.projectAdmins[i] = []backlogclient.User{fakeUser(i, fmt.Sprintf("u%d", i), "利用者", 2)}
		api.projectTeams[i] = []backlogclient.Team{fakeTeam(i, fmt.Sprintf("チーム%d", i))}
	}
	api.projectUsersErr[3] = errors.New("フェイク: 参加者取得に失敗")
	api.projectAdminsErr[5] = errors.New("フェイク: 管理者取得に失敗")
	api.projectTeamsErr[7] = errors.New("フェイク: チーム取得に失敗")

	e := NewEngine(api, nil)
	res := &Result{Warnings: []string{}}
	before := runtime.NumGoroutine()
	fetched, stats := e.fetchProjectMembers(context.Background(), projects, res, true)
	assertNoGoroutineLeak(t, before)

	if len(fetched) != len(projects) {
		t.Fatalf("結果件数 = %d, want %d", len(fetched), len(projects))
	}
	for i, pm := range fetched {
		if pm.projectID != projects[i].ID || pm.projectKey != projects[i].ProjectKey {
			t.Fatalf("結果 %d = %d/%s, want %d/%s(元順が崩れている)",
				i, pm.projectID, pm.projectKey, projects[i].ID, projects[i].ProjectKey)
		}
	}
	if stats.targets != 8 || stats.userFailed != 1 || stats.teamFailed != 1 {
		t.Errorf("集計 = %+v, want targets 8 / userFailed 1 / teamFailed 1", stats)
	}
	// 警告はプロジェクト順(EX03 参加者 → EX05 管理者 → EX07 チーム)
	if len(res.Warnings) != 3 {
		t.Fatalf("警告 = %v, want 3 件", res.Warnings)
	}
	wantOrder := []string{"EX03", "EX05", "EX07"}
	for i, key := range wantOrder {
		if !strings.Contains(res.Warnings[i], key) {
			t.Errorf("警告[%d] = %q, want %s を含む", i, res.Warnings[i], key)
		}
	}
	// membersComplete の判定(参加者失敗・管理者失敗)が維持されている
	if fetched[2].membersComplete {
		t.Error("参加者取得に失敗したプロジェクトが membersComplete になっている")
	}
	if fetched[4].membersComplete {
		t.Error("管理者取得に失敗したプロジェクトが membersComplete になっている")
	}
	if !fetched[0].membersComplete {
		t.Error("両方取得できたプロジェクトが membersComplete でない")
	}
	if len(fetched[6].teams) != 0 {
		t.Error("チーム取得に失敗したプロジェクトにチームが入っている")
	}
}

// TestFetchProjectMembers_BoundedConcurrency は並列度が 4 を超えず、
// かつ実際に並行実行されていることを確認する。
func TestFetchProjectMembers_BoundedConcurrency(t *testing.T) {
	api := newFakeAPI()
	api.projectCallDelay = 10 * time.Millisecond
	var projects []store.Project
	for i := int64(1); i <= 12; i++ {
		projects = append(projects, store.Project{ID: i, ProjectKey: fmt.Sprintf("EX%02d", i)})
		api.projectUsers[i] = []backlogclient.User{fakeUser(i, fmt.Sprintf("u%d", i), "利用者", 2)}
		api.projectAdmins[i] = nil
	}

	e := NewEngine(api, nil)
	res := &Result{Warnings: []string{}}
	fetched, stats := e.fetchProjectMembers(context.Background(), projects, res, false)
	if len(fetched) != 12 || stats.userFailed != 0 {
		t.Fatalf("結果 = %d 件 / %+v", len(fetched), stats)
	}
	maxInFlight := api.maxProjectInFlight()
	if maxInFlight > projectFetchConcurrency {
		t.Errorf("同時実行数 = %d, want %d 以下", maxInFlight, projectFetchConcurrency)
	}
	if maxInFlight < 2 {
		t.Errorf("同時実行数 = %d, want 2 以上(並列化されていない)", maxInFlight)
	}
	t.Logf("同時実行数の最大 = %d(上限 %d)", maxInFlight, projectFetchConcurrency)
}

// TestFetchProjectMembers_ResultMatchesSerial は並列版の結果が
// 直列に取得した場合と完全に一致する(順序・内容・警告・集計)ことを確認する。
func TestFetchProjectMembers_ResultMatchesSerial(t *testing.T) {
	build := func() (*fakeAPI, []store.Project) {
		api := newFakeAPI()
		var projects []store.Project
		for i := int64(1); i <= 9; i++ {
			projects = append(projects, store.Project{ID: i, ProjectKey: fmt.Sprintf("EX%02d", i)})
			api.projectUsers[i] = []backlogclient.User{
				fakeUser(i, fmt.Sprintf("u%d", i), "利用者", 2),
				fakeUser(100+i, fmt.Sprintf("a%d", i), "管理者", 1),
			}
			api.projectAdmins[i] = []backlogclient.User{fakeUser(100+i, fmt.Sprintf("a%d", i), "管理者", 1)}
			api.projectTeams[i] = []backlogclient.Team{fakeTeam(i%3+1, fmt.Sprintf("チーム%d", i%3+1))}
		}
		api.projectUsersErr[2] = errors.New("フェイク: 参加者取得に失敗")
		api.projectAdminsErr[4] = errors.New("フェイク: 管理者取得に失敗")
		api.projectTeamsErr[6] = errors.New("フェイク: チーム取得に失敗")
		return api, projects
	}

	// 直列基準(1 プロジェクトずつ fetchOneProjectMembers を呼ぶ)
	serialAPI, projects := build()
	serialEngine := NewEngine(serialAPI, nil)
	var wantMembers []projectMembers
	wantStats := projectFetchStats{targets: len(projects)}
	var wantWarnings []string
	for _, p := range projects {
		pm, oc := serialEngine.fetchOneProjectMembers(context.Background(), p, true)
		wantMembers = append(wantMembers, pm)
		if oc.userFailed {
			wantStats.userFailed++
		}
		if oc.teamFailed {
			wantStats.teamFailed++
		}
		wantWarnings = append(wantWarnings, oc.warnings...)
	}

	parallelAPI, _ := build()
	e := NewEngine(parallelAPI, nil)
	res := &Result{Warnings: []string{}}
	got, stats := e.fetchProjectMembers(context.Background(), projects, res, true)

	if stats != wantStats {
		t.Errorf("集計 = %+v, want %+v", stats, wantStats)
	}
	if fmt.Sprint(res.Warnings) != fmt.Sprint(wantWarnings) {
		t.Errorf("警告 = %v, want %v", res.Warnings, wantWarnings)
	}
	if len(got) != len(wantMembers) {
		t.Fatalf("結果件数 = %d, want %d", len(got), len(wantMembers))
	}
	for i := range got {
		if fmt.Sprintf("%+v", got[i]) != fmt.Sprintf("%+v", wantMembers[i]) {
			t.Errorf("結果[%d] = %+v, want %+v", i, got[i], wantMembers[i])
		}
	}
}

// TestSyncUsers_DegradedPath_ParallelIsDeterministic は縮退パスの同期を
// 2 回実行しても、合成されたユーザ順・警告順が変わらないことを確認する
// (並列化しても集約はプロジェクト順で行われる)。
func TestSyncUsers_DegradedPath_ParallelIsDeterministic(t *testing.T) {
	run := func() (*Result, []string) {
		api := newFakeAPI()
		api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
		api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
		s := openTempStore(t)
		var projects []store.Project
		for i := int64(1); i <= 6; i++ {
			projects = append(projects, store.Project{ID: i, ProjectKey: fmt.Sprintf("EX%02d", i), Name: "検証用"})
			api.projectUsers[i] = []backlogclient.User{fakeUser(i, fmt.Sprintf("u%d", i), "利用者", 2)}
			api.projectAdmins[i] = nil
			api.projectTeams[i] = []backlogclient.Team{fakeTeam(i, fmt.Sprintf("チーム%d", i))}
		}
		api.projectUsersErr[2] = errors.New("フェイク: 参加者取得に失敗")
		api.projectTeamsErr[5] = errors.New("フェイク: チーム取得に失敗")
		seedProjects(t, s, projects...)

		e := newTestEngine(t, api, s)
		res, err := e.SyncUsers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, r := range userRows(t, s) {
			codes = append(codes, r.UserCode)
		}
		return res, codes
	}

	res1, codes1 := run()
	res2, codes2 := run()
	if fmt.Sprint(res1.Warnings) != fmt.Sprint(res2.Warnings) {
		t.Errorf("警告が実行ごとに異なる:\n1 回目 = %v\n2 回目 = %v", res1.Warnings, res2.Warnings)
	}
	if fmt.Sprint(codes1) != fmt.Sprint(codes2) {
		t.Errorf("合成ユーザが実行ごとに異なる: %v / %v", codes1, codes2)
	}
	if res1.Fetched != 5 {
		t.Errorf("合成ユーザ数 = %d, want 5(1 プロジェクトは取得失敗)", res1.Fetched)
	}
}
