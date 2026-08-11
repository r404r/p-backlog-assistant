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
	// observed はサーバ報告値(GET /rateLimit またはレスポンスヘッダ)を
	// 一度でも反映したかどうか。UI へ「実測値かどうか」を伝えるために使う。
	observed bool
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
// サーバ報告値ではないため observed は立てない。
func (r *RateLimiter) Configure(limits map[Category]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for cat, limit := range limits {
		r.configureLocked(cat, limit, -1, time.Time{}, false)
	}
}

// configureLocked は 1 区分を設定する(mu 保持前提)。
// remaining < 0 は「残量不明(=満枠扱い)」、reset のゼロ値は「不明(=1 分後)」。
// observed はこの設定値がサーバ報告由来かどうか。
func (r *RateLimiter) configureLocked(cat Category, limit, remaining int, reset time.Time, observed bool) {
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
	r.buckets[cat] = &bucket{
		limit:           float64(limit),
		tokens:          tokens,
		reset:           reset,
		resetFromServer: fromServer,
		observed:        observed,
	}
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
		// GET /rateLimit の応答はサーバ報告値なので observed を立てる
		r.configureLocked(cat, *ls.Limit, remaining, reset, true)
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
	if remaining >= 0 || !reset.IsZero() {
		b.observed = true // サーバ報告値を反映した(推定ではない)
	}
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

// CategoryStatus は 1 区分のレート制限残量(UI 表示用)。
// Observed が false の区分は「サーバから実値を取得できていない」ことを示し、
// Limit / Remaining / ResetUnix は参考値にならない(未初期化なら 0)。
type CategoryStatus struct {
	Category  Category
	Limit     int
	Remaining int
	ResetUnix int64 // 現在ウィンドウのリセット時刻(Unix 秒。未初期化は 0)
	Observed  bool
}

// snapshotCategories はスナップショットで返す区分と順序(UI の表示順)。
var snapshotCategories = []Category{CategoryRead, CategoryUpdate, CategorySearch, CategoryIcon}

// Snapshot は区分ごとの現在残量を返す(常に 4 区分・固定順)。
// 値は「サーバ観測値」と「固定ウィンドウの経過による自然回復」のみを反映し、
// 推定による補正は行わない。未初期化(GET /rateLimit 未取得)の区分は
// Observed = false かつ各値 0 で返す。
func (r *RateLimiter) Snapshot() []CategoryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]CategoryStatus, 0, len(snapshotCategories))
	for _, cat := range snapshotCategories {
		st := CategoryStatus{Category: cat}
		if b, ok := r.buckets[cat]; ok {
			b.rollover(now) // 経過したウィンドウぶんだけ自然回復させる
			st.Limit = int(b.limit)
			st.Remaining = int(b.tokens)
			st.ResetUnix = b.reset.Unix()
			st.Observed = b.observed
		}
		out = append(out, st)
	}
	return out
}
