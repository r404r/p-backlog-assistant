package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	stdsync "sync"
	"time"

	"backlog-assistant/internal/backlogclient"
)

// fakeAPI は Backlog API のフェイク。offset ページング・updatedSince 絞り込み・
// activities の minId 消化を実 API と同じ挙動で再現する。
//
// 同期エンジンが取得を並行実行する(課題ページのパイプライン・プロジェクト単位の
// 有界並列)ため、呼び出し記録は mu で保護する。応答データ側のフィールドは
// テストのセットアップ時にのみ書き込み、goroutine 起動後は読み取り専用とする。
type fakeAPI struct {
	// mu は呼び出し記録・同時実行数カウンタを保護する。
	mu stdsync.Mutex
	// projectInFlight / projectMaxInFlight はプロジェクト単位取得の同時実行数
	//(有界並列の検証用)。
	projectInFlight    int
	projectMaxInFlight int
	// issuesDelay / projectCallDelay は応答遅延の注入(オーバーラップの検証用)。
	issuesDelay      time.Duration
	projectCallDelay time.Duration

	issues     []backlogclient.Issue // created 昇順で保持する
	activities []backlogclient.Activity
	projects   []backlogclient.Project

	// deletedKeys / deletedIDs に含まれる課題は GET /issues/:key が 404 を返す。
	deletedKeys map[string]bool
	deletedIDs  map[int64]bool
	// getIssueOnly は一覧には現れないが GET /issues/:key では取得できる課題
	//(offset ページング中の並行追加・削除で取り逃したケースの再現)。
	// 値には GET が返す課題(一覧より新しい内容を持ちうる)を入れる。
	getIssueOnly map[string]backlogclient.Issue

	// 呼び出し記録(検証用)
	issueQueries    []backlogclient.IssueQuery
	activityQueries []backlogclient.ActivityQuery
	getIssueCalls   []string

	// ユーザ・チーム同期(SyncUsers)用の応答。
	users         []backlogclient.User
	teams         []backlogclient.Team
	projectUsers  map[int64][]backlogclient.User
	projectAdmins map[int64][]backlogclient.User
	// projectTeams はプロジェクト単位のチーム(GET /projects/:id/teams)。
	projectTeams map[int64][]backlogclient.Team

	// ユーザ・チーム同期の呼び出し記録(検証用)。
	teamOffsets        []int
	projectUsersCalls  []int64
	projectAdminsCalls []int64
	projectTeamsCalls  []int64

	// errors はエンドポイントごとの注入エラー。
	issuesErr     error
	countErr      error
	activitiesErr error
	projectsErr   error
	usersErr      error
	teamsErr      error
	// projectUsersErr / projectAdminsErr / projectTeamsErr はプロジェクト単位の注入エラー。
	projectUsersErr  map[int64]error
	projectAdminsErr map[int64]error
	projectTeamsErr  map[int64]error
	// failIssuesAtOffset は指定 offset のページ取得を失敗させる(異常終了の再現)。
	failIssuesAtOffset int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		deletedKeys:        map[string]bool{},
		deletedIDs:         map[int64]bool{},
		getIssueOnly:       map[string]backlogclient.Issue{},
		projectUsers:       map[int64][]backlogclient.User{},
		projectAdmins:      map[int64][]backlogclient.User{},
		projectTeams:       map[int64][]backlogclient.Team{},
		projectUsersErr:    map[int64]error{},
		projectAdminsErr:   map[int64]error{},
		projectTeamsErr:    map[int64]error{},
		failIssuesAtOffset: -1,
	}
}

// fakeUser は検証用のユーザを組み立てる(実在しないダミー値のみ)。
func fakeUser(id int64, code, name string, roleType int) backlogclient.User {
	raw, _ := json.Marshal(map[string]any{"id": id, "userId": code, "name": name, "roleType": roleType})
	return backlogclient.User{
		ID: id, UserCode: code, Name: name,
		MailAddress: code + "@example.com", RoleType: roleType, RawJSON: string(raw),
	}
}

func (f *fakeAPI) GetUsersRaw(ctx context.Context) ([]backlogclient.User, error) {
	if f.usersErr != nil {
		return nil, f.usersErr
	}
	return f.users, nil
}

// GetTeamsPaged は offset ページングを実 API と同じ挙動で再現する。
func (f *fakeAPI) GetTeamsPaged(ctx context.Context, offset, count int) ([]backlogclient.Team, error) {
	f.teamOffsets = append(f.teamOffsets, offset)
	if f.teamsErr != nil {
		return nil, f.teamsErr
	}
	if offset >= len(f.teams) {
		return nil, nil
	}
	end := offset + count
	if end > len(f.teams) {
		end = len(f.teams)
	}
	return f.teams[offset:end], nil
}

// enterProjectCall はプロジェクト単位取得の呼び出しを記録し、
// 同時実行数の最大値を更新する。戻り値は退出用の関数。
func (f *fakeAPI) enterProjectCall(calls *[]int64, projectID int64) func() {
	f.mu.Lock()
	*calls = append(*calls, projectID)
	f.projectInFlight++
	if f.projectInFlight > f.projectMaxInFlight {
		f.projectMaxInFlight = f.projectInFlight
	}
	delay := f.projectCallDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return func() {
		f.mu.Lock()
		f.projectInFlight--
		f.mu.Unlock()
	}
}

// maxProjectInFlight はプロジェクト単位取得の同時実行数の最大値を返す。
func (f *fakeAPI) maxProjectInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.projectMaxInFlight
}

// recordedIssueQueries は記録済みの課題クエリのコピーを返す。
func (f *fakeAPI) recordedIssueQueries() []backlogclient.IssueQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]backlogclient.IssueQuery, len(f.issueQueries))
	copy(out, f.issueQueries)
	return out
}

func (f *fakeAPI) GetProjectUsers(ctx context.Context, projectID int64) ([]backlogclient.User, error) {
	defer f.enterProjectCall(&f.projectUsersCalls, projectID)()
	if err := f.projectUsersErr[projectID]; err != nil {
		return nil, err
	}
	return f.projectUsers[projectID], nil
}

func (f *fakeAPI) GetProjectAdministrators(ctx context.Context, projectID int64) ([]backlogclient.User, error) {
	defer f.enterProjectCall(&f.projectAdminsCalls, projectID)()
	if err := f.projectAdminsErr[projectID]; err != nil {
		return nil, err
	}
	return f.projectAdmins[projectID], nil
}

func (f *fakeAPI) GetProjectTeams(ctx context.Context, projectID int64) ([]backlogclient.Team, error) {
	defer f.enterProjectCall(&f.projectTeamsCalls, projectID)()
	if err := f.projectTeamsErr[projectID]; err != nil {
		return nil, err
	}
	return f.projectTeams[projectID], nil
}

// fakeTeam は検証用のチームを組み立てる(実在しないダミー値のみ)。
func fakeTeam(id int64, name string, memberIDs ...int64) backlogclient.Team {
	raw, _ := json.Marshal(map[string]any{"id": id, "name": name})
	return backlogclient.Team{ID: id, Name: name, MemberIDs: memberIDs, RawJSON: string(raw)}
}

// addIssue はフェイクへ課題を追加する。
func (f *fakeAPI) addIssue(id int64, key string, projectID int64, summary, created, updated string) {
	raw, _ := json.Marshal(map[string]any{
		"id": id, "issueKey": key, "projectId": projectID,
		"summary": summary, "created": created, "updated": updated,
	})
	f.issues = append(f.issues, backlogclient.Issue{
		ID: id, IssueKey: key, ProjectID: projectID, Summary: summary,
		Created: created, Updated: updated, RawJSON: string(raw),
	})
	sort.Slice(f.issues, func(i, j int) bool { return f.issues[i].Created < f.issues[j].Created })
}

// addActivity は課題削除(type=4)のアクティビティを追加する。
// content が nil の場合は判別不能な content(防御的パースの確認用)にする。
func (f *fakeAPI) addActivity(id int64, projectID int64, projectKey string, content map[string]any) {
	raw := json.RawMessage(`{}`)
	if content != nil {
		b, _ := json.Marshal(content)
		raw = b
	}
	f.activities = append(f.activities, backlogclient.Activity{
		ID: id, Type: 4, ProjectID: projectID, ProjectKey: projectKey, Content: raw,
	})
	sort.Slice(f.activities, func(i, j int) bool { return f.activities[i].ID < f.activities[j].ID })
}

func (f *fakeAPI) GetProjects(ctx context.Context) ([]backlogclient.Project, error) {
	if f.projectsErr != nil {
		return nil, f.projectsErr
	}
	return f.projects, nil
}

func (f *fakeAPI) matchQuery(i backlogclient.Issue, q backlogclient.IssueQuery) bool {
	inProject := false
	for _, pid := range q.ProjectIDs {
		if pid == i.ProjectID {
			inProject = true
			break
		}
	}
	if !inProject {
		return false
	}
	// updatedSince は日付単位(その日 00:00 以降を含む)
	if q.UpdatedSince != "" && i.Updated < q.UpdatedSince {
		return false
	}
	return true
}

func (f *fakeAPI) GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error) {
	f.mu.Lock()
	f.issueQueries = append(f.issueQueries, q)
	delay := f.issuesDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	// 実 API と同様にキャンセルを尊重する(パイプラインの停止確認に必要)。
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.issuesErr != nil {
		return nil, f.issuesErr
	}
	if f.failIssuesAtOffset >= 0 && q.Offset == f.failIssuesAtOffset {
		return nil, fmt.Errorf("フェイク: offset %d の取得に失敗", q.Offset)
	}
	var matched []backlogclient.Issue
	for _, i := range f.issues {
		if f.matchQuery(i, q) {
			matched = append(matched, i)
		}
	}
	if q.Offset >= len(matched) {
		return nil, nil
	}
	end := q.Offset + q.Count
	if end > len(matched) {
		end = len(matched)
	}
	return matched[q.Offset:end], nil
}

func (f *fakeAPI) GetIssuesCount(ctx context.Context, q backlogclient.IssueQuery) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	n := 0
	for _, i := range f.issues {
		if f.matchQuery(i, q) {
			n++
		}
	}
	return n, nil
}

func (f *fakeAPI) GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error) {
	f.getIssueCalls = append(f.getIssueCalls, issueIDOrKey)
	if f.deletedKeys[issueIDOrKey] {
		return nil, fmt.Errorf("%w: %s", backlogclient.ErrNotFound, issueIDOrKey)
	}
	if i, ok := f.getIssueOnly[issueIDOrKey]; ok {
		issue := i
		return &issue, nil
	}
	for _, i := range f.issues {
		if i.IssueKey == issueIDOrKey || fmt.Sprint(i.ID) == issueIDOrKey {
			if f.deletedIDs[i.ID] {
				return nil, fmt.Errorf("%w: %s", backlogclient.ErrNotFound, issueIDOrKey)
			}
			issue := i
			return &issue, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", backlogclient.ErrNotFound, issueIDOrKey)
}

func (f *fakeAPI) GetSpaceActivities(ctx context.Context, q backlogclient.ActivityQuery) ([]backlogclient.Activity, error) {
	f.activityQueries = append(f.activityQueries, q)
	if f.activitiesErr != nil {
		return nil, f.activitiesErr
	}
	var matched []backlogclient.Activity
	for _, a := range f.activities {
		if q.MinID > 0 && a.ID < q.MinID { // minId は下限(含む)
			continue
		}
		if len(q.ActivityTypeIDs) > 0 {
			ok := false
			for _, t := range q.ActivityTypeIDs {
				if t == a.Type {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		matched = append(matched, a)
	}
	if q.Order == "desc" {
		sort.Slice(matched, func(i, j int) bool { return matched[i].ID > matched[j].ID })
	}
	if q.Count > 0 && len(matched) > q.Count {
		matched = matched[:q.Count]
	}
	return matched, nil
}
