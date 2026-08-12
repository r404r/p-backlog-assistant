package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// seedProjects はローカル DB にプロジェクトを登録し、プロジェクト同期が
// 完了している状態(sync_state)にする
// (ユーザ・チーム同期はローカル projects を対象に縮退・補完取得を行い、
// その前提としてプロジェクト同期の完了を確認する。R1)。
func seedProjects(t *testing.T, s *store.Store, projects ...store.Project) {
	t.Helper()
	ctx := context.Background()
	for i := range projects {
		p := projects[i]
		if err := s.UpsertProject(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	markProjectsSynced(t, s)
}

// seedProjectsOnly はプロジェクト行だけを登録する(同期状態は記録しない)。
// 「前回の同期でプロジェクト行は残っているが、今回の起動ではまだ同期していない」
// 状態の再現に使う(R1)。
func seedProjectsOnly(t *testing.T, s *store.Store, projects ...store.Project) {
	t.Helper()
	ctx := context.Background()
	for i := range projects {
		p := projects[i]
		if err := s.UpsertProject(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
}

// markProjectsSynced はプロジェクト同期が完了している状態を作る。
func markProjectsSynced(t *testing.T, s *store.Store) {
	t.Helper()
	if err := s.SetSyncCompleted(context.Background(), store.DataKindProjects, store.ProjectScopeAll,
		"2026-08-12T00:00:00Z", "2026-08-12"); err != nil {
		t.Fatal(err)
	}
}

func userRows(t *testing.T, s *store.Store) []store.UserRow {
	t.Helper()
	res, err := s.ListUserRows(context.Background(), store.UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Users
}

// --- 管理者パス -----------------------------------------------------------

// TestSyncUsers_AdminPath は管理者権限がある場合の同期
// (users 全置換 + teams 全ページ消化 + プロジェクト参加者・管理者)を確認する。
func TestSyncUsers_AdminPath(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{
		fakeUser(1, "admin", "あ 管理", 1),
		fakeUser(2, "user1", "い 一般", 2),
	}
	// 250 チーム = count=100 で 3 ページ(最終ページは 50 件)
	for i := 1; i <= 250; i++ {
		api.teams = append(api.teams, backlogclient.Team{
			ID: int64(i), Name: fmt.Sprintf("チーム%03d", i), MemberIDs: []int64{1},
			RawJSON: fmt.Sprintf(`{"id":%d}`, i),
		})
	}
	api.teams[0].MemberIDs = []int64{1, 2}
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectAdmins[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}

	s := openTempStore(t)
	seedProjects(t, s, store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"})

	// 旧データ(退会ユーザ・解散チーム・脱退した参加関係)を仕込む
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 99, UserCode: "old", Name: "旧 ユーザ"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 999, Name: "旧チーム", MemberIDs: []int64{99}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 1, []store.ProjectUser{{UserID: 99}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeUsersSpace {
		t.Errorf("Mode = %s, want %s", res.Mode, ModeUsersSpace)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("警告 = %v, want 空", res.Warnings)
	}
	// teams は 100 件ずつ 3 ページ + 空ページで消化する(境界: 最終ページ 50 件)
	if len(api.teamOffsets) != 3 || api.teamOffsets[0] != 0 || api.teamOffsets[1] != 100 || api.teamOffsets[2] != 200 {
		t.Errorf("teams の offset = %v, want [0 100 200]", api.teamOffsets)
	}

	rows := userRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("ユーザ件数 = %d, want 2(旧データが残っている)", len(rows))
	}
	if rows[0].UserCode != "admin" || rows[0].RoleName != "管理者" {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if len(rows[0].TeamNames) != 250 {
		t.Errorf("管理ユーザの所属チーム数 = %d, want 250(全ページ消化)", len(rows[0].TeamNames))
	}
	if len(rows[1].TeamNames) != 1 || rows[1].TeamNames[0] != "チーム001" {
		t.Errorf("一般ユーザの所属チーム = %v", rows[1].TeamNames)
	}
	if strings.Join(rows[0].ProjectKeys, ",") != "EXA" || strings.Join(rows[0].AdminProjectKeys, ",") != "EXA" {
		t.Errorf("管理ユーザのプロジェクト = %v / %v", rows[0].ProjectKeys, rows[0].AdminProjectKeys)
	}
	if strings.Join(rows[1].ProjectKeys, ",") != "EXA" || len(rows[1].AdminProjectKeys) != 0 {
		t.Errorf("一般ユーザのプロジェクト = %v / %v", rows[1].ProjectKeys, rows[1].AdminProjectKeys)
	}

	// sync_state(users / project_id 0)が更新される
	state, err := s.GetSyncState(ctx, store.DataKindUsers, store.ProjectScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.LastSyncedAt == "" {
		t.Fatalf("同期状態 = %+v", state)
	}
}

// TestSyncUsers_TeamPagingExactMultiple はチーム件数がページサイズの倍数のとき
// 空ページまで取得して終了することを確認する(取り逃し防止の境界)。
func TestSyncUsers_TeamPagingExactMultiple(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	for i := 1; i <= 200; i++ {
		api.teams = append(api.teams, backlogclient.Team{
			ID: int64(i), Name: fmt.Sprintf("チーム%03d", i), MemberIDs: []int64{1},
		})
	}
	s := openTempStore(t)
	e := newTestEngine(t, api, s)

	if _, err := e.SyncUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.teamOffsets) != 3 || api.teamOffsets[2] != 200 {
		t.Errorf("teams の offset = %v, want [0 100 200](空ページまで消化)", api.teamOffsets)
	}
	if n := len(userRows(t, s)[0].TeamNames); n != 200 {
		t.Errorf("所属チーム数 = %d, want 200", n)
	}
}

// --- 縮退パス -------------------------------------------------------------

// TestSyncUsers_DegradedPath は GET /users が 403 の場合に、
// ローカル projects のプロジェクト参加者からユーザ集合を合成することを確認する。
func TestSyncUsers_DegradedPath(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectUsers[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2), fakeUser(3, "user2", "う 閲覧", 4)}
	api.projectAdmins[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2)}

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// 管理者権限があった頃のキャッシュ(縮退時は破棄されること)
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 99, UserCode: "old", Name: "旧 ユーザ"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 999, Name: "旧チーム", MemberIDs: []int64{99}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeUsersProjects {
		t.Errorf("Mode = %s, want %s", res.Mode, ModeUsersProjects)
	}
	if len(res.Warnings) == 0 {
		t.Error("縮退の警告が付いていない")
	}

	rows := userRows(t, s)
	if len(rows) != 3 {
		t.Fatalf("ユーザ件数 = %d, want 3(合成 + 旧データ破棄)", len(rows))
	}
	names := []string{rows[0].Name, rows[1].Name, rows[2].Name}
	if strings.Join(names, ",") != "あ 管理,い 一般,う 閲覧" {
		t.Errorf("合成されたユーザ = %v", names)
	}
	if strings.Join(rows[1].ProjectKeys, ",") != "EXA,EXB" {
		t.Errorf("参加プロジェクト = %v", rows[1].ProjectKeys)
	}
	if strings.Join(rows[1].AdminProjectKeys, ",") != "EXB" {
		t.Errorf("管理プロジェクト = %v", rows[1].AdminProjectKeys)
	}
	// teams が 403 の場合は管理者由来キャッシュを破棄する(設計書 2 節)
	for _, r := range rows {
		if len(r.TeamNames) != 0 {
			t.Errorf("%s の所属チーム = %v, want 空(teams は取得できていない)", r.Name, r.TeamNames)
		}
	}
}

// TestSyncUsers_DegradedPath_ComposesProjectTeams は縮退パスで
// プロジェクト単位のチーム(GET /projects/:id/teams)を合成し、
// スペース /teams が 403 でもチーム情報が得られることを確認する(高 1)。
// 同じチームが複数プロジェクトに現れても team ID で重複排除する。
func TestSyncUsers_DegradedPath_ComposesProjectTeams(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectUsers[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2)}
	// チーム 10 は両プロジェクトに現れる(重複排除の確認)
	api.projectTeams[1] = []backlogclient.Team{
		fakeTeam(10, "開発チーム", 1, 2),
		fakeTeam(11, "運用チーム", 1),
	}
	api.projectTeams[2] = []backlogclient.Team{
		fakeTeam(10, "開発チーム", 1, 2),
		fakeTeam(12, "検証チーム", 2),
	}

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != ModeUsersProjects {
		t.Errorf("Mode = %s, want %s", res.Mode, ModeUsersProjects)
	}
	// 対象プロジェクトごとに 1 回ずつ取得する
	if len(api.projectTeamsCalls) != 2 {
		t.Errorf("GetProjectTeams の呼び出し = %v, want 2 回", api.projectTeamsCalls)
	}

	rows := userRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("ユーザ件数 = %d, want 2", len(rows))
	}
	admin, general := rows[0], rows[1]
	if strings.Join(admin.TeamNames, ",") != "運用チーム,開発チーム" {
		t.Errorf("管理ユーザの所属チーム = %v, want [運用チーム 開発チーム]", admin.TeamNames)
	}
	if strings.Join(general.TeamNames, ",") != "検証チーム,開発チーム" {
		t.Errorf("一般ユーザの所属チーム = %v, want [検証チーム 開発チーム]", general.TeamNames)
	}
}

// TestSyncUsers_DegradedPath_ProjectTeamFailureKeepsCache は縮退パスで
// 一部プロジェクトのチーム取得に失敗した場合、取得できた分のみを反映し、
// 未取得のチームを全置換で消さないことを確認する(高 1)。
func TestSyncUsers_DegradedPath_ProjectTeamFailureKeepsCache(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsers[2] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectTeams[1] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}
	api.projectTeamsErr[2] = errors.New("フェイク: プロジェクト 2 のチーム取得に失敗")

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// プロジェクト 2 由来のチーム(今回は取得できない)。据え置く。
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 20, Name: "別部門チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "チーム一覧を取得できませんでした") && !hasWarning(res, "取得できた分のみ") {
		t.Errorf("警告 = %v, want チーム取得失敗の旨", res.Warnings)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "別部門チーム,開発チーム" {
		t.Errorf("所属チーム = %v, want [別部門チーム 開発チーム](既存を残しつつ取得分を反映)", rows[0].TeamNames)
	}
}

// TestSyncUsers_SpaceTeamsDeniedWithUsersOK_ComposesProjectTeams は
// 「/users 成功・/teams 403」でもプロジェクト経由でチームを合成することを
// 確認する(高 1)。チームの縮退判定は /teams 自体の結果を基準に行うため、
// ユーザ一覧が取得できていてもチーム取得はプロジェクト単位へ縮退する。
func TestSyncUsers_SpaceTeamsDeniedWithUsersOK_ComposesProjectTeams(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectUsers[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2)}
	// チーム 10 は両プロジェクトに現れる(重複排除の確認)
	api.projectTeams[1] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1, 2), fakeTeam(11, "運用チーム", 1)}
	api.projectTeams[2] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1, 2), fakeTeam(12, "検証チーム", 2)}

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// ユーザ側は縮退していない(スペース /users は成功している)
	if res.Mode != ModeUsersSpace {
		t.Errorf("Mode = %s, want %s", res.Mode, ModeUsersSpace)
	}
	if len(api.projectTeamsCalls) != 2 {
		t.Errorf("GetProjectTeams の呼び出し = %v, want 2 回(/teams 403 のため縮退)", api.projectTeamsCalls)
	}
	rows := userRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("ユーザ件数 = %d, want 2", len(rows))
	}
	admin, general := rows[0], rows[1]
	if strings.Join(admin.TeamNames, ",") != "運用チーム,開発チーム" {
		t.Errorf("管理ユーザの所属チーム = %v, want [運用チーム 開発チーム]", admin.TeamNames)
	}
	if strings.Join(general.TeamNames, ",") != "検証チーム,開発チーム" {
		t.Errorf("一般ユーザの所属チーム = %v, want [検証チーム 開発チーム]", general.TeamNames)
	}
	if !hasWarning(res, "チーム一覧") {
		t.Errorf("警告 = %v, want チーム一覧の権限がない旨", res.Warnings)
	}
}

// TestSyncUsers_SpaceTeamsDeniedWithUsersOK_KeepsExistingTeamsOnPartialFailure は
// 「/users 成功・/teams 403」で一部プロジェクトのチーム取得に失敗した場合、
// 既存のチーム情報を消さずに取得できた分だけを反映することを確認する
// (高 1・中 1(c))。
func TestSyncUsers_SpaceTeamsDeniedWithUsersOK_KeepsExistingTeamsOnPartialFailure(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsers[2] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectTeams[1] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}
	api.projectTeamsErr[2] = errors.New("フェイク: プロジェクト 2 のチーム取得に失敗")

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// 取得できなかったプロジェクト由来の既存チーム。据え置く。
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 20, Name: "別部門チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "別部門チーム,開発チーム" {
		t.Errorf("所属チーム = %v, want [別部門チーム 開発チーム](既存を残しつつ取得分を反映)", rows[0].TeamNames)
	}
	if !hasWarning(res, "削除反映は行っていません") {
		t.Errorf("警告 = %v, want 削除反映を行っていない旨", res.Warnings)
	}
}

// TestSyncUsers_SpaceTeamsSuccessReplacesRegardlessOfProjectFailure は
// スペース /teams が成功した場合、プロジェクト側の取得失敗に左右されず
// 完全な一覧で全置換することを確認する(中 1(a))。
// この場合はプロジェクト単位のチーム取得も行わない。
func TestSyncUsers_SpaceTeamsSuccessReplacesRegardlessOfProjectFailure(t *testing.T) {
	api := newFakeAPI()
	// ユーザ側は縮退 + 一部プロジェクトの参加者取得に失敗させる
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsersErr[2] = errors.New("フェイク: プロジェクト 2 の参加者取得に失敗")
	api.teams = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// 解散した旧チーム。スペース /teams が完全に取れているので削除される。
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 999, Name: "旧チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.projectTeamsCalls) != 0 {
		t.Errorf("スペース /teams 成功時に GetProjectTeams を呼んでいる: %v", api.projectTeamsCalls)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "開発チーム" {
		t.Errorf("所属チーム = %v, want [開発チーム](スペース一覧で全置換)", rows[0].TeamNames)
	}
	if hasWarning(res, "チーム") {
		t.Errorf("警告 = %v, want チーム関連の警告なし", res.Warnings)
	}
}

// TestSyncUsers_SpaceTeamsTransientError_MergesProjectTeams は
// スペース /teams が一時エラー(403 以外)の場合、プロジェクト経由で
// 取得できた分を削除反映なしで反映することを確認する(中 1(b))。
func TestSyncUsers_SpaceTeamsTransientError_MergesProjectTeams(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teamsErr = errors.New("フェイク: 通信エラー")
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectTeams[1] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}

	s := openTempStore(t)
	seedProjects(t, s, store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"})
	ctx := context.Background()
	// 今回取得できないチーム。一時エラーでは破棄しない。
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 20, Name: "別部門チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.projectTeamsCalls) != 1 {
		t.Errorf("GetProjectTeams の呼び出し = %v, want 1 回(スペース /teams が失敗)", api.projectTeamsCalls)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "別部門チーム,開発チーム" {
		t.Errorf("所属チーム = %v, want [別部門チーム 開発チーム](据え置き + 取得分の反映)", rows[0].TeamNames)
	}
	if !hasWarning(res, "削除反映は行っていません") {
		t.Errorf("警告 = %v, want 削除反映を行っていない旨", res.Warnings)
	}
}

// TestSyncUsers_AdminPath_DoesNotFetchProjectTeams は管理者パスでは
// スペース /teams で全チームが取れるため、プロジェクト単位のチーム取得を
// 行わない(余計な API 呼び出しをしない)ことを確認する(高 1)。
func TestSyncUsers_AdminPath_DoesNotFetchProjectTeams(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teams = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}

	s := openTempStore(t)
	seedProjects(t, s, store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"})

	e := newTestEngine(t, api, s)
	if _, err := e.SyncUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(api.projectTeamsCalls) != 0 {
		t.Errorf("管理者パスで GetProjectTeams を呼んでいる: %v", api.projectTeamsCalls)
	}
	if n := len(userRows(t, s)[0].TeamNames); n != 1 {
		t.Errorf("所属チーム数 = %d, want 1", n)
	}
}

// TestSyncUsers_DegradedPath_PartialProjectFailure はプロジェクト単位の取得失敗を
// 警告に集約し、取得できたプロジェクトの結果は反映して全体を継続することを確認する。
// 併せて、参加者取得に失敗したプロジェクトがある場合は
// ユーザの削除反映(全置換)を行わないことを確認する(高 2)。
func TestSyncUsers_DegradedPath_PartialProjectFailure(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectAdmins[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsersErr[2] = errors.New("フェイク: プロジェクト 2 の参加者取得に失敗")

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// プロジェクト 2 の旧キャッシュ(取得失敗時は据え置く)
	if err := s.ReplaceProjectUsers(ctx, 2, []store.ProjectUser{{UserID: 1}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatalf("プロジェクト単位の失敗で同期全体が失敗した: %v", err)
	}
	if !hasWarning(res, "ユーザの削除反映は行っていません") {
		t.Errorf("警告 = %v, want 削除反映を行っていない旨", res.Warnings)
	}
	rows := userRows(t, s)
	if len(rows) != 1 || rows[0].UserCode != "admin" {
		t.Fatalf("合成されたユーザ = %+v", rows)
	}
	if strings.Join(rows[0].ProjectKeys, ",") != "EXA,EXB" {
		t.Errorf("参加プロジェクト = %v, want [EXA EXB](取得失敗分は旧キャッシュ据え置き)", rows[0].ProjectKeys)
	}
	if strings.Join(rows[0].AdminProjectKeys, ",") != "EXA" {
		t.Errorf("管理プロジェクト = %v, want [EXA]", rows[0].AdminProjectKeys)
	}
}

// TestSyncUsers_DegradedPath_KeepsUsersOnPartialFailure は縮退パスの部分失敗で
// 「取得に失敗したプロジェクトにのみ所属する既存ユーザ」が消えないことを確認する(高 2)。
func TestSyncUsers_DegradedPath_KeepsUsersOnPartialFailure(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsersErr[2] = errors.New("フェイク: プロジェクト 2 の参加者取得に失敗")

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// ユーザ 9 はプロジェクト 2(取得に失敗する側)にのみ所属する既存ユーザ
	if err := s.ReplaceUsers(ctx, []*store.User{
		{ID: 1, UserCode: "admin", Name: "あ 管理", RoleType: 1},
		{ID: 9, UserCode: "only2", Name: "え 別部門", RoleType: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceProjectUsers(ctx, 2, []store.ProjectUser{{UserID: 9}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := userRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("ユーザ件数 = %d, want 2(取得失敗分のユーザが消えた): %+v", len(rows), rows)
	}
	var found bool
	for _, r := range rows {
		if r.UserCode == "only2" {
			found = true
			if strings.Join(r.ProjectKeys, ",") != "EXB" {
				t.Errorf("既存ユーザの参加プロジェクト = %v, want [EXB]", r.ProjectKeys)
			}
		}
	}
	if !found {
		t.Errorf("取得に失敗したプロジェクトにのみ所属する既存ユーザが消えた: %+v", rows)
	}
	if !hasWarning(res, "ユーザの削除反映は行っていません") {
		t.Errorf("警告 = %v, want 削除反映を行っていない旨", res.Warnings)
	}
}

// TestSyncUsers_DegradedPath_AllProjectsFailErrors は縮退パスで
// すべての対象プロジェクトの参加者取得に失敗した場合、
// 空集合でキャッシュを壊さずエラー(同期失敗)にすることを確認する(高 2)。
func TestSyncUsers_DegradedPath_AllProjectsFailErrors(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.projectUsersErr[1] = errors.New("フェイク: プロジェクト 1 の参加者取得に失敗")
	api.projectUsersErr[2] = errors.New("フェイク: プロジェクト 2 の参加者取得に失敗")

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 1, UserCode: "admin", Name: "あ 管理"}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	if _, err := e.SyncUsers(ctx); err == nil {
		t.Fatal("全プロジェクトの取得失敗でエラーにならなかった")
	}
	if len(userRows(t, s)) != 1 {
		t.Error("全プロジェクト失敗時に users キャッシュが破棄された")
	}
}

// TestSyncUsers_AdminListFailureKeepsProjectUsers は管理者一覧だけ取得に失敗した
// プロジェクトの project_users を据え置く(is_admin を全て false で置換しない)
// ことを確認する(中 1)。
func TestSyncUsers_AdminListFailureKeepsProjectUsers(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1), fakeUser(2, "user1", "い 一般", 2)}
	api.projectAdminsErr[1] = errors.New("フェイク: プロジェクト 1 の管理者取得に失敗")
	api.projectUsers[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2)}
	api.projectAdmins[2] = []backlogclient.User{fakeUser(2, "user1", "い 一般", 2)}

	s := openTempStore(t)
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	ctx := context.Background()
	// プロジェクト 1 の旧キャッシュ(管理者フラグ付き)。管理者一覧が取れない間は据え置く。
	if err := s.ReplaceProjectUsers(ctx, 1, []store.ProjectUser{{UserID: 1, IsAdmin: true}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(res, "管理者一覧") {
		t.Errorf("警告 = %v, want 管理者一覧の取得失敗", res.Warnings)
	}
	rows := userRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("ユーザ件数 = %d, want 2", len(rows))
	}
	admin, general := rows[0], rows[1]
	// プロジェクト 1 は据え置き(管理者フラグが false で上書きされていない)
	if strings.Join(admin.AdminProjectKeys, ",") != "EXA" {
		t.Errorf("管理ユーザの管理プロジェクト = %v, want [EXA](据え置き)", admin.AdminProjectKeys)
	}
	if strings.Join(admin.ProjectKeys, ",") != "EXA" {
		t.Errorf("管理ユーザの参加プロジェクト = %v, want [EXA]", admin.ProjectKeys)
	}
	// プロジェクト 2 は両方取得できたので通常どおり置換される
	if strings.Join(general.ProjectKeys, ",") != "EXB" {
		t.Errorf("一般ユーザの参加プロジェクト = %v, want [EXB](プロジェクト 1 は据え置きのため反映されない)", general.ProjectKeys)
	}
	if strings.Join(general.AdminProjectKeys, ",") != "EXB" {
		t.Errorf("一般ユーザの管理プロジェクト = %v, want [EXB]", general.AdminProjectKeys)
	}
}

// --- プロジェクト未同期時の保護(R1)---------------------------------------

// TestSyncUsers_DegradedPath_UnsyncedProjectsKeepsCache は GET /users が 403 で、
// かつプロジェクトが未同期のときに、既存のユーザ・チームキャッシュを消さずに
// エラーで失敗することを確認する(R1)。
//
// 縮退パスはローカル projects を唯一の入力にするため、未同期による「0 件」を
// 「参加プロジェクトが 1 つも無い」と取り違えると、合成結果(空集合)で
// キャッシュを全置換してしまう。
func TestSyncUsers_DegradedPath_UnsyncedProjectsKeepsCache(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)

	s := openTempStore(t)
	ctx := context.Background()
	// 以前(権限があった頃・プロジェクト同期済みだった頃)のキャッシュ
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 1, UserCode: "admin", Name: "あ 管理"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 10, Name: "開発チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	if _, err := e.SyncUsers(ctx); err == nil {
		t.Fatal("プロジェクト未同期でもエラーにならなかった")
	} else if !strings.Contains(err.Error(), "プロジェクト") {
		t.Errorf("エラー = %v, want プロジェクトの同期を促す内容", err)
	}

	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1(既存キャッシュが破棄された)", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "開発チーム" {
		t.Errorf("所属チーム = %v, want [開発チーム](据え置き)", rows[0].TeamNames)
	}
}

// TestSyncUsers_DegradedPath_SyncedProjectsWithNoProjects は
// プロジェクト同期済みで参加プロジェクトが 0 件(= 正常応答)の場合は、
// 従来どおり空集合で全置換する(閲覧できないユーザ情報を残さない)ことを
// 確認する(R1 の境界)。
func TestSyncUsers_DegradedPath_SyncedProjectsWithNoProjects(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = fmt.Errorf("%w: GET /api/v2/users", backlogclient.ErrPermissionDenied)
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 1, UserCode: "admin", Name: "あ 管理"}}); err != nil {
		t.Fatal(err)
	}
	// プロジェクトは 0 件だが同期は完了している
	markProjectsSynced(t, s)

	e := newTestEngine(t, api, s)
	if _, err := e.SyncUsers(ctx); err != nil {
		t.Fatalf("プロジェクト 0 件(同期済み)でエラーになった: %v", err)
	}
	if rows := userRows(t, s); len(rows) != 0 {
		t.Errorf("ユーザ件数 = %d, want 0(参加プロジェクトが無いので破棄)", len(rows))
	}
}

// TestSyncUsers_SpaceTeamsDenied_UnsyncedProjectsKeepsTeams は
// 「/users 成功・/teams 403」でプロジェクトが未同期のとき、チームキャッシュを
// 空集合で破棄せず据え置くことを確認する(R1)。
// プロジェクト経由の補完ができない状態では「チームが 0 件」を確認できない。
func TestSyncUsers_SpaceTeamsDenied_UnsyncedProjectsKeepsTeams(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 10, Name: "開発チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatalf("ユーザ同期まで失敗した: %v", err)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	if strings.Join(rows[0].TeamNames, ",") != "開発チーム" {
		t.Errorf("所属チーム = %v, want [開発チーム](据え置き)", rows[0].TeamNames)
	}
	if !hasWarning(res, "未同期") {
		t.Errorf("警告 = %v, want プロジェクトが未同期である旨", res.Warnings)
	}
}

// TestSyncUsers_SpaceTeamsDenied_UnsyncedProjectsWithStaleRowsKeepsTeams は
// 「プロジェクト行はローカルに残っているが sync_state は未同期」の状態で
// /teams が 403 のとき、チーム情報を一切更新せず据え置くことを確認する(R1)。
//
// 残っているプロジェクト行が現在の参加状況を表している保証はないため、
// そこから合成したチームを反映すると古い情報で上書きしてしまう。
// 「据え置く」という警告どおり、teams / team_members は不変にする。
func TestSyncUsers_SpaceTeamsDenied_UnsyncedProjectsWithStaleRowsKeepsTeams(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teamsErr = fmt.Errorf("%w: GET /api/v2/teams", backlogclient.ErrPermissionDenied)
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	// プロジェクト経由では取得できるが、未同期のため反映してはならない
	api.projectTeams[1] = []backlogclient.Team{fakeTeam(10, "開発チーム", 1)}

	s := openTempStore(t)
	ctx := context.Background()
	// 同期状態は記録しない(プロジェクト行だけが前回の同期から残っている)
	seedProjectsOnly(t, s, store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"})
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 20, Name: "別部門チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatalf("ユーザ同期まで失敗した: %v", err)
	}
	rows := userRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("ユーザ件数 = %d, want 1", len(rows))
	}
	// teams / team_members とも不変(合成したチームを混ぜない)
	if strings.Join(rows[0].TeamNames, ",") != "別部門チーム" {
		t.Errorf("所属チーム = %v, want [別部門チーム](据え置き)", rows[0].TeamNames)
	}
	if !hasWarning(res, "未同期") {
		t.Errorf("警告 = %v, want プロジェクトが未同期である旨", res.Warnings)
	}
}

// hasWarning は警告に部分文字列を含むものがあるかを返す。
func hasWarning(res *Result, substr string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestSyncUsers_FailsOnNonPermissionError は 403 以外のユーザ一覧取得失敗を
// 縮退と取り違えず、エラーとして返すことを確認する
// (通信エラーを縮退扱いすると users キャッシュを不完全な集合で全置換してしまう)。
func TestSyncUsers_FailsOnNonPermissionError(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = errors.New("フェイク: 通信エラー")
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 1, UserCode: "admin", Name: "あ 管理"}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	if _, err := e.SyncUsers(ctx); err == nil {
		t.Fatal("通信エラーでエラーにならなかった")
	}
	// 既存キャッシュは壊さない
	if len(userRows(t, s)) != 1 {
		t.Error("取得失敗時に users キャッシュが破棄された")
	}
}

// TestSyncUsers_TeamsTransientErrorKeepsCache は teams の一時的な取得失敗
// (403 以外)ではキャッシュを据え置き、警告のみ付けることを確認する。
func TestSyncUsers_TeamsTransientErrorKeepsCache(t *testing.T) {
	api := newFakeAPI()
	api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.teamsErr = errors.New("フェイク: 通信エラー")

	s := openTempStore(t)
	ctx := context.Background()
	if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 10, Name: "開発チーム", MemberIDs: []int64{1}}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	res, err := e.SyncUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Error("チーム取得失敗の警告が付いていない")
	}
	rows := userRows(t, s)
	if len(rows) != 1 || len(rows[0].TeamNames) != 1 {
		t.Errorf("所属チーム = %+v, want 据え置き", rows)
	}
}

// --- 反映の原子性(R7)-----------------------------------------------------

// TestSyncUsers_ApplyIsAtomic は DB 反映の途中で失敗した場合に、
// users / teams / team_members / project_users / sync_state のいずれも
// 更新されず、旧世代が丸ごと残ることを確認する(R7)。
// 反映を段階ごとに別コミットしていると、ここで新旧世代が混在する
// (例: users だけ新しく、参加関係は旧世代のまま)。
func TestSyncUsers_ApplyIsAtomic(t *testing.T) {
	stages := []string{applyStageUsers, applyStageTeams, applyStageProjectUsers, applyStageSyncState}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			api := newFakeAPI()
			api.users = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
			api.teams = []backlogclient.Team{{ID: 1, Name: "新チーム", MemberIDs: []int64{1}}}
			api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
			api.projectAdmins[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}

			s := openTempStore(t)
			ctx := context.Background()
			seedProjects(t, s, store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"})

			// 旧世代のキャッシュ(反映が失敗したら丸ごと残るべきもの)
			if err := s.ReplaceUsers(ctx, []*store.User{{ID: 99, UserCode: "old", Name: "旧 ユーザ"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.ReplaceTeams(ctx, []*store.Team{{ID: 999, Name: "旧チーム", MemberIDs: []int64{99}}}); err != nil {
				t.Fatal(err)
			}
			if err := s.ReplaceProjectUsers(ctx, 1, []store.ProjectUser{{UserID: 99}}); err != nil {
				t.Fatal(err)
			}

			e := newTestEngine(t, api, s)
			e.applyUsersStageHook = func(got string) error {
				if got == stage {
					return fmt.Errorf("フェイク: %s の反映に失敗", stage)
				}
				return nil
			}
			if _, err := e.SyncUsers(ctx); err == nil {
				t.Fatal("反映失敗でもエラーにならなかった")
			}

			rows := userRows(t, s)
			if len(rows) != 1 || rows[0].UserCode != "old" {
				t.Fatalf("users = %+v, want 旧世代のみ", rows)
			}
			if len(rows[0].TeamNames) != 1 || rows[0].TeamNames[0] != "旧チーム" {
				t.Errorf("teams / team_members = %v, want [旧チーム]", rows[0].TeamNames)
			}
			if len(rows[0].ProjectKeys) != 1 || rows[0].ProjectKeys[0] != "EXA" {
				t.Errorf("project_users = %v, want [EXA]", rows[0].ProjectKeys)
			}
			st, err := s.GetSyncState(ctx, store.DataKindUsers, store.ProjectScopeAll)
			if err != nil {
				t.Fatal(err)
			}
			if st != nil && st.LastSyncedAt != "" {
				t.Errorf("同期状態 = %+v, want 未更新", st)
			}
		})
	}
}

// TestSyncUsers_DegradedPartialApplyIsAtomic は縮退パスの部分失敗
// (ユーザは UPSERT に留め、削除反映を行わない経路。高 2)でも
// 反映が単一トランザクションであることを確認する(R7)。
// UPSERT だけ先にコミットされると、同期に失敗したのにユーザ情報だけ
// 新しくなった中途半端なキャッシュが残る。
func TestSyncUsers_DegradedPartialApplyIsAtomic(t *testing.T) {
	api := newFakeAPI()
	api.usersErr = backlogclient.ErrPermissionDenied
	api.teamsErr = backlogclient.ErrPermissionDenied
	// プロジェクト 1 は取得成功、プロジェクト 2 は失敗 = 部分失敗
	api.projectUsers[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectAdmins[1] = []backlogclient.User{fakeUser(1, "admin", "あ 管理", 1)}
	api.projectUsersErr[2] = errors.New("フェイク: 取得失敗")

	s := openTempStore(t)
	ctx := context.Background()
	seedProjects(t, s,
		store.Project{ID: 1, ProjectKey: "EXA", Name: "検証用 A"},
		store.Project{ID: 2, ProjectKey: "EXB", Name: "検証用 B"})
	if err := s.ReplaceUsers(ctx, []*store.User{{ID: 99, UserCode: "old", Name: "旧 ユーザ"}}); err != nil {
		t.Fatal(err)
	}

	e := newTestEngine(t, api, s)
	e.applyUsersStageHook = func(stage string) error {
		if stage == applyStageProjectUsers {
			return errors.New("フェイク: 参加関係の反映に失敗")
		}
		return nil
	}
	if _, err := e.SyncUsers(ctx); err == nil {
		t.Fatal("反映失敗でもエラーにならなかった")
	}
	rows := userRows(t, s)
	if len(rows) != 1 || rows[0].UserCode != "old" {
		t.Errorf("users = %+v, want 旧世代のみ(UPSERT もロールバックされる)", rows)
	}
}
