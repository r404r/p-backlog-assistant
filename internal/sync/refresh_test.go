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
	if _, err := newTestEngine(t, refreshAPI, refreshStore).RefreshIssue(ctx, 1, "EXA-1"); err != nil {
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

	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
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

	_, err := newTestEngine(t, api, s).RefreshIssue(ctx, 1, "OTH-1")
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

	_, err := e.RefreshIssue(ctx, 1, "EXA-1")
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

			_, err := newTestEngine(t, api, s).RefreshIssue(context.Background(), 1, key)
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

	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
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

	if _, err := e.RefreshIssue(ctx, 0, "EXA-1"); err == nil {
		t.Error("プロジェクト未指定でエラーにならなかった")
	}
	if _, err := e.RefreshIssue(ctx, 1, ""); err == nil {
		t.Error("課題キー未指定でエラーにならなかった")
	}
	if len(api.getIssueCalls) != 0 {
		t.Errorf("前提条件が揃わないのに API を呼んだ: %v", api.getIssueCalls)
	}
}

// --- コメントのオンデマンド取得 ---------------------------------------------

// addComment はフェイクへコメントを追加する(本文が空なら変更履歴のみの項目)。
func addComment(api *fakeAPI, issueKey string, id int64, author, content, created string) {
	api.comments[issueKey] = append(api.comments[issueKey], backlogclient.Comment{
		ID: id, Content: content, AuthorName: author, Created: created, Updated: created,
	})
}

// TestRefreshIssue_StoresComments は課題本体に続けてコメントを取得・保存し、
// 本文の無い項目は保存せず件数だけを記録することを確認する。
func TestRefreshIssue_StoresComments(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "コメントのある課題")}
	addComment(api, "EXA-1", 11, "山田 太郎", "一次調査の結果です", "2026-08-01T10:00:00Z")
	addComment(api, "EXA-1", 12, "佐藤 花子", "対応しました", "2026-08-02T10:00:00Z")
	// 本文が無い項目(状態変更等の変更履歴のみ)
	addComment(api, "EXA-1", 13, "鈴木 一郎", "", "2026-08-03T10:00:00Z")
	s := openTempStore(t)

	res, err := newTestEngine(t, api, s).RefreshIssue(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Comments != 2 || res.HistoryOnly != 1 || res.Truncated {
		t.Errorf("結果 = %+v, want {Comments:2 HistoryOnly:1 Truncated:false}", res)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("警告 = %v, want 空", res.Warnings)
	}

	// 本文のある 2 件だけが新しい順で保存されていること
	got, err := s.ListIssueComments(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("保存件数 = %d, want 2(本文の無い項目は保存しない)", len(got))
	}
	if got[0].ID != 12 || got[1].ID != 11 {
		t.Errorf("並び = %d, %d, want 12, 11(新しい順)", got[0].ID, got[1].ID)
	}
	if got[0].AuthorName != "佐藤 花子" || got[0].Content != "対応しました" {
		t.Errorf("コメント = %+v", got[0])
	}
	if got[0].ProjectID != 1 || got[0].IssueID != 1 {
		t.Errorf("課題・プロジェクトの対応が誤っている: %+v", got[0])
	}

	status, err := s.GetIssueCommentStatus(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if status.FetchedAt == "" || status.HistoryOnly != 1 || status.Truncated {
		t.Errorf("取得結果 = %+v", status)
	}
}

// TestRefreshIssue_ReplacesComments は再取得でコメントが全入れ替えになること
// (Backlog 側で削除・編集されたコメントが残らないこと)を確認する。
func TestRefreshIssue_ReplacesComments(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	addComment(api, "EXA-1", 11, "山田 太郎", "古いコメント", "2026-08-01T10:00:00Z")
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}

	// Backlog 側で 11 が削除され、12 が追加されたことにする
	api.comments["EXA-1"] = nil
	addComment(api, "EXA-1", 12, "佐藤 花子", "新しいコメント", "2026-08-05T10:00:00Z")

	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListIssueComments(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 12 {
		t.Errorf("再取得後のコメント = %+v, want 12 の 1 件のみ", got)
	}
}

// TestRefreshIssue_PagesCommentsUpToLimit はコメントを 100 件ずつページングし、
// 上限(500 件)で打ち切って上限到達を伝えることを確認する。
func TestRefreshIssue_PagesCommentsUpToLimit(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "コメントが多い課題")}
	// 上限を超える 620 件(すべて本文あり)
	for i := 1; i <= 620; i++ {
		addComment(api, "EXA-1", int64(i), "山田 太郎",
			fmt.Sprintf("コメント %d", i), fmt.Sprintf("2026-08-01T00:%02d:%02d Z", i/60, i%60))
	}
	s := openTempStore(t)

	res, err := newTestEngine(t, api, s).RefreshIssue(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Comments != commentFetchLimit {
		t.Errorf("取得件数 = %d, want %d(上限で打ち切る)", res.Comments, commentFetchLimit)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true(上限に達した)")
	}
	// 1 ページ 100 件で 5 ページ + 打ち切り確認の 1 回(上限を超えて取りに行かない)
	pages := commentFetchLimit / commentPageSize
	if len(api.commentQueries) != pages+1 {
		t.Errorf("API 呼び出し = %d 回, want %d(%d ページ + 打ち切り確認 1 回)",
			len(api.commentQueries), pages+1, pages)
	}
	for _, q := range api.commentQueries[:pages] {
		if q.Count != commentPageSize || q.Order != "desc" {
			t.Errorf("ページングパラメータ = %+v", q)
		}
	}
	// 打ち切り確認は 1 件だけ問い合わせる(余分な転送をしない)
	if last := api.commentQueries[pages]; last.Count != 1 {
		t.Errorf("打ち切り確認のパラメータ = %+v, want count=1", last)
	}
	// 2 ページ目以降は maxId を進めて同じページを取り直さないこと
	if api.commentQueries[0].MaxID != 0 || api.commentQueries[1].MaxID == 0 {
		t.Errorf("maxId の進み方が誤っている: %+v", api.commentQueries)
	}
	// 保存されるのは新しい方から 500 件(最古の 120 件は入らない)
	got, err := s.ListIssueComments(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != commentFetchLimit || got[0].ID != 620 {
		t.Errorf("保存件数 = %d / 先頭 = %d, want %d / 620", len(got), got[0].ID, commentFetchLimit)
	}
}

// TestRefreshIssue_CommentFailureKeepsIssueUpdate はコメント取得だけが失敗した
// 場合に、課題本体の反映は維持したまま警告として返すことを確認する(部分失敗)。
//
// コメントは付随情報なので、その取得失敗で課題本体の最新化まで巻き戻すのは
// 利用者にとって損失が大きい。
func TestRefreshIssue_CommentFailureKeepsIssueUpdate(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "同期時点の件名")}
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.SyncIssues(ctx, 1, ModeFull, nil); err != nil {
		t.Fatal(err)
	}

	updated := richIssue(1, "EXA-1", 1, "更新後の件名")
	api.issues = []backlogclient.Issue{updated}
	api.commentsErr = fmt.Errorf("%w: GET /api/v2/issues/EXA-1/comments",
		backlogclient.ErrPermissionDenied)

	res, err := e.RefreshIssue(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatalf("コメント取得の失敗が全体の失敗になった: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("警告 = %v, want 1 件", res.Warnings)
	}
	// 警告文にも課題キー・URL を載せない(動作ログへ記録されるため)
	for _, ng := range []string{"EXA-1", "/api/v2"} {
		if strings.Contains(res.Warnings[0], ng) {
			t.Errorf("警告に %s が含まれている: %s", ng, res.Warnings[0])
		}
	}
	// 課題本体は最新化されていること
	got := storedIssue(t, s, 1, "EXA-1")
	if got == nil || got.Summary != "更新後の件名" {
		t.Errorf("課題 = %+v, want 更新後の件名", got)
	}
	// コメントの取得時刻は更新しない(取得できていないため)
	status, err := s.GetIssueCommentStatus(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if status.FetchedAt != "" {
		t.Errorf("コメント取得時刻 = %q, want 空(取得できていない)", status.FetchedAt)
	}
}

// TestRefreshIssue_NotFoundSkipsComments は課題本体が取得できない場合に
// コメントへ触れないことを確認する(存在しない課題のコメントを消さない)。
func TestRefreshIssue_NotFoundSkipsComments(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	addComment(api, "EXA-1", 11, "山田 太郎", "取得済みのコメント", "2026-08-01T10:00:00Z")
	s := openTempStore(t)
	e := newTestEngine(t, api, s)
	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListIssueComments(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}

	api.deletedKeys["EXA-1"] = true
	if _, err := e.RefreshIssue(ctx, 1, "EXA-1"); !errors.Is(err, backlogclient.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	after, err := s.ListIssueComments(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("404 でコメントが変わった: before %+v / after %+v", before, after)
	}
	// コメント API 自体を呼んでいないこと
	if len(api.commentQueries) != 1 {
		t.Errorf("コメント取得の呼び出し = %d 回, want 1(1 回目の成功時のみ)", len(api.commentQueries))
	}
}

// commentIDBase はテストで使うコメント ID の起点。
// 実際の Backlog のコメント ID はスペース全体の連番で 1 から始まらないため、
// 「ID が 1 まで下がった」という特殊経路に頼らないようにする。
const commentIDBase = 100000

// TestRefreshIssue_TruncatedOnlyWhenOlderCommentsExist は上限ちょうどの課題を
// 「打ち切り」と誤って伝えないことを確認する。
//
// 上限に達した時点でもう 1 件だけ問い合わせ、さらに古いコメントが実在する場合に
// だけ Truncated にする(追加リクエストは上限到達時の 1 回だけ)。
func TestRefreshIssue_TruncatedOnlyWhenOlderCommentsExist(t *testing.T) {
	cases := []struct {
		name  string
		total int
		want  bool
	}{
		{"上限より少ない", commentFetchLimit - 1, false},
		{"上限ちょうど", commentFetchLimit, false},
		{"上限より 1 件多い", commentFetchLimit + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			api := newFakeAPI()
			api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
			// ID は 1 から始めない。最古の ID が 1 だと「これ以上古い ID は
			// 存在しない」と ID だけで判定できてしまい、追加確認の経路を通らない
			for i := 1; i <= c.total; i++ {
				addComment(api, "EXA-1", commentIDBase+int64(i), "山田 太郎",
					fmt.Sprintf("コメント %d", i), "2026-08-01T00:00:00Z")
			}
			s := openTempStore(t)

			res, err := newTestEngine(t, api, s).RefreshIssue(ctx, 1, "EXA-1")
			if err != nil {
				t.Fatal(err)
			}
			if res.Truncated != c.want {
				t.Errorf("Truncated = %v, want %v(コメント %d 件)", res.Truncated, c.want, c.total)
			}
			wantSaved := c.total
			if wantSaved > commentFetchLimit {
				wantSaved = commentFetchLimit
			}
			if res.Comments != wantSaved {
				t.Errorf("保存件数 = %d, want %d", res.Comments, wantSaved)
			}
		})
	}
}

// TestRefreshIssue_TruncationProbeFailureWarnsTruncated は打ち切り確認の
// 問い合わせが失敗した場合に、取得済みのコメントは保存しつつ
// 「打ち切ったかもしれない」側へ倒すことを確認する。
//
// 過小警告(実際は続きがあるのに案内しない)より、過剰警告(Backlog で
// 確認するよう促す)のほうが害が小さいため。
func TestRefreshIssue_TruncationProbeFailureWarnsTruncated(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	for i := 1; i <= commentFetchLimit; i++ {
		addComment(api, "EXA-1", commentIDBase+int64(i), "山田 太郎",
			fmt.Sprintf("コメント %d", i), "2026-08-01T00:00:00Z")
	}
	// 上限到達後の 1 件確認だけを失敗させる
	api.commentsErrAfterCalls = commentFetchLimit / commentPageSize

	res, err := newTestEngine(t, api, openTempStore(t)).RefreshIssue(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Comments != commentFetchLimit {
		t.Errorf("保存件数 = %d, want %d(取得済みは保存する)", res.Comments, commentFetchLimit)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true(確認できない場合は打ち切り扱い)")
	}
}

// TestRefreshIssue_PartialPageIsFinalPage は「戻り件数 < count」でページ終端と
// 判定してよいこと(backlogclient が要素を握り潰さない契約)を前提に、
// 端数ページで追加のリクエストを行わないことを確認する。
func TestRefreshIssue_PartialPageIsFinalPage(t *testing.T) {
	ctx := context.Background()
	api := newFakeAPI()
	api.issues = []backlogclient.Issue{richIssue(1, "EXA-1", 1, "課題")}
	for i := 1; i <= 150; i++ {
		addComment(api, "EXA-1", int64(i), "山田 太郎", fmt.Sprintf("コメント %d", i), "2026-08-01T00:00:00Z")
	}

	res, err := newTestEngine(t, api, openTempStore(t)).RefreshIssue(ctx, 1, "EXA-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Comments != 150 || res.Truncated {
		t.Errorf("結果 = %+v, want 150 件・打ち切りなし", res)
	}
	if len(api.commentQueries) != 2 {
		t.Errorf("API 呼び出し = %d 回, want 2(100 件 + 50 件で終端)", len(api.commentQueries))
	}
}
