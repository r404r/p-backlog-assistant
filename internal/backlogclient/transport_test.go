package backlogclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestInterceptor() *interceptor {
	t := newInterceptor(NewRateLimiter())
	t.sleep = func(req *http.Request, d time.Duration) error { return nil } // テストでは実待機しない
	t.jitter = func() float64 { return 0 }
	return t
}

func TestInterceptor_NormalizesUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/users/myself?apiKey=DUMMY", nil)
	_, err := newTestInterceptor().Do(req)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "DUMMY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

func TestInterceptor_NormalizesForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/users?apiKey=DUMMY", nil)
	_, err := newTestInterceptor().Do(req)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if strings.Contains(err.Error(), "DUMMY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

func TestInterceptor_RetriesOn429(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 {
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Unix()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/space", nil)
	resp, err := newTestInterceptor().Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if calls != 3 {
		t.Errorf("リクエスト回数 = %d, want 3(429 ×2 + 成功 ×1)", calls)
	}
}

func TestInterceptor_GivesUpAfterMaxRetries(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ic := newTestInterceptor()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/space?apiKey=DUMMY", nil)
	_, err := ic.Do(req)
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("err = %v, want ErrRateLimitExceeded", err)
	}
	if calls != ic.maxRetries+1 {
		t.Errorf("リクエスト回数 = %d, want %d", calls, ic.maxRetries+1)
	}
	if strings.Contains(err.Error(), "DUMMY") {
		t.Error("エラーメッセージに API キーが含まれている")
	}
}

// noGetBodyReader は GetBody を自動設定させないための io.Reader ラッパー。
type noGetBodyReader struct{ r io.Reader }

func (n *noGetBodyReader) Read(p []byte) (int, error) { return n.r.Read(p) }

func TestInterceptor_DoesNotRetry429WithoutGetBody(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// io.Reader(バッファ非対応型)を渡すと http.NewRequest は GetBody を設定しない
	body := &noGetBodyReader{r: strings.NewReader(`{"summary":"x"}`)}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v2/issues", body)
	if req.GetBody != nil {
		t.Fatal("前提が崩れている: GetBody が設定されている")
	}
	resp, err := newTestInterceptor().Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	// 再送不能なのでリトライせず 429 をそのまま返す
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("リクエスト回数 = %d, want 1(リトライしない)", calls)
	}
}

func TestInterceptor_DoesNotFollowRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/steal", http.StatusFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/api/v2/space", nil)
	resp, err := newTestInterceptor().Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	// リダイレクトを追従せず 302 をそのまま返す(別オリジンへの API キー再送を防ぐ)
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302(リダイレクト非追従)", resp.StatusCode)
	}
}

func TestRetryDelay_WaitsUntilReset(t *testing.T) {
	ic := newTestInterceptor()
	base := time.Unix(2_000_000, 0)
	ic.now = func() time.Time { return base }

	// reset が 10 秒後 → 待機は 10 秒 + バックオフ(0.5s)以上
	d := ic.retryDelay(fmt.Sprint(base.Unix()+10), 0)
	if d < 10*time.Second {
		t.Errorf("retryDelay = %v, want >= 10s", d)
	}
	// attempt が増えるとバックオフが単調増加
	d0 := ic.retryDelay("", 0)
	d2 := ic.retryDelay("", 2)
	if d2 <= d0 {
		t.Errorf("バックオフが増加していない: attempt0=%v attempt2=%v", d0, d2)
	}
}
