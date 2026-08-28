package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

// newSyncTestService は同期テスト用に、プロファイルを 1 件保存した
// ProfileService とそのプロファイル ID を返す。
func newSyncTestService(t *testing.T, fake *fakeConnector) (*ProfileService, string) {
	t.Helper()
	s, _, _ := newTestService(t, fake)
	res, err := s.SaveProfile(context.Background(), "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, res.Profile.ID
}

// findSyncState は ListSyncStates から該当する 1 行を取り出す(見つからなければ nil)。
func findSyncState(t *testing.T, s *ProfileService, profileID, dataKind string, projectID int64) *store.SyncState {
	t.Helper()
	states, err := s.ListSyncStates(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range states {
		if states[i].DataKind == dataKind && states[i].ProjectID == projectID {
			return &states[i]
		}
	}
	return nil
}

func fakeIssue(id int64, key string, projectID int64, summary, created, updated string) backlogclient.Issue {
	raw, _ := json.Marshal(map[string]any{"id": id, "issueKey": key, "summary": summary})
	return backlogclient.Issue{
		ID: id, IssueKey: key, ProjectID: projectID, Summary: summary,
		StatusName: "処理中", AssigneeName: "担当 太郎",
		Created: created, Updated: updated, RawJSON: string(raw),
	}
}

func TestStoreForProfile_CachesAndFailsWithoutUser(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)

	st1, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if st1 != st2 {
		t.Error("同一プロファイルで DB がキャッシュされていない")
	}

	// 接続実績の無いプロファイル(LastUserID = 0)は DB を特定できない
	if _, err := s.storeForProfile("unknown"); err == nil {
		t.Error("存在しないプロファイルでエラーにならなかった")
	}
}

func TestSyncProjects_ThroughService(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		projects: []backlogclient.Project{
			{ID: 1, ProjectKey: "EXA", Name: "検証用", RawJSON: `{"id":1}`},
		},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	res, err := s.SyncProjects(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Upserted != 1 {
		t.Errorf("res = %+v", res)
	}
	projects, err := s.ListProjects(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ProjectKey != "EXA" {
		t.Errorf("projects = %+v", projects)
	}
}

func TestSyncIssues_ThenSearchAndFilterOptions(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "ログイン不具合", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
			fakeIssue(2, "EXA-2", 1, "画面崩れ", "2026-02-01T00:00:00Z", "2026-08-02T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	// 進捗コールバックが呼ばれること(実行 ID がそのまま届くこと)
	var progressCalls int
	s.SetSyncProgressHandler(func(ev SyncProgressEvent) {
		if ev.ProfileID != id || ev.ProjectID != 1 {
			t.Errorf("進捗の宛先 = %s / %d", ev.ProfileID, ev.ProjectID)
		}
		if ev.RunID != "run-1" {
			t.Errorf("実行 ID = %q, want \"run-1\"", ev.RunID)
		}
		progressCalls++
	})

	res, err := s.SyncIssues(ctx, id, 1, "full", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != syncpkg.ModeFull || res.Upserted != 2 {
		t.Errorf("res = %+v", res)
	}
	if progressCalls == 0 {
		t.Error("進捗コールバックが呼ばれていない")
	}

	found, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1, Keyword: "ログイン"})
	if err != nil {
		t.Fatal(err)
	}
	if found.Total != 1 || found.Issues[0].IssueKey != "EXA-1" {
		t.Errorf("検索結果 = %+v", found)
	}

	opts, err := s.ListFilterOptions(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.StatusNames) != 1 || opts.StatusNames[0] != "処理中" {
		t.Errorf("状態候補 = %v", opts.StatusNames)
	}

	state := findSyncState(t, s, id, store.DataKindIssues, 1)
	if state == nil || state.LastSyncedAt == "" {
		t.Errorf("同期状態 = %+v", state)
	}
	if state.ActivityCursor != 900 {
		t.Errorf("ActivityCursor = %d, want 900", state.ActivityCursor)
	}
}

// TestSearchIssues_ReportsTruncated は上限で切り詰めた検索結果が Truncated =
// true で返ること(中 3)を確認する。画面プレビューはこのフラグと Total で
// 「N 件中 M 件を表示」を示す。
// (Excel 出力は上限を溜め込まない走査経路へ移したため、このフラグを使わない。
// TestIterateIssues_StreamsAllMatchingIssues を参照。R4)
func TestSearchIssues_ReportsTruncated(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "課題 1", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
			fakeIssue(2, "EXA-2", 1, "課題 2", "2026-02-01T00:00:00Z", "2026-08-02T00:00:00Z"),
			fakeIssue(3, "EXA-3", 1, "課題 3", "2026-03-01T00:00:00Z", "2026-08-03T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncIssues(ctx, id, 1, "full", ""); err != nil {
		t.Fatal(err)
	}

	// 上限に収まる場合は Truncated = false
	full, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if full.Truncated || full.Total != 3 || len(full.Issues) != 3 {
		t.Errorf("上限内の結果 = {total:%d rows:%d truncated:%v}, want {3 3 false}",
			full.Total, len(full.Issues), full.Truncated)
	}

	// 上限を超える場合は Truncated = true(総件数は切り詰め前の値)
	cut, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Truncated {
		t.Error("Truncated = false, want true(上限で切り詰めた)")
	}
	if cut.Total != 3 || len(cut.Issues) != 2 {
		t.Errorf("切り詰め時の結果 = {total:%d rows:%d}, want {3 2}", cut.Total, len(cut.Issues))
	}
}

// TestIterateIssues_StreamsAllMatchingIssues は Excel 出力の走査経路(R4)が
// 上限に関わらず条件一致全件を 1 件ずつ渡すこと、および visit のエラーで
// 打ち切れることを確認する。
func TestIterateIssues_StreamsAllMatchingIssues(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "課題 1", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
			fakeIssue(2, "EXA-2", 1, "課題 2", "2026-02-01T00:00:00Z", "2026-08-02T00:00:00Z"),
			fakeIssue(3, "EXA-3", 1, "課題 3", "2026-03-01T00:00:00Z", "2026-08-03T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncIssues(ctx, id, 1, "full", ""); err != nil {
		t.Fatal(err)
	}

	// Limit を指定しても走査は打ち切られない(出力は「条件一致全件」が契約)
	var keys []string
	res, err := s.IterateIssues(ctx, id, store.IssueFilter{ProjectID: 1, Limit: 2},
		func(is *store.Issue) error {
			keys = append(keys, is.IssueKey)
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || res.Total != 3 {
		t.Errorf("走査結果 = %v / total %d, want 3 件", keys, res.Total)
	}

	// visit のエラーで打ち切れること(件数上限の打ち切りがこの経路)
	stop := errors.New("打ち切り")
	visited := 0
	if _, err := s.IterateIssues(ctx, id, store.IssueFilter{ProjectID: 1},
		func(*store.Issue) error {
			visited++
			return stop
		}); !errors.Is(err, stop) {
		t.Fatalf("err = %v, want %v", err, stop)
	}
	if visited != 1 {
		t.Errorf("走査した件数 = %d, want 1", visited)
	}

	// 打ち切った後もローカル DB を続けて使えること(読み取り Tx が残らないこと)
	if _, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1}); err != nil {
		t.Fatalf("打ち切り後の検索に失敗: %v", err)
	}
}

// TestSyncIssues_AutoModeFallsBackToFullOnFirstSync は UI 既定の "auto" が
// 未同期プロジェクトでフル同期になること(低 1)を確認する。
// 既定を incremental にすると初回同期が必ず失敗するため、この受理を保証する。
func TestSyncIssues_AutoModeFallsBackToFullOnFirstSync(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "初回同期の課題", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)

	res, err := s.SyncIssues(context.Background(), id, 1, "auto", "")
	if err != nil {
		t.Fatalf("auto モードの初回同期が失敗した: %v", err)
	}
	if res.Mode != syncpkg.ModeFull {
		t.Errorf("mode = %s, want full(未同期はフル同期へフォールバック)", res.Mode)
	}
	if res.Upserted != 1 {
		t.Errorf("res = %+v", res)
	}
}

func TestSyncIssues_RejectsUnknownMode(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)

	if _, err := s.SyncIssues(context.Background(), id, 1, "weekly", ""); err == nil {
		t.Fatal("不明なモードでエラーにならなかった")
	}
}

// TestListSyncStates_UnsyncedIsEmpty は、一度も同期していないプロファイルで
// 同期状態が空になること(未同期の判定材料になること)を確認する。
func TestListSyncStates_UnsyncedIsEmpty(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)

	states, err := s.ListSyncStates(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Errorf("未同期の状態 = %+v, want 空", states)
	}
}

// TestDeleteProfile_WaitsForRunningSync は実行中の同期と DeleteProfile が
// 排他されること(高 2)を検証する。ライフサイクルロック(profileMu)が無いと
// 削除直後に同期が古いプロファイル情報で store キャッシュ・DB を再作成しうる。
func TestDeleteProfile_WaitsForRunningSync(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "同期中の課題", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		activities:    []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
		issuesEntered: make(chan struct{}),
		issuesRelease: make(chan struct{}),
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	// DB を開いた回数を数える(削除後に開き直されないことの確認)
	var opens int32
	openStore := s.openStore
	s.openStore = func(path string) (*store.Store, error) {
		atomic.AddInt32(&opens, 1)
		return openStore(path)
	}
	var removeCalls int32
	s.removeDB = func(host string, userID int) error {
		atomic.AddInt32(&removeCalls, 1)
		return nil
	}

	syncDone := make(chan error, 1)
	go func() {
		_, err := s.SyncIssues(ctx, id, 1, "full", "")
		syncDone <- err
	}()
	<-fake.issuesEntered // 同期が API 取得(DB オープン済み)まで進んだ

	delDone := make(chan error, 1)
	go func() { delDone <- s.DeleteProfile(id, true) }()

	// 同期が終わるまで DeleteProfile は進めない
	select {
	case err := <-delDone:
		t.Fatalf("同期中に DeleteProfile が完了した(排他されていない): %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	close(fake.issuesRelease)
	if err := <-syncDone; err != nil {
		t.Fatalf("同期が失敗した: %v", err)
	}
	select {
	case err := <-delDone:
		if err != nil {
			t.Fatalf("DeleteProfile が失敗した: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("同期完了後も DeleteProfile が終わらない")
	}

	// 削除後に store キャッシュ・DB が再作成されていないこと
	s.storesMu.Lock()
	cached := len(s.stores)
	s.storesMu.Unlock()
	if cached != 0 {
		t.Errorf("削除後も store キャッシュが %d 件残っている", cached)
	}
	if got := atomic.LoadInt32(&opens); got != 1 {
		t.Errorf("DB オープン回数 = %d, want 1(削除後に開き直さない)", got)
	}
	if got := atomic.LoadInt32(&removeCalls); got != 1 {
		t.Errorf("removeDB 呼び出し = %d 回, want 1", got)
	}
	if _, err := s.cfg.Get(id); err == nil {
		t.Error("プロファイルが削除されていない")
	}
}

// TestDeleteProfile_ClosesStoreBeforeRemovingDB は DB 削除の前に
// ローカル DB 接続を閉じることを確認する(開いたままの削除は Windows で失敗する)。
func TestDeleteProfile_ClosesStoreBeforeRemovingDB(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)

	if _, err := s.storeForProfile(id); err != nil {
		t.Fatal(err)
	}
	openAtRemove := true
	s.removeDB = func(host string, userID int) error {
		s.storesMu.Lock()
		defer s.storesMu.Unlock()
		openAtRemove = len(s.stores) > 0
		return nil
	}
	if err := s.DeleteProfile(id, true); err != nil {
		t.Fatal(err)
	}
	if openAtRemove {
		t.Error("DB 接続を開いたまま削除処理が呼ばれた")
	}
}

// TestGetIssueDetail は課題詳細ポップアップ(画面 2)の材料が
// ローカル DB だけで揃うこと(API を呼ばないこと)を確認する。
//
// 親課題キーの引き当ては 1 件だけ引く(プロジェクト全体の対応表は作らない)。
func TestGetIssueDetail(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	st, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssues(ctx, []*store.Issue{
		{ID: 100, IssueKey: "EXA-1", ProjectID: 1, Summary: "親課題", RawJSON: `{"id":100}`},
		{ID: 101, IssueKey: "EXA-2", ProjectID: 1, Summary: "子課題", RawJSON: `{"id":101,"parentIssueId":100}`},
		{ID: 102, IssueKey: "EXA-3", ProjectID: 1, Summary: "親が未同期", RawJSON: `{"id":102,"parentIssueId":9999}`},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("課題と親課題キーの対応を返す", func(t *testing.T) {
		det, err := s.GetIssueDetail(ctx, id, 1, "EXA-2")
		if err != nil {
			t.Fatal(err)
		}
		if det.Issue == nil || det.Issue.Summary != "子課題" {
			t.Fatalf("課題 = %+v", det.Issue)
		}
		if det.ParentKeys[100] != "EXA-1" {
			t.Errorf("親課題キー = %+v, want map[100:EXA-1]", det.ParentKeys)
		}
	})

	t.Run("ローカルに無い親は対応表に載らない", func(t *testing.T) {
		det, err := s.GetIssueDetail(ctx, id, 1, "EXA-3")
		if err != nil {
			t.Fatal(err)
		}
		// 呼び出し側(app)が ID:<数値> へ縮退させる
		if len(det.ParentKeys) != 0 {
			t.Errorf("親課題キー = %+v, want 空", det.ParentKeys)
		}
	})

	t.Run("親を持たない課題も引ける", func(t *testing.T) {
		det, err := s.GetIssueDetail(ctx, id, 1, "EXA-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(det.ParentKeys) != 0 {
			t.Errorf("親課題キー = %+v, want 空", det.ParentKeys)
		}
	})

	t.Run("見つからない課題は明確なエラー", func(t *testing.T) {
		if _, err := s.GetIssueDetail(ctx, id, 1, "EXA-9"); err == nil {
			t.Fatal("エラーにならなかった")
		}
		// 別プロジェクトの課題キーも取り違えない
		if _, err := s.GetIssueDetail(ctx, id, 2, "EXA-1"); err == nil {
			t.Error("別プロジェクトの課題が返った")
		}
	})
}

// TestGetIssueDetail_ProjectRawJSON は課題詳細に「その課題のプロジェクト」の
// 生 JSON が添えられることを確認する(記法設定 textFormattingRule の判定材料)。
//
// 判定を課題と同じ取得結果に結合させるのは、詳細の取得中にプロジェクトを
// 切り替えても判定元がずれないようにするため(画面は選択中プロジェクトを見ない)。
// プロジェクトが未登録の場合は空文字にし、詳細表示自体は成立させる。
func TestGetIssueDetail_ProjectRawJSON(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	st, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A",
		RawJSON: `{"id":1,"textFormattingRule":"markdown"}`}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertIssues(ctx, []*store.Issue{
		{ID: 100, IssueKey: "EXA-1", ProjectID: 1, Summary: "課題", RawJSON: `{"id":100}`},
		{ID: 200, IssueKey: "EXB-1", ProjectID: 2, Summary: "別プロジェクト", RawJSON: `{"id":200}`},
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("プロジェクトの生 JSON を添える", func(t *testing.T) {
		det, err := s.GetIssueDetail(ctx, id, 1, "EXA-1")
		if err != nil {
			t.Fatal(err)
		}
		if det.ProjectRawJSON != `{"id":1,"textFormattingRule":"markdown"}` {
			t.Errorf("プロジェクトの生 JSON = %q", det.ProjectRawJSON)
		}
	})

	t.Run("未登録のプロジェクトは空文字", func(t *testing.T) {
		det, err := s.GetIssueDetail(ctx, id, 2, "EXB-1")
		if err != nil {
			t.Fatal(err)
		}
		if det.ProjectRawJSON != "" {
			t.Errorf("プロジェクトの生 JSON = %q, want 空文字", det.ProjectRawJSON)
		}
	})
}
