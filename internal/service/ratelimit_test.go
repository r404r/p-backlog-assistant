package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"backlog-assistant/internal/backlogclient"
)

// newRateLimitTestService は保存済みプロファイル 1 件を用意したサービスを返す
// (クライアントキャッシュは空の状態。SaveProfile が無効化するため)。
func newRateLimitTestService(t *testing.T, fake *fakeConnector) (*ProfileService, string, *int) {
	t.Helper()
	s, _, newClientCalls := newTestService(t, fake)
	res, err := s.SaveProfile(context.Background(), "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, res.Profile.ID, newClientCalls
}

// warmClientCache は通常の操作(権限確認)でクライアントを生成させ、
// レート制限のスナップショットが読める状態にする。
func warmClientCache(t *testing.T, s *ProfileService, profileID string) {
	t.Helper()
	if _, err := s.GetPermissionStatus(context.Background(), profileID); err != nil {
		t.Fatalf("クライアントの生成に失敗しました: %v", err)
	}
}

func TestGetRateLimitStatus_ReturnsSnapshot(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		rateLimit: []backlogclient.CategoryStatus{
			{Category: backlogclient.CategoryRead, Limit: 600, Remaining: 599, ResetUnix: 1_000_060, Observed: true},
			{Category: backlogclient.CategoryUpdate, Limit: 150, Remaining: 150, ResetUnix: 1_000_060, Observed: true},
			{Category: backlogclient.CategorySearch, Limit: 150, Remaining: 149, ResetUnix: 1_000_060, Observed: true},
			{Category: backlogclient.CategoryIcon, Limit: 0, Remaining: 0, ResetUnix: 0, Observed: false},
		},
	}
	s, id, _ := newRateLimitTestService(t, fake)
	warmClientCache(t, s, id)

	st, err := s.GetRateLimitStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRateLimitStatus が失敗しました: %v", err)
	}
	if len(st.Categories) != 4 {
		t.Fatalf("区分数 = %d, want 4", len(st.Categories))
	}
	wantNames := []string{"read", "update", "search", "icon"}
	for i, c := range st.Categories {
		if c.Name != wantNames[i] {
			t.Errorf("%d 番目の区分名 = %q, want %q", i, c.Name, wantNames[i])
		}
	}
	read := st.Categories[0]
	if read.Limit != 600 || read.Remaining != 599 || read.ResetUnix != 1_000_060 || !read.Observed {
		t.Errorf("read = %+v, want limit 600 / remaining 599 / resetUnix 1000060 / observed true", read)
	}
	icon := st.Categories[3]
	if icon.Observed || icon.Limit != 0 || icon.Remaining != 0 || icon.ResetUnix != 0 {
		t.Errorf("未初期化区分 = %+v, want すべて 0 かつ observed false", icon)
	}
}

// TestGetRateLimitStatus_DoesNotCallAPIForUncachedProfile は、クライアント未生成の
// プロファイルでも API 通信(クライアント生成・InitRateLimit)を起こさず、
// 全区分 observed = false を返すことを確認する(中 1)。
// 「追加通信なし」は 10 秒間隔で自動更新される画面表示の前提。
func TestGetRateLimitStatus_DoesNotCallAPIForUncachedProfile(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, id, newClientCalls := newRateLimitTestService(t, fake)
	before := *newClientCalls

	st, err := s.GetRateLimitStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("GetRateLimitStatus が失敗しました: %v", err)
	}
	if got := *newClientCalls - before; got != 0 {
		t.Errorf("クライアント生成回数 = %d, want 0(追加通信をしない)", got)
	}
	if fake.initCalls != 0 {
		t.Errorf("InitRateLimit の呼び出し回数 = %d, want 0", fake.initCalls)
	}
	if len(st.Categories) != 4 {
		t.Fatalf("区分数 = %d, want 4", len(st.Categories))
	}
	for _, c := range st.Categories {
		if c.Observed || c.Limit != 0 || c.Remaining != 0 || c.ResetUnix != 0 {
			t.Errorf("未生成プロファイルの区分 = %+v, want すべて 0 かつ observed false", c)
		}
	}
}

// TestGetRateLimitStatus_DoesNotRetryInit は、キャッシュ済みクライアントに対しても
// InitRateLimit の再試行(実 API 呼び出し)を誘発しないことを確認する(中 1)。
func TestGetRateLimitStatus_DoesNotRetryInit(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)}
	fake := &fakeConnector{info: testInfo(), initErr: errors.New("rateLimit 取得失敗")}
	s, id, _ := newRateLimitTestService(t, fake)
	s.now = clock.now
	warmClientCache(t, s, id) // 初期化に失敗した状態でキャッシュされる
	if fake.initCalls != 1 {
		t.Fatalf("生成時の InitRateLimit 呼び出し回数 = %d, want 1", fake.initCalls)
	}

	// 再試行間隔を過ぎても、残量表示からは再試行しない
	clock.advance(rateLimitInitRetryInterval + time.Second)
	st, err := s.GetRateLimitStatus(context.Background(), id)
	if err != nil {
		t.Fatalf("初期化失敗時も残量取得は成功すべき: %v", err)
	}
	if st == nil {
		t.Fatal("結果が nil です")
	}
	if fake.initCalls != 1 {
		t.Errorf("InitRateLimit の呼び出し回数 = %d, want 1(残量表示から再試行しない)", fake.initCalls)
	}
}

func TestGetRateLimitStatus_UnknownProfile(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)

	if _, err := s.GetRateLimitStatus(context.Background(), "missing"); err == nil {
		t.Error("未知のプロファイル ID ではエラーを期待しました")
	}
}
