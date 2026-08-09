package backlogclient

import (
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// 正規化済みエラー。errors.Is で判定する。
var (
	// ErrUnauthorized は 401(API キー無効等)の正規化エラー。
	ErrUnauthorized = errors.New("認証に失敗しました(API キーが無効です)")
	// ErrPermissionDenied は 403(権限不足)の正規化エラー。UI の機能縮退判定に使う。
	ErrPermissionDenied = errors.New("権限がありません")
	// ErrRateLimitExceeded は 429 がリトライ上限まで解消しなかった場合のエラー。
	ErrRateLimitExceeded = errors.New("レート制限を超過しました")
)

// interceptor は kenzo0107/backlog に OptionHTTPClient で差し込む HTTP クライアント。
// 全リクエストに対して以下を行う:
//   - 区分別トークンバケットでの送信自制
//   - X-RateLimit-* ヘッダの監視
//   - 429 の自動リトライ(X-RateLimit-Reset まで待機 + 指数バックオフ + ジッタ)
//   - 401/403 の正規化(ErrUnauthorized / ErrPermissionDenied)
//   - エラーメッセージ中の apiKey マスク
type interceptor struct {
	base       *http.Client
	limiter    *RateLimiter
	maxRetries int
	now        func() time.Time
	sleep      func(req *http.Request, d time.Duration) error
	jitter     func() float64 // [0,1)
}

func newInterceptor(limiter *RateLimiter) *interceptor {
	return &interceptor{
		base: &http.Client{
			Timeout: 60 * time.Second,
			// リダイレクト追従を無効化する: API キーがクエリで送られるため、
			// リダイレクト先(別オリジンの可能性)へキーが再送されるのを防ぐ。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		limiter:    limiter,
		maxRetries: 3,
		now:        time.Now,
		sleep: func(req *http.Request, d time.Duration) error {
			timer := time.NewTimer(d)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return req.Context().Err()
			case <-timer.C:
				return nil
			}
		},
		jitter: rand.Float64,
	}
}

// maskedURL はエラーメッセージ用に apiKey をマスクした URL 文字列を返す。
func maskedURL(req *http.Request) string {
	return MaskAPIKey(req.URL.String())
}

// Do は httpClient インターフェース(kenzo0107/backlog)の実装。
func (t *interceptor) Do(req *http.Request) (*http.Response, error) {
	cat := classifyRequest(req)
	for attempt := 0; ; attempt++ {
		if err := t.limiter.Wait(req.Context(), cat); err != nil {
			return nil, err
		}
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
		resp, err := t.base.Do(req)
		if err != nil {
			if ctxErr := req.Context().Err(); ctxErr != nil {
				return nil, ctxErr
			}
			// url.Error は URL(apiKey 含む)を文言に含むため必ずマスクする。
			return nil, fmt.Errorf("HTTP リクエストに失敗しました: %s", MaskAPIKey(err.Error()))
		}
		t.limiter.ObserveHeaders(cat, resp.Header)

		switch resp.StatusCode {
		case http.StatusUnauthorized:
			resp.Body.Close()
			return nil, fmt.Errorf("%w: %s %s", ErrUnauthorized, req.Method, maskedURL(req))
		case http.StatusForbidden:
			resp.Body.Close()
			return nil, fmt.Errorf("%w: %s %s", ErrPermissionDenied, req.Method, maskedURL(req))
		case http.StatusTooManyRequests:
			// Body 付きで GetBody が無いリクエストは再送できない
			// (初回送信で Body を消費済みのため)。リトライせず 429 をそのまま返す。
			if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
				return resp, nil
			}
			reset := resp.Header.Get("X-RateLimit-Reset")
			resp.Body.Close()
			if attempt >= t.maxRetries {
				return nil, fmt.Errorf("%w(リトライ %d 回でも解消せず): %s %s",
					ErrRateLimitExceeded, t.maxRetries, req.Method, maskedURL(req))
			}
			if err := t.sleep(req, t.retryDelay(reset, attempt)); err != nil {
				return nil, err
			}
			continue
		}
		return resp, nil
	}
}

// retryDelay は 429 時の待機時間を計算する:
// X-RateLimit-Reset(unix 秒)までの残り時間 + 指数バックオフ + ジッタ。
func (t *interceptor) retryDelay(resetHeader string, attempt int) time.Duration {
	var untilReset time.Duration
	if resetHeader != "" {
		if unix, err := strconv.ParseInt(resetHeader, 10, 64); err == nil {
			untilReset = time.Unix(unix, 0).Sub(t.now())
		}
	}
	if untilReset < 0 {
		untilReset = 0
	}
	if untilReset > 2*time.Minute { // ヘッダ異常値の防御
		untilReset = 2 * time.Minute
	}
	backoff := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond // 0.5s, 1s, 2s, ...
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	jitter := time.Duration(t.jitter() * float64(time.Second)) // 0〜1s
	return untilReset + backoff + jitter
}
