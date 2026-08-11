package sync

import (
	"context"
	"errors"
	"fmt"
	stdsync "sync"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// ユーザ・チーム同期のモード(Result.Mode)。
const (
	// ModeUsersSpace はスペース単位の取得(GET /users が使える管理者パス)。
	ModeUsersSpace Mode = "users-space"
	// ModeUsersProjects はプロジェクト単位取得への縮退パス(GET /users が 403)。
	ModeUsersProjects Mode = "users-projects"
)

// projectMembers は 1 プロジェクトぶんの取得結果。
type projectMembers struct {
	projectID  int64
	projectKey string
	// users は参加者 + 管理者(縮退時のユーザ集合合成に使う)。
	users []backlogclient.User
	// members は project_users へ書き込む行(is_admin 込み)。
	members []store.ProjectUser
	// membersComplete は参加者・管理者の両方を取得できたか。
	// 偽のときは project_users を置換せず据え置く(中 1)。
	membersComplete bool
	// teams はスペース /teams を取得できない場合に補うプロジェクト単位のチーム(高 1)。
	teams []backlogclient.Team
}

// projectFetchStats はプロジェクト単位取得の成否の集計。
type projectFetchStats struct {
	// targets は対象プロジェクト数。
	targets int
	// userFailed は参加者一覧の取得に失敗したプロジェクト数。
	// 1 件でもあると、合成したユーザ集合はスペース全体を網羅していない(高 2)。
	userFailed int
	// teamFailed はプロジェクト単位のチーム取得に失敗したプロジェクト数
	// (スペース /teams が取得できず、プロジェクト経由で補う場合のみ)。
	// 1 件でもあると、合成したチーム集合は網羅的でない(高 1)。
	teamFailed int
}

// SyncUsers はユーザ・チーム・プロジェクト参加者を同期する(設計書 3 節)。
//
// 管理者パス(GET /users が成功):
//   - users をスペース単位で全置換
//   - teams を count=100 + offset で全ページ消化し、teams / team_members を全置換
//   - ローカル projects の各プロジェクトの参加者・管理者で project_users を置換
//
// 縮退パス(GET /users が 403):
//   - ローカル projects の各プロジェクトの参加者からユーザ集合を合成する
//     (mailAddress 等、プロジェクト単位の応答に含まれない項目は空になりうる)
//   - 全対象プロジェクトの参加者を取得できた場合のみ users を全置換する。
//     1 件でも失敗した場合は取得できたユーザの UPSERT に留め、削除反映を行わない(高 2)
//
// チームの縮退は /users とは独立に、スペース /teams 自体の結果で判定する(高 1)。
// /teams が取得できない場合のみ各プロジェクトの /projects/:id/teams を合成する
// (「/users 成功・/teams 403」でもチーム情報を失わない)。
//
// プロジェクト単位の取得失敗は警告に集約し、同期全体は継続する
// (縮退パスで全プロジェクトの参加者取得に失敗した場合のみエラー)。
// 反映は原則として境界単位の全置換(退会・脱退した古い所属関係を残さない)。
func (e *Engine) SyncUsers(ctx context.Context) (*Result, error) {
	start := e.now()
	res := &Result{Mode: ModeUsersSpace, Warnings: []string{}}
	fetchedAt := e.nowString()

	// 1. スペース全体のユーザ一覧。403 のみ縮退の根拠とする
	//    (通信エラーを縮退と取り違えると、不完全な集合で users を全置換してしまう)。
	degraded := false
	spaceUsers, err := e.api.GetUsersRaw(ctx)
	if err != nil {
		if !errors.Is(err, backlogclient.ErrPermissionDenied) {
			return nil, fmt.Errorf("ユーザ一覧の取得に失敗しました: %w", err)
		}
		degraded = true
		res.Mode = ModeUsersProjects
		res.warn("ユーザ一覧(スペース全体)の取得権限がありません。プロジェクト単位の参加者からユーザ情報を合成します")
	}

	// 1-2. 縮退パス(プロジェクト単位取得)の前提確認(R1)。
	//      縮退パスはローカル projects を唯一の入力にするため、プロジェクト同期が
	//      未完了だと「まだ何も取得していない」状態を「参加プロジェクトが 0 件」と
	//      取り違え、合成結果(空集合)で既存キャッシュを全置換してしまう。
	//      ユーザ側は情報源そのものを失うため、明確なエラーで失敗させる
	//      (この時点では DB を一切書き換えていないのでキャッシュは不変)。
	projectsSynced, err := e.projectsSynced(ctx)
	if err != nil {
		return nil, err
	}
	if degraded && !projectsSynced {
		return nil, errors.New("ユーザ一覧(スペース全体)の取得権限がないため、プロジェクトの参加者からユーザ情報を合成する必要があります。プロジェクトを先に同期してください")
	}

	// 2. スペース全体のチーム一覧。チームの取得元は /users の成否ではなく
	//    この結果で決める(高 1)。成功していれば完全な一覧が得られるため
	//    プロジェクト単位の取得は不要、失敗していれば(403・一時エラーとも)
	//    プロジェクト経由で補う。
	spaceTeams, spaceTeamsErr := e.fetchAllTeams(ctx)

	// 3. 取得対象はローカルの projects(= アクセス可能な参加プロジェクト)。
	projects, err := e.st.ListProjects(ctx)
	if err != nil {
		return nil, err
	}

	// 4. プロジェクト単位の参加者・管理者(必要ならチームも)を集める。
	//    失敗は警告に集約して継続する。
	fetched, stats := e.fetchProjectMembers(ctx, projects, res, spaceTeamsErr != nil)

	// 縮退パスでは合成結果がユーザ情報そのものになるため、全プロジェクトの
	// 取得に失敗した場合は同期失敗とする(空集合でキャッシュを壊さない。高 2)。
	if degraded && stats.targets > 0 && stats.userFailed == stats.targets {
		return nil, errors.New("すべての対象プロジェクトで参加者を取得できなかったため、ユーザ情報を同期できませんでした")
	}
	// 縮退パスの部分失敗。取得できたユーザ集合はスペース全体を網羅していない。
	partial := degraded && stats.userFailed > 0

	// 5. users の反映。縮退時はプロジェクト参加者から合成する。
	users := spaceUsers
	if degraded {
		users = synthesizeUsers(fetched)
	}
	rows := toStoreUsers(users, fetchedAt)
	if partial {
		// 取得できたユーザだけを UPSERT する(既存ユーザは削除しない。高 2)。
		if err := e.st.UpsertUsers(ctx, rows); err != nil {
			return nil, err
		}
		res.warn("一部プロジェクトの取得に失敗したためユーザの削除反映は行っていません")
	} else if err := e.st.ReplaceUsers(ctx, rows); err != nil {
		return nil, err
	}
	res.Fetched = len(users)
	res.Upserted = len(rows)

	// 6. teams / team_members の反映。判定基準はスペース /teams の結果(中 1)。
	if err := e.applyTeams(ctx, spaceTeams, spaceTeamsErr, fetched, stats.teamFailed, projectsSynced, res, fetchedAt); err != nil {
		return nil, err
	}

	// 7. project_users をプロジェクト単位で置換する。
	//    参加者・管理者の両方を取得できたプロジェクトのみが対象(中 1)。
	for _, pm := range fetched {
		if !pm.membersComplete {
			continue
		}
		if err := e.st.ReplaceProjectUsers(ctx, pm.projectID, pm.members); err != nil {
			return nil, err
		}
	}

	// 8. 完了時刻を保存する(users はスペース共通なので project_id = 0)。
	now := e.now().UTC()
	if err := e.st.SetSyncCompleted(ctx, store.DataKindUsers, store.ProjectScopeAll,
		now.Format(time.RFC3339), now.Format("2006-01-02")); err != nil {
		return nil, err
	}
	res.DurationMs = e.now().Sub(start).Milliseconds()
	return res, nil
}

// projectsSynced はプロジェクト同期が 1 度でも完了しているかを返す(R1)。
//
// ローカル projects が空であることには「参加プロジェクトが 0 件」と
// 「まだ同期していない」の 2 つの意味があり、前者だけがキャッシュの全置換を
// 正当化する。両者は sync_state(projects / project_id = 0)の有無で区別する。
func (e *Engine) projectsSynced(ctx context.Context) (bool, error) {
	st, err := e.st.GetSyncState(ctx, store.DataKindProjects, store.ProjectScopeAll)
	if err != nil {
		return false, err
	}
	return st != nil && st.LastSyncedAt != "", nil
}

// projectFetchConcurrency はプロジェクト単位取得の並列度。
// API 側は read 区分のレートリミッタ(スレッドセーフ)が流量を抑えるため、
// ここでは「待ち時間を隠せる程度」の小さな上限に留める。
const projectFetchConcurrency = 4

// projectFetchOutcome は 1 プロジェクトぶんの取得で生じた副作用
// (警告・失敗の別)をワーカー goroutine の外へ持ち出すための値。
// Result や集計カウンタを goroutine から直接触らず(共有ミューテーション禁止)、
// 呼び出し元がプロジェクト順にまとめて反映する。
type projectFetchOutcome struct {
	userFailed bool
	teamFailed bool
	warnings   []string
}

func (o *projectFetchOutcome) warn(format string, args ...any) {
	o.warnings = append(o.warnings, fmt.Sprintf(format, args...))
}

// fetchProjectMembers は各プロジェクトの参加者と管理者(needTeams ならチームも)を
// 取得する。
// 参加者の取得に失敗したプロジェクトは警告を付けて飛ばす(既存キャッシュは据え置く)。
// 管理者一覧だけ失敗した場合は、参加者の合成には使うが project_users は据え置く
// (is_admin を全て false で置換すると、管理者フラグが消えるため。中 1)。
//
// チームの取得は参加者の取得結果に依存させない(高 1)。参加者が取れなかった
// プロジェクトでもチームは取得を試み、取れた分は反映対象に含める。
//
// 並行設計の意図:
//   - プロジェクト間には依存が無く待ち時間の大半が API 応答待ちなので、
//     projectFetchConcurrency 本のワーカーで有界並列に取得する。
//     並列度を絞るのは、レートリミッタ待ちのリクエストを大量に積まないため。
//   - 各ワーカーは自分の担当インデックスの要素だけに書き込むため、
//     結果の並びは常にプロジェクト一覧の元順どおりになる。
//   - 警告・失敗件数は goroutine 内で共有せず outcome に溜め、
//     全ワーカーの終了後に元順で集約する。したがって並列化しても
//     警告の内容・順序・件数は直列実行時と一致する。
func (e *Engine) fetchProjectMembers(ctx context.Context, projects []store.Project, res *Result, needTeams bool) ([]projectMembers, projectFetchStats) {
	stats := projectFetchStats{targets: len(projects)}
	out := make([]projectMembers, len(projects))
	outcomes := make([]projectFetchOutcome, len(projects))

	workers := projectFetchConcurrency
	if len(projects) < workers {
		workers = len(projects)
	}
	if workers > 0 {
		indexes := make(chan int)
		var wg stdsync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range indexes {
					// 書き込み先はインデックスごとに排他(共有状態を持たない)
					out[i], outcomes[i] = e.fetchOneProjectMembers(ctx, projects[i], needTeams)
				}
			}()
		}
		for i := range projects {
			indexes <- i
		}
		close(indexes)
		wg.Wait()
	}

	// 集約は呼び出し元 goroutine で元順に行う(順序・件数を直列実行と揃える)
	for _, oc := range outcomes {
		if oc.userFailed {
			stats.userFailed++
		}
		if oc.teamFailed {
			stats.teamFailed++
		}
		res.Warnings = append(res.Warnings, oc.warnings...)
	}
	return out, stats
}

// fetchOneProjectMembers は 1 プロジェクトぶんの取得を行う(ワーカーの本体)。
// 共有状態には触れず、結果と副作用(警告・失敗)を値で返す。
func (e *Engine) fetchOneProjectMembers(ctx context.Context, p store.Project, needTeams bool) (projectMembers, projectFetchOutcome) {
	pm := projectMembers{projectID: p.ID, projectKey: p.ProjectKey}
	var oc projectFetchOutcome

	users, err := e.api.GetProjectUsers(ctx, p.ID)
	if err != nil {
		oc.userFailed = true
		oc.warn("プロジェクト %s の参加者を取得できませんでした(このプロジェクトの参加情報は更新していません): %v", p.ProjectKey, err)
	} else {
		e.fillProjectMembers(ctx, &pm, users, &oc)
	}

	// スペース /teams が取得できない場合のみプロジェクト単位で補う(高 1)。
	// 取得できている場合は完全な一覧があるため呼ばない。
	if needTeams {
		teams, terr := e.api.GetProjectTeams(ctx, p.ID)
		if terr != nil {
			oc.teamFailed = true
			oc.warn("プロジェクト %s のチーム一覧を取得できませんでした(このプロジェクトのチーム情報は更新していません): %v", p.ProjectKey, terr)
		} else {
			pm.teams = teams
		}
	}
	return pm, oc
}

// fillProjectMembers は取得済みの参加者と管理者一覧から pm の users / members を
// 組み立てる。管理者一覧の取得に失敗した場合は membersComplete を偽のままにし、
// project_users を据え置かせる(中 1)。
// 警告は呼び出し元(ワーカー)の outcome へ溜める。
func (e *Engine) fillProjectMembers(ctx context.Context, pm *projectMembers, users []backlogclient.User, oc *projectFetchOutcome) {
	admins, aerr := e.api.GetProjectAdministrators(ctx, pm.projectID)
	if aerr != nil {
		oc.warn("プロジェクト %s の管理者一覧を取得できませんでした(このプロジェクトの参加情報は据え置きます): %v", pm.projectKey, aerr)
		admins = nil
	}
	pm.membersComplete = aerr == nil

	isAdmin := make(map[int64]bool, len(admins))
	for _, a := range admins {
		isAdmin[a.ID] = true
	}
	seen := make(map[int64]bool, len(users))
	for _, u := range users {
		if seen[u.ID] {
			continue
		}
		seen[u.ID] = true
		pm.users = append(pm.users, u)
		pm.members = append(pm.members, store.ProjectUser{UserID: u.ID, IsAdmin: isAdmin[u.ID]})
	}
	// 参加者一覧に現れない管理者(応答差異への備え)も参加者として記録し、
	// 管理者フラグを落とさない。
	for _, a := range admins {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		pm.users = append(pm.users, a)
		pm.members = append(pm.members, store.ProjectUser{UserID: a.ID, IsAdmin: true})
	}
}

// applyTeams は teams / team_members を反映する。
// 取得元と据え置きの判定はスペース /teams の結果だけで決める(高 1・中 1)。
//
//   - 成功: 完全な一覧が得られているのでそのまま全置換する。
//     プロジェクト側の取得失敗には影響されない(そもそも取得していない)。
//   - 403: プロジェクト経由で合成した一覧を使う。全プロジェクトで取得できていれば
//     全置換し(合成が空なら管理者由来キャッシュを破棄)、
//     取りこぼしがあれば MergeTeams(削除なし)+ 警告に留める。
//     ただしプロジェクトが未同期の場合は取りこぼしと同じ扱いにする(R1)。
//   - 403 以外の一時エラー: キャッシュは破棄しない。プロジェクト経由で取得できた分が
//     あれば MergeTeams で反映し、無ければ据え置く。いずれも警告を付ける。
//
// teamFailed はプロジェクト単位のチーム取得に失敗した件数。
// projectsSynced はプロジェクト同期が完了しているか(R1)。
func (e *Engine) applyTeams(ctx context.Context, spaceTeams []backlogclient.Team, spaceErr error,
	fetched []projectMembers, teamFailed int, projectsSynced bool, res *Result, fetchedAt string) error {
	if spaceErr == nil {
		return e.st.ReplaceTeams(ctx, toStoreTeams(spaceTeams, fetchedAt))
	}

	projectTeams := mergeTeams(collectProjectTeams(fetched))
	mergeFetched := func() error {
		if len(projectTeams) == 0 {
			return nil
		}
		return e.st.MergeTeams(ctx, toStoreTeams(projectTeams, fetchedAt))
	}

	if !errors.Is(spaceErr, backlogclient.ErrPermissionDenied) {
		// 一時的な失敗ではキャッシュを据え置く(誤って破棄しない)。
		if len(projectTeams) == 0 {
			res.warn("チーム一覧を取得できませんでした(既存のチーム情報を据え置きます): %v", spaceErr)
			return nil
		}
		if err := mergeFetched(); err != nil {
			return err
		}
		res.warn("チーム一覧(スペース全体)を取得できませんでした。参加プロジェクト経由で取得できた分のみ反映しました(削除反映は行っていません): %v", spaceErr)
		return nil
	}

	if !projectsSynced {
		// プロジェクトが未同期では補完取得の対象自体が未確定で、「チームが 0 件」を
		// 確認できない。既存キャッシュを空集合で破棄しないよう据え置く(R1)。
		//
		// 取得できた分のマージも行わない。ローカルに残っているプロジェクト行は
		// 前回の同期時点のものでしかなく(現在も参加しているとは限らない)、
		// そこから合成したチームを反映すると古い情報で上書きしてしまう。
		// 警告どおり teams / team_members は完全に不変にする。
		res.warn("チーム一覧(スペース全体)の取得権限がありません。プロジェクトが未同期のためチーム情報を補完できず、既存のチーム情報を据え置きます(プロジェクトを先に同期してください)")
		return nil
	}

	if teamFailed > 0 {
		// 取得に失敗したプロジェクトのチームを取りこぼしているため全置換しない。
		if err := mergeFetched(); err != nil {
			return err
		}
		res.warn("チーム一覧(スペース全体)の取得権限がありません。一部プロジェクトのチーム取得にも失敗したため、取得できた分のみ反映しました(削除反映は行っていません)")
		return nil
	}

	if err := e.st.ReplaceTeams(ctx, toStoreTeams(projectTeams, fetchedAt)); err != nil {
		return err
	}
	if len(projectTeams) == 0 {
		// 権限が縮退した(以前は取得できていた)場合に管理者由来キャッシュを
		// 残さないため、チーム情報を破棄する(設計書 2 節)。
		res.warn("チーム一覧の取得権限がありません。チーム情報のキャッシュを破棄しました")
	} else {
		res.warn("チーム一覧(スペース全体)の取得権限がありません。参加プロジェクト経由で取得できたチームのみ反映しました")
	}
	return nil
}

// collectProjectTeams はプロジェクト単位に取得したチームを 1 本にまとめる。
func collectProjectTeams(fetched []projectMembers) []backlogclient.Team {
	var all []backlogclient.Team
	for _, pm := range fetched {
		all = append(all, pm.teams...)
	}
	return all
}

// mergeTeams はチーム列を team ID で重複排除して合成する(高 1)。
// 同一チームが複数プロジェクトの応答に現れるため、先に現れたものを残しつつ
// 空欄(名前・メンバー・raw_json)は後続の応答で補完する。
func mergeTeams(list []backlogclient.Team) []backlogclient.Team {
	index := map[int64]int{}
	out := []backlogclient.Team{}
	for _, t := range list {
		i, ok := index[t.ID]
		if !ok {
			index[t.ID] = len(out)
			out = append(out, t)
			continue
		}
		if out[i].Name == "" {
			out[i].Name = t.Name
		}
		if len(out[i].MemberIDs) == 0 {
			out[i].MemberIDs = t.MemberIDs
		}
		if out[i].RawJSON == "" {
			out[i].RawJSON = t.RawJSON
		}
	}
	return out
}

// fetchAllTeams は GET /teams を count=100 + offset で全ページ消化する。
// 返却件数がページサイズ未満になるまで進める(既定の 20 件で止めない)。
func (e *Engine) fetchAllTeams(ctx context.Context) ([]backlogclient.Team, error) {
	var all []backlogclient.Team
	for page := 0; ; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("チーム取得のページ数が上限(%d)を超えました", maxPages)
		}
		teams, err := e.api.GetTeamsPaged(ctx, page*pageSize, pageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, teams...)
		if len(teams) < pageSize {
			return all, nil
		}
	}
}

// synthesizeUsers はプロジェクト単位の取得結果からスペースのユーザ集合を合成する
// (縮退パス)。同一ユーザが複数プロジェクトに現れるため ID で重複排除し、
// 後から現れた応答で空欄を補完する。
func synthesizeUsers(fetched []projectMembers) []backlogclient.User {
	index := map[int64]int{}
	out := []backlogclient.User{}
	for _, pm := range fetched {
		for _, u := range pm.users {
			i, ok := index[u.ID]
			if !ok {
				index[u.ID] = len(out)
				out = append(out, u)
				continue
			}
			// 取れている項目を優先して残す(応答ごとの欠落を埋める)
			if out[i].UserCode == "" {
				out[i].UserCode = u.UserCode
			}
			if out[i].Name == "" {
				out[i].Name = u.Name
			}
			if out[i].MailAddress == "" {
				out[i].MailAddress = u.MailAddress
			}
			if out[i].RoleType == 0 {
				out[i].RoleType = u.RoleType
			}
			if out[i].RawJSON == "" {
				out[i].RawJSON = u.RawJSON
			}
		}
	}
	return out
}

// toStoreUsers は API のユーザを store の行へ変換する(raw_json を保持)。
func toStoreUsers(users []backlogclient.User, fetchedAt string) []*store.User {
	rows := make([]*store.User, 0, len(users))
	for _, u := range users {
		rows = append(rows, &store.User{
			ID: u.ID, UserCode: u.UserCode, Name: u.Name,
			MailAddress: u.MailAddress, RoleType: u.RoleType,
			RawJSON: u.RawJSON, FetchedAt: fetchedAt,
		})
	}
	return rows
}

// toStoreTeams は API のチームを store の行へ変換する(members は team_members へ)。
func toStoreTeams(teams []backlogclient.Team, fetchedAt string) []*store.Team {
	rows := make([]*store.Team, 0, len(teams))
	for _, t := range teams {
		rows = append(rows, &store.Team{
			ID: t.ID, Name: t.Name, MemberIDs: t.MemberIDs,
			RawJSON: t.RawJSON, FetchedAt: fetchedAt,
		})
	}
	return rows
}
