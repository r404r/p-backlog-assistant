package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGetIssueComments_ParamsAndMapping はパス・クエリの組み立てと
// 正常応答の解析(id / content / createdUser.name / created / updated)を固定する。
func TestGetIssueComments_ParamsAndMapping(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[
			{"id":901,"content":"コメント本文",
			 "createdUser":{"id":42,"name":"担当 太郎"},
			 "created":"2026-08-01T00:00:00Z","updated":"2026-08-02T00:00:00Z",
			 "changeLog":[],"extra":"x"},
			{"id":902,"content":"2 件目",
			 "createdUser":{"id":43,"name":"別 花子"},
			 "created":"2026-08-03T00:00:00Z","updated":"2026-08-03T00:00:00Z"}
		]`)
	}))
	defer srv.Close()

	comments, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{
		MaxID: 900,
		Order: "desc",
		Count: MaxPageSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/issues/EXA-1/comments" {
		t.Errorf("path = %q", gotPath)
	}
	for k, want := range map[string]string{
		"maxId":  "900",
		"order":  "desc",
		"count":  "100",
		"apiKey": "DUMMY-KEY",
	} {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// 指定していない minId は送信しない
	if _, ok := gotQuery["minId"]; ok {
		t.Errorf("minId が送信されている: %v", gotQuery["minId"])
	}

	if len(comments) != 2 {
		t.Fatalf("件数 = %d, want 2", len(comments))
	}
	want := Comment{
		ID:         901,
		Content:    "コメント本文",
		AuthorName: "担当 太郎",
		Created:    "2026-08-01T00:00:00Z",
		Updated:    "2026-08-02T00:00:00Z",
	}
	if comments[0] != want {
		t.Errorf("comments[0] = %+v\nwant %+v", comments[0], want)
	}
	if comments[1].ID != 902 || comments[1].AuthorName != "別 花子" {
		t.Errorf("comments[1] = %+v", comments[1])
	}
}

// TestGetIssueComments_MinIDAndZeroValuesOmitted は minId の送信と、
// 0 / 空文字のパラメータを送らないことを確認する。
func TestGetIssueComments_MinIDAndZeroValuesOmitted(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	if _, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{MinID: 800}); err != nil {
		t.Fatal(err)
	}
	if got := gotQuery.Get("minId"); got != "800" {
		t.Errorf("minId = %q, want 800", got)
	}
	for _, k := range []string{"maxId", "order", "count"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("%s が送信されている: %v", k, gotQuery[k])
		}
	}
}

// TestGetIssueComments_PathIsEscaped は課題キーをパスへエスケープして埋め込むことを確認する。
func TestGetIssueComments_PathIsEscaped(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path はデコード済みでスラッシュを区別できないため、
		// エスケープされたままのパスで確認する
		gotPath = r.URL.EscapedPath()
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	if _, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA 1/2", CommentQuery{}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/issues/EXA%201%2F2/comments" {
		t.Errorf("path = %q", gotPath)
	}
}

// TestGetIssueComments_NullContent は content が null / キー欠落でも
// 要素自体を返し、Content を空文字にすることを確認する
// (呼び出し側が「変更履歴のみの項目」として数えるため要素は落とさない)。
func TestGetIssueComments_NullContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"id":901,"content":null,"createdUser":{"id":42,"name":"担当 太郎"},"created":"2026-08-01T00:00:00Z"},
			{"id":902,"createdUser":{"id":42,"name":"担当 太郎"},"created":"2026-08-02T00:00:00Z"}
		]`)
	}))
	defer srv.Close()

	comments, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("件数 = %d, want 2(変更履歴のみの項目も返す)", len(comments))
	}
	for i, c := range comments {
		if c.Content != "" {
			t.Errorf("comments[%d].Content = %q, want 空文字", i, c.Content)
		}
		if c.AuthorName != "担当 太郎" {
			t.Errorf("comments[%d].AuthorName = %q", i, c.AuthorName)
		}
	}
}

// TestGetIssueComments_MissingCreatedUser は createdUser が無い / null でも
// 落ちず、AuthorName が空文字になることを確認する。
func TestGetIssueComments_MissingCreatedUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"id":901,"content":"本文","createdUser":null},
			{"id":902,"content":"本文 2"}
		]`)
	}))
	defer srv.Close()

	comments, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("件数 = %d, want 2", len(comments))
	}
	for i, c := range comments {
		if c.AuthorName != "" {
			t.Errorf("comments[%d].AuthorName = %q, want 空文字", i, c.AuthorName)
		}
	}
}

// TestGetIssueComments_EmptyArray は空配列(コメント 0 件)が正常であることを確認する。
func TestGetIssueComments_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	comments, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{})
	if err != nil {
		t.Fatalf("空配列がエラーになった: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("件数 = %d, want 0", len(comments))
	}
}

// TestGetIssueComments_RejectsInvalidID は id が無い / 0 以下の要素を
// エラーにすることを確認する。
//
// 読み飛ばして残りを返すと、応答件数と戻り件数がずれる。呼び出し側
// (internal/sync)は「戻り件数 < count ならページ終端」でページングを打ち切り、
// 取得できた範囲でローカルのコメントを全入れ替えするため、1 件でも黙って
// 落とすと古いページを未取得のまま「全件取得できた」と誤認して既存コメントを
// 消してしまう。id はページング(maxId 遡り)にも必須のため、
// 欠けた応答は縮退させずエラーにする(GetProjects と同じ「破壊的な判断の
// 根拠になる応答は厳格に扱う」方針)。
//
// なお、この失敗で課題詳細全体が失われることはない。呼び出し側はコメント取得の
// 失敗を警告として扱い、課題本体の最新化と既存コメントの表示は維持する。
func TestGetIssueComments_RejectsInvalidID(t *testing.T) {
	cases := map[string]string{
		"id 欠落":   `[{"content":"id 欠落"},{"id":903,"content":"正常"}]`,
		"id null": `[{"id":null,"content":"id が null"},{"id":903,"content":"正常"}]`,
		"id が 0":  `[{"id":0,"content":"id が 0"},{"id":903,"content":"正常"}]`,
		"id が負":   `[{"id":-1,"content":"id が負"},{"id":903,"content":"正常"}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetIssueComments(
				context.Background(), "EXA-1", CommentQuery{}); err == nil {
				t.Error("id が不正な要素を含む応答がエラーにならなかった")
			}
		})
	}
}

// TestGetIssueComments_ReturnsAllElements は正常な応答では
// 「応答の要素数 = 戻り件数」になることを確認する。
// 呼び出し側のページ終端判定(戻り件数 < count で終端)が成り立つ前提。
func TestGetIssueComments_ReturnsAllElements(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"id":901,"content":"1 件目"},
			{"id":902,"content":null},
			{"id":903,"content":"3 件目"}
		]`)
	}))
	defer srv.Close()

	comments, err := newFakeClient(srv.URL).GetIssueComments(
		context.Background(), "EXA-1", CommentQuery{})
	if err != nil {
		t.Fatal(err)
	}
	// 本文が null の項目も要素としては返す(呼び出し側が変更履歴として数える)
	if len(comments) != 3 {
		t.Errorf("件数 = %d, want 3(応答の要素数と一致)", len(comments))
	}
}

// TestGetIssueComments_RejectsNonArray は JSON 配列でない応答をエラーにすることを確認する。
func TestGetIssueComments_RejectsNonArray(t *testing.T) {
	cases := map[string]string{
		"オブジェクト": `{"errors":[{"message":"エラー"}]}`,
		"null":   `null`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{}); err == nil {
				t.Fatal("配列でない応答がエラーにならなかった")
			}
		})
	}
}

// TestGetIssueComments_NotFound は 404 が ErrNotFound へ正規化され、
// エラーメッセージに API キーが含まれないことを確認する。
func TestGetIssueComments_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"No issue."}]}`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetIssueComments(context.Background(), "EXA-1", CommentQuery{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}
