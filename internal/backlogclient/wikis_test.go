package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWikiListValidation(t *testing.T) {
	for _, body := range []string{`null`, `[{"id":0}]`, `[{"id":1,"projectId":2,"name":"x","updated":"T1"}]`, `[{"id":1,"projectId":1,"name":"x","updated":"T1"},{"id":1,"projectId":1,"name":"x","updated":"T1"}]`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			if _, err := newFakeClient(srv.URL).GetWikis(context.Background(), 1); err == nil {
				t.Fatal("invalid list accepted")
			}
		})
	}
}
func TestWikiAPIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apiKey") != "DUMMY-KEY" {
			t.Error("missing key")
		}
		if r.URL.Path == "/api/v2/wikis" {
			if r.URL.Query().Get("projectIdOrKey") != "1" {
				t.Error("missing project")
			}
			fmt.Fprint(w, `[{"id":10,"projectId":1,"name":"例","updated":"T1","tags":[{"id":2,"name":"tag"}]}]`)
		} else {
			fmt.Fprint(w, `{"id":10,"projectId":1,"name":"例","updated":"T1","content":"本文","attachments":[{"id":3,"name":"sample.txt","size":4}]}`)
		}
	}))
	defer srv.Close()
	c := newFakeClient(srv.URL)
	pages, err := c.GetWikis(context.Background(), 1)
	if err != nil || len(pages) != 1 {
		t.Fatal(pages, err)
	}
	p, err := c.GetWiki(context.Background(), 10)
	if err != nil || p.Content != "本文" || len(p.Attachments) != 1 {
		t.Fatal(p, err)
	}
}
func TestWikiDownloadStatusesAndPartialCleanup(t *testing.T) {
	for _, code := range []int{200, 302, 400, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/unexpected")
				w.WriteHeader(code)
				fmt.Fprint(w, "data")
			}))
			defer srv.Close()
			var b strings.Builder
			err := newFakeClient(srv.URL).DownloadWikiAttachment(context.Background(), 1, 2, &b)
			if (err == nil) != (code == 200) {
				t.Fatal(code, err)
			}
			if code != 200 && b.Len() != 0 {
				t.Fatal("wrote error body")
			}
		})
	}
}
func TestWikiDownloadStreamingAndCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 8 {
			fmt.Fprint(w, "chunk")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(15 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	c := newFakeClient(srv.URL)
	c.httpDo.(*interceptor).base.Timeout = 30 * time.Millisecond
	if err := c.DownloadWikiAttachment(context.Background(), 1, 2, io.Discard); err != nil {
		t.Fatal("attachment used JSON total timeout:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := c.DownloadWikiAttachment(ctx, 1, 2, io.Discard); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestWikiPermissionBusinessErrors(t *testing.T) {
	for _, code := range []int{2, 5, 7} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"errors":[{"code":%d,"message":"private"}]}`, code)
			}))
			defer srv.Close()
			_, err := newFakeClient(srv.URL).GetWikis(context.Background(), 1)
			if errors.Is(err, ErrPermissionDenied) != (code == 2 || code == 5) {
				t.Fatal(code, err)
			}
		})
	}
}
func TestWikiDownloadInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, "part")
	}))
	defer srv.Close()
	var dst strings.Builder
	err := newFakeClient(srv.URL).DownloadWikiAttachment(context.Background(), 1, 1, &dst)
	if err == nil || strings.Contains(err.Error(), "DUMMY-KEY") {
		t.Fatal(err)
	}
}

func TestWikiDownloadPermissionErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrUnauthorized},
		{http.StatusForbidden, ErrPermissionDenied},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "private error body")
			}))
			defer srv.Close()
			var dst strings.Builder
			err := newFakeClient(srv.URL).DownloadWikiAttachment(context.Background(), 1, 2, &dst)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
				t.Fatalf("error = %v, want HTTP %d and %v", err, tc.status, tc.want)
			}
			if dst.Len() != 0 {
				t.Fatal("wrote error body")
			}
			if strings.Contains(err.Error(), "DUMMY-KEY") || strings.Contains(err.Error(), srv.URL) || strings.Contains(err.Error(), "private error body") {
				t.Fatal("error contains request or response details")
			}
		})
	}
}
