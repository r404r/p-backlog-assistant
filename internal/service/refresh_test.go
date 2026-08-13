package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// 課題詳細ポップアップの「最新の状態を取得」(RefreshIssue)のテスト。
//
// 検証の主眼は「1 件取得の反映がローカル DB へ正しく届くこと」と
// 「1 件取得はプロジェクト同期ではないこと(同期状態を動かさない)」。

func TestRefreshIssue_ReturnsUpdatedDetailAndHitsSearch(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "同期時点の件名", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncIssues(ctx, id, 1, "full", ""); err != nil {
		t.Fatal(err)
	}

	// Backlog 側で件名と状態が変わったことにする
	updated := fakeIssue(1, "EXA-1", 1, "更新後の件名", "2026-01-01T00:00:00Z", "2026-08-13T00:00:00Z")
	updated.StatusName = "完了"
	fake.issues = []backlogclient.Issue{updated}

	detail, err := s.RefreshIssue(ctx, id, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Issue == nil {
		t.Fatal("詳細が返らなかった")
	}
	if detail.Issue.Summary != "更新後の件名" || detail.Issue.StatusName != "完了" {
		t.Errorf("返った詳細 = %+v", detail.Issue)
	}
	// 取得時刻(注記に出す「いつ時点か」)も更新されること
	if detail.Issue.FetchedAt == "" {
		t.Error("FetchedAt が空(注記の時刻を更新できない)")
	}

	// 全文検索の索引(FTS)も更新されていること(トリガー任せの確認)
	hit, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1, Keyword: "更新後"})
	if err != nil {
		t.Fatal(err)
	}
	if hit.Total != 1 || hit.Issues[0].IssueKey != "EXA-1" {
		t.Errorf("更新後の件名で検索した結果 = %+v", hit)
	}
	old, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 1, Keyword: "同期時点"})
	if err != nil {
		t.Fatal(err)
	}
	if old.Total != 0 {
		t.Errorf("更新前の件名で %d 件ヒットした(索引が古いまま)", old.Total)
	}
}

// TestRefreshIssue_DoesNotUpdateSyncState は 1 件取得が同期状態を変えないことを
// 確認する。1 件の最新化はプロジェクト同期の完了とは別概念であり、
// プロジェクト一覧・同期状態画面の鮮度表示を誤って新しく見せてはならない。
func TestRefreshIssue_DoesNotUpdateSyncState(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "課題", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncIssues(ctx, id, 1, "full", ""); err != nil {
		t.Fatal(err)
	}
	before := findSyncState(t, s, id, store.DataKindIssues, 1)

	if _, err := s.RefreshIssue(ctx, id, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	after := findSyncState(t, s, id, store.DataKindIssues, 1)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("同期状態が変わった: before %+v / after %+v", before, after)
	}
}

// TestRefreshIssue_RejectsIssueOfAnotherProject は別プロジェクトの課題を
// 取り違えて保存しないことを確認する。
func TestRefreshIssue_RejectsIssueOfAnotherProject(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(2, "OTH-1", 2, "別プロジェクトの課題", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	if _, err := s.RefreshIssue(ctx, id, 1, "OTH-1"); err == nil {
		t.Fatal("別プロジェクトの課題でエラーにならなかった")
	}
	// ローカル DB には書かれていないこと
	found, err := s.SearchIssues(ctx, id, store.IssueFilter{ProjectID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if found.Total != 0 {
		t.Errorf("別プロジェクトの課題が保存された: %+v", found)
	}
}

// TestRefreshIssue_NotFoundKeepsLocalIssue は 404 でローカルの課題を変更せず、
// 削除の可能性を案内するエラーを返すことを確認する。
func TestRefreshIssue_NotFoundKeepsLocalIssue(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		issues: []backlogclient.Issue{
			fakeIssue(1, "EXA-1", 1, "同期済みの課題", "2026-01-01T00:00:00Z", "2026-08-01T00:00:00Z"),
		},
		activities: []backlogclient.Activity{{ID: 900, Type: 4, ProjectID: 1, ProjectKey: "EXA"}},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncIssues(ctx, id, 1, "full", ""); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetIssueDetail(ctx, id, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}

	// Backlog 側では削除された(GET が 404)
	fake.issues = nil

	_, err = s.RefreshIssue(ctx, id, 1, "EXA-1")
	if err == nil {
		t.Fatal("404 でエラーにならなかった")
	}
	if !errors.Is(err, backlogclient.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound を含むエラー", err)
	}

	after, err := s.GetIssueDetail(ctx, id, 1, "EXA-1")
	if err != nil {
		t.Fatalf("404 の後にローカルの課題が読めなくなった: %v", err)
	}
	if !reflect.DeepEqual(before.Issue, after.Issue) {
		t.Errorf("404 でローカルの課題が変わった: before %+v / after %+v", before.Issue, after.Issue)
	}
}
