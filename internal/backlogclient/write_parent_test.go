package backlogclient

import (
	"context"
	"net/http"
	"testing"
)

// TestCreateIssue_ParentIssueID は新規追加で parentIssueId が送信されることを
// 確認する(CF5。既存課題を親にする追加)。
func TestCreateIssue_ParentIssueID(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated, sampleIssueJSON)

	parent := int64(100)
	if _, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
		ParentIssueID: &parent,
	}); err != nil {
		t.Fatal(err)
	}
	if got.form.Get("parentIssueId") != "100" {
		t.Errorf("form = %v", got.form)
	}
}

// TestCreateIssue_OmitsParentIssueID は未指定なら送信しないことを確認する。
func TestCreateIssue_OmitsParentIssueID(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusCreated, sampleIssueJSON)

	if _, err := newFakeClient(srv.URL).CreateIssue(context.Background(), IssueCreate{
		ProjectID: 1, Summary: "件名", IssueTypeID: 11, PriorityID: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.form["parentIssueId"]; ok {
		t.Errorf("未指定の parentIssueId が送信されている: %v", got.form)
	}
}

// TestUpdateIssue_ParentIssueID は更新で parentIssueId が送信されること、
// および 0(クリア)が空文字で送信されることを確認する。
func TestUpdateIssue_ParentIssueID(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	parent := int64(100)
	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{
		ParentIssueID: &parent,
	}); err != nil {
		t.Fatal(err)
	}
	if got.form.Get("parentIssueId") != "100" {
		t.Errorf("form = %v", got.form)
	}
}

func TestUpdateIssue_ClearsParentIssueID(t *testing.T) {
	srv, got := newCapturingServer(t, http.StatusOK, sampleIssueJSON)

	clear := int64(0)
	if _, err := newFakeClient(srv.URL).UpdateIssue(context.Background(), "EXA-1", IssueUpdate{
		ParentIssueID: &clear,
	}); err != nil {
		t.Fatal(err)
	}
	v, ok := got.form["parentIssueId"]
	if !ok {
		t.Fatalf("クリア指定の parentIssueId が送信されていない: %v", got.form)
	}
	if len(v) != 1 || v[0] != "" {
		t.Errorf("form[parentIssueId] = %v, want 空文字", v)
	}
}

// TestParseIssue_ParentIssueID は応答の parentIssueId が Issue に写ることを
// 確認する(親の 1 階層検証・再送前突合で使う)。
func TestParseIssue_ParentIssueID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int64
	}{
		{"親あり", `{"id":101,"parentIssueId":100}`, 100},
		{"親なし", `{"id":101,"parentIssueId":null}`, 0},
		{"項目なし", `{"id":101}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issue, err := parseIssue([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if issue.ParentIssueID != c.want {
				t.Errorf("ParentIssueID = %d, want %d", issue.ParentIssueID, c.want)
			}
		})
	}
}
