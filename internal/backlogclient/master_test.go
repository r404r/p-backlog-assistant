package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetProjectCustomFields(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		fmt.Fprint(w, `[
			{"id":1,"typeId":1,"name":"顧客名","description":"取引先の名称","required":true,
			 "applicableIssueTypes":[11,12],"allowAddItem":false,"items":null},
			{"id":2,"typeId":5,"name":"重要度","description":"","required":false,
			 "applicableIssueTypes":null,"allowInput":true,"allowAddItem":true,
			 "items":[{"id":21,"name":"高","displayOrder":0},{"id":22,"name":"低","displayOrder":1}]}
		]`)
	}))
	defer srv.Close()

	defs, err := newFakeClient(srv.URL).GetProjectCustomFields(context.Background(), "EXA")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/projects/EXA/customFields" {
		t.Errorf("path = %q", gotPath)
	}
	if len(defs) != 2 {
		t.Fatalf("件数 = %d, want 2", len(defs))
	}

	first := defs[0]
	if first.ID != 1 || first.TypeID != 1 || first.Name != "顧客名" || first.Description != "取引先の名称" {
		t.Errorf("defs[0] = %+v", first)
	}
	if !first.Required || first.AllowInput || first.AllowAddItem {
		t.Errorf("defs[0] のフラグ = required:%v allowInput:%v allowAddItem:%v",
			first.Required, first.AllowInput, first.AllowAddItem)
	}
	if len(first.ApplicableIssueTypes) != 2 || first.ApplicableIssueTypes[0] != 11 {
		t.Errorf("defs[0].ApplicableIssueTypes = %v", first.ApplicableIssueTypes)
	}
	// items が null でも nil にしない(呼び出し側の nil 判定を不要にする)
	if first.Items == nil || len(first.Items) != 0 {
		t.Errorf("defs[0].Items = %#v, want 空スライス", first.Items)
	}

	second := defs[1]
	if second.TypeID != 5 || !second.AllowInput || !second.AllowAddItem {
		t.Errorf("defs[1] = %+v", second)
	}
	// applicableIssueTypes が null なら空スライス(= 全課題種別に適用)
	if second.ApplicableIssueTypes == nil || len(second.ApplicableIssueTypes) != 0 {
		t.Errorf("defs[1].ApplicableIssueTypes = %#v, want 空スライス", second.ApplicableIssueTypes)
	}
	if len(second.Items) != 2 || second.Items[0].ID != 21 || second.Items[0].Name != "高" {
		t.Errorf("defs[1].Items = %+v", second.Items)
	}
	if second.Items[1].DisplayOrder != 1 {
		t.Errorf("defs[1].Items[1].DisplayOrder = %d, want 1", second.Items[1].DisplayOrder)
	}
}

// TestGetProjectCustomFields_AcceptsEmptyArray はカスタム属性が 0 件のプロジェクトを
// 正常に受理することを確認する。
func TestGetProjectCustomFields_AcceptsEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	defs, err := newFakeClient(srv.URL).GetProjectCustomFields(context.Background(), "1")
	if err != nil {
		t.Fatalf("空配列がエラーになった: %v", err)
	}
	if len(defs) != 0 {
		t.Errorf("件数 = %d, want 0", len(defs))
	}
}

// TestGetProjectCustomFields_RejectsInvalidResponses は既存のマスタ取得と同じ流儀
// (JSON null・id <= 0 を拒否)であることを確認する。
func TestGetProjectCustomFields_RejectsInvalidResponses(t *testing.T) {
	bodies := map[string]string{
		"null":            `null`,
		"id 欠落":           `[{"typeId":1,"name":"顧客名"}]`,
		"id が 0":          `[{"id":0,"typeId":1,"name":"顧客名"}]`,
		"id が null":       `[{"id":null,"typeId":1,"name":"顧客名"}]`,
		"typeId 欠落":       `[{"id":1,"name":"顧客名"}]`,
		"typeId が 0":      `[{"id":1,"typeId":0,"name":"顧客名"}]`,
		"名前欠落":            `[{"id":1,"typeId":1}]`,
		"名前が空":            `[{"id":1,"typeId":1,"name":""}]`,
		"選択肢の id 欠落":      `[{"id":1,"typeId":5,"name":"重要度","items":[{"name":"高"}]}]`,
		"適用課題種別に null 混入": `[{"id":1,"typeId":1,"name":"顧客名","applicableIssueTypes":[11,null]}]`,
		"正常と混在":           `[{"id":1,"typeId":1,"name":"顧客名"},{"typeId":2,"name":"備考"}]`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()

			_, err := newFakeClient(srv.URL).GetProjectCustomFields(context.Background(), "1")
			if err == nil {
				t.Fatal("不正応答がエラーにならなかった")
			}
			if !strings.Contains(err.Error(), "カスタム属性一覧") {
				t.Errorf("エラーメッセージ = %q", err.Error())
			}
		})
	}
}

// TestGetProjectCustomFields_NotFound は未対応プラン等の 404 が ErrNotFound として
// 返ること(呼び出し側が縮退判定できること)を確認する。
func TestGetProjectCustomFields_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"No project."}]}`)
	}))
	defer srv.Close()

	_, err := newFakeClient(srv.URL).GetProjectCustomFields(context.Background(), "1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

// TestGetProjectCustomFields_EscapesProjectKey はプロジェクトキーがパスへ
// そのまま埋め込まれず、URL エスケープされることを確認する
// (GetIssue と同じ扱い。区切り文字を含むキーでパスが壊れないようにする)。
func TestGetProjectCustomFields_EscapesProjectKey(t *testing.T) {
	var gotEscapedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEscapedPath = r.URL.EscapedPath()
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	if _, err := newFakeClient(srv.URL).GetProjectCustomFields(context.Background(), "EX A/B"); err != nil {
		t.Fatal(err)
	}
	if gotEscapedPath != "/api/v2/projects/EX%20A%2FB/customFields" {
		t.Errorf("escaped path = %q", gotEscapedPath)
	}
}
