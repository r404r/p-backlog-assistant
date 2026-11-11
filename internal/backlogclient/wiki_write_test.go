package backlogclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikiWriteFormsAndUncertain(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		for _, status := range []int{200, 400, 408, 500} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.Method != method {
						t.Errorf("method=%s", r.Method)
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					for k, want := range map[string]string{"name": "Guide/New", "content": "sample body", "mailNotify": "false"} {
						if !r.PostForm.Has(k) || r.PostForm.Get(k) != want {
							t.Errorf("field %s", k)
						}
					}
					if method == http.MethodPost && r.PostForm.Get("projectId") != "1" {
						t.Error("project missing")
					}
					w.WriteHeader(status)
					if status == 200 {
						fmt.Fprint(w, `{"id":10,"projectId":1,"name":"Guide/New","content":"sample body","updated":"T2","createdUser":{"id":42}}`)
					} else {
						fmt.Fprint(w, `{"errors":[{"message":"sample body Guide/New"}]}`)
					}
				}))
				defer srv.Close()
				c := newFakeClient(srv.URL)
				in := WikiWrite{Name: "Guide/New", Content: "sample body"}
				var p *Wiki
				var err error
				if method == http.MethodPost {
					p, err = c.CreateWiki(context.Background(), 1, in)
				} else {
					p, err = c.UpdateWiki(context.Background(), 10, in)
				}
				if calls != 1 || (err == nil) != (status == 200) {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
				if status == 200 && p.CreatedUserID != 42 {
					t.Fatal("creator missing")
				}
				if status == 408 || status == 500 {
					if !IsUncertain(err) {
						t.Fatal(err)
					}
				}
				if err != nil && (strings.Contains(err.Error(), "sample body") || strings.Contains(err.Error(), "Guide/New")) {
					t.Fatal("private fields in error")
				}
			})
		}
	}
}

func TestWikiInvalidWriteResponseIsUncertain(t *testing.T) {
	for _, body := range []string{`null`, `{}`, `{"id":11,"projectId":1,"name":"x","content":"y","updated":"T2"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := newFakeClient(srv.URL).UpdateWiki(context.Background(), 10, WikiWrite{Name: "x", Content: "y"})
		srv.Close()
		if !IsUncertain(err) {
			t.Fatal("invalid success accepted", err)
		}
	}
}

func TestWikiInterruptedResponseIsNotResent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Length", "1000")
		fmt.Fprint(w, `{"id":10`)
	}))
	defer srv.Close()
	_, err := newFakeClient(srv.URL).CreateWiki(context.Background(), 1, WikiWrite{Name: "Guide/New", Content: "sample"})
	if !IsUncertain(err) || calls != 1 {
		t.Fatal(err, calls)
	}
}
