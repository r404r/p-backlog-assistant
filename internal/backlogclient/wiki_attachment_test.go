package backlogclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAttachmentTransferLengthAndIdentity(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept-Encoding") != "identity" {
				t.Error("identity required")
			}
			if truncated {
				w.Header().Set("Content-Length", "20")
			}
			_, _ = io.WriteString(w, "data")
		}))
		var dst strings.Builder
		transfer, err := newFakeClient(srv.URL).DownloadWikiAttachmentWithProgress(context.Background(), 1, 2, &dst, func(int64, int64) {})
		srv.Close()
		if truncated {
			var failure *AttachmentError
			if !errors.As(err, &failure) || failure.Code != "truncated" {
				t.Fatal(err)
			}
		} else if err != nil || transfer.ReceivedBytes != 4 || dst.String() != "data" {
			t.Fatal(transfer, err)
		}
	}
}

func TestAttachmentUnknownLengthEmptyAndIdle(t *testing.T) {
	for _, scenario := range []string{"chunked", "empty", "idle"} {
		t.Run(scenario, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "empty" {
					w.Header().Set("Content-Length", "0")
					return
				}
				w.(http.Flusher).Flush()
				if scenario == "idle" {
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, "data")
			}))
			defer srv.Close()
			var dst strings.Builder
			out, err := newFakeClient(srv.URL).downloadWikiAttachmentWithProgress(context.Background(), 1, 2, &dst, nil, 20*time.Millisecond)
			if scenario == "idle" {
				var failure *AttachmentError
				if !errors.As(err, &failure) || failure.Code != "timeout" {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "empty" && (out.ReceivedBytes != 0 || out.ContentLength != 0) {
				t.Fatal(out)
			}
			if scenario == "chunked" && (out.ContentLength != -1 || dst.String() != "data") {
				t.Fatal(out, dst.String())
			}
		})
	}
}

func TestAttachmentNormalEOFMustMatchContentLength(t *testing.T) {
	for _, length := range []int64{3, 4, 5, -1} {
		ctx, cancel := context.WithCancelCause(context.Background())
		var dst strings.Builder
		response := &http.Response{ContentLength: length, Body: io.NopCloser(strings.NewReader("data"))}
		out, err := copyWikiAttachmentBody(ctx, cancel, response, &dst, nil, time.Second)
		cancel(nil)
		if length == 3 || length == 5 {
			var failure *AttachmentError
			if !errors.As(err, &failure) || failure.Code != "truncated" {
				t.Fatalf("length=%d, out=%+v, err=%v", length, out, err)
			}
		} else if err != nil || out.ReceivedBytes != 4 {
			t.Fatalf("length=%d, out=%+v, err=%v", length, out, err)
		}
	}
}
