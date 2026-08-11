package service

import (
	"context"

	"backlog-assistant/internal/backlogclient"
)

// RateLimitCategory は 1 区分のレート制限残量(フロントエンドの表示用)。
// Observed が false の区分はサーバから実値を取得できていないことを示し、
// UI は「不明」として扱う(Limit / Remaining / ResetUnix は 0)。
type RateLimitCategory struct {
	Name      string `json:"name"` // read / update / search / icon
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	ResetUnix int64  `json:"resetUnix"` // 現在ウィンドウのリセット時刻(Unix 秒)
	Observed  bool   `json:"observed"`
}

// RateLimitStatus は区分別のレート制限残量。
type RateLimitStatus struct {
	Categories []RateLimitCategory `json:"categories"`
}

// GetRateLimitStatus は保存済みプロファイルのレート制限残量を返す。
//
// 完全な読み取り専用パスであり、API 通信は一切行わない(中 1)。
// 返すのはキャッシュ済みクライアントがこれまでの通信で観測した値
// (GET /rateLimit の実値・レスポンスヘッダ)と、固定ウィンドウの経過による
// 自然回復だけ。クライアント未生成のプロファイルでは生成も InitRateLimit の
// 再試行も行わず、全区分 observed = false を返す(残量は本来の操作を 1 度でも
// 行えば観測される)。画面は 10 秒間隔でこれを呼ぶため、ここで通信すると
// 「追加通信なし」の契約が崩れる。
//
// 保存済み設定・クライアントキャッシュに触るため、入口で profileMu.RLock を取り
// SaveProfile / DeleteProfile と排他する(中 1)。
// ロック順序は profileMu → clientsMu。
func (s *ProfileService) GetRateLimitStatus(ctx context.Context, profileID string) (*RateLimitStatus, error) {
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()

	// 未知のプロファイル ID は誤りとして伝える(空の残量表示で紛らわせない)。
	// 保存済み設定の参照だけで、API 通信は伴わない。
	if _, err := s.cfg.Get(profileID); err != nil {
		return nil, err
	}

	s.clientsMu.Lock()
	client := s.snapshotClientLocked(profileID)
	s.clientsMu.Unlock()

	var snapshot []backlogclient.CategoryStatus
	if client != nil {
		snapshot = client.RateLimitSnapshot()
	} else {
		snapshot = unobservedSnapshot()
	}
	categories := make([]RateLimitCategory, 0, len(snapshot))
	for _, c := range snapshot {
		categories = append(categories, RateLimitCategory{
			Name:      string(c.Category),
			Limit:     c.Limit,
			Remaining: c.Remaining,
			ResetUnix: c.ResetUnix,
			Observed:  c.Observed,
		})
	}
	return &RateLimitStatus{Categories: categories}, nil
}

// snapshotClientLocked はキャッシュ済みクライアントを返す(clientsMu 保持前提)。
// clientForProfile と違い、生成も InitRateLimit の再試行も行わない
// (残量表示から API 通信を誘発しないための読み取り専用パス。中 1)。
// キャッシュに無ければ nil を返す。
func (s *ProfileService) snapshotClientLocked(profileID string) connector {
	if e, ok := s.clients[profileID]; ok {
		return e.client
	}
	return nil
}

// unobservedSnapshot は「未観測」のスナップショット(全区分 observed = false)を返す。
// 区分と順序はクライアントのスナップショットと揃える(UI の表示順を変えない)。
func unobservedSnapshot() []backlogclient.CategoryStatus {
	cats := []backlogclient.Category{
		backlogclient.CategoryRead,
		backlogclient.CategoryUpdate,
		backlogclient.CategorySearch,
		backlogclient.CategoryIcon,
	}
	out := make([]backlogclient.CategoryStatus, 0, len(cats))
	for _, c := range cats {
		out = append(out, backlogclient.CategoryStatus{Category: c})
	}
	return out
}
