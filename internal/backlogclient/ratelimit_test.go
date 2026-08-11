package backlogclient

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/kenzo0107/backlog"
)

// fakeClock はテスト用の手動クロック。
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

func newTestLimiter(clock *fakeClock, limits map[Category]int) *RateLimiter {
	r := NewRateLimiter()
	r.now = clock.now
	r.Configure(limits)
	return r
}

func TestFixedWindow_ConsumeAndBlock(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryRead: 5})

	// 上限までは待機なしで取得できる
	for i := 0; i < 5; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatalf("take %d: 待機時間 %v, want 0", i+1, d)
		}
	}
	// 6 回目はウィンドウのリセット(1 分後)まで待機が必要
	d := r.reserve(CategoryRead)
	if d <= 0 {
		t.Fatal("枯渇後に待機時間 0 で取得できてしまった")
	}
	want := time.Minute
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(リセットまで)", d, want)
	}
}

func TestFixedWindow_NoContinuousRefill(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategorySearch: 60})

	for i := 0; i < 60; i++ {
		if d := r.reserve(CategorySearch); d != 0 {
			t.Fatalf("初期トークンの消費に失敗(%d 回目)", i+1)
		}
	}
	// 固定ウィンドウなので途中経過(10 秒)では 1 個も補充されない
	clock.advance(10 * time.Second)
	if d := r.reserve(CategorySearch); d == 0 {
		t.Fatal("リセット前に補充されてしまった(連続補充は廃止されたはず)")
	}
	// リセット時刻を過ぎると全回復する
	clock.advance(50 * time.Second)
	for i := 0; i < 60; i++ {
		if d := r.reserve(CategorySearch); d != 0 {
			t.Fatalf("リセット後 %d 回目の取得に失敗", i+1)
		}
	}
	if d := r.reserve(CategorySearch); d == 0 {
		t.Fatal("リセット後の上限を超えて取得できてしまった")
	}
}

func TestFixedWindow_CapAtLimit(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryUpdate: 10})

	// 長時間放置しても容量(=毎分上限)を超えて貯まらない
	clock.advance(1 * time.Hour)
	for i := 0; i < 10; i++ {
		if d := r.reserve(CategoryUpdate); d != 0 {
			t.Fatalf("%d 回目の取得に失敗", i+1)
		}
	}
	if d := r.reserve(CategoryUpdate); d == 0 {
		t.Fatal("容量を超えてトークンが貯まっている")
	}
}

func TestFixedWindow_ObserveRemainingClamps(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryRead: 100})

	// サーバ側の残量が 2 と報告されたら、ローカルのトークンも 2 に切り詰める
	r.ObserveRemaining(CategoryRead, 2)
	if d := r.reserve(CategoryRead); d != 0 {
		t.Fatal("1 個目の取得に失敗")
	}
	if d := r.reserve(CategoryRead); d != 0 {
		t.Fatal("2 個目の取得に失敗")
	}
	if d := r.reserve(CategoryRead); d == 0 {
		t.Fatal("残量報告(2)を超えて取得できてしまった")
	}
}

func TestFixedWindow_ObserveHeadersUpdatesReset(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryRead: 10})

	// サーバ報告: 残量 0、リセットは 30 秒後
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", "1000030")
	r.ObserveHeaders(CategoryRead, h)

	d := r.reserve(CategoryRead)
	if d <= 0 {
		t.Fatal("残量 0 の報告後に取得できてしまった")
	}
	want := 30 * time.Second
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(サーバ報告のリセットまで)", d, want)
	}
	// リセット時刻を過ぎると全回復する
	clock.advance(31 * time.Second)
	if d := r.reserve(CategoryRead); d != 0 {
		t.Errorf("リセット後の取得に失敗: %v", d)
	}
}

func TestConfigureFromRateLimit_HonorsRemainingAndReset(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := NewRateLimiter()
	r.now = clock.now

	iptr := func(n int) *int { return &n }
	reset := int(clock.t.Add(20 * time.Second).Unix())
	r.ConfigureFromRateLimit(&backlog.RateLimit{
		Read: &backlog.LimitStatus{Limit: iptr(100), Remaining: iptr(2), Reset: &reset},
	})

	// remaining=2 を尊重し、満枠(100)で開始しない
	for i := 0; i < 2; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatalf("%d 個目の取得に失敗", i+1)
		}
	}
	d := r.reserve(CategoryRead)
	if d <= 0 {
		t.Fatal("remaining(2)を超えて取得できてしまった")
	}
	want := 20 * time.Second
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(reset まで)", d, want)
	}
	// reset を過ぎると limit(100)まで全回復する
	clock.advance(21 * time.Second)
	for i := 0; i < 100; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatalf("リセット後 %d 個目の取得に失敗", i+1)
		}
	}
	if d := r.reserve(CategoryRead); d == 0 {
		t.Fatal("limit を超えて取得できてしまった")
	}
}

func TestObserve_RejectsOlderResetFromStaleResponse(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := NewRateLimiter()
	r.now = clock.now

	// サーバ報告の reset(60 秒後)で初期化する
	iptr := func(n int) *int { return &n }
	reset60 := int(clock.t.Add(60 * time.Second).Unix())
	r.ConfigureFromRateLimit(&backlog.RateLimit{
		Read: &backlog.LimitStatus{Limit: iptr(2), Remaining: iptr(2), Reset: &reset60},
	})

	// 遅延して届いた旧ウィンドウの応答(reset = 30 秒後 = 保持値より古い)は拒否する
	h := http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(clock.t.Add(30*time.Second).Unix(), 10))
	r.ObserveHeaders(CategoryRead, h)

	for i := 0; i < 2; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatalf("%d 個目の取得に失敗", i+1)
		}
	}
	d := r.reserve(CategoryRead)
	if d <= 0 {
		t.Fatal("枯渇後に取得できてしまった")
	}
	// ウィンドウ境界が 30 秒へ巻き戻されていないこと(60 秒のまま)
	want := 60 * time.Second
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(古い reset で巻き戻さない)", d, want)
	}
}

func TestObserve_AcceptsNewerReset(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := NewRateLimiter()
	r.now = clock.now

	iptr := func(n int) *int { return &n }
	reset30 := int(clock.t.Add(30 * time.Second).Unix())
	r.ConfigureFromRateLimit(&backlog.RateLimit{
		Read: &backlog.LimitStatus{Limit: iptr(1), Remaining: iptr(1), Reset: &reset30},
	})

	// より新しい reset(50 秒後)は単調に前進する方向なので受け入れる
	h := http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(clock.t.Add(50*time.Second).Unix(), 10))
	r.ObserveHeaders(CategoryRead, h)

	if d := r.reserve(CategoryRead); d != 0 {
		t.Fatal("1 個目の取得に失敗")
	}
	d := r.reserve(CategoryRead)
	want := 50 * time.Second
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(新しい reset を採用)", d, want)
	}
}

func TestObserve_GuessedResetIsOverwritable(t *testing.T) {
	// Configure(reset 不明 = 1 分後と推定)の場合、推定値より小さい
	// サーバ報告の reset でも上書きできる(推定値に単調性は適用しない)
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryRead: 1})

	h := http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(clock.t.Add(20*time.Second).Unix(), 10))
	r.ObserveHeaders(CategoryRead, h)

	if d := r.reserve(CategoryRead); d != 0 {
		t.Fatal("1 個目の取得に失敗")
	}
	d := r.reserve(CategoryRead)
	want := 20 * time.Second
	if d < want-time.Second || d > want+time.Second {
		t.Errorf("待機時間 = %v, want 約 %v(推定 reset はサーバ報告で上書き)", d, want)
	}
}

func TestRateLimiter_UnconfiguredPassthrough(t *testing.T) {
	r := NewRateLimiter()
	for i := 0; i < 1000; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatal("未初期化の RateLimiter はパススルーであるべき")
		}
	}
}

// --- レート制限残量のスナップショット ------------------------------------------

// indexSnapshot はスナップショットを区分で引けるようにする。
func indexSnapshot(t *testing.T, snap []CategoryStatus) map[Category]CategoryStatus {
	t.Helper()
	// 区分は常に read / update / search / icon の 4 つが返る(未初期化も含む)
	want := []Category{CategoryRead, CategoryUpdate, CategorySearch, CategoryIcon}
	if len(snap) != len(want) {
		t.Fatalf("スナップショットの件数 = %d, want %d", len(snap), len(want))
	}
	out := map[Category]CategoryStatus{}
	for i, s := range snap {
		if s.Category != want[i] {
			t.Errorf("%d 番目の区分 = %s, want %s(順序が固定であること)", i, s.Category, want[i])
		}
		out[s.Category] = s
	}
	return out
}

func TestSnapshot_ReflectsConfigureAndConsumption(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategoryRead: 5})

	got := indexSnapshot(t, r.Snapshot())
	read := got[CategoryRead]
	if read.Limit != 5 || read.Remaining != 5 {
		t.Errorf("初期状態 = limit %d / remaining %d, want 5 / 5", read.Limit, read.Remaining)
	}
	if want := clock.t.Add(time.Minute).Unix(); read.ResetUnix != want {
		t.Errorf("resetUnix = %d, want %d", read.ResetUnix, want)
	}
	if read.Observed {
		t.Error("サーバ値を観測していないのに observed = true")
	}

	// 2 回消費したぶんだけ残量が減る(推定はしない)
	for i := 0; i < 2; i++ {
		if d := r.reserve(CategoryRead); d != 0 {
			t.Fatalf("%d 個目の取得に失敗", i+1)
		}
	}
	if got := indexSnapshot(t, r.Snapshot())[CategoryRead]; got.Remaining != 3 {
		t.Errorf("消費後の remaining = %d, want 3", got.Remaining)
	}
}

func TestSnapshot_UnconfiguredCategoryIsNotObserved(t *testing.T) {
	r := NewRateLimiter()
	for _, s := range indexSnapshot(t, r.Snapshot()) {
		if s.Observed {
			t.Errorf("%s: 未初期化なのに observed = true", s.Category)
		}
		if s.Limit != 0 || s.Remaining != 0 || s.ResetUnix != 0 {
			t.Errorf("%s: 未初期化の値 = %+v, want すべて 0", s.Category, s)
		}
	}
}

func TestSnapshot_ObservedFromServerValues(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := NewRateLimiter()
	r.now = clock.now

	// GET /rateLimit の値はサーバ観測値
	iptr := func(n int) *int { return &n }
	reset := int(clock.t.Add(30 * time.Second).Unix())
	r.ConfigureFromRateLimit(&backlog.RateLimit{
		Read: &backlog.LimitStatus{Limit: iptr(100), Remaining: iptr(40), Reset: &reset},
	})
	read := indexSnapshot(t, r.Snapshot())[CategoryRead]
	if !read.Observed {
		t.Error("GET /rateLimit の値を反映したのに observed = false")
	}
	if read.Limit != 100 || read.Remaining != 40 {
		t.Errorf("limit / remaining = %d / %d, want 100 / 40", read.Limit, read.Remaining)
	}
	if int64(reset) != read.ResetUnix {
		t.Errorf("resetUnix = %d, want %d", read.ResetUnix, reset)
	}

	// レスポンスヘッダの観測値も反映される
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "7")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(clock.t.Add(40*time.Second).Unix(), 10))
	r.ObserveHeaders(CategoryRead, h)
	read = indexSnapshot(t, r.Snapshot())[CategoryRead]
	if !read.Observed || read.Remaining != 7 {
		t.Errorf("ヘッダ観測後 = %+v, want remaining 7 / observed true", read)
	}
	if want := clock.t.Add(40 * time.Second).Unix(); read.ResetUnix != want {
		t.Errorf("ヘッダ観測後の resetUnix = %d, want %d", read.ResetUnix, want)
	}
}

func TestSnapshot_RecoversAfterWindow(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	r := newTestLimiter(clock, map[Category]int{CategorySearch: 20})
	for i := 0; i < 20; i++ {
		if d := r.reserve(CategorySearch); d != 0 {
			t.Fatalf("%d 個目の取得に失敗", i+1)
		}
	}
	if got := indexSnapshot(t, r.Snapshot())[CategorySearch]; got.Remaining != 0 {
		t.Errorf("枯渇後の remaining = %d, want 0", got.Remaining)
	}

	// ウィンドウのリセット時刻を過ぎたら自然回復した値が見える
	clock.advance(61 * time.Second)
	got := indexSnapshot(t, r.Snapshot())[CategorySearch]
	if got.Remaining != 20 {
		t.Errorf("リセット後の remaining = %d, want 20", got.Remaining)
	}
	if got.ResetUnix <= clock.t.Unix() {
		t.Errorf("リセット後の resetUnix = %d, want 現在時刻(%d)より後", got.ResetUnix, clock.t.Unix())
	}
}

func TestClassifyRequest(t *testing.T) {
	mk := func(method, path string) *http.Request {
		return &http.Request{Method: method, URL: &url.URL{Path: path}}
	}
	cases := []struct {
		method, path string
		want         Category
	}{
		// search 区分: 課題一覧・件数、Wiki 一覧・件数
		{"GET", "/api/v2/issues", CategorySearch},
		{"GET", "/api/v2/issues/count", CategorySearch},
		{"GET", "/api/v2/wikis", CategorySearch},
		{"GET", "/api/v2/wikis/count", CategorySearch},
		// read 区分
		{"GET", "/api/v2/issues/EX-1", CategoryRead},
		{"GET", "/api/v2/users/myself", CategoryRead},
		{"GET", "/api/v2/wikis/123", CategoryRead},
		{"GET", "/api/v2/projects", CategoryRead},
		// icon 区分: スペースロゴ・プロジェクトアイコン・ユーザアイコン・チームアイコン
		{"GET", "/api/v2/space/image", CategoryIcon},
		{"GET", "/api/v2/projects/EXAMPLE/image", CategoryIcon},
		{"GET", "/api/v2/users/1/icon", CategoryIcon},
		{"GET", "/api/v2/teams/5/icon", CategoryIcon},
		// update 区分
		{"POST", "/api/v2/issues", CategoryUpdate},
		{"PATCH", "/api/v2/issues/EX-1", CategoryUpdate},
		{"DELETE", "/api/v2/issues/EX-1", CategoryUpdate},
	}
	for _, c := range cases {
		if got := classifyRequest(mk(c.method, c.path)); got != c.want {
			t.Errorf("classifyRequest(%s %s) = %s, want %s", c.method, c.path, got, c.want)
		}
	}
}
