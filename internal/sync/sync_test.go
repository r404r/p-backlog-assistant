package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "example.backlog.jp_12345.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newTestEngine は固定時刻・小さいページサイズのエンジンを返す。
func newTestEngine(t *testing.T, api *fakeAPI, s *store.Store) *Engine {
	t.Helper()
	e := NewEngine(api, s)
	e.now = func() time.Time { return time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC) }
	return e
}

func syncState(t *testing.T, s *store.Store, projectID int64) *store.SyncState {
	t.Helper()
	st, err := s.GetSyncState(context.Background(), store.DataKindIssues, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// --- フル同期 -------------------------------------------------------------

func TestFullSync_PagesAllIssuesAndPromotesCursor(t *testing.T) {
	api := newFakeAPI()
	// 250 件 = ページサイズ 100 で 3 ページ(最終ページは 50 件)
	for i := 1; i <= 250; i++ {
		api.addIssue(int64(i), fmt.Sprintf("EXA-%d", i), 1, "件名",
			fmt.Sprintf("2026-01-%02dT00:00:00Z", (i%28)+1), "2026-08-01T00:00:00Z")
	}
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})

	s := openTempStore(t)
	e := newTestEngine(t, api, s)

	var progress []Progress
	res, err := e.SyncIssues(context.Background(), 1, ModeFull, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeFull {
		t.Errorf("Mode = %s", res.Mode)
	}
	if res.Fetched != 250 || res.Upserted != 250 {
		t.Errorf("fetched = %d / upserted = %d, want 250", res.Fetched, res.Upserted)
	}
	// 全リクエストで projectId[] が指定されていること(スペース全件取得の防止)
	for _, q := range api.issueQueries {
		if len(q.ProjectIDs) != 1 || q.ProjectIDs[0] != 1 {
			t.Errorf("projectId[] = %v", q.ProjectIDs)
		}
		if q.Sort != "created" || q.Order != "asc" || q.Count != 100 {
			t.Errorf("フル同期のページングパラメータ = %+v", q)
		}
	}
	if len(api.issueQueries) != 3 {
		t.Errorf("ページ数 = %d, want 3", len(api.issueQueries))
	}
	// 進捗は total 付きで通知される
	if len(progress) == 0 || progress[len(progress)-1].Total != 250 {
		t.Errorf("進捗 = %+v", progress)
	}

	ids, err := s.ListIssueIDs(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 250 {
		t.Errorf("DB の課題数 = %d, want 250", len(ids))
	}
	// 完了時に activity_start_pending が activity_cursor へ昇格する
	st := syncState(t, s, 1)
	if st.ActivityCursor != 900 || st.ActivityStartPending != 0 {
		t.Errorf("カーソル = %d / pending = %d, want 900 / 0", st.ActivityCursor, st.ActivityStartPending)
	}
	if st.LastSyncedAt == "" || st.LastSyncDate != "2026-08-09" {
		t.Errorf("同期時刻 = %+v", st)
	}
}

// TestFullSync_ConfirmsDeleteCandidates はローカルにのみ残った課題を
// GET /issues/:key の 404 で確認してから削除することを検証する。
func TestFullSync_ConfirmsDeleteCandidates(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "残る", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z")
	api.addIssue(2, "EXA-2", 1, "残る2", "2026-01-02T00:00:00Z", "2026-08-01T00:00:00Z")
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})

	s := openTempStore(t)
	ctx := context.Background()
	// ローカルにだけ存在する 2 件(片方はリモートに実在 = 誤削除してはいけない)
	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 3, IssueKey: "EXA-3", ProjectID: 1, Summary: "削除済み"},
		{ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "ページング中に取り逃した"},
	}); err != nil {
		t.Fatal(err)
	}
	api.deletedKeys["EXA-3"] = true // 404 → 削除確定
	// EXA-4 は一覧では返らない(ページング中の取り逃し)が実在する課題
	api.getIssueOnly["EXA-4"] = backlogclient.Issue{
		ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "ページング中に取り逃した",
		Created: "2026-01-04T00:00:00Z", Updated: "2026-08-01T00:00:00Z",
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 1 {
		t.Errorf("削除件数 = %d, want 1", res.Deleted)
	}
	if len(api.getIssueCalls) != 2 {
		t.Errorf("削除確認の呼び出し = %v, want 2 件", api.getIssueCalls)
	}
	ids, err := s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	// 1, 2(リモート由来)と 4(404 でなかったので残す)
	if len(ids) != 3 {
		t.Errorf("残った課題 = %v, want 3 件(EXA-3 のみ削除)", ids)
	}
	for _, id := range ids {
		if id == 3 {
			t.Error("404 が確認された課題が削除されていない")
		}
	}
}

// TestFullSync_UpsertsIssueRecoveredByDeleteConfirm は削除候補の存在確認(200)で
// 取得できた課題を、同じ同期内で UPSERT して最新化することを確認する(中 2)。
// offset ページング中の変動で一覧から漏れた課題の古いローカル内容を残さない。
func TestFullSync_UpsertsIssueRecoveredByDeleteConfirm(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "一覧に載る", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z")
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})
	// EXA-4 は一覧から漏れたが実在し、内容は更新されている
	api.getIssueOnly["EXA-4"] = backlogclient.Issue{
		ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "更新後の件名",
		StatusName: "処理中", Created: "2026-01-04T00:00:00Z", Updated: "2026-08-09T00:00:00Z",
		RawJSON: `{"id":4}`,
	}

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "更新前の件名",
			StatusName: "未対応", Updated: "2026-07-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 {
		t.Errorf("削除件数 = %d, want 0(実在する課題は削除しない)", res.Deleted)
	}
	got, err := s.SearchIssues(ctx, store.IssueFilter{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	var found *store.Issue
	for i := range got.Issues {
		if got.Issues[i].ID == 4 {
			found = &got.Issues[i]
		}
	}
	if found == nil {
		t.Fatal("一覧から漏れた課題が消えている")
	}
	if found.Summary != "更新後の件名" || found.StatusName != "処理中" ||
		found.Updated != "2026-08-09T00:00:00Z" {
		t.Errorf("最新化されていない: %+v", *found)
	}
	// 検索インデックス(search_text)も更新される
	hit, err := s.SearchIssues(ctx, store.IssueFilter{ProjectID: 1, Keyword: "更新後の件名"})
	if err != nil {
		t.Fatal(err)
	}
	if hit.Total != 1 {
		t.Errorf("更新後の件名で検索できない: %+v", hit)
	}
	if res.Upserted != 2 {
		t.Errorf("upserted = %d, want 2(一覧 1 件 + 存在確認で回収した 1 件)", res.Upserted)
	}
}

// TestFullSync_TooManyCandidatesSkipsDeletion は削除候補が多数(100 件以上)の
// 場合に削除せず警告を返すことを検証する(リコンシリエーションは次マイルストーン)。
func TestFullSync_TooManyCandidatesSkipsDeletion(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "残る", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z")
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})

	s := openTempStore(t)
	ctx := context.Background()
	local := make([]*store.Issue, 0, 120)
	for i := int64(100); i < 220; i++ {
		local = append(local, &store.Issue{ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "旧"})
	}
	if err := s.UpsertIssues(ctx, local); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 {
		t.Errorf("削除件数 = %d, want 0(多数の候補は保留)", res.Deleted)
	}
	if len(api.getIssueCalls) != 0 {
		t.Errorf("多数候補なのに個別確認が走った: %d 件", len(api.getIssueCalls))
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, "/"), "削除候補") {
		t.Errorf("警告 = %v", res.Warnings)
	}
	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 121 {
		t.Errorf("課題数 = %d, want 121(1 件も削除しない)", len(ids))
	}
}

// TestFullSync_AbortKeepsCursor は途中で異常終了したときに
// activity_cursor を前進させないことを検証する(次回フル同期のやり直し)。
func TestFullSync_AbortKeepsCursor(t *testing.T) {
	api := newFakeAPI()
	for i := 1; i <= 150; i++ {
		api.addIssue(int64(i), fmt.Sprintf("EXA-%d", i), 1, "件名",
			fmt.Sprintf("2026-01-%02dT00:00:00Z", (i%28)+1), "2026-08-01T00:00:00Z")
	}
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})
	api.failIssuesAtOffset = 100 // 2 ページ目で失敗

	s := openTempStore(t)
	ctx := context.Background()
	// 事前のカーソル(前回同期の確定値)
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1, ActivityCursor: 500,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeFull, nil); err == nil {
		t.Fatal("ページ取得失敗でもエラーにならなかった")
	}
	st := syncState(t, s, 1)
	if st.ActivityCursor != 500 {
		t.Errorf("ActivityCursor = %d, want 500(異常終了では前進しない)", st.ActivityCursor)
	}
	if st.ActivityStartPending != 900 {
		t.Errorf("ActivityStartPending = %d, want 900(開始境界は保存済み)", st.ActivityStartPending)
	}
}

// --- 差分同期 -------------------------------------------------------------

// TestIncrementalSync_UpdatedSinceIsPreviousDayUTC は verification.md 項目 1 の規則
// (前回同期時刻(UTC)の日付 − 1 日)を検証する。
func TestIncrementalSync_UpdatedSinceIsPreviousDayUTC(t *testing.T) {
	cases := []struct {
		lastSyncedAt string
		want         string
	}{
		{"2026-08-09T12:00:00Z", "2026-08-08"},
		// UTC 00:00 直後 → 前日
		{"2026-08-09T00:00:00Z", "2026-08-08"},
		// JST 表記でも UTC へ変換してから日付を取る(JST 08:30 = UTC 前日 23:30)
		{"2026-08-09T08:30:00+09:00", "2026-08-07"},
		// 月初 → 前月末
		{"2026-03-01T00:30:00Z", "2026-02-28"},
	}
	for _, c := range cases {
		got, err := updatedSinceFor(c.lastSyncedAt)
		if err != nil {
			t.Fatalf("%s: %v", c.lastSyncedAt, err)
		}
		if got != c.want {
			t.Errorf("updatedSinceFor(%s) = %s, want %s", c.lastSyncedAt, got, c.want)
		}
	}
}

func TestIncrementalSync_OnlyChangedRowsUpserted(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "変化なし", "2026-01-01T00:00:00Z", "2026-08-05T00:00:00Z")
	api.addIssue(2, "EXA-2", 1, "更新後", "2026-01-02T00:00:00Z", "2026-08-08T00:00:00Z")
	api.addIssue(3, "EXA-3", 1, "新規", "2026-08-08T00:00:00Z", "2026-08-08T00:00:00Z")

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "変化なし", Updated: "2026-08-05T00:00:00Z"},
		{ID: 2, IssueKey: "EXA-2", ProjectID: 1, Summary: "更新前", Updated: "2026-08-06T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeIncremental {
		t.Errorf("Mode = %s", res.Mode)
	}
	// updatedSince = 前回同期日 − 1 日 = 2026-08-07 → 3 件中 2 件が対象
	if got := api.issueQueries[0].UpdatedSince; got != "2026-08-07" {
		t.Errorf("updatedSince = %q, want 2026-08-07", got)
	}
	if res.Fetched != 2 {
		t.Errorf("fetched = %d, want 2", res.Fetched)
	}
	if res.Upserted != 2 {
		t.Errorf("upserted = %d, want 2(更新 1 + 新規 1)", res.Upserted)
	}

	got, err := s.SearchIssues(ctx, store.IssueFilter{ProjectID: 1, Keyword: "更新後"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 {
		t.Errorf("更新が反映されていない: %+v", got)
	}
}

// TestIncrementalSync_PagesIssuesAtPageBoundary は差分同期の課題取得が
// ページサイズ(100)の境界で複数ページを正しく消化することを確認する(低 1(a))。
// 件数がページサイズの倍数のとき、空ページを 1 回読んで終了する。
func TestIncrementalSync_PagesIssuesAtPageBoundary(t *testing.T) {
	api := newFakeAPI()
	// 200 件 = ちょうど 2 ページぶん(3 回目は 0 件で終了)
	for i := 1; i <= 200; i++ {
		api.addIssue(int64(i), fmt.Sprintf("EXA-%d", i), 1, "件名",
			fmt.Sprintf("2026-01-%02dT%02d:00:00Z", (i%28)+1, i%24), "2026-08-08T00:00:00Z")
	}
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 200 || res.Upserted != 200 {
		t.Errorf("fetched = %d / upserted = %d, want 200 / 200", res.Fetched, res.Upserted)
	}
	if len(api.issueQueries) != 3 {
		t.Fatalf("ページ数 = %d, want 3(100 件 × 2 + 空ページ)", len(api.issueQueries))
	}
	for i, q := range api.issueQueries {
		if q.Offset != i*100 || q.Count != 100 {
			t.Errorf("ページ %d のパラメータ = offset %d / count %d", i, q.Offset, q.Count)
		}
		if q.UpdatedSince != "2026-08-07" {
			t.Errorf("ページ %d の updatedSince = %q", i, q.UpdatedSince)
		}
		if len(q.ProjectIDs) != 1 || q.ProjectIDs[0] != 1 {
			t.Errorf("ページ %d の projectId[] = %v", i, q.ProjectIDs)
		}
	}
	ids, err := s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 200 {
		t.Errorf("DB の課題数 = %d, want 200", len(ids))
	}
}

// TestIncrementalSync_SkipsUnchangedRows は updated が同じ行を UPSERT しないことを確認する。
func TestIncrementalSync_SkipsUnchangedRows(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "リモートの件名", "2026-01-01T00:00:00Z", "2026-08-08T00:00:00Z")

	s := openTempStore(t)
	ctx := context.Background()
	// updated は同じだが件名が違う(= ローカル値が優先され、更新されない)
	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "ローカルの件名", Updated: "2026-08-08T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 1 || res.Upserted != 0 {
		t.Errorf("fetched = %d / upserted = %d, want 1 / 0", res.Fetched, res.Upserted)
	}
}

func TestIncrementalSync_DeleteDetectionAdvancesCursor(t *testing.T) {
	api := newFakeAPI()
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 11, IssueKey: "EXA-11", ProjectID: 1, Summary: "ID で削除", Updated: "2026-08-01T00:00:00Z"},
		{ID: 12, IssueKey: "EXA-12", ProjectID: 1, Summary: "key_id で削除", Updated: "2026-08-01T00:00:00Z"},
		{ID: 13, IssueKey: "EXA-13", ProjectID: 1, Summary: "残る", Updated: "2026-08-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}

	api.addActivity(101, 1, "EXA", map[string]any{"id": 11, "summary": "削除された課題"})
	api.addActivity(102, 1, "EXA", map[string]any{"key_id": 12}) // ID 無し → key_id + projectKey
	api.addActivity(103, 1, "EXA", nil)                          // 判別不能 → 警告
	api.addActivity(104, 2, "EXB", map[string]any{"id": 13})     // 別プロジェクト → 対象外

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 {
		t.Errorf("削除件数 = %d, want 2", res.Deleted)
	}
	if len(res.Warnings) == 0 {
		t.Error("判別不能な content の警告が無い")
	}
	// activityTypeId[]=4 / minId = 前回カーソル / order=asc で問い合わせる
	q := api.activityQueries[0]
	if len(q.ActivityTypeIDs) != 1 || q.ActivityTypeIDs[0] != 4 {
		t.Errorf("activityTypeId[] = %v", q.ActivityTypeIDs)
	}
	if q.MinID != 100 || q.Order != "asc" || q.Count != 100 {
		t.Errorf("アクティビティのパラメータ = %+v", q)
	}
	// カーソルは消化済みの最大 ID まで前進する
	st := syncState(t, s, 1)
	if st.ActivityCursor != 104 {
		t.Errorf("ActivityCursor = %d, want 104", st.ActivityCursor)
	}

	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 1 || ids[0] != 13 {
		t.Errorf("残った課題 = %v, want [13]", ids)
	}
}

// seedIncrementalState は差分同期の前提(ローカル課題 + 確定済みカーソル)を作る。
func seedIncrementalState(t *testing.T, s *store.Store, issues []*store.Issue, cursor int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: cursor,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestIncrementalSync_SkipsActivityWithoutProject は project.id が欠落した
// 削除アクティビティでは課題を削除せず、警告のみ付けることを確認する(中 1)。
func TestIncrementalSync_SkipsActivityWithoutProject(t *testing.T) {
	api := newFakeAPI()
	s := openTempStore(t)
	ctx := context.Background()
	seedIncrementalState(t, s, []*store.Issue{
		{ID: 11, IssueKey: "EXA-11", ProjectID: 1, Summary: "残る", Updated: "2026-08-01T00:00:00Z"},
	}, 100)

	// project.id が 0(欠落)。content.id は当該プロジェクトの課題を指している
	api.addActivity(101, 0, "", map[string]any{"id": 11})

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 {
		t.Errorf("削除件数 = %d, want 0(プロジェクトを特定できないイベントは削除しない)", res.Deleted)
	}
	if !strings.Contains(strings.Join(res.Warnings, "/"), "プロジェクト") {
		t.Errorf("警告 = %v, want プロジェクト特定不可の警告", res.Warnings)
	}
	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 1 || ids[0] != 11 {
		t.Errorf("残った課題 = %v, want [11]", ids)
	}
}

// TestIncrementalSync_SkipsActivityOfOtherProject は project.id が対象外の
// イベントで、content.id が同期対象プロジェクトの課題を指していても
// 削除しないことを確認する(中 1)。
func TestIncrementalSync_SkipsActivityOfOtherProject(t *testing.T) {
	api := newFakeAPI()
	s := openTempStore(t)
	ctx := context.Background()
	seedIncrementalState(t, s, []*store.Issue{
		{ID: 11, IssueKey: "EXA-11", ProjectID: 1, Summary: "残る", Updated: "2026-08-01T00:00:00Z"},
	}, 100)

	// 別プロジェクト(2)のイベントだが content.id はプロジェクト 1 の課題 ID
	api.addActivity(101, 2, "EXB", map[string]any{"id": 11})
	// 課題キー経路も同様に対象外
	api.addActivity(102, 2, "EXA", map[string]any{"key_id": 11})

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 0 {
		t.Errorf("削除件数 = %d, want 0(別プロジェクトのイベント)", res.Deleted)
	}
	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 1 || ids[0] != 11 {
		t.Errorf("残った課題 = %v, want [11]", ids)
	}
}

// TestIncrementalSync_DeleteAndCursorAreAtomic は削除反映に失敗した場合に
// カーソルが前進しないこと(同一トランザクション)を検証する。
func TestIncrementalSync_DeleteAndCursorAreAtomic(t *testing.T) {
	api := newFakeAPI()
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssues(ctx, []*store.Issue{
		{ID: 11, IssueKey: "EXA-11", ProjectID: 1, Updated: "2026-08-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}
	api.addActivity(101, 1, "EXA", map[string]any{"id": 11})

	e := newTestEngine(t, api, s)
	// 削除反映(トランザクション内)を故意に失敗させる
	e.applyDeletionsHook = func() error { return fmt.Errorf("フェイク: 削除反映に失敗") }

	if _, err := e.SyncIssues(ctx, 1, ModeIncremental, nil); err == nil {
		t.Fatal("削除反映失敗でもエラーにならなかった")
	}
	st := syncState(t, s, 1)
	if st.ActivityCursor != 100 {
		t.Errorf("ActivityCursor = %d, want 100(削除反映が失敗したら前進しない)", st.ActivityCursor)
	}
	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 1 {
		t.Errorf("ロールバックされていない: %v", ids)
	}
}

// TestIncrementalSync_ActivitiesPaging は activities を全ページ消化することを確認する。
func TestIncrementalSync_ActivitiesPaging(t *testing.T) {
	api := newFakeAPI()
	s := openTempStore(t)
	ctx := context.Background()

	local := make([]*store.Issue, 0, 150)
	for i := int64(1); i <= 150; i++ {
		local = append(local, &store.Issue{ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1,
			Updated: "2026-08-01T00:00:00Z"})
	}
	if err := s.UpsertIssues(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSyncState(ctx, &store.SyncState{
		DataKind: store.DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-08T00:00:00Z", ActivityCursor: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 150; i++ {
		api.addActivity(1000+i, 1, "EXA", map[string]any{"id": i})
	}

	res, err := newTestEngine(t, api, s).SyncIssues(ctx, 1, ModeIncremental, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 150 {
		t.Errorf("削除件数 = %d, want 150", res.Deleted)
	}
	if len(api.activityQueries) < 2 {
		t.Errorf("アクティビティのページ数 = %d, want 2 以上", len(api.activityQueries))
	}
	st := syncState(t, s, 1)
	if st.ActivityCursor != 1150 {
		t.Errorf("ActivityCursor = %d, want 1150", st.ActivityCursor)
	}
}

// --- モード判定 -----------------------------------------------------------

func TestDecideMode(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		state *store.SyncState
		want  Mode
	}{
		{"状態なし", nil, ModeFull},
		{"カーソルなし", &store.SyncState{LastSyncedAt: "2026-08-08T00:00:00Z"}, ModeFull},
		{"同期時刻なし", &store.SyncState{ActivityCursor: 1}, ModeFull},
		{"時刻が不正", &store.SyncState{ActivityCursor: 1, LastSyncedAt: "not-a-time"}, ModeFull},
		{"長期未同期", &store.SyncState{ActivityCursor: 1, LastSyncedAt: "2026-07-01T00:00:00Z"}, ModeFull},
		{"未来時刻(時計ずれ)", &store.SyncState{ActivityCursor: 1, LastSyncedAt: "2026-09-01T00:00:00Z"}, ModeFull},
		{"通常", &store.SyncState{ActivityCursor: 1, LastSyncedAt: "2026-08-08T00:00:00Z"}, ModeIncremental},
	}
	for _, c := range cases {
		if got := DecideMode(c.state, now); got != c.want {
			t.Errorf("%s: DecideMode = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestSyncIssues_AutoFallsBackToFull は ModeAuto がカーソル無しでフル同期になることを確認する。
func TestSyncIssues_AutoFallsBackToFull(t *testing.T) {
	api := newFakeAPI()
	api.addIssue(1, "EXA-1", 1, "件名", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z")
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})

	s := openTempStore(t)
	res, err := newTestEngine(t, api, s).SyncIssues(context.Background(), 1, ModeAuto, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeFull {
		t.Errorf("Mode = %s, want full", res.Mode)
	}
}

// --- プロジェクト同期 -----------------------------------------------------

func TestSyncProjects_UpsertsAndPrunes(t *testing.T) {
	api := newFakeAPI()
	api.projects = []backlogclient.Project{
		{ID: 1, ProjectKey: "EXA", Name: "検証用", RawJSON: `{"id":1}`},
		{ID: 2, ProjectKey: "EXB", Name: "別件", RawJSON: `{"id":2}`},
	}

	s := openTempStore(t)
	ctx := context.Background()
	// アクセス不能になるプロジェクト(3)のキャッシュを事前に作る
	if err := s.UpsertProject(ctx, &store.Project{ID: 3, ProjectKey: "EXC", Name: "旧"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssues(ctx, []*store.Issue{{ID: 99, IssueKey: "EXC-1", ProjectID: 3}}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 2 || res.Upserted != 2 {
		t.Errorf("res = %+v", res)
	}
	if res.Deleted != 1 {
		t.Errorf("破棄したプロジェクト = %d, want 1", res.Deleted)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Errorf("プロジェクト = %+v", projects)
	}
	// アクセス不能プロジェクトの課題キャッシュも破棄される
	ids, _ := s.ListIssueIDs(ctx, 3)
	if len(ids) != 0 {
		t.Errorf("アクセス不能プロジェクトの課題が残っている: %v", ids)
	}
	st, err := s.GetSyncState(ctx, store.DataKindProjects, store.ProjectScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.LastSyncedAt == "" {
		t.Errorf("projects の同期時刻が記録されていない: %+v", st)
	}
}

// TestSyncProjects_EmptyResponseDiscardsCache は GET /projects が正常に
// 空配列を返した場合(全プロジェクトから除外された)に、ローカルキャッシュを
// すべて破棄することを確認する(高 1。設計書 2 節の情報漏えい防止)。
func TestSyncProjects_EmptyResponseDiscardsCache(t *testing.T) {
	api := newFakeAPI()
	api.projects = nil // 正常応答で 0 件

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "EXA", Name: "旧"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssues(ctx, []*store.Issue{{ID: 99, IssueKey: "EXA-1", ProjectID: 1}}); err != nil {
		t.Fatal(err)
	}

	res, err := newTestEngine(t, api, s).SyncProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 1 {
		t.Errorf("破棄したプロジェクト = %d, want 1", res.Deleted)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("プロジェクトキャッシュが残っている: %+v", projects)
	}
	ids, _ := s.ListIssueIDs(ctx, 1)
	if len(ids) != 0 {
		t.Errorf("課題キャッシュが残っている: %v", ids)
	}
}

// TestSyncProjects_InvalidResponseKeepsCache は、GET /projects の応答が不正
// (JSON null・id 欠落)で backlogclient がエラーを返した場合に、
// キャッシュ全削除(DeleteProjectsNotIn)へ到達しないことを確認する(中 3)。
// 不正応答の検出自体は backlogclient.GetProjects 側のテストで担保する。
func TestSyncProjects_InvalidResponseKeepsCache(t *testing.T) {
	api := newFakeAPI()
	api.projectsErr = fmt.Errorf("プロジェクト一覧の応答が不正です(JSON 配列ではありません)")

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "EXA", Name: "旧"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertIssues(ctx, []*store.Issue{{ID: 99, IssueKey: "EXA-1", ProjectID: 1}}); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestEngine(t, api, s).SyncProjects(ctx); err == nil {
		t.Fatal("不正応答でもエラーにならなかった")
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("不正応答でプロジェクトキャッシュが破棄された: %+v", projects)
	}
	ids, err := s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("不正応答で課題キャッシュが破棄された: %v", ids)
	}
}

// TestSyncProjects_FetchErrorKeepsCache は GET /projects の取得失敗が
// キャッシュ破棄へ到達しないこと(空応答との取り違えが起きない前提)を確認する。
func TestSyncProjects_FetchErrorKeepsCache(t *testing.T) {
	api := newFakeAPI()
	api.projectsErr = fmt.Errorf("フェイク: プロジェクト一覧の取得に失敗")

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "EXA", Name: "旧"}); err != nil {
		t.Fatal(err)
	}

	if _, err := newTestEngine(t, api, s).SyncProjects(ctx); err == nil {
		t.Fatal("取得失敗でもエラーにならなかった")
	}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("取得失敗でキャッシュが破棄された: %+v", projects)
	}
}
