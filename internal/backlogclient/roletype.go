package backlogclient

import "github.com/kenzo0107/backlog"

// RoleType は Backlog API の roleType 実値。
//
// 重要: kenzo0107/backlog の RoleType 定数(RoleTypeAdministrator 等)は
// iota が 0 始まりのため API 実値(1 始まり)と 1 ずれる既知バグがある。
// ライブラリの定数は絶対に使わず、必ずこの自前定数で判定すること。
// (JSON デコード後の数値そのものは API 実値なので、値の変換は不要。
// ずれているのは「定数の定義」だけである。)
type RoleType int

const (
	RoleAdmin         RoleType = 1 // 管理者
	RoleUser          RoleType = 2 // 一般ユーザ
	RoleReporter      RoleType = 3 // レポーター
	RoleViewer        RoleType = 4 // 閲覧者
	RoleGuestReporter RoleType = 5 // ゲストレポーター
	RoleGuestViewer   RoleType = 6 // ゲスト閲覧者
)

// String はロールの日本語表示名を返す。
func (r RoleType) String() string {
	switch r {
	case RoleAdmin:
		return "管理者"
	case RoleUser:
		return "一般ユーザ"
	case RoleReporter:
		return "レポーター"
	case RoleViewer:
		return "閲覧者"
	case RoleGuestReporter:
		return "ゲストレポーター"
	case RoleGuestViewer:
		return "ゲスト閲覧者"
	default:
		return "不明"
	}
}

// IsValid は API 実値として妥当な範囲かを返す。
func (r RoleType) IsValid() bool {
	return r >= RoleAdmin && r <= RoleGuestViewer
}

// RoleTypeOf はライブラリの User から API 実値の RoleType を取り出す。
// backlog.RoleType は JSON の数値をそのまま保持しているため int 変換のみで良い
// (ライブラリの定数と比較してはならない)。
func RoleTypeOf(u *backlog.User) RoleType {
	if u == nil {
		return 0
	}
	return RoleType(int(u.RoleType))
}
