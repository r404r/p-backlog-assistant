package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// captured は書き込み系リクエストの記録。
type captured struct {
	method      string
	path        string
	contentType string
	form        url.Values
	query       url.Values
}

// newCapturingServer はリクエストを記録し、body を返すサーバを立てる。
func newCapturingServer(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("リクエストボディを解析できません: %v", err)
		}
		got.method = r.Method
		got.path = r.URL.Path
		got.contentType = r.Header.Get("Content-Type")
		got.form = form
		got.query = r.URL.Query()
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

const sampleIssueJSON = `{"id":101,"issueKey":"EXA-1","projectId":1,"summary":"件名",
	"issueType":{"id":11,"name":"タスク"},"priority":{"id":3,"name":"中"},
	"status":{"id":1,"name":"未対応"},"assignee":{"id":501,"name":"担当 太郎"},
	"dueDate":"2026-09-01T00:00:00Z","updated":"2026-08-10T01:02:03Z"}`

func TestCreateIssue(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated, sampleIssueJSON)

	desc := "詳細本文"
	assignee := int64(501)
	due := "2026-09-01"
	issue, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID:   1,
		Summary:     "件名",
		IssueTypeID: 11,
		PriorityID:  3,
		Description: &desc,
		AssigneeID:  &assignee,
		DueDate:     &due,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/api/v2/issues" {
		t.Errorf("リクエスト = %s %s", got.method, got.path)
	}
	if !strings.HasPrefix(got.contentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	// API キーはクエリで送る(ボディには入れない)
	if got.query.Get("apiKey") == "" {
		t.Error("apiKey がクエリに含まれていない")
	}
	if got.form.Get("apiKey") != "" {
		t.Error("apiKey がボディに含まれている")
	}
	for key, want := range map[string]string{
		"projectId":   "1",
		"summary":     "件名",
		"issueTypeId": "11",
		"priorityId":  "3",
		"description": "詳細本文",
		"assigneeId":  "501",
		"dueDate":     "2026-09-01",
	} {
		if v := got.form.Get(key); v != want {
			t.Errorf("form[%s] = %q, want %q", key, v, want)
		}
	}
	if issue == nil || issue.ID != 101 || issue.IssueKey != "EXA-1" {
		t.Errorf("issue = %+v", issue)
	}
}

// TestCreateIssue_OmitsUnsetFields は nil のフィールドを送信しないことを確認する。
func TestCreateIssue_OmitsUnsetFields(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated, sampleIssueJSON)

	if _, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"description", "assigneeId", "dueDate"} {
		if _, ok := got.form[key]; ok {
			t.Errorf("未指定の %s が送信されている", key)
		}
	}
}

// TestCreateIssue_RejectsMissingRequired は必須項目の欠落を送信前に弾くことを確認する。
func TestCreateIssue_RejectsMissingRequired(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated, sampleIssueJSON)
	c := newFakeClient(srv.URL)
	cases := map[string]IssueCreate{
		"プロジェクト無し": {Summary: "件名", IssueTypeID: 11, PriorityID: 3},
		"件名無し":     {ProjectID: 1, IssueTypeID: 11, PriorityID: 3},
		"種別無し":     {ProjectID: 1, Summary: "件名", PriorityID: 3},
		"優先度無し":    {ProjectID: 1, Summary: "件名", IssueTypeID: 11},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.CreateIssue(context.Background(), in); err == nil {
				t.Fatal("必須項目が欠けているのにエラーにならなかった")
			}
			if got.method != "" {
				t.Error("検証前にリクエストが送信された")
			}
		})
	}
}

func TestUpdateIssue(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	summary := "新しい件名"
	statusID := int64(2)
	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{
		Summary:  &summary,
		StatusID: &statusID,
	}); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPatch || got.path != "/api/v2/issues/EXA-1" {
		t.Errorf("リクエスト = %s %s", got.method, got.path)
	}
	if got.form.Get("summary") != "新しい件名" || got.form.Get("statusId") != "2" {
		t.Errorf("form = %v", got.form)
	}
	// nil のフィールドは送信しない(= 変更しない)
	for _, key := range []string{"description", "issueTypeId", "priorityId", "assigneeId", "dueDate"} {
		if _, ok := got.form[key]; ok {
			t.Errorf("未指定の %s が送信されている", key)
		}
	}
}

// TestUpdateIssue_ClearsFields はクリア指定(担当者・期限・詳細)が
// 空文字パラメータとして送信されることを確認する(設計書 5 節の #CLEAR#)。
func TestUpdateIssue_ClearsFields(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	clearAssignee := int64(0)
	empty := ""
	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{
		AssigneeID:  &clearAssignee,
		DueDate:     &empty,
		Description: &empty,
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"assigneeId", "dueDate", "description"} {
		v, ok := got.form[key]
		if !ok {
			t.Errorf("クリア指定の %s が送信されていない", key)
			continue
		}
		if len(v) != 1 || v[0] != "" {
			t.Errorf("form[%s] = %v, want 空文字", key, v)
		}
	}
}

// TestUpdateIssue_RejectsEmptyUpdate は変更内容が 1 つも無い更新を送信しないことを確認する。
func TestUpdateIssue_RejectsEmptyUpdate(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{}); err == nil {
		t.Fatal("空の更新がエラーにならなかった")
	}
	if got.method != "" {
		t.Error("検証前にリクエストが送信された")
	}
}

// TestUpdateIssue_RejectsEmptyKey は課題キー未指定を弾くことを確認する
// (空キーだと /api/v2/issues/ への PATCH となり別 API を叩きうる)。
func TestUpdateIssue_RejectsEmptyKey(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusOK, sampleIssueJSON)
	summary := "件名"
	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "", IssueUpdate{Summary: &summary}); err == nil {
		t.Fatal("課題キー未指定がエラーにならなかった")
	}
}

// TestWriteError_IncludesAPIMessage は API のエラーメッセージを結果に載せることを確認する
// (一括更新の失敗理由を行ごとにユーザへ提示する必要があるため)。
func TestWriteError_IncludesAPIMessage(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusBadRequest,
		`{"errors":[{"message":"priorityId は必須です","code":7}]}`)

	_, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
	})
	if err == nil {
		t.Fatal("エラー応答が成功として扱われた")
	}
	if !strings.Contains(err.Error(), "priorityId は必須です") {
		t.Errorf("エラーメッセージ = %q", err.Error())
	}
}

// TestWriteError_RejectedOnHTTPStatus は 4xx(408 を除く)を受信した失敗が
// 確定的拒否(RejectedError)になることを確認する(高 4 / 2 回目 高 2)。
// クライアント側の誤りによる拒否はサーバが反映していないことが保証されるため、
// 呼び出し側は失敗として確定してよい。
func TestWriteError_RejectedOnHTTPStatus(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusNotFound,
		http.StatusConflict, http.StatusUnprocessableEntity,
	} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			srv, _ := newCapturingServer(t, status, `{"errors":[{"message":"拒否されました"}]}`)
			summary := "新しい件名"
			_, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1",
				IssueUpdate{Summary: &summary})
			if err == nil {
				t.Fatal("エラーにならなかった")
			}
			if !IsRejected(err) {
				t.Errorf("IsRejected = false, want true(err = %v)", err)
			}
			if IsUncertain(err) {
				t.Errorf("確定的拒否が成否不明に分類された: %v", err)
			}
			var re *RejectedError
			if !errors.As(err, &re) || re.StatusCode != status {
				t.Errorf("RejectedError = %+v, want StatusCode %d", re, status)
			}
			// 404 は従来どおり ErrNotFound として判定できること(後方互換)
			if status == http.StatusNotFound && !errors.Is(err, ErrNotFound) {
				t.Errorf("404 が ErrNotFound として判定できない: %v", err)
			}
		})
	}
}

// TestWriteError_UncertainOnServerFailure は 408(タイムアウト)と 5xx を
// 成否不明(UncertainError)として扱うことを確認する(2 回目 高 2)。
// サーバ側で書き込みを終えた後に障害・タイムアウトが起きた可能性があり、
// 「反映されていない」と断定できないため、失敗として確定してはならない。
func TestWriteError_UncertainOnServerFailure(t *testing.T) {
	for _, status := range []int{
		http.StatusRequestTimeout,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(fmt.Sprintf("HTTP %d", status), func(t *testing.T) {
			srv, _ := newCapturingServer(t, status, `{"errors":[{"message":"サーバエラー"}]}`)
			summary := "新しい件名"
			_, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1",
				IssueUpdate{Summary: &summary})
			if err == nil {
				t.Fatal("エラーにならなかった")
			}
			if !IsUncertain(err) {
				t.Errorf("IsUncertain = false, want true(err = %v)", err)
			}
			if IsRejected(err) {
				t.Errorf("成否不明が確定的拒否に分類された: %v", err)
			}
		})
	}
}

// TestWriteError_UncertainOnMissingIssueID は 2xx でも応答に課題 ID が
// 無い場合を成否不明として扱うことを確認する(2 回目 中 1)。
// ID が取れない応答を成功として扱うと、作成された課題を追跡できないまま
// 「完了」と記録してしまう。
func TestWriteError_UncertainOnMissingIssueID(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"id":0}`, `{"id":null}`} {
		t.Run(body, func(t *testing.T) {
			srv, _ := newCapturingServer(t, http.StatusCreated, body)
			_, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
				ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
			})
			if err == nil {
				t.Fatal("課題 ID の無い応答が成功として扱われた")
			}
			if !IsUncertain(err) {
				t.Errorf("IsUncertain = false, want true(err = %v)", err)
			}

			summary := "新しい件名"
			_, uerr := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1",
				IssueUpdate{Summary: &summary})
			if uerr == nil || !IsUncertain(uerr) {
				t.Errorf("更新の応答: err = %v, want UncertainError", uerr)
			}
		})
	}
}

// TestWriteError_UncertainOnBrokenResponse は 2xx を受け取った後に応答を
// 解釈できなかった場合、成否不明(UncertainError)になることを確認する(高 4)。
func TestWriteError_UncertainOnBrokenResponse(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusCreated, `{壊れた JSON`)

	_, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
	})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if !IsUncertain(err) {
		t.Errorf("IsUncertain = false, want true(err = %v)", err)
	}
	if IsRejected(err) {
		t.Errorf("成否不明が確定的拒否に分類された: %v", err)
	}
}

// TestWriteError_UncertainOnNetworkFailure は応答を受け取れなかった場合に
// 成否不明になることを確認する(サーバへ届いて反映された可能性を否定できない)。
func TestWriteError_UncertainOnNetworkFailure(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusOK, sampleIssueJSON)
	srv.Close() // 接続できない状態にする

	_, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
	})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if !IsUncertain(err) {
		t.Errorf("IsUncertain = false, want true(err = %v)", err)
	}
}

// TestWriteError_ValidationIsNotUncertain は送信前の入力検証エラーが
// 成否不明に分類されないことを確認する(送信していないため確定的失敗)。
func TestWriteError_ValidationIsNotUncertain(t *testing.T) {
	srv, _ := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	_, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if IsUncertain(err) {
		t.Errorf("送信前の検証エラーが成否不明に分類された: %v", err)
	}
}

// TestWriteRequestsUseUpdateCategory は書き込みが update 区分として
// レート制限・429 リトライ経路に載ることを確認する(区分の振り分けのみ)。
func TestWriteRequestsUseUpdateCategory(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v2/issues"},
		{http.MethodPatch, "/api/v2/issues/EXA-1"},
	} {
		req, err := http.NewRequest(tc.method, "https://example.backlog.jp"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cat := classifyRequest(req); cat != CategoryUpdate {
			t.Errorf("%s %s の区分 = %s, want %s", tc.method, tc.path, cat, CategoryUpdate)
		}
	}
}

func TestGetProjectIssueTypes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[{"id":11,"name":"タスク","projectId":1},{"id":12,"name":"バグ","projectId":1}]`)
	}))
	defer srv.Close()

	types, err := newFakeClient(srv.URL).GetProjectIssueTypes(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/projects/1/issueTypes" {
		t.Errorf("path = %q", gotPath)
	}
	if len(types) != 2 || types[0].ID != 11 || types[0].Name != "タスク" {
		t.Errorf("types = %+v", types)
	}
}

func TestGetPriorities(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[{"id":2,"name":"高"},{"id":3,"name":"中"}]`)
	}))
	defer srv.Close()

	priorities, err := newFakeClient(srv.URL).GetPriorities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/priorities" {
		t.Errorf("path = %q", gotPath)
	}
	if len(priorities) != 2 || priorities[1].Name != "中" {
		t.Errorf("priorities = %+v", priorities)
	}
}

func TestGetProjectStatuses(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[{"id":1,"name":"未対応"},{"id":4,"name":"完了"}]`)
	}))
	defer srv.Close()

	statuses, err := newFakeClient(srv.URL).GetProjectStatuses(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/projects/7/statuses" {
		t.Errorf("path = %q", gotPath)
	}
	if len(statuses) != 2 || statuses[0].ID != 1 {
		t.Errorf("statuses = %+v", statuses)
	}
}

// TestMasterData_RejectsInvalidResponses は既存の取得系と同じ流儀
// (JSON null・id <= 0 を拒否)であることを確認する。
func TestMasterData_RejectsInvalidResponses(t *testing.T) {
	bodies := map[string]string{
		"null":      `null`,
		"id 欠落":     `[{"name":"タスク"}]`,
		"id が 0":    `[{"id":0,"name":"タスク"}]`,
		"id が null": `[{"id":null,"name":"タスク"}]`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			c := newFakeClient(srv.URL)
			if _, err := c.GetProjectIssueTypes(context.Background(), 1); err == nil {
				t.Error("種別: 不正応答がエラーにならなかった")
			}
			if _, err := c.GetPriorities(context.Background()); err == nil {
				t.Error("優先度: 不正応答がエラーにならなかった")
			}
			if _, err := c.GetProjectStatuses(context.Background(), 1); err == nil {
				t.Error("状態: 不正応答がエラーにならなかった")
			}
		})
	}
}
