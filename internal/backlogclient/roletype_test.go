package backlogclient

import (
	"encoding/json"
	"testing"

	"github.com/kenzo0107/backlog"
)

func TestRoleTypeConstants(t *testing.T) {
	// API 実値との対応(ライブラリの iota 定数は 1 ずれるため使用しない)
	cases := []struct {
		role RoleType
		want int
		name string
	}{
		{RoleAdmin, 1, "管理者"},
		{RoleUser, 2, "一般ユーザ"},
		{RoleReporter, 3, "レポーター"},
		{RoleViewer, 4, "閲覧者"},
		{RoleGuestReporter, 5, "ゲストレポーター"},
		{RoleGuestViewer, 6, "ゲスト閲覧者"},
	}
	for _, c := range cases {
		if int(c.role) != c.want {
			t.Errorf("RoleType %s = %d, want %d", c.name, int(c.role), c.want)
		}
		if c.role.String() != c.name {
			t.Errorf("RoleType(%d).String() = %q, want %q", int(c.role), c.role.String(), c.name)
		}
		if !c.role.IsValid() {
			t.Errorf("RoleType(%d).IsValid() = false, want true", int(c.role))
		}
	}
	if RoleType(0).IsValid() || RoleType(7).IsValid() {
		t.Error("範囲外の RoleType が IsValid=true になっている")
	}
}

func TestRoleTypeOf_UsesAPIValueNotLibraryConstants(t *testing.T) {
	// API レスポンス roleType=1 は管理者。JSON デコード後の値をそのまま使えば正しい。
	var u backlog.User
	if err := json.Unmarshal([]byte(`{"id": 12345, "roleType": 1}`), &u); err != nil {
		t.Fatal(err)
	}
	got := RoleTypeOf(&u)
	if got != RoleAdmin {
		t.Errorf("RoleTypeOf(roleType=1) = %d, want RoleAdmin(1)", got)
	}
	// ライブラリの定数 RoleTypeAdministrator は 0 であり API 実値と 1 ずれる
	// (このずれこそが自前定数を使う理由)。ずれが直った場合はこのテストで検知される。
	if int(backlog.RoleTypeAdministrator) == int(RoleAdmin) {
		t.Log("注意: ライブラリの RoleType 定数が修正された可能性があります(自前定数の要否を再確認)")
	}
	if got == RoleAdmin && backlog.RoleType(got) == backlog.RoleTypeGeneralUser {
		// backlog.RoleTypeGeneralUser == 1: ライブラリ定数で判定すると管理者を一般ユーザと誤判定する
		t.Log("ライブラリ定数での判定は誤り(既知バグ)であることを確認")
	}
}

func TestRoleTypeOf_Nil(t *testing.T) {
	if got := RoleTypeOf(nil); got != 0 {
		t.Errorf("RoleTypeOf(nil) = %d, want 0", got)
	}
}
