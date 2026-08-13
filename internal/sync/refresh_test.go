package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// richIssue は全項目を埋めた課題を返す(同期経由と RefreshIssue 経由の
// 変換結果を項目単位で突き合わせるため、ゼロ値を残さない)。
func richIssue(id int64, key string, projectID int64, summary string) backlogclient.Issue {
	raw, _ := json.Marshal(map[string]any{
		"id": id, "issueKey": key, "projectId": projectID, "summary": summary,
	})
	return backlogclient.Issue{
		ID: id, IssueKey: key, ProjectID: projectID,
		Summary: summary, Description: "本文",
		StatusID: 2, StatusName: "処理中",
		AssigneeID: 7, AssigneeName: "担当 太郎",
		IssueTypeName: "タスク", PriorityName: "中",
		Created: "2026-01-01T00:00:00Z", Updated: "2026-08-01T00:00:00Z",
		DueDate: "2026-09-30", RawJSON: string(raw),
	}
}

// storedIssue はローカル DB の課題 1 件を返す(無ければ nil)。
func storedIssue(t *testing.T, s *store.Store, projectID int64, key string) *store.Issue {
	t.Helper()
	got, err := s.GetIssueByKey(context.Background(), projectID, key)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestRefreshIssue_WritesSameRowAsFullSync は 1 件取得の反映が、同期経由の
// 反映とまったく同じ行になることを確認する(変換ロジックを共有している保証)。
func TestRefreshIssue_WritesSameRowAsFullSync(t *testing.T) {
	ctx := context.Background()
	issue := richIssue(1, "EXA-1", 1, "同期と同じ行になること")

	// 同期経由(比較の基準)
	syncAPI := newFakeAPI()
	syncAPI.issues = []backlogclient.Issue{issue}
	syncStore := openTempStore(t)
	if _, err := newTestEngine(t, syncAPI, syncStore).SyncIssues(ctx, 1, ModeFull, nil); err != nil {
		t.Fatal(err)
	}

	// 1 件取得経由
	refreshAPI := newFakeAPI()
	refreshAPI.issues = []backlogclient.Issue{issue}
	refreshStore := openTempStore(t)
	if err := newTestEngine(t, refreshAPI, refreshStore).RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}

	want := storedIssue(t, syncStore, 1, "EXA-1")
	got := storedIssue(t, refreshStore, 1, "EXA-1")
	if want == nil || got == nil {
		t.Fatalf("課題行 = 同期経由 %+v / 1 件取得経由 %+v", want, got)
	}
	// fetched_at はテスト用の固定時刻なので両者で一致する(変換の同一性を全項目で見る)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("1 件取得の行 = %+v\n同期経由の行 = %+v", got, want)
	}
}

// TestRefreshIssue_UpdatesExistingRow は既存行が最新内容で上書きされること
// (検索索引の材料である件名・本文も更新されること)を確認する。
func TestRefreshIssue_UpdatesExistingRow(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "同期時点の件名")}
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.SyncIssues(ctx, 1, ModeFull, nil); err != nil {
		t.Fatal(err)
	}

	// Backlog 側が更新されたことにする
	updated := richIssue(1, "EXA-1", 1, "更新後の件名")
	updated.StatusName = "完了"
	updated.Updated = "2026-08-13T00:00:00Z"
	api.issues = []backlogclient.Issue{updated}

	if err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	got := storedIssue(t, s, 1, "EXA-1")
	if got == nil || got.Summary != "更新後の件名" || got.StatusName != "完了" {
		t.Fatalf("更新後の行 = %+v", got)
	}
}

// TestRefreshIssue_RejectsIssueOfAnotherProject は、応答の課題が別プロジェクトの
// ものだった場合に DB を変更せずエラーにすることを確認する(取り違え防止)。
func TestRefreshIssue_RejectsIssueOfAnotherProject(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(2, "OTH-1", 2, "別プロジェクトの課題")}
	s := openTempStore(t)

	err := newTestEngine(t, api, s).RefreshIssue(ctx, 1, "OTH-1")
	if err == nil {
		t.Fatal("別プロジェクトの課題でエラーにならなかった")
	}
	if got := storedIssue(t, s, 2, "OTH-1"); got != nil {
		t.Errorf("別プロジェクトの課題が保存された: %+v", got)
	}
}

// TestRefreshIssue_NotFoundKeepsLocalRow は 404 でローカル行を変更しないこと、
// および削除の可能性を案内するエラーを返すことを確認する。
//
// 削除の反映(論理削除)は同期に委ねる。1 件の 404 で消すと、一時的な
// 権限変更・URL 取り違えでローカルの課題を失いかねないため。
func TestRefreshIssue_NotFoundKeepsLocalRow(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "同期済みの課題")}
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.SyncIssues(ctx, 1, ModeFull, nil); err != nil {
		t.Fatal(err)
	}
	before := storedIssue(t, s, 1, "EXA-1")

	// Backlog 側では削除された(GET が 404)
	api.deletedKeys["EXA-1"] = true

	err := e.RefreshIssue(ctx, 1, "EXA-1")
	if err == nil {
		t.Fatal("404 でエラーにならなかった")
	}
	if !errors.Is(err, backlogclient.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound を含むエラー", err)
	}
	// 課題キーはメッセージに載せない(動作ログのマスク方針)
	if msg := err.Error(); strings.Contains(msg, "EXA-1") {
		t.Errorf("エラーメッセージに課題キーが含まれている: %s", msg)
	}
	if after := storedIssue(t, s, 1, "EXA-1"); !reflect.DeepEqual(before, after) {
		t.Errorf("404 でローカル行が変わった: before %+v / after %+v", before, after)
	}
}

// TestRefreshIssue_MasksIssueReferenceInAPIErrors は 404 以外の API エラー
// (401・403・429・5xx・通信エラー)でも、返すエラーに課題キー・URL が
// 載らないことを固定する。
//
// このエラーはバインディング(appOp)の失敗ログへそのまま記録されるため、
// 元エラーの URL(/api/v2/issues/<課題キー>)が残ると課題キーが動作ログへ
// 混入してしまう。分類(sentinel)は errors.Is で判定できる形を保つ。
func TestRefreshIssue_MasksIssueReferenceInAPIErrors(t *testing.T) {
	const key = "EXA-1"
	const path = "/api/v2/issues/" + key
	cases := []struct {
		name     string
		injected error
		// sentinel が非 nil なら errors.Is で判定できること
		sentinel error
		// keep は分類・原因を追うために残ってほしい文字列
		keep string
	}{
		{
			name:     "認証エラー",
			injected: fmt.Errorf("%w: GET %s", backlogclient.ErrUnauthorized, path),
			sentinel: backlogclient.ErrUnauthorized,
			keep:     "認証に失敗しました",
		},
		{
			name:     "権限エラー",
			injected: fmt.Errorf("%w: GET %s", backlogclient.ErrPermissionDenied, path),
			sentinel: backlogclient.ErrPermissionDenied,
			keep:     "権限がありません",
		},
		{
			name:     "レート制限",
			injected: fmt.Errorf("%w: GET %s", backlogclient.ErrRateLimitExceeded, path),
			sentinel: backlogclient.ErrRateLimitExceeded,
			keep:     "レート制限",
		},
		{
			name:     "サーバエラー",
			injected: fmt.Errorf("Backlog API がエラーを返しました(HTTP 503): GET %s", path),
			keep:     "HTTP 503",
		},
		{
			name: "通信エラー",
			injected: fmt.Errorf(
				`Get "https://example.backlog.jp%s?apiKey=***": dial tcp: connection refused`, path),
			keep: "connection refused",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := newFakeAPI()
			api.getIssueErr = c.injected
			s := openTempStore(t)

			err := newTestEngine(t, api, s).RefreshIssue(context.Background(), 1, key)
			if err == nil {
				t.Fatal("エラーにならなかった")
			}
			if c.sentinel != nil && !errors.Is(err, c.sentinel) {
				t.Errorf("errors.Is による分類ができない: %v", err)
			}
			msg := err.Error()
			if strings.Contains(msg, key) {
				t.Errorf("エラーメッセージに課題キーが含まれている: %s", msg)
			}
			for _, ng := range []string{"http://", "https://", "/api/v2"} {
				if strings.Contains(msg, ng) {
					t.Errorf("エラーメッセージに URL(%s)が含まれている: %s", ng, msg)
				}
			}
			if !strings.Contains(msg, c.keep) {
				t.Errorf("原因を追う手掛かり(%s)が失われている: %s", c.keep, msg)
			}
		})
	}
}

// TestRefreshIssue_DoesNotTouchSyncState は 1 件取得が同期状態を更新しないことを
// 確認する。1 件の最新化は「プロジェクト同期の完了」ではないため、鮮度表示
// (最終同期時刻)や差分同期の起点を動かしてはならない。
func TestRefreshIssue_DoesNotTouchSyncState(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	api.addActivity(900, 1, "EXA", map[string]any{"id": 1})
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.SyncIssues(ctx, 1, ModeFull, nil); err != nil {
		t.Fatal(err)
	}
	before := syncState(t, s, 1)

	if err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	after := syncState(t, s, 1)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("同期状態が変わった: before %+v / after %+v", before, after)
	}
}

// TestRefreshIssue_RejectsEmptyArguments は前提条件の検証を確認する。
func TestRefreshIssue_RejectsEmptyArguments(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	e := newTestEngine(t, api, openTempStore(t))

	if err := e.RefreshIssue(ctx, 0, "EXA-1"); err == nil {
		t.Error("プロジェクト未指定でエラーにならなかった")
	}
	if err := e.RefreshIssue(ctx, 1, ""); err == nil {
		t.Error("課題キー未指定でエラーにならなかった")
	}
	if len(api.getIssueCalls) != 0 {
		t.Errorf("前提条件が揃わないのに API を呼んだ: %v", api.getIssueCalls)
	}
}
