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

func TestGetUsersRaw(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[
			{"id":1,"userId":"admin","name":"管理 太郎","roleType":1,
			 "mailAddress":"admin@example.com","lang":"ja"},
			{"id":2,"userId":"user1","name":"一般 花子","roleType":2,
			 "mailAddress":"user1@example.com"}
		]`)
	}))
	defer srv.Close()

	users, err := newFakeClient(srv.URL).GetUsersRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/users" {
		t.Errorf("path = %q", gotPath)
	}
	// GET /users にページングパラメータは無い(送信しない)
	for _, k := range []string{"count", "offset"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("%s が送信されている: %v", k, gotQuery[k])
		}
	}
	if len(users) != 2 {
		t.Fatalf("件数 = %d, want 2", len(users))
	}
	want := User{ID: 1, UserCode: "admin", Name: "管理 太郎", MailAddress: "admin@example.com", RoleType: 1}
	got := users[0]
	got.RawJSON = ""
	if got != want {
		t.Errorf("users[0] = %+v\nwant %+v", got, want)
	}
	// raw_json は API レスポンス要素全体(未知フィールド含む)を保持する
	if !strings.Contains(users[0].RawJSON, `"lang":"ja"`) {
		t.Errorf("RawJSON に未知フィールドが残っていない: %s", users[0].RawJSON)
	}
}

// TestGetUsersRaw_PermissionDenied は 403 が ErrPermissionDenied へ正規化されること
// (縮退パスの判定根拠)を確認する。
func TestGetUsersRaw_PermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"errors":[{"message":"Administrator role required."}]}`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetUsersRaw(context.Background())
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

// TestGetUsersRaw_RejectsInvalidResponse は GetProjects と同様の応答検証
// (null 拒否・id <= 0 拒否)を確認する。
func TestGetUsersRaw_RejectsInvalidResponse(t *testing.T) {
	cases := map[string]string{
		"null":      `null`,
		"id 欠落":     `[{"userId":"admin"}]`,
		"id が null": `[{"id":null,"userId":"admin"}]`,
		"id が 0":    `[{"id":0,"userId":"admin"}]`,
		"id が負":     `[{"id":-1,"userId":"admin"}]`,
		"正常と混在":     `[{"id":1,"userId":"admin"},{"userId":"user1"}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetUsersRaw(context.Background()); err == nil {
				t.Fatal("不正な応答がエラーにならなかった")
			}
		})
	}
}

// TestGetUsersRaw_AcceptsEmptyArray は正常な空配列を受理することを確認する。
func TestGetUsersRaw_AcceptsEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	users, err := newFakeClient(srv.URL).GetUsersRaw(context.Background())
	if err != nil {
		t.Fatalf("空配列がエラーになった: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("件数 = %d, want 0", len(users))
	}
}

func TestGetTeamsPaged(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[
			{"id":10,"name":"開発チーム","displayOrder":0,
			 "members":[{"id":1,"userId":"admin","name":"管理 太郎","roleType":1},
			            {"id":2,"userId":"user1","name":"一般 花子","roleType":2}]},
			{"id":11,"name":"運用チーム","members":[]}
		]`)
	}))
	defer srv.Close()

	teams, err := newFakeClient(srv.URL).GetTeamsPaged(context.Background(), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/teams" {
		t.Errorf("path = %q", gotPath)
	}
	for k, want := range map[string]string{"count": "100", "offset": "100"} {
		if got := gotQuery.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if len(teams) != 2 {
		t.Fatalf("件数 = %d, want 2", len(teams))
	}
	if teams[0].ID != 10 || teams[0].Name != "開発チーム" {
		t.Errorf("teams[0] = %+v", teams[0])
	}
	if len(teams[0].MemberIDs) != 2 || teams[0].MemberIDs[0] != 1 || teams[0].MemberIDs[1] != 2 {
		t.Errorf("MemberIDs = %v", teams[0].MemberIDs)
	}
	if len(teams[1].MemberIDs) != 0 {
		t.Errorf("teams[1].MemberIDs = %v, want 空", teams[1].MemberIDs)
	}
	if !strings.Contains(teams[0].RawJSON, `"displayOrder":0`) {
		t.Errorf("RawJSON = %s", teams[0].RawJSON)
	}
}

// TestGetTeamsPaged_PermissionDenied は 403 の正規化を確認する
// (チーム取得は権限不足・プラン制限のいずれでも 403 になりうる)。
func TestGetTeamsPaged_PermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetTeamsPaged(context.Background(), 0, 100)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

// TestGetTeamsPaged_RejectsInvalidResponse は null・不正 id を拒否することを確認する。
func TestGetTeamsPaged_RejectsInvalidResponse(t *testing.T) {
	for name, body := range map[string]string{
		"null":   `null`,
		"id 欠落":  `[{"name":"開発チーム"}]`,
		"id が 0": `[{"id":0,"name":"開発チーム"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetTeamsPaged(context.Background(), 0, 100); err == nil {
				t.Fatal("不正な応答がエラーにならなかった")
			}
		})
	}
}

// TestGetProjectTeams はプロジェクト単位のチーム取得(縮退パスでチーム情報を
// 合成するための API)を確認する(高 1)。
func TestGetProjectTeams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `[
			{"id":10,"name":"開発チーム","displayOrder":0,
			 "members":[{"id":1,"userId":"admin","name":"管理 太郎","roleType":1},
			            {"id":2,"userId":"user1","name":"一般 花子","roleType":2}]},
			{"id":11,"name":"運用チーム","members":[]}
		]`)
	}))
	defer srv.Close()

	teams, err := newFakeClient(srv.URL).GetProjectTeams(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/projects/7/teams" {
		t.Errorf("path = %q", gotPath)
	}
	// この API に count / offset は無い(送信しない)
	for _, k := range []string{"count", "offset"} {
		if _, ok := gotQuery[k]; ok {
			t.Errorf("%s が送信されている: %v", k, gotQuery[k])
		}
	}
	if len(teams) != 2 {
		t.Fatalf("件数 = %d, want 2", len(teams))
	}
	if teams[0].ID != 10 || teams[0].Name != "開発チーム" {
		t.Errorf("teams[0] = %+v", teams[0])
	}
	if len(teams[0].MemberIDs) != 2 || teams[0].MemberIDs[0] != 1 || teams[0].MemberIDs[1] != 2 {
		t.Errorf("MemberIDs = %v", teams[0].MemberIDs)
	}
	if len(teams[1].MemberIDs) != 0 {
		t.Errorf("teams[1].MemberIDs = %v, want 空", teams[1].MemberIDs)
	}
	if !strings.Contains(teams[0].RawJSON, `"displayOrder":0`) {
		t.Errorf("RawJSON = %s", teams[0].RawJSON)
	}
}

// TestGetProjectTeams_PermissionDenied は 403 の正規化を確認する
// (プロジェクト単位のチーム取得もプラン制限等で 403 になりうる)。
func TestGetProjectTeams_PermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetProjectTeams(context.Background(), 1)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

// TestGetProjectTeams_RejectsInvalidResponse は既存のチーム取得と同じ応答検証
// (null 拒否・id <= 0 拒否)が効くことを確認する(高 1)。
func TestGetProjectTeams_RejectsInvalidResponse(t *testing.T) {
	for name, body := range map[string]string{
		"null":          `null`,
		"id 欠落":         `[{"name":"開発チーム"}]`,
		"id が 0":        `[{"id":0,"name":"開発チーム"}]`,
		"メンバーの id が 欠落": `[{"id":10,"name":"開発チーム","members":[{"userId":"admin"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetProjectTeams(context.Background(), 1); err == nil {
				t.Fatal("不正な応答がエラーにならなかった")
			}
		})
	}
}

func TestGetProjectUsersAndAdministrators(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `[{"id":2,"userId":"user1","name":"一般 花子","roleType":2,
			"mailAddress":"user1@example.com"}]`)
	}))
	defer srv.Close()

	c := newFakeClient(srv.URL)
	users, err := c.GetProjectUsers(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	admins, err := c.GetProjectAdministrators(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/v2/projects/7/users", "/api/v2/projects/7/administrators"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Errorf("paths = %v, want %v", paths, want)
	}
	if len(users) != 1 || users[0].ID != 2 || users[0].UserCode != "user1" {
		t.Errorf("users = %+v", users)
	}
	if len(admins) != 1 || admins[0].Name != "一般 花子" {
		t.Errorf("admins = %+v", admins)
	}
}

// TestGetProjectUsers_RejectsInvalidResponse は null・不正 id を拒否することを確認する
// (合成したユーザ集合で users を全置換するため、異常応答を受理してはならない)。
func TestGetProjectUsers_RejectsInvalidResponse(t *testing.T) {
	for name, body := range map[string]string{
		"null":   `null`,
		"id が 0": `[{"id":0,"userId":"user1"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			if _, err := newFakeClient(srv.URL).GetProjectUsers(context.Background(), 1); err == nil {
				t.Fatal("不正な応答がエラーにならなかった")
			}
		})
	}
}
