package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kenzo0107/backlog"
	"github.com/zalando/go-keyring"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/config"
	"backlog-assistant/internal/secret"
	"backlog-assistant/internal/store"
)

// fakeClock はテスト用の手動クロック。
type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// fakeConnector は connector のフェイク実装。
type fakeConnector struct {
	info      *backlogclient.ConnectionInfo
	testErr   error
	usersErr  error
	teamsErr  error
	initErr   error
	initCalls int

	// 同期系(syncpkg.API)の応答
	projects   []backlogclient.Project
	issues     []backlogclient.Issue
	activities []backlogclient.Activity

	// ユーザ・チーム同期(SyncUsers)の応答。
	// rawUsers / pagedTeams は自前 HTTP 実装(backlogclient.GetUsersRaw 等)側で、
	// GetUsers / GetTeams(ライブラリ経由・権限判定用)とは別物。
	rawUsers      []backlogclient.User
	rawUsersErr   error
	pagedTeams    []backlogclient.Team
	pagedTeamsErr error
	projectUsers  map[int64][]backlogclient.User
	projectAdmins map[int64][]backlogclient.User
	// projectTeams は縮退パスでのプロジェクト単位のチーム取得(高 1)。
	projectTeams map[int64][]backlogclient.Team

	// 同期中の並行操作を検証するためのブロック機構(高 2)。
	// issuesEntered が非 nil のとき、最初の GetIssues でそれを閉じてから
	// issuesRelease が閉じられるまで待つ。
	issuesEntered chan struct{}
	issuesRelease chan struct{}
	enterOnce     sync.Once

	// usersEntered が非 nil のとき、最初の GetUsers でそれを閉じる。
	// プロファイル削除中に旧クライアントで API が呼ばれないことの検証に使う(中 1)。
	usersEntered chan struct{}
	usersOnce    sync.Once

	// 一括更新・追加(bulk.API)の応答と呼び出し記録。
	issueTypes []backlogclient.IssueType
	priorities []backlogclient.Priority
	statuses   []backlogclient.Status
	created    []backlogclient.IssueCreate
	updated    []string
	writeErr   error
}

func (f *fakeConnector) GetProjects(ctx context.Context) ([]backlogclient.Project, error) {
	return f.projects, nil
}

func (f *fakeConnector) GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error) {
	if f.issuesEntered != nil {
		f.enterOnce.Do(func() { close(f.issuesEntered) })
		<-f.issuesRelease
	}
	if q.Offset > 0 {
		return nil, nil // 1 ページで終わり
	}
	var out []backlogclient.Issue
	for _, i := range f.issues {
		for _, pid := range q.ProjectIDs {
			if i.ProjectID == pid {
				out = append(out, i)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeConnector) GetIssuesCount(ctx context.Context, q backlogclient.IssueQuery) (int, error) {
	issues, err := f.GetIssues(ctx, q)
	return len(issues), err
}

func (f *fakeConnector) GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error) {
	for _, i := range f.issues {
		if i.IssueKey == issueIDOrKey {
			issue := i
			return &issue, nil
		}
	}
	return nil, backlogclient.ErrNotFound
}

func (f *fakeConnector) GetSpaceActivities(ctx context.Context, q backlogclient.ActivityQuery) ([]backlogclient.Activity, error) {
	return f.activities, nil
}

func (f *fakeConnector) GetUsersRaw(ctx context.Context) ([]backlogclient.User, error) {
	if f.rawUsersErr != nil {
		return nil, f.rawUsersErr
	}
	return f.rawUsers, nil
}

func (f *fakeConnector) GetTeamsPaged(ctx context.Context, offset, count int) ([]backlogclient.Team, error) {
	if f.pagedTeamsErr != nil {
		return nil, f.pagedTeamsErr
	}
	if offset >= len(f.pagedTeams) {
		return nil, nil
	}
	end := offset + count
	if end > len(f.pagedTeams) {
		end = len(f.pagedTeams)
	}
	return f.pagedTeams[offset:end], nil
}

func (f *fakeConnector) GetProjectUsers(ctx context.Context, projectID int64) ([]backlogclient.User, error) {
	return f.projectUsers[projectID], nil
}

func (f *fakeConnector) GetProjectAdministrators(ctx context.Context, projectID int64) ([]backlogclient.User, error) {
	return f.projectAdmins[projectID], nil
}

func (f *fakeConnector) GetProjectTeams(ctx context.Context, projectID int64) ([]backlogclient.Team, error) {
	return f.projectTeams[projectID], nil
}

// 一括更新・追加(bulk.API)のフェイク実装。

func (f *fakeConnector) CreateIssue(ctx context.Context, in backlogclient.IssueCreate) (*backlogclient.Issue, error) {
	f.created = append(f.created, in)
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	return &backlogclient.Issue{ID: 900 + int64(len(f.created)), IssueKey: "EXA-900", ProjectID: in.ProjectID}, nil
}

func (f *fakeConnector) UpdateIssue(ctx context.Context, issueIDOrKey string, in backlogclient.IssueUpdate) (*backlogclient.Issue, error) {
	f.updated = append(f.updated, issueIDOrKey)
	if f.writeErr != nil {
		return nil, f.writeErr
	}
	return &backlogclient.Issue{IssueKey: issueIDOrKey}, nil
}

func (f *fakeConnector) GetProjectIssueTypes(ctx context.Context, projectID int64) ([]backlogclient.IssueType, error) {
	return f.issueTypes, nil
}

func (f *fakeConnector) GetPriorities(ctx context.Context) ([]backlogclient.Priority, error) {
	return f.priorities, nil
}

func (f *fakeConnector) GetProjectStatuses(ctx context.Context, projectID int64) ([]backlogclient.Status, error) {
	return f.statuses, nil
}

func (f *fakeConnector) TestConnection(ctx context.Context) (*backlogclient.ConnectionInfo, error) {
	if f.testErr != nil {
		return nil, f.testErr
	}
	return f.info, nil
}

func (f *fakeConnector) GetUsers(ctx context.Context) ([]*backlog.User, error) {
	if f.usersEntered != nil {
		f.usersOnce.Do(func() { close(f.usersEntered) })
	}
	return nil, f.usersErr
}

func (f *fakeConnector) GetTeams(ctx context.Context) ([]*backlog.Team, error) {
	return nil, f.teamsErr
}

func (f *fakeConnector) InitRateLimit(ctx context.Context) error {
	f.initCalls++
	return f.initErr
}

// newTestService はキーチェーンをモック化し、一時ディレクトリの config を使う
// ProfileService を返す。newClient は fake を返し、生成回数を数える。
func newTestService(t *testing.T, fake *fakeConnector) (*ProfileService, string, *int) {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	s := NewProfileService(config.NewManagerAt(dir))
	newClientCalls := new(int)
	s.newClient = func(spaceURL, apiKey string) (connector, error) {
		*newClientCalls++
		return fake, nil
	}
	s.removeDB = func(host string, userID int) error { return nil }
	// ローカル DB は一時ディレクトリに作る(既定の os.UserConfigDir を汚さない)
	dataDir := t.TempDir()
	s.dbPathFor = func(host string, userID int) (string, error) {
		return store.DBPathIn(dataDir, host, userID), nil
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir, newClientCalls
}

func testInfo() *backlogclient.ConnectionInfo {
	return &backlogclient.ConnectionInfo{UserID: 42, UserCode: "user", Name: "テスト 太郎", RoleType: 1}
}

// makeConfigDirReadOnly は config 保存を失敗させるためにディレクトリを読み取り専用にする。
func makeConfigDirReadOnly(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root では権限による書き込み失敗を再現できない")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

func TestSaveProfile_SetsLastUserID(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)

	res, err := s.SaveProfile(context.Background(), "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Profile.LastUserID != 42 {
		t.Errorf("LastUserID = %d, want 42", res.Profile.LastUserID)
	}
	if k, err := secret.Get(res.Profile.ID); err != nil || k != "KEY-1" {
		t.Errorf("キーチェーンの内容 = %q, %v, want KEY-1", k, err)
	}
}

func TestSaveProfile_ConfigFailureRestoresOldKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, dir, _ := newTestService(t, fake)
	ctx := context.Background()

	// 既存プロファイル(旧キー)を作る
	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "OLD-KEY")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// config 保存を失敗させ、新キーで上書き保存を試みる
	makeConfigDirReadOnly(t, dir)
	_, err = s.SaveProfile(ctx, id, "検証用", "https://example.backlog.jp", "NEW-KEY")
	if err == nil {
		t.Fatal("config 保存失敗時に SaveProfile が成功してしまった")
	}

	// 補償処理: キーチェーンは旧キーへ復元されている
	k, gerr := secret.Get(id)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if k != "OLD-KEY" {
		t.Errorf("補償後のキー = %q, want OLD-KEY(旧値へ復元)", k)
	}
}

func TestSaveProfile_ConfigFailureDeletesNewKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, dir, _ := newTestService(t, fake)
	ctx := context.Background()

	// 既存プロファイル(config にはあるがキーチェーン未保存)を用意する
	id := "profile-without-key"
	if err := s.cfg.Upsert(config.Profile{ID: id, Name: "n", SpaceURL: "https://example.backlog.jp"}); err != nil {
		t.Fatal(err)
	}

	makeConfigDirReadOnly(t, dir)
	_, err := s.SaveProfile(ctx, id, "n", "https://example.backlog.jp", "NEW-KEY")
	if err == nil {
		t.Fatal("config 保存失敗時に SaveProfile が成功してしまった")
	}

	// 補償処理: 退避すべき旧キーが無かったので、新キーは削除されている
	if _, gerr := secret.Get(id); !errors.Is(gerr, secret.ErrNotFound) {
		t.Errorf("補償後の secret.Get のエラー = %v, want ErrNotFound(新キー削除)", gerr)
	}
}

func TestDeleteProfile_DBFailureKeepsProfile(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// DB 削除を失敗させる
	dbErr := errors.New("DB ファイルを削除できません")
	s.removeDB = func(host string, userID int) error { return dbErr }

	if err := s.DeleteProfile(id, true); !errors.Is(err, dbErr) {
		t.Fatalf("DeleteProfile のエラー = %v, want %v", err, dbErr)
	}
	// プロファイルとキーは残っている(再試行可能)
	if _, err := s.cfg.Get(id); err != nil {
		t.Errorf("DB 削除失敗後にプロファイルが消えている: %v", err)
	}
	if k, err := secret.Get(id); err != nil || k != "KEY-1" {
		t.Errorf("DB 削除失敗後にキーが消えている: %q, %v", k, err)
	}
}

func TestDeleteProfile_RemovesOnlyOwnDatabase(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	var gotHost string
	var gotUserID int
	calls := 0
	s.removeDB = func(host string, userID int) error {
		calls++
		gotHost = host
		gotUserID = userID
		return nil
	}

	if err := s.DeleteProfile(id, true); err != nil {
		t.Fatal(err)
	}
	// 当該プロファイルの DB(ホスト × LastUserID)のみを削除対象にする
	if calls != 1 || gotHost != "example.backlog.jp" || gotUserID != 42 {
		t.Errorf("removeDB 呼び出し = %d 回 (%q, %d), want 1 回 (example.backlog.jp, 42)", calls, gotHost, gotUserID)
	}
	if _, err := s.cfg.Get(id); !errors.Is(err, config.ErrProfileNotFound) {
		t.Errorf("削除後の Get のエラー = %v, want ErrProfileNotFound", err)
	}
	if _, err := secret.Get(id); !errors.Is(err, secret.ErrNotFound) {
		t.Errorf("削除後のキー取得エラー = %v, want ErrNotFound", err)
	}
}

func TestDeleteProfile_SkipsDBWhenNotRequested(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.removeDB = func(host string, userID int) error { calls++; return nil }
	if err := s.DeleteProfile(res.Profile.ID, false); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("deleteDB=false なのに removeDB が %d 回呼ばれた", calls)
	}
}

func TestGetPermissionStatus_Matrix(t *testing.T) {
	denied := backlogclient.ErrPermissionDenied
	cases := []struct {
		name               string
		usersErr, teamsErr error
		wantAdmin          bool
		wantDegraded       bool
	}{
		{"両方OK", nil, nil, true, false},
		{"users のみ 403", denied, nil, false, true},
		{"teams のみ 403", nil, denied, false, true},
		{"両方 403", denied, denied, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeConnector{info: testInfo(), usersErr: c.usersErr, teamsErr: c.teamsErr}
			s, _, _ := newTestService(t, fake)
			ctx := context.Background()
			res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
			if err != nil {
				t.Fatal(err)
			}
			st, err := s.GetPermissionStatus(ctx, res.Profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			if st.AdminAvailable != c.wantAdmin || st.Degraded != c.wantDegraded {
				t.Errorf("status = {admin:%v degraded:%v}, want {admin:%v degraded:%v}",
					st.AdminAvailable, st.Degraded, c.wantAdmin, c.wantDegraded)
			}
			if st.Message == "" {
				t.Error("Message が空")
			}
		})
	}
}

func TestGetPermissionStatus_NetworkErrorIsReturned(t *testing.T) {
	netErr := errors.New("接続できません")
	fake := &fakeConnector{info: testInfo(), usersErr: netErr}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()
	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPermissionStatus(ctx, res.Profile.ID); !errors.Is(err, netErr) {
		t.Errorf("エラー = %v, want %v(403 以外は権限判定しない)", err, netErr)
	}
}

func TestClientCache_ReusedAndInitializedOnce(t *testing.T) {
	// InitRateLimit の失敗はベストエフォート(エラーにしない)ことも同時に確認する
	fake := &fakeConnector{info: testInfo(), initErr: errors.New("rateLimit 取得失敗")}
	s, _, newClientCalls := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID
	callsAfterSave := *newClientCalls // SaveProfile は接続テスト用に 1 回生成する

	// 2 回呼んでもクライアントは 1 回だけ生成され、InitRateLimit も 1 回だけ呼ばれる
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := *newClientCalls - callsAfterSave; got != 1 {
		t.Errorf("キャッシュ後の newClient 生成回数 = %d, want 1", got)
	}
	if fake.initCalls != 1 {
		t.Errorf("InitRateLimit 呼び出し回数 = %d, want 1", fake.initCalls)
	}

	// 保存(キー変更の可能性)でキャッシュが無効化され、再生成される
	if _, err := s.SaveProfile(ctx, id, "検証用", "https://example.backlog.jp", "KEY-2"); err != nil {
		t.Fatal(err)
	}
	before := *newClientCalls
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := *newClientCalls - before; got != 1 {
		t.Errorf("保存後の newClient 再生成回数 = %d, want 1(キャッシュ無効化)", got)
	}
}

// ---------------------------------------------------------------------------
// 高 1: キーフィンガープリント照合
// ---------------------------------------------------------------------------

func TestSaveProfile_StoresKeyFingerprint(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)

	res, err := s.SaveProfile(context.Background(), "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	want := keyFingerprint("KEY-1")
	if len(want) != 16 {
		t.Fatalf("フィンガープリント長 = %d, want 16", len(want))
	}
	if res.Profile.KeyFingerprint != want {
		t.Errorf("KeyFingerprint = %q, want %q", res.Profile.KeyFingerprint, want)
	}
	// config.json にも永続化されている
	saved, err := s.cfg.Get(res.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.KeyFingerprint != want {
		t.Errorf("保存された KeyFingerprint = %q, want %q", saved.KeyFingerprint, want)
	}
}

func TestClientForProfile_RejectsMismatchedKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// クラッシュ等で「config と一致しないキー」がキーチェーンに残った状態を再現する
	if err := secret.Save(id, "STALE-KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPermissionStatus(ctx, id); !errors.Is(err, errKeyMismatch) {
		t.Errorf("エラー = %v, want errKeyMismatch(不一致キーで API を呼ばない)", err)
	}
}

func TestClientForProfile_LegacyProfileSkipsFingerprintCheck(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	// 旧バージョンで保存されたプロファイル(フィンガープリント無し)は照合をスキップする
	id := "legacy-profile"
	if err := s.cfg.Upsert(config.Profile{ID: id, Name: "旧", SpaceURL: "https://example.backlog.jp"}); err != nil {
		t.Fatal(err)
	}
	if err := secret.Save(id, "LEGACY-KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Errorf("後方互換: フィンガープリント空なら照合スキップのはずがエラー: %v", err)
	}
}

func TestSaveProfile_KeyReuseRejectsMismatchedKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID
	if err := secret.Save(id, "STALE-KEY"); err != nil {
		t.Fatal(err)
	}
	// apiKey 空(既存キー流用)の経路でも不一致を検知して中断する
	if _, err := s.SaveProfile(ctx, id, "検証用", "https://example.backlog.jp", ""); !errors.Is(err, errKeyMismatch) {
		t.Errorf("エラー = %v, want errKeyMismatch(不一致キーの流用を拒否)", err)
	}
}

func TestTestConnectionForProfile_RejectsMismatchedKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID
	if err := secret.Save(id, "STALE-KEY"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TestConnectionForProfile(ctx, id, "https://example.backlog.jp", ""); !errors.Is(err, errKeyMismatch) {
		t.Errorf("エラー = %v, want errKeyMismatch(空キー経路の照合)", err)
	}
}

// ---------------------------------------------------------------------------
// 高 2: DeleteProfile の補償処理
// ---------------------------------------------------------------------------

func TestDeleteProfile_ConfigFailureRestoresKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, dir, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// config の削除(保存)を失敗させる
	makeConfigDirReadOnly(t, dir)
	if err := s.DeleteProfile(id, false); err == nil {
		t.Fatal("config 削除失敗時に DeleteProfile が成功してしまった")
	}
	// 補償処理: プロファイルは残っており、キーも復元されている
	if _, err := s.cfg.Get(id); err != nil {
		t.Errorf("config 削除失敗後にプロファイルが消えている: %v", err)
	}
	if k, err := secret.Get(id); err != nil || k != "KEY-1" {
		t.Errorf("補償後のキー = %q, %v, want KEY-1(キーチェーンへ復元)", k, err)
	}
}

func TestDeleteProfile_MissingKeyProceedsWithoutBackup(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)

	// キーチェーンにキーが無いプロファイル(not-found は退避なしで続行)
	id := "profile-without-key"
	if err := s.cfg.Upsert(config.Profile{ID: id, Name: "n", SpaceURL: "https://example.backlog.jp"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(id, false); err != nil {
		t.Fatalf("キー無しプロファイルの削除に失敗: %v", err)
	}
	if _, err := s.cfg.Get(id); !errors.Is(err, config.ErrProfileNotFound) {
		t.Errorf("削除後の Get のエラー = %v, want ErrProfileNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// 中 1: InitRateLimit 失敗時の再試行
// ---------------------------------------------------------------------------

func TestClientForProfile_RetriesRateLimitInit(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_000_000, 0)}
	fake := &fakeConnector{info: testInfo(), initErr: errors.New("rateLimit 取得失敗")}
	s, _, _ := newTestService(t, fake)
	s.now = clock.now
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// 初回生成時に 1 回試行して失敗する(エラーにはしない)
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 1 {
		t.Fatalf("初回の InitRateLimit 呼び出し回数 = %d, want 1", fake.initCalls)
	}
	// 再試行間隔(30 秒)内は再試行しない
	clock.advance(29 * time.Second)
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 1 {
		t.Errorf("間隔内に再試行された: initCalls = %d, want 1", fake.initCalls)
	}
	// 間隔経過後は再試行する(まだ失敗)
	clock.advance(2 * time.Second)
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 2 {
		t.Errorf("間隔経過後に再試行されない: initCalls = %d, want 2", fake.initCalls)
	}
	// 失敗直後は次の間隔まで再試行しない
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 2 {
		t.Errorf("失敗直後に再試行された: initCalls = %d, want 2", fake.initCalls)
	}
	// 間隔経過 + 成功で初期化完了
	clock.advance(31 * time.Second)
	fake.initErr = nil
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 3 {
		t.Errorf("成功時の initCalls = %d, want 3", fake.initCalls)
	}
	// 成功後はもう再試行しない
	clock.advance(time.Hour)
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	if fake.initCalls != 3 {
		t.Errorf("初期化完了後に再試行された: initCalls = %d, want 3", fake.initCalls)
	}
}

// ---------------------------------------------------------------------------
// 中 1(2 回目レビュー): 読み取り系メソッドのライフサイクルロック
// ---------------------------------------------------------------------------

// TestGetPermissionStatus_WaitsForProfileDeletion は、DeleteProfile の実行中に
// GetPermissionStatus が旧プロファイルのキー・キャッシュクライアントで API を
// 呼ばないこと(= 入口で profileMu.RLock を取ること)を検証する。
// 削除完了後は対象プロファイルが存在しないためエラーになる。
func TestGetPermissionStatus_WaitsForProfileDeletion(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	// 旧キーのクライアントをキャッシュへ載せておく(削除中に使い回せる状態を作る)
	if _, err := s.GetPermissionStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	// 以降の GetUsers 呼び出しを検知できるようにする
	fake.usersEntered = make(chan struct{})

	// DeleteProfile を profileMu.Lock 保持中(DB 削除中)で停止させる
	entered := make(chan struct{})
	release := make(chan struct{})
	s.removeDB = func(host string, userID int) error {
		close(entered)
		<-release
		return nil
	}
	delDone := make(chan error, 1)
	go func() { delDone <- s.DeleteProfile(id, true) }()
	<-entered

	permDone := make(chan error, 1)
	go func() {
		_, perr := s.GetPermissionStatus(ctx, id)
		permDone <- perr
	}()

	select {
	case <-fake.usersEntered:
		t.Error("削除処理中に旧プロファイルのクライアントで API が呼ばれた")
	case <-permDone:
		t.Error("削除処理中に GetPermissionStatus が完了した(排他されていない)")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)

	if err := <-delDone; err != nil {
		t.Fatalf("DeleteProfile が失敗した: %v", err)
	}
	select {
	case perr := <-permDone:
		if perr == nil {
			t.Error("削除完了後の GetPermissionStatus がエラーにならなかった")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("削除完了後も GetPermissionStatus が終わらない")
	}
}

// ---------------------------------------------------------------------------
// 中 2: DB の共有参照と孤児化
// ---------------------------------------------------------------------------

func TestDeleteProfile_SkipsDBSharedByOtherProfile(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	// 同一 (host, LastUserID) を参照するプロファイルを 2 つ作る
	res1, err := s.SaveProfile(ctx, "", "プロファイル A", "https://example.backlog.jp", "KEY-A")
	if err != nil {
		t.Fatal(err)
	}
	res2, err := s.SaveProfile(ctx, "", "プロファイル B", "https://example.backlog.jp", "KEY-B")
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	s.removeDB = func(host string, userID int) error { calls++; return nil }

	// A の削除では B が同じ DB を参照しているため DB 削除をスキップする(中 2(a))
	if err := s.DeleteProfile(res1.Profile.ID, true); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("共有 DB が削除された: removeDB %d 回, want 0", calls)
	}
	// 最後の参照者 B の削除では DB を削除する
	if err := s.DeleteProfile(res2.Profile.ID, true); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("最後の参照者の削除で removeDB %d 回, want 1", calls)
	}
}

func TestSaveProfile_RemovesOrphanedDBOnUserChange(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	res, err := s.SaveProfile(ctx, "", "検証用", "https://example.backlog.jp", "KEY-1")
	if err != nil {
		t.Fatal(err)
	}
	id := res.Profile.ID

	var gotHost string
	var gotUserID int
	calls := 0
	s.removeDB = func(host string, userID int) error {
		calls++
		gotHost = host
		gotUserID = userID
		return nil
	}

	// API キー変更で接続ユーザが 42 → 99 に変わった
	fake.info = &backlogclient.ConnectionInfo{UserID: 99, UserCode: "user2", Name: "テスト 次郎", RoleType: 1}
	res2, err := s.SaveProfile(ctx, id, "検証用", "https://example.backlog.jp", "KEY-2")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Profile.LastUserID != 99 {
		t.Fatalf("LastUserID = %d, want 99", res2.Profile.LastUserID)
	}
	// 旧ユーザ(42)の DB は誰からも参照されなくなったため削除される(中 2(b))
	if calls != 1 || gotHost != "example.backlog.jp" || gotUserID != 42 {
		t.Errorf("removeDB 呼び出し = %d 回 (%q, %d), want 1 回 (example.backlog.jp, 42)", calls, gotHost, gotUserID)
	}
}

func TestSaveProfile_KeepsSharedDBOnUserChange(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	ctx := context.Background()

	// 同一 (host, LastUserID=42) を参照するプロファイルを 2 つ作る
	res1, err := s.SaveProfile(ctx, "", "プロファイル A", "https://example.backlog.jp", "KEY-A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveProfile(ctx, "", "プロファイル B", "https://example.backlog.jp", "KEY-B"); err != nil {
		t.Fatal(err)
	}

	calls := 0
	s.removeDB = func(host string, userID int) error { calls++; return nil }

	// A のユーザが変わっても、B が旧 DB を参照しているため削除しない(中 2(b))
	fake.info = &backlogclient.ConnectionInfo{UserID: 99, UserCode: "user2", Name: "テスト 次郎", RoleType: 1}
	if _, err := s.SaveProfile(ctx, res1.Profile.ID, "プロファイル A", "https://example.backlog.jp", "KEY-A2"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("共有 DB が削除された: removeDB %d 回, want 0", calls)
	}
}

// コピー&ペースト由来の前後空白・改行が API キーに混入しても、送信・保存前に
// トリムされること(実環境で「ブラウザでは成功するのにアプリでは 401」となった
// 白 → 401 バグの原因)。
func TestSaveProfile_TrimsAPIKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	var gotKey string
	inner := s.newClient
	s.newClient = func(spaceURL, apiKey string) (connector, error) {
		gotKey = apiKey
		return inner(spaceURL, apiKey)
	}

	res, err := s.SaveProfile(context.Background(), "", "検証", "https://example.backlog.jp", "  secret-key\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" {
		t.Errorf("接続テストに渡ったキー = %q, want %q", gotKey, "secret-key")
	}
	stored, err := secret.Get(res.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "secret-key" {
		t.Errorf("キーチェーン保存値 = %q, want %q", stored, "secret-key")
	}
}

// TestConnectionForProfile も同様にトリムして送信すること。
func TestTestConnectionForProfile_TrimsAPIKey(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	var gotKey string
	inner := s.newClient
	s.newClient = func(spaceURL, apiKey string) (connector, error) {
		gotKey = apiKey
		return inner(spaceURL, apiKey)
	}

	if _, err := s.TestConnectionForProfile(context.Background(), "", "https://example.backlog.jp", "\tsecret-key \n"); err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" {
		t.Errorf("接続テストに渡ったキー = %q, want %q", gotKey, "secret-key")
	}
}

// キー内部の空白・制御文字(コピー範囲の誤り確実)は送信前に明確なエラーに
// すること(サーバの 401 より原因が分かりやすい)。公式仕様はキーの文字種を
// 規定していないため、非 ASCII や記号は拒否しない(正当なキーを弾かない)。
func TestSaveProfile_RejectsInvalidAPIKeyCharacters(t *testing.T) {
	fake := &fakeConnector{info: testInfo()}
	s, _, _ := newTestService(t, fake)
	for _, key := range []string{"secret key", "secret\tkey", "secret\nkey", "secret\u00a0key", "secret\u0085key"} {
		if _, err := s.SaveProfile(context.Background(), "", "検証", "https://example.backlog.jp", key); err == nil {
			t.Errorf("key=%q: エラーになるべき", key)
		}
	}
	// 正常キーおよび文字種を断定できないキー(記号・通常の非 ASCII)は通す(サーバの判定に委ねる)
	for i, key := range []string{"abcDEF0123", "abc=def", "abc-def_ghi", "abcキー123"} {
		if _, err := s.SaveProfile(context.Background(), "", fmt.Sprintf("検証%d", i), "https://example.backlog.jp", key); err != nil {
			t.Errorf("key=%q: エラーになるべきでない: %v", key, err)
		}
	}
}
