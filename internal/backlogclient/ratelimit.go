package backlogclient

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kenzo0107/backlog"
)

// Category は Backlog API のレート制限区分。
type Category string

const (
	CategoryRead   Category = "read"
	CategoryUpdate Category = "update"
	CategorySearch Category = "search"
	CategoryIcon   Category = "icon"
)

// classifyRequest はリクエストをレート制限区分に振り分ける。
// 区分は公式ドキュメント(developer.nulab.com の Backlog レート制限)の
// 対象 API 一覧に合わせる:
//   - search: 課題一覧 / 課題数 / Wiki ページ一覧 / Wiki ページ数の取得
//   - icon:   スペースロゴ / プロジェクトアイコン / ユーザアイコン / チームアイコンの取得
//   - update: 上記以外の追加・更新・削除(GET 以外)
//   - read:   上記以外の取得(GET)
func classifyRequest(req *http.Request) Category {
	path := req.URL.Path
	if isIconPath(path) {
		return CategoryIcon
	}
	if req.Method == http.MethodGet {
		switch path {
		case "/api/v2/issues", "/api/v2/issues/count",
			"/api/v2/wikis", "/api/v2/wikis/count":
			return CategorySearch
		}
		return CategoryRead
	}
	return CategoryUpdate
}

// isIconPath は icon 区分のエンドポイントかを判定する。
// 対象: /space/image、/projects/:projectIdOrKey/image、
// /users/:userId/icon、/teams/:teamId/icon。
func isIconPath(path string) bool {
	if path == "/api/v2/space/image" {
		return true
	}
	if strings.HasPrefix(path, "/api/v2/projects/") && strings.HasSuffix(path, "/image") {
		return true
	}
	if (strings.HasPrefix(path, "/api/v2/users/") || strings.HasPrefix(path, "/api/v2/teams/")) &&
		strings.HasSuffix(path, "/icon") {
		return true
	}
	return false
}

// bucket は 1 区分ぶんのウィンドウ状態。Backlog のレート制限はユーザ単位の
// 固定ウィンドウ(毎分リセット)なので、時間経過による連続補充は行わず、
// reset 時刻を過ぎたら tokens を limit へ全回復させる。
type bucket struct {
	limit  float64   // 毎分の上限
	tokens float64   // 現在ウィンドウの残り回数
	reset  time.Time // 現在ウィンドウのリセット時刻
	// resetFromServer は reset がサーバ報告値(またはその後のロールオーバーで
	// そこから導出した値)かどうか。false なら「1 分後」等のローカル推定値。
	// サーバ報告値を保持している間は、それより古い reset での上書きを拒否する
	// (単調性の保証。低 1)。
	resetFromServer bool
}

// rollover は reset 時刻を過ぎていたら次のウィンドウへ進め、トークンを全回復する。
func (b *bucket) rollover(now time.Time) {
	if now.Before(b.reset) {
		return
	}
	// 経過したウィンドウ数だけ reset を進める(長時間放置でもループしない)
	elapsed := now.Sub(b.reset)
	n := elapsed/time.Minute + 1
	b.reset = b.reset.Add(n * time.Minute)
	b.tokens = b.limit
}

// RateLimiter は区分ごとの固定ウィンドウカウンタ群。
// 上限値はハードコードせず、GET /api/v2/rateLimit の実値から Configure する。
// Configure 前(未初期化)はパススルーで動作する。
type RateLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[Category]*bucket
}

// NewRateLimiter は未初期化(パススルー)状態の RateLimiter を返す。
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{now: time.Now, buckets: map[Category]*bucket{}}
}

// Configure は区分ごとの毎分上限を設定する(0 以下の区分はパススルーのまま)。
// 残量・リセット時刻が不明な場合の設定用(満枠 + 1 分後リセットで開始)。
func (r *RateLimiter) Configure(limits map[Category]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for cat, limit := range limits {
		r.configureLocked(cat, limit, -1, time.Time{})
	}
}

// configureLocked は 1 区分を設定する(mu 保持前提)。
// remaining < 0 は「残量不明(=満枠扱い)」、reset のゼロ値は「不明(=1 分後)」。
func (r *RateLimiter) configureLocked(cat Category, limit, remaining int, reset time.Time) {
	if limit <= 0 {
		delete(r.buckets, cat)
		return
	}
	now := r.now()
	tokens := float64(limit)
	if remaining >= 0 && remaining < limit {
		tokens = float64(remaining)
	}
	fromServer := true
	if reset.IsZero() || !reset.After(now) {
		reset = now.Add(time.Minute)
		fromServer = false // ローカル推定値(後続のサーバ報告で上書き可)
	}
	r.buckets[cat] = &bucket{limit: float64(limit), tokens: tokens, reset: reset, resetFromServer: fromServer}
}

// ConfigureFromRateLimit はライブラリの RateLimit レスポンスから設定する。
// GET /rateLimit が返す remaining(現在ウィンドウの残量)と reset(リセット時刻)
// を尊重し、満枠で開始しない。
func (r *RateLimiter) ConfigureFromRateLimit(rl *backlog.RateLimit) {
	if rl == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set := func(cat Category, ls *backlog.LimitStatus) {
		if ls == nil || ls.Limit == nil {
			return
		}
		remaining := -1
		if ls.Remaining != nil {
			remaining = *ls.Remaining
		}
		var reset time.Time
		if ls.Reset != nil {
			reset = time.Unix(int64(*ls.Reset), 0)
		}
		r.configureLocked(cat, *ls.Limit, remaining, reset)
	}
	set(CategoryRead, rl.Read)
	set(CategoryUpdate, rl.Update)
	set(CategorySearch, rl.Search)
	set(CategoryIcon, rl.Icon)
}

// reserve はトークンを 1 消費できれば 0 を、できなければリセットまでの待機時間を返す。
func (r *RateLimiter) reserve(cat Category) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[cat]
	if !ok {
		return 0 // 未初期化区分はパススルー
	}
	now := r.now()
	b.rollover(now)
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	// 固定ウィンドウなのでリセット時刻まで待つ(連続補充はしない)
	return b.reset.Sub(now)
}

// Wait は区分 cat のトークンが得られるまで待機する(ctx キャンセルで中断)。
func (r *RateLimiter) Wait(ctx context.Context, cat Category) error {
	for {
		d := r.reserve(cat)
		if d <= 0 {
			return nil
		}
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// ObserveRemaining はレスポンスヘッダの X-RateLimit-Remaining を反映する。
// サーバ側の残量より多くのトークンを持っていた場合は保守的に切り詰める。
func (r *RateLimiter) ObserveRemaining(cat Category, remaining int) {
	r.observe(cat, remaining, time.Time{})
}

// observe はサーバ報告の remaining / reset をバケットへ反映する。
// remaining < 0 は「不明」、reset のゼロ値は「不明」。
func (r *RateLimiter) observe(cat Category, remaining int, reset time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[cat]
	if !ok {
		return
	}
	now := r.now()
	b.rollover(now)
	// サーバ報告のリセット時刻でウィンドウ境界を補正する。
	// 単調性チェック: サーバ報告値を保持している場合、現在の reset より
	// 古い(小さい)reset は拒否する(遅延して届いた旧ウィンドウの応答で
	// ウィンドウ境界を巻き戻さないため。低 1)。ローカル推定値は常に上書き可。
	if !reset.IsZero() && reset.After(now) {
		if !b.resetFromServer || reset.After(b.reset) {
			b.reset = reset
			b.resetFromServer = true
		}
	}
	// 残量はサーバ値へクランプ(応答順序の乱れを考慮し、増やす方向には使わない)
	if remaining >= 0 && float64(remaining) < b.tokens {
		b.tokens = float64(remaining)
	}
}

// ObserveHeaders はレスポンスの X-RateLimit-* ヘッダを監視して反映する。
func (r *RateLimiter) ObserveHeaders(cat Category, h http.Header) {
	remaining := -1
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			remaining = n
		}
	}
	var reset time.Time
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			reset = time.Unix(unix, 0)
		}
	}
	if remaining < 0 && reset.IsZero() {
		return
	}
	r.observe(cat, remaining, reset)
}

// Snapshot は UI 表示用に区分ごとの現在残量(概算)を返す。
func (r *RateLimiter) Snapshot() map[Category]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[Category]int{}
	now := r.now()
	for cat, b := range r.buckets {
		b.rollover(now)
		out[cat] = int(b.tokens)
	}
	return out
}
