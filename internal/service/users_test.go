package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

func fakeAPIUser(id int64, code, name string, roleType int) backlogclient.User {
	return backlogclient.User{
		ID: id, UserCode: code, Name: name,
		MailAddress: code + "@example.com", RoleType: roleType,
		RawJSON: fmt.Sprintf(`{"id":%d,"userId":%q}`, id, code),
	}
}

// TestSyncUsers_ThenListUsers は service 経由の同期 → 一覧照会を確認する
// (app.go の Wails バインディングはこの 2 メソッドへ委譲する)。
func TestSyncUsers_ThenListUsers(t *testing.T) {
	fake := &fakeConnector{
		info: testInfo(),
		projects: []backlogclient.Project{
			{ID: 1, ProjectKey: "EXA", Name: "検証用", RawJSON: `{"id":1}`},
		},
		rawUsers: []backlogclient.User{
			fakeAPIUser(1, "admin", "あ 管理", 1),
			fakeAPIUser(2, "user1", "い 一般", 2),
		},
		pagedTeams: []backlogclient.Team{
			{ID: 10, Name: "開発チーム", MemberIDs: []int64{1, 2}, RawJSON: `{"id":10}`},
		},
		projectUsers: map[int64][]backlogclient.User{
			1: {fakeAPIUser(1, "admin", "あ 管理", 1), fakeAPIUser(2, "user1", "い 一般", 2)},
		},
		projectAdmins: map[int64][]backlogclient.User{
			1: {fakeAPIUser(1, "admin", "あ 管理", 1)},
		},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()

	// プロジェクトを先に同期しておく(ユーザ同期はローカル projects を対象にする)
	if _, err := s.SyncProjects(ctx, id); err != nil {
		t.Fatal(err)
	}
	res, err := s.SyncUsers(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != syncpkg.ModeUsersSpace {
		t.Errorf("Mode = %s, want %s", res.Mode, syncpkg.ModeUsersSpace)
	}
	if res.Upserted != 2 {
		t.Errorf("res = %+v", res)
	}

	list, err := s.ListUsers(ctx, id, store.UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 || len(list.Users) != 2 {
		t.Fatalf("一覧 = %+v", list)
	}
	admin := list.Users[0]
	if admin.UserCode != "admin" || admin.RoleName != "管理者" {
		t.Errorf("users[0] = %+v", admin)
	}
	if strings.Join(admin.TeamNames, ",") != "開発チーム" {
		t.Errorf("TeamNames = %v", admin.TeamNames)
	}
	if strings.Join(admin.ProjectKeys, ",") != "EXA" || strings.Join(admin.AdminProjectKeys, ",") != "EXA" {
		t.Errorf("プロジェクト = %v / %v", admin.ProjectKeys, admin.AdminProjectKeys)
	}
	if len(list.Users[1].AdminProjectKeys) != 0 {
		t.Errorf("一般ユーザに管理者プロジェクトが付いている: %v", list.Users[1].AdminProjectKeys)
	}

	// 絞り込み(名前・ログイン ID の部分一致)
	filtered, err := s.ListUsers(ctx, id, store.UserFilter{Keyword: "user1"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Users[0].UserCode != "user1" {
		t.Errorf("絞り込み結果 = %+v", filtered)
	}

	// 同期状態(users / project_id 0)が記録される
	state := findSyncState(t, s, id, store.DataKindUsers, store.ProjectScopeAll)
	if state == nil || state.LastSyncedAt == "" {
		t.Errorf("同期状態 = %+v", state)
	}
}

// TestSyncUsers_DegradedThroughService は権限不足(403)時に service 経由でも
// 縮退モードとなり、プロジェクト参加者からユーザが合成されることを確認する。
func TestSyncUsers_DegradedThroughService(t *testing.T) {
	denied := fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	fake := &fakeConnector{
		info: testInfo(),
		projects: []backlogclient.Project{
			{ID: 1, ProjectKey: "EXA", Name: "検証用", RawJSON: `{"id":1}`},
		},
		rawUsersErr:   denied,
		pagedTeamsErr: denied,
		projectUsers: map[int64][]backlogclient.User{
			1: {fakeAPIUser(2, "user1", "い 一般", 2)},
		},
	}
	s, id := newSyncTestService(t, fake)
	ctx := context.Background()
	if _, err := s.SyncProjects(ctx, id); err != nil {
		t.Fatal(err)
	}

	res, err := s.SyncUsers(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != syncpkg.ModeUsersProjects {
		t.Errorf("Mode = %s, want %s", res.Mode, syncpkg.ModeUsersProjects)
	}
	if len(res.Warnings) == 0 {
		t.Error("縮退の警告が付いていない")
	}
	list, err := s.ListUsers(ctx, id, store.UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || list.Users[0].UserCode != "user1" {
		t.Errorf("合成されたユーザ = %+v", list.Users)
	}
}

// TestListUsers_RequiresConnectedProfile は接続実績の無いプロファイルで
// エラーになること(DB を特定できない)を確認する。
func TestListUsers_RequiresConnectedProfile(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _ := newSyncTestService(t, fake)

	if _, err := s.ListUsers(context.Background(), "unknown", store.UserFilter{}); err == nil {
		t.Fatal("存在しないプロファイルでエラーにならなかった")
	}
}
