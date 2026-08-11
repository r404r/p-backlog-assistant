package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func testUser(id int64, code, name, mail string, roleType int) *User {
	return &User{
		ID: id, UserCode: code, Name: name, MailAddress: mail, RoleType: roleType,
		RawJSON: `{"id":` + strconv.FormatInt(id, 10) + `}`, FetchedAt: "2026-08-09T00:00:00Z",
	}
}

// TestReplaceUsers_ReplacesAll は users がスペース単位の全置換であること
// (退会ユーザが残らないこと)を確認する(設計書 3 節)。
func TestReplaceUsers_ReplacesAll(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "admin@example.com", 1),
		testUser(2, "user1", "一般 花子", "user1@example.com", 2),
	}); err != nil {
		t.Fatal(err)
	}
	// 2 回目の置換で 1 件だけにする(旧データは残らない)
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(2, "user1", "一般 花子(改称)", "user1@example.com", 3),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Users) != 1 {
		t.Fatalf("件数 = %d(total %d), want 1", len(res.Users), res.Total)
	}
	u := res.Users[0]
	if u.ID != 2 || u.Name != "一般 花子(改称)" || u.RoleType != 3 {
		t.Errorf("user = %+v", u)
	}
	if u.RoleName != "レポーター" {
		t.Errorf("RoleName = %q, want レポーター", u.RoleName)
	}
}

// TestReplaceTeams_ReplacesTeamsAndMembers は teams と team_members が
// まとめて全置換されることを確認する。
func TestReplaceTeams_ReplacesTeamsAndMembers(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.ReplaceTeams(ctx, []*Team{
		{ID: 10, Name: "開発チーム", MemberIDs: []int64{1, 2}, RawJSON: `{"id":10}`},
		{ID: 11, Name: "運用チーム", MemberIDs: []int64{2}, RawJSON: `{"id":11}`},
	}); err != nil {
		t.Fatal(err)
	}
	teams, members := countRows(t, s, "teams"), countRows(t, s, "team_members")
	if teams != 2 || members != 3 {
		t.Fatalf("teams = %d, team_members = %d, want 2 / 3", teams, members)
	}

	// 置換後は旧チーム・旧メンバー関係が残らない
	if err := s.ReplaceTeams(ctx, []*Team{
		{ID: 11, Name: "運用チーム", MemberIDs: []int64{1}, RawJSON: `{"id":11}`},
	}); err != nil {
		t.Fatal(err)
	}
	if teams, members = countRows(t, s, "teams"), countRows(t, s, "team_members"); teams != 1 || members != 1 {
		t.Fatalf("置換後 teams = %d, team_members = %d, want 1 / 1", teams, members)
	}

	// 空スライスでの置換は全消去(権限縮退時に管理者由来キャッシュを破棄する経路)
	if err := s.ReplaceTeams(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if teams, members = countRows(t, s, "teams"), countRows(t, s, "team_members"); teams != 0 || members != 0 {
		t.Fatalf("全消去後 teams = %d, team_members = %d, want 0 / 0", teams, members)
	}
}

// TestReplaceProjectUsers_PerProject は project_users がプロジェクト単位の
// 置換であり、他プロジェクトの行を巻き添えにしないことを確認する。
func TestReplaceProjectUsers_PerProject(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.ReplaceProjectUsers(ctx, 1, []ProjectUser{
		{UserID: 1, IsAdmin: true}, {UserID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 2, []ProjectUser{{UserID: 2}}); err != nil {
		t.Fatal(err)
	}
	// プロジェクト 1 のみ置換する
	if err := s.ReplaceProjectUsers(ctx, 1, []ProjectUser{{UserID: 3, IsAdmin: true}}); err != nil {
		t.Fatal(err)
	}

	got, err := listProjectUsers(ctx, s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UserID != 3 || !got[0].IsAdmin {
		t.Errorf("project 1 の行 = %+v", got)
	}
	if got, err = listProjectUsers(ctx, s, 2); err != nil {
		t.Fatal(err)
	} else if len(got) != 1 || got[0].UserID != 2 || got[0].IsAdmin {
		t.Errorf("project 2 の行 = %+v(他プロジェクトの置換で壊れた)", got)
	}
}

// TestListUserRows_JoinsTeamsAndProjects は所属チーム・参加プロジェクト・
// 管理者プロジェクトが JOIN で組み立てられ、名前順に並ぶことを確認する。
func TestListUserRows_JoinsTeamsAndProjects(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "あ 管理", "admin@example.com", 1),
		testUser(2, "user1", "い 一般", "user1@example.com", 2),
		testUser(3, "user2", "う 閲覧", "user2@example.com", 4),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*Team{
		{ID: 10, Name: "開発チーム", MemberIDs: []int64{1, 2}},
		{ID: 11, Name: "運用チーム", MemberIDs: []int64{2}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*Project{
		{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		{ID: 2, ProjectKey: "EXB", Name: "検証用 B"},
	} {
		if err := s.UpsertProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceProjectUsers(ctx, 1, []ProjectUser{
		{UserID: 1, IsAdmin: true}, {UserID: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 2, []ProjectUser{{UserID: 2, IsAdmin: true}}); err != nil {
		t.Fatal(err)
	}

	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Users) != 3 {
		t.Fatalf("件数 = %d(total %d), want 3", len(res.Users), res.Total)
	}
	// 名前順
	if res.Users[0].Name != "あ 管理" || res.Users[1].Name != "い 一般" || res.Users[2].Name != "う 閲覧" {
		t.Errorf("並び順 = %v", []string{res.Users[0].Name, res.Users[1].Name, res.Users[2].Name})
	}
	admin, general, viewer := res.Users[0], res.Users[1], res.Users[2]
	if admin.RoleName != "管理者" || viewer.RoleName != "閲覧者" {
		t.Errorf("RoleName = %q / %q", admin.RoleName, viewer.RoleName)
	}
	if strings.Join(admin.TeamNames, ",") != "開発チーム" {
		t.Errorf("admin.TeamNames = %v", admin.TeamNames)
	}
	if strings.Join(general.TeamNames, ",") != "開発チーム,運用チーム" &&
		strings.Join(general.TeamNames, ",") != "運用チーム,開発チーム" {
		t.Errorf("general.TeamNames = %v", general.TeamNames)
	}
	if strings.Join(admin.ProjectKeys, ",") != "EXA" {
		t.Errorf("admin.ProjectKeys = %v", admin.ProjectKeys)
	}
	if strings.Join(admin.AdminProjectKeys, ",") != "EXA" {
		t.Errorf("admin.AdminProjectKeys = %v", admin.AdminProjectKeys)
	}
	if strings.Join(general.ProjectKeys, ",") != "EXA,EXB" {
		t.Errorf("general.ProjectKeys = %v", general.ProjectKeys)
	}
	if strings.Join(general.AdminProjectKeys, ",") != "EXB" {
		t.Errorf("general.AdminProjectKeys = %v", general.AdminProjectKeys)
	}
	// 所属が無いユーザは空スライス(JSON で null にならないこと)
	if viewer.TeamNames == nil || len(viewer.TeamNames) != 0 {
		t.Errorf("viewer.TeamNames = %v, want 空スライス", viewer.TeamNames)
	}
	if viewer.ProjectKeys == nil || viewer.AdminProjectKeys == nil {
		t.Error("参加が無いユーザのプロジェクト列が nil になっている")
	}
}

// TestListUserRows_Filters はキーワード(名前・ログイン ID の部分一致)・
// roleType・上限の絞り込みを確認する。
func TestListUserRows_Filters(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "admin@example.com", 1),
		testUser(2, "hanako", "一般 花子", "user1@example.com", 2),
		testUser(3, "taro", "一般 太郎", "user2@example.com", 2),
	}); err != nil {
		t.Fatal(err)
	}

	// 名前の部分一致
	res, err := s.ListUserRows(ctx, UserFilter{Keyword: "太郎"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Errorf("名前の部分一致 = %d 件, want 2", res.Total)
	}
	// ログイン ID の部分一致
	if res, err = s.ListUserRows(ctx, UserFilter{Keyword: "hana"}); err != nil {
		t.Fatal(err)
	} else if res.Total != 1 || res.Users[0].UserCode != "hanako" {
		t.Errorf("ログイン ID の部分一致 = %+v", res.Users)
	}
	// roleType(0 は全件)
	if res, err = s.ListUserRows(ctx, UserFilter{RoleType: 2}); err != nil {
		t.Fatal(err)
	} else if res.Total != 2 {
		t.Errorf("roleType 絞り込み = %d 件, want 2", res.Total)
	}
	if res, err = s.ListUserRows(ctx, UserFilter{RoleType: 0}); err != nil {
		t.Fatal(err)
	} else if res.Total != 3 {
		t.Errorf("roleType 0(全件)= %d 件, want 3", res.Total)
	}
	// 上限(Total は切り詰め前の件数)
	if res, err = s.ListUserRows(ctx, UserFilter{Limit: 2}); err != nil {
		t.Fatal(err)
	} else if res.Total != 3 || len(res.Users) != 2 || !res.Truncated {
		t.Errorf("上限適用 = {total:%d rows:%d truncated:%v}, want {3 2 true}",
			res.Total, len(res.Users), res.Truncated)
	}
	// LIKE メタ文字はエスケープされる(全件一致にならない)
	if res, err = s.ListUserRows(ctx, UserFilter{Keyword: "%"}); err != nil {
		t.Fatal(err)
	} else if res.Total != 0 {
		t.Errorf("%% の検索 = %d 件, want 0", res.Total)
	}
}

// TestRoleName_UnknownIncludesValue は未知の roleType が
// 「不明(N)」形式で数値を含むこと(画面・Excel で値を確認できること)を確認する(中 4)。
func TestRoleName_UnknownIncludesValue(t *testing.T) {
	for roleType, want := range map[int]string{
		1: "管理者", 2: "一般ユーザ", 3: "レポーター",
		4: "閲覧者", 5: "ゲストレポーター", 6: "ゲスト閲覧者",
		0: "不明(0)", 7: "不明(7)", 99: "不明(99)", -1: "不明(-1)",
	} {
		if got := RoleName(roleType); got != want {
			t.Errorf("RoleName(%d) = %q, want %q", roleType, got, want)
		}
	}
}

// TestListUserRows_UnknownRoleName は未知 roleType のユーザ行でも
// 数値付きの表示名が付くことを確認する(中 4)。
func TestListUserRows_UnknownRoleName(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "future", "未知 ロール", "future@example.com", 99),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Users[0].RoleName != "不明(99)" {
		t.Errorf("RoleName = %q, want 不明(99)", res.Users[0].RoleName)
	}
	if res.Users[0].RoleType != 99 {
		t.Errorf("RoleType = %d, want 99(数値も返す)", res.Users[0].RoleType)
	}
}

// TestListUserRows_KeywordMatchesMail はキーワード検索の対象に
// メールアドレスが含まれることを確認する(中 2。TS 契約・画面の説明と一致させる)。
func TestListUserRows_KeywordMatchesMail(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "kanri@example.com", 1),
		testUser(2, "hanako", "一般 花子", "ippan@example.org", 2),
	}); err != nil {
		t.Fatal(err)
	}

	// 名前にもログイン ID にも含まれない文字列でメールに一致する
	res, err := s.ListUserRows(ctx, UserFilter{Keyword: "example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Users) != 1 || res.Users[0].ID != 2 {
		t.Fatalf("メール部分一致 = %+v, want ID 2 の 1 件", res.Users)
	}
	if res, err = s.ListUserRows(ctx, UserFilter{Keyword: "kanri"}); err != nil {
		t.Fatal(err)
	} else if res.Total != 1 || res.Users[0].ID != 1 {
		t.Errorf("メールのローカル部一致 = %+v", res.Users)
	}
	// 3 列すべてに一致しない語は 0 件
	if res, err = s.ListUserRows(ctx, UserFilter{Keyword: "該当なし"}); err != nil {
		t.Fatal(err)
	} else if res.Total != 0 {
		t.Errorf("不一致の検索 = %d 件, want 0", res.Total)
	}
}

// TestListUserRows_ManyUsers は大量件数でも SQLite の変数上限
// (既定 32,766)に触れず、関連情報が正しく組み立てられることを確認する(中 3)。
func TestListUserRows_ManyUsers(t *testing.T) {
	const n = 1000
	s := openTempStore(t)
	ctx := context.Background()

	users := make([]*User, 0, n)
	members := make([]int64, 0, n)
	projectUsers := make([]ProjectUser, 0, n)
	for i := 1; i <= n; i++ {
		id := int64(i)
		users = append(users, testUser(id, fmt.Sprintf("user%04d", i), fmt.Sprintf("ユーザ%04d", i),
			fmt.Sprintf("user%04d@example.com", i), 2))
		members = append(members, id)
		projectUsers = append(projectUsers, ProjectUser{UserID: id, IsAdmin: i%2 == 0})
	}
	if err := s.ReplaceUsers(ctx, users); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*Team{{ID: 10, Name: "全社チーム", MemberIDs: members}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(ctx, &Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 1, projectUsers); err != nil {
		t.Fatal(err)
	}

	res, err := s.ListUserRows(ctx, UserFilter{Limit: n})
	if err != nil {
		t.Fatalf("大量件数で失敗した: %v", err)
	}
	if res.Total != n || len(res.Users) != n {
		t.Fatalf("件数 = %d(total %d), want %d", len(res.Users), res.Total, n)
	}
	admins := 0
	for _, u := range res.Users {
		if len(u.TeamNames) != 1 || u.TeamNames[0] != "全社チーム" {
			t.Fatalf("user %d の所属チーム = %v", u.ID, u.TeamNames)
		}
		if len(u.ProjectKeys) != 1 || u.ProjectKeys[0] != "EXA" {
			t.Fatalf("user %d の参加プロジェクト = %v", u.ID, u.ProjectKeys)
		}
		admins += len(u.AdminProjectKeys)
	}
	if admins != n/2 {
		t.Errorf("管理者プロジェクトの合計 = %d, want %d", admins, n/2)
	}
}

// TestReplaceUsers_RollsBackOnFailure は全置換が単一トランザクション
// (WithTx 経由)で行われ、途中失敗時に削除も巻き戻ることを確認する(低 2a)。
func TestReplaceUsers_RollsBackOnFailure(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "admin@example.com", 1),
	}); err != nil {
		t.Fatal(err)
	}

	// 同一 ID を 2 件渡すと 2 件目の INSERT が主キー制約で失敗する
	// (DELETE 済みの状態で失敗するため、巻き戻らなければ 0 件になる)。
	err := s.ReplaceUsers(ctx, []*User{
		testUser(2, "user1", "一般 花子", "user1@example.com", 2),
		testUser(2, "user1", "一般 花子", "user1@example.com", 2),
	})
	if err == nil {
		t.Fatal("重複 ID の挿入がエラーにならなかった")
	}
	if n := countRows(t, s, "users"); n != 1 {
		t.Fatalf("失敗後の users = %d 件, want 1(ロールバックされていない)", n)
	}
	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 1 || res.Users[0].ID != 1 {
		t.Errorf("残っている行 = %+v, want 置換前の行", res.Users)
	}
}

// TestReplaceTeams_RollsBackOnFailure は teams / team_members の全置換が
// 単一トランザクション(WithTx 経由)であることを確認する(低 2a)。
func TestReplaceTeams_RollsBackOnFailure(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceTeams(ctx, []*Team{
		{ID: 10, Name: "開発チーム", MemberIDs: []int64{1, 2}},
	}); err != nil {
		t.Fatal(err)
	}

	err := s.ReplaceTeams(ctx, []*Team{
		{ID: 11, Name: "運用チーム", MemberIDs: []int64{1}},
		{ID: 11, Name: "運用チーム(重複)", MemberIDs: []int64{2}},
	})
	if err == nil {
		t.Fatal("重複 ID の挿入がエラーにならなかった")
	}
	if teams, members := countRows(t, s, "teams"), countRows(t, s, "team_members"); teams != 1 || members != 2 {
		t.Fatalf("失敗後 teams = %d, team_members = %d, want 1 / 2(ロールバックされていない)", teams, members)
	}
}

// TestUpsertUsers_KeepsExistingUsers は UPSERT が既存ユーザを削除しないこと
// (縮退パスの部分失敗時にユーザが消えないこと)を確認する(高 2)。
func TestUpsertUsers_KeepsExistingUsers(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "admin@example.com", 1),
		testUser(2, "hanako", "一般 花子", "user1@example.com", 2),
	}); err != nil {
		t.Fatal(err)
	}

	// 1 は更新、3 は追加。2 は対象外だが削除されない。
	if err := s.UpsertUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎(改称)", "admin@example.com", 1),
		testUser(3, "taro", "一般 太郎", "user2@example.com", 2),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 3 {
		t.Fatalf("件数 = %d, want 3(既存ユーザが削除された)", len(res.Users))
	}
	byID := map[int64]UserRow{}
	for _, u := range res.Users {
		byID[u.ID] = u
	}
	if byID[1].Name != "管理 太郎(改称)" {
		t.Errorf("更新されていない: %+v", byID[1])
	}
	if byID[2].Name != "一般 花子" {
		t.Errorf("対象外のユーザが変わった: %+v", byID[2])
	}
	if byID[3].UserCode != "taro" {
		t.Errorf("追加されていない: %+v", byID[3])
	}
}

// TestMergeTeams_KeepsOtherTeams は指定チームのみ更新し、
// 未指定のチーム・そのメンバー関係を削除しないことを確認する(高 1・高 2)。
func TestMergeTeams_KeepsOtherTeams(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceTeams(ctx, []*Team{
		{ID: 10, Name: "開発チーム", MemberIDs: []int64{1, 2}},
		{ID: 11, Name: "運用チーム", MemberIDs: []int64{2}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.MergeTeams(ctx, []*Team{
		{ID: 10, Name: "開発チーム(改称)", MemberIDs: []int64{1}}, // メンバーは置換される
		{ID: 12, Name: "新設チーム", MemberIDs: []int64{3}},
	}); err != nil {
		t.Fatal(err)
	}
	if teams := countRows(t, s, "teams"); teams != 3 {
		t.Fatalf("teams = %d, want 3(未指定のチームが消えた)", teams)
	}
	names := map[int64]string{}
	rows, err := s.DB().QueryContext(ctx, `SELECT id, name FROM teams`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		names[id] = name
	}
	if names[10] != "開発チーム(改称)" || names[11] != "運用チーム" || names[12] != "新設チーム" {
		t.Errorf("チーム名 = %v", names)
	}
	// 10 のメンバーは置換(1 のみ)、11 は据え置き(2)、12 は追加(3)
	if members := countRows(t, s, "team_members"); members != 3 {
		t.Errorf("team_members = %d, want 3", members)
	}
}

// TestListUserRows_EmptyResultIsNotNull は該当 0 件でも Users が空スライスであること
// (JSON で null にならないこと)を確認する(低 2b)。
func TestListUserRows_EmptyResultIsNotNull(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	res, err := s.ListUserRows(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Users == nil {
		t.Fatal("ユーザ 0 件で Users が nil(JSON で null になる)")
	}
	if len(res.Users) != 0 || res.Total != 0 {
		t.Fatalf("結果 = %+v, want 空", res)
	}

	// 条件に一致しない場合も同様
	if err := s.ReplaceUsers(ctx, []*User{
		testUser(1, "admin", "管理 太郎", "admin@example.com", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if res, err = s.ListUserRows(ctx, UserFilter{Keyword: "該当なし"}); err != nil {
		t.Fatal(err)
	} else if res.Users == nil || len(res.Users) != 0 {
		t.Fatalf("不一致時の Users = %v, want 空スライス", res.Users)
	}

	// 関連が 1 つも無いユーザの配列も nil にしない
	if res, err = s.ListUserRows(ctx, UserFilter{}); err != nil {
		t.Fatal(err)
	} else if u := res.Users[0]; u.TeamNames == nil || u.ProjectKeys == nil || u.AdminProjectKeys == nil {
		t.Errorf("関連が無いユーザの配列が nil: %+v", u)
	}
}

// --- テスト補助 -----------------------------------------------------------

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func listProjectUsers(ctx context.Context, s *Store, projectID int64) ([]ProjectUser, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT user_id, is_admin FROM project_users WHERE project_id = ? ORDER BY user_id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectUser{}
	for rows.Next() {
		var pu ProjectUser
		var isAdmin int
		if err := rows.Scan(&pu.UserID, &isAdmin); err != nil {
			return nil, err
		}
		pu.IsAdmin = isAdmin != 0
		out = append(out, pu)
	}
	return out, rows.Err()
}
