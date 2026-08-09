package backlogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// newFakeClient は httptest サーバを指す Client を組み立てる(テスト専用)。
// New() はホストを *.backlog.jp / *.backlog.com に限定するため、
// テストでは構造体を直接組み立てる。
func newFakeClient(srvURL string) *Client {
	limiter := NewRateLimiter()
	ic := newInterceptor(limiter)
	ic.sleep = func(req *http.Request, d time.Duration) error { return nil } // 実待機しない
	ic.jitter = func() float64 { return 0 }
	return &Client{
		spaceURL: srvURL,
		host:     strings.TrimPrefix(srvURL, "http://"),
		apiKey:   "DUMMY-KEY",
		httpDo:   ic,
		limiter:  limiter,
	}
}

func TestGetProjects(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[
			{"id":1,"projectKey":"EXA","name":"検証用","archived":false,"extra":"x"},
			{"id":2,"projectKey":"EXB","name":"保管済","archived":true}
		]`)
	}))
	defer srv.Close()

	projects, err := newFakeClient(srv.URL).GetProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/projects" {
		t.Errorf("path = %q", gotPath)
	}
	if len(projects) != 2 {
		t.Fatalf("件数 = %d, want 2", len(projects))
	}
	if projects[0].ID != 1 || projects[0].ProjectKey != "EXA" || projects[0].Name != "検証用" || projects[0].Archived {
		t.Errorf("projects[0] = %+v", projects[0])
	}
	if !projects[1].Archived {
		t.Error("projects[1].Archived = false, want true")
	}
	// raw_json は API レスポンスの要素全体(未知フィールド含む)を保持する
	if !strings.Contains(projects[0].RawJSON, `"extra":"x"`) {
		t.Errorf("RawJSON に未知フィールドが残っていない: %s", projects[0].RawJSON)
	}
}

// TestGetProjects_AcceptsEmptyArray は正常な空配列(参加プロジェクト 0 件)は
// これまでどおり受理することを確認する(空応答はキャッシュ全破棄の正当な根拠)。
func TestGetProjects_AcceptsEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	projects, err := newFakeClient(srv.URL).GetProjects(context.Background())
	if err != nil {
		t.Fatalf("空配列がエラーになった: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("件数 = %d, want 0", len(projects))
	}
}

// TestGetProjects_RejectsNullResponse は JSON null を空配列として受理しないこと
// (中 3)を確認する。null を 0 件とみなすと、異常応答でローカルキャッシュを
// すべて破棄してしまう。
func TestGetProjects_RejectsNullResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `null`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetProjects(context.Background())
	if err == nil {
		t.Fatal("null 応答がエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "プロジェクト一覧の応答が不正です") {
		t.Errorf("エラーメッセージ = %q", err.Error())
	}
}

// TestGetProjects_RejectsInvalidID は id が欠落・0・負の要素を含む応答を
// エラーにすること(中 3)を確認する。ゼロ値の ID を受理すると、
// 実在しないプロジェクト ID で突合してキャッシュを誤って破棄しうる。
func TestGetProjects_RejectsInvalidID(t *testing.T) {
	cases := map[string]string{
		"id 欠落":     `[{"projectKey":"EXA","name":"検証用"}]`,
		"id が null": `[{"id":null,"projectKey":"EXA"}]`,
		"id が 0":    `[{"id":0,"projectKey":"EXA"}]`,
		"id が負":     `[{"id":-1,"projectKey":"EXA"}]`,
		"正常と混在":     `[{"id":1,"projectKey":"EXA"},{"projectKey":"EXB"}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			_, err := newFakeClient(srv.URL).GetProjects(context.Background())
			if err == nil {
				t.Fatal("id が不正な応答がエラーにならなかった")
			}
			if !strings.Contains(err.Error(), "プロジェクト一覧の応答が不正です") {
				t.Errorf("エラーメッセージ = %q", err.Error())
			}
		})
	}
}

func TestGetIssues_ParamsAndMapping(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[{
			"id":101,"projectId":1,"issueKey":"EXA-1","keyId":1,
			"summary":"件名","description":"詳細",
			"issueType":{"id":9,"name":"タスク"},
			"priority":{"id":3,"name":"中"},
			"status":{"id":2,"name":"処理中"},
			"assignee":{"id":42,"name":"担当 太郎"},
			"dueDate":"2026-09-01","created":"2026-08-01T00:00:00Z","updated":"2026-08-02T00:00:00Z"
		}]`)
	}))
	defer srv.Close()

	issues, err := newFakeClient(srv.URL).GetIssues(context.Background(), IssueQuery{
		ProjectIDs:   []int64{1, 2},
		UpdatedSince: "2026-08-01",
		Sort:         "created",
		Order:        "asc",
		Count:        100,
		Offset:       200,
	})
	if err != nil {
		t.Fatal(err)
	}
	// projectId[] は必ず指定される(指定漏れはスペース全件取得になる)
	if got := gotQuery["projectId[]"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("projectId[] = %v", got)
	}
	for k, want := range map[string]string{
		"updatedSince": "2026-08-01",
		"sort":         "created",
		"order":        "asc",
		"count":        "100",
		"offset":       "200",
		"apiKey":       "DUMMY-KEY",
	} {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	if len(issues) != 1 {
		t.Fatalf("件数 = %d", len(issues))
	}
	i := issues[0]
	want := Issue{
		ID: 101, ProjectID: 1, IssueKey: "EXA-1",
		Summary: "件名", Description: "詳細",
		StatusID: 2, StatusName: "処理中",
		AssigneeID: 42, AssigneeName: "担当 太郎",
		IssueTypeName: "タスク", PriorityName: "中",
		Created: "2026-08-01T00:00:00Z", Updated: "2026-08-02T00:00:00Z", DueDate: "2026-09-01",
	}
	i.RawJSON = ""
	if i != want {
		t.Errorf("issue = %+v\nwant %+v", i, want)
	}
	if !strings.Contains(issues[0].RawJSON, `"keyId":1`) {
		t.Errorf("RawJSON = %s", issues[0].RawJSON)
	}
}

// TestGetIssues_MissingFields は担当者未割当・null フィールドでも落ちないことを確認する。
func TestGetIssues_MissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":1,"projectId":1,"issueKey":"EXA-1","assignee":null,"status":null}]`)
	}))
	defer srv.Close()

	issues, err := newFakeClient(srv.URL).GetIssues(context.Background(), IssueQuery{ProjectIDs: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].AssigneeID != 0 || issues[0].StatusName != "" {
		t.Errorf("issues = %+v", issues)
	}
}

func TestGetIssues_RequiresProjectID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("projectId[] 未指定でリクエストが送信された")
	}))
	defer srv.Close()

	if _, err := newFakeClient(srv.URL).GetIssues(context.Background(), IssueQuery{}); err == nil {
		t.Fatal("projectId[] 未指定でもエラーにならなかった")
	}
	if _, err := newFakeClient(srv.URL).GetIssuesCount(context.Background(), IssueQuery{}); err == nil {
		t.Fatal("count でも projectId[] 未指定がエラーにならなかった")
	}
}

func TestGetIssuesCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/issues/count" {
			t.Errorf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"count":1234}`)
	}))
	defer srv.Close()

	n, err := newFakeClient(srv.URL).GetIssuesCount(context.Background(), IssueQuery{ProjectIDs: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1234 {
		t.Errorf("count = %d, want 1234", n)
	}
}

// TestGetIssues_RetriesOn429 は既存 transport(429 リトライ)を経由することを確認する。
func TestGetIssues_RetriesOn429(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `[{"id":1,"projectId":1,"issueKey":"EXA-1"}]`)
	}))
	defer srv.Close()

	issues, err := newFakeClient(srv.URL).GetIssues(context.Background(), IssueQuery{ProjectIDs: []int64{1}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("リクエスト回数 = %d, want 2(429 → 再送)", calls)
	}
	if len(issues) != 1 {
		t.Errorf("件数 = %d", len(issues))
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"No issue."}]}`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetIssue(context.Background(), "EXA-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

func TestGetSpaceActivities(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/space/activities" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[
			{"id":500,"type":4,"project":{"id":1,"projectKey":"EXA"},
			 "content":{"id":101,"key_id":1,"summary":"削除された課題"},
			 "created":"2026-08-05T00:00:00Z"},
			{"id":501,"type":4,"project":{"id":1,"projectKey":"EXA"},
			 "content":{},"created":"2026-08-06T00:00:00Z"}
		]`)
	}))
	defer srv.Close()

	acts, err := newFakeClient(srv.URL).GetSpaceActivities(context.Background(), ActivityQuery{
		ActivityTypeIDs: []int{4},
		MinID:           499,
		Order:           "asc",
		Count:           100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gotQuery["activityTypeId[]"]; len(got) != 1 || got[0] != "4" {
		t.Errorf("activityTypeId[] = %v", got)
	}
	for k, want := range map[string]string{"minId": "499", "order": "asc", "count": "100"} {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(acts) != 2 {
		t.Fatalf("件数 = %d", len(acts))
	}
	if acts[0].ID != 500 || acts[0].Type != 4 || acts[0].ProjectID != 1 || acts[0].ProjectKey != "EXA" {
		t.Errorf("acts[0] = %+v", acts[0])
	}
	var content map[string]json.RawMessage
	if err := json.Unmarshal(acts[0].Content, &content); err != nil {
		t.Fatalf("content をパースできない: %v", err)
	}
	if string(content["id"]) != "101" {
		t.Errorf("content.id = %s", content["id"])
	}
}

// TestGetSpaceActivities_MinIDOmittedWhenZero は minId=0 を送らない
// (初回はカーソル無しで最新から取得する)ことを確認する。
func TestGetSpaceActivities_MinIDOmittedWhenZero(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	if _, err := newFakeClient(srv.URL).GetSpaceActivities(context.Background(), ActivityQuery{Count: 1, Order: "desc"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotQuery["minId"]; ok {
		t.Errorf("minId が送信されている: %v", gotQuery["minId"])
	}
}

func TestRawGet_ServerErrorIsMasked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"errors":[{"message":"内部エラー"}]}`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetProjects(context.Background())
	if err == nil {
		t.Fatal("500 でエラーにならなかった")
	}
	if strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Errorf("エラーメッセージに API キーが含まれている: %v", err)
	}
}
