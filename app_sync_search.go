package main

// app_sync_search.go は同期(プロジェクト・課題・ユーザ)とローカル検索の
// バインディング(frontend/src/lib/backend.ts の契約と対)。
// Excel 出力は app_export.go、一括更新・追加は app_bulk.go にある。

import (
	"fmt"
	"log/slog"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
)

// ProjectRow はプロジェクト一覧の 1 行(課題同期の最終時刻付き)。
type ProjectRow struct {
	ID           int64  `json:"id"`
	ProjectKey   string `json:"projectKey"`
	Name         string `json:"name"`
	LastSyncedAt string `json:"lastSyncedAt"`
	// SyncStateUnknown は同期状態の取得に失敗したことを示す(中 1)。
	// 真のときの LastSyncedAt は「未同期」ではなく「不明」であり、
	// UI は未同期の警告を出してはならない。
	SyncStateUnknown bool `json:"syncStateUnknown"`
}

// newProjectRows はプロジェクト一覧と同期状態一覧を突き合わせて一覧の行を組み立てる。
//
// 同期状態は「課題(issues)」の行だけを見る。プロジェクト一覧の鮮度表示は
// 課題同期の最終時刻を指すため、ユーザ・チーム等の行を拾ってはならない。
// syncStateUnknown が真のときは全行を「不明」にする(取得自体が失敗したため、
// どのプロジェクトについても未同期と断定できない。中 1)。
func newProjectRows(projects []store.Project, states []store.SyncState, syncStateUnknown bool) []ProjectRow {
	lastSyncedAt := make(map[int64]string, len(states))
	for _, st := range states {
		if st.DataKind == store.DataKindIssues {
			lastSyncedAt[st.ProjectID] = st.LastSyncedAt
		}
	}
	rows := make([]ProjectRow, 0, len(projects))
	for _, p := range projects {
		last := ""
		if !syncStateUnknown {
			last = lastSyncedAt[p.ID]
		}
		rows = append(rows, ProjectRow{
			ID:               p.ID,
			ProjectKey:       p.ProjectKey,
			Name:             p.Name,
			LastSyncedAt:     last,
			SyncStateUnknown: syncStateUnknown,
		})
	}
	return rows
}

// ListProjects はローカル DB のプロジェクト一覧を返す。
//
// 同期状態はプロジェクトごとに引かず、ListSyncStates で一度に取得して
// メモリ上で突き合わせる(以前はプロジェクト数 + 1 回のクエリを発行していた。R18)。
func (a *App) ListProjects(profileID string) ([]ProjectRow, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "ListProjects", attrs,
		func(s *service.ProfileService) ([]ProjectRow, []slog.Attr, error) {
			projects, err := s.ListProjects(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			states, serr := s.ListSyncStates(a.ctx, profileID)
			if serr != nil {
				// 鮮度が取れないと同期済みでも「未同期」と表示されてしまうため、
				// 「不明」であることを UI へ伝えつつ原因をログに残す(黙って握り潰さない)
				a.log.OpError("ListProjects 同期状態の取得", serr, slog.String("profileId", profileID))
			}
			rows := newProjectRows(projects, states, serr != nil)
			return rows, []slog.Attr{slog.Int("count", len(rows))}, nil
		})
}

// SyncProjects は参加プロジェクト一覧を API から取得してローカル DB へ反映する。
func (a *App) SyncProjects(profileID string) error {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOpErr(a, "SyncProjects", attrs,
		func(s *service.ProfileService) ([]slog.Attr, error) {
			res, err := s.SyncProjects(a.ctx, profileID)
			if err != nil {
				return nil, err
			}
			return []slog.Attr{
				slog.Int("fetched", res.Fetched),
				slog.Int("upserted", res.Upserted),
				slog.Int("deleted", res.Deleted),
				slog.Int("warnings", len(res.Warnings)),
				slog.Int64("durationMs", res.DurationMs),
			}, nil
		})
}

// SyncResultDTO は同期結果(フロント契約: warnings は null 不可)。
type SyncResultDTO struct {
	Mode       string   `json:"mode"`
	Fetched    int      `json:"fetched"`
	Upserted   int      `json:"upserted"`
	Deleted    int      `json:"deleted"`
	Warnings   []string `json:"warnings"`
	DurationMs int64    `json:"durationMs"`
}

// SyncIssues は指定プロジェクトの課題を同期する(mode: full / incremental / auto)。
//
// runID は進捗イベント(sync:progress)に載せる実行識別子で、画面が
// 「自分が開始した実行の進捗か」を判定するために使う(中 4)。
// 呼び出し側(画面)が採番するのは、進捗イベントが本メソッドの戻り値より
// 先に届くため、戻り値で識別子を渡す方式では取りこぼすからである。
// 進捗表示が不要な呼び出しは空文字でよい。
func (a *App) SyncIssues(profileID string, projectID int64, mode, runID string) (*SyncResultDTO, error) {
	attrs := []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("projectId", projectID),
		slog.String("mode", mode),
	}
	return appOp(a, "SyncIssues", attrs,
		func(s *service.ProfileService) (*SyncResultDTO, []slog.Attr, error) {
			res, err := s.SyncIssues(a.ctx, profileID, projectID, mode, runID)
			if err != nil {
				return nil, nil, err
			}
			warnings := res.Warnings
			if warnings == nil {
				warnings = []string{}
			}
			return &SyncResultDTO{
					Mode:       string(res.Mode),
					Fetched:    res.Fetched,
					Upserted:   res.Upserted,
					Deleted:    res.Deleted,
					Warnings:   warnings,
					DurationMs: res.DurationMs,
				},
				// 警告本文は課題名等を含みうるため件数のみ記録する
				[]slog.Attr{
					slog.String("executedMode", string(res.Mode)),
					slog.Int("fetched", res.Fetched),
					slog.Int("upserted", res.Upserted),
					slog.Int("deleted", res.Deleted),
					slog.Int("warnings", len(warnings)),
					slog.Int64("durationMs", res.DurationMs),
				}, nil
		})
}

// IssueRowDTO は検索結果の 1 行(プレビュー・Excel 出力の共通形)。
type IssueRowDTO struct {
	IssueKey      string `json:"issueKey"`
	Summary       string `json:"summary"`
	StatusName    string `json:"statusName"`
	AssigneeName  string `json:"assigneeName"`
	IssueTypeName string `json:"issueTypeName"`
	PriorityName  string `json:"priorityName"`
	Created       string `json:"created"`
	Updated       string `json:"updated"`
	DueDate       string `json:"dueDate"`
	// CustomFields は画面で要求されたカスタム属性の表示文字列
	// (キー = Excel 出力と同じ列キー cf_{定義ID})。
	// 要求されていない属性は含めない(全属性を詰めると解析コストと
	// 転送量が課題数 × 属性数で膨らむため)。
	CustomFields map[string]string `json:"customFields"`
}

// issueRowDTOOf は課題 1 件を検索結果の DTO へ詰め替える。
//
// customFieldIDs で要求されたカスタム属性だけを、Excel 出力と同じ規約で
// 表示文字列にして載せる(整形は Go 側に寄せ、画面は文字列を並べるだけにする)。
// 値を持たない定義も空文字で埋め、行ごとにキーの有無が変わらないようにする。
func issueRowDTOOf(is *store.Issue, customFieldIDs []int64) IssueRowDTO {
	row := IssueRowDTO{
		IssueKey:      is.IssueKey,
		Summary:       is.Summary,
		StatusName:    is.StatusName,
		AssigneeName:  is.AssigneeName,
		IssueTypeName: is.IssueTypeName,
		PriorityName:  is.PriorityName,
		Created:       is.Created,
		Updated:       is.Updated,
		DueDate:       is.DueDate,
		// フロント契約では null を返さない(常にオブジェクト)
		CustomFields: make(map[string]string, len(customFieldIDs)),
	}
	if len(customFieldIDs) == 0 {
		return row
	}
	// 生 JSON の解析は 1 行 1 回。解釈できない課題は空欄へ縮退させ、
	// 1 件のデータ不備で検索結果全体を失わせない(Excel 出力と同じ流儀)。
	// 解釈はテンプレート出力(app_bulk.go)と共通の関数を使う。
	values := bulkCustomFieldValues(is.RawJSON)
	for _, id := range customFieldIDs {
		row.CustomFields[export.CustomColumnKey(id)] = values[id]
	}
	return row
}

// IssueSearchDTO は検索結果(表示上限で切っても total は総件数)。
type IssueSearchDTO struct {
	Rows  []IssueRowDTO `json:"rows"`
	Total int           `json:"total"`
	// Unverifiable はカスタム属性条件を判定できなかった課題の件数
	// (ローカルの生 JSON が古い・壊れている行)。0 でなければ、結果は
	// 「条件に合う全件」ではないため、画面はその旨を警告すること。
	Unverifiable int `json:"unverifiable"`
}

// SearchIssues はローカル DB から課題を抽出する(store.IssueFilter の json 名は
// フロント契約 IssueQuery と一致)。
//
// columns は画面の一覧に表示する列キー(Excel 出力の列選択と同じ形式)。
// このうち cf_{定義ID} のものだけを見て、カスタム属性の値を行に載せる。
// 固定列は常に返すため、固定列キーの有無は結果に影響しない。
func (a *App) SearchIssues(profileID string, query store.IssueFilter, columns []string) (*IssueSearchDTO, error) {
	attrs := a.searchAttrs(profileID, query)
	return appOp(a, "SearchIssues", attrs,
		func(s *service.ProfileService) (*IssueSearchDTO, []slog.Attr, error) {
			res, err := s.SearchIssues(a.ctx, profileID, query)
			if err != nil {
				return nil, nil, err
			}
			customFieldIDs := export.CustomColumnIDs(columns)
			rows := make([]IssueRowDTO, 0, len(res.Issues))
			for i := range res.Issues {
				rows = append(rows, issueRowDTOOf(&res.Issues[i], customFieldIDs))
			}
			return &IssueSearchDTO{Rows: rows, Total: res.Total, Unverifiable: res.Unverifiable},
				[]slog.Attr{
					slog.Int("rows", len(rows)),
					slog.Int("total", res.Total),
					slog.Bool("truncated", res.Truncated),
					slog.Int("unverifiable", res.Unverifiable),
					slog.Int("customFieldColumns", len(customFieldIDs)),
				}, nil
		})
}

// IssueCustomFieldDTO は課題詳細に表示するカスタム属性 1 件
// (frontend/src/lib/backend.ts の IssueCustomField と対)。
type IssueCustomFieldDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// IssueCommentDTO は課題詳細に表示するコメント 1 件
// (frontend/src/lib/backend.ts の IssueComment と対)。
//
// 本文を持つコメントだけを返す(状態変更等の変更履歴のみの項目は保存しておらず、
// 件数を IssueDetailDTO.CommentsHistoryOnly で伝える)。
type IssueCommentDTO struct {
	AuthorName string `json:"authorName"`
	Content    string `json:"content"`
	Created    string `json:"created"`
}

// IssueDetailDTO は課題 1 件の詳細(frontend/src/lib/backend.ts の IssueDetail と対)。
//
// 中身はすべてローカル DB へ取り込んだ時点の内容(通常は最終同期時点。
// RefreshIssueDetail でこの課題だけを取り込み直した場合はその時点)。
// FetchedAt はその取得時刻で、画面が「いつ時点の内容か」を注記するために使う。
type IssueDetailDTO struct {
	IssueKey      string `json:"issueKey"`
	Summary       string `json:"summary"`
	Description   string `json:"description"`
	StatusName    string `json:"statusName"`
	AssigneeName  string `json:"assigneeName"`
	IssueTypeName string `json:"issueTypeName"`
	PriorityName  string `json:"priorityName"`
	Created       string `json:"created"`
	Updated       string `json:"updated"`
	DueDate       string `json:"dueDate"`
	// ParentIssueKey は親課題の表記(CF5 と同じ規約)。
	// 親なし・判定不能は空文字、ローカルに無い親は ID:<数値>。
	ParentIssueKey string `json:"parentIssueKey"`
	// CustomFields は課題が持つカスタム属性の全件(フロント契約: null を返さない)。
	CustomFields []IssueCustomFieldDTO `json:"customFields"`
	// FetchedAt はこの課題をローカルへ取り込んだ時刻(RFC3339)。
	FetchedAt string `json:"fetchedAt"`
	// Comments は保存済みのコメント(新しい順。フロント契約: null を返さない)。
	// コメントは同期では取得されないため、RefreshIssueDetail を実行していない
	// 課題では空になる(「コメント 0 件」と区別するのは CommentsFetchedAt)。
	Comments []IssueCommentDTO `json:"comments"`
	// CommentsFetchedAt はコメントを取得した時刻(RFC3339)。空文字 = 未取得。
	CommentsFetchedAt string `json:"commentsFetchedAt"`
	// CommentsHistoryOnly は本文が無い(変更履歴のみの)項目の件数。
	CommentsHistoryOnly int `json:"commentsHistoryOnly"`
	// CommentsTruncated は取得上限に達し、古いコメントを取得しきれていないこと。
	CommentsTruncated bool `json:"commentsTruncated"`
	// Warnings は部分的な失敗(コメントだけ取得できなかった等)。
	// 課題本体の内容は有効なため、画面は詳細を表示したうえでこれを添える
	// (フロント契約: null を返さない)。
	Warnings []string `json:"warnings"`
}

// issueDetailDTOOf は課題 1 件を詳細 DTO へ詰め替える。
//
// カスタム属性は定義(GET /projects/:id/customFields)を取りに行かず、
// 生 JSON の customFields に含まれる name と表示規約(customfield.FormatValue)
// だけで組み立てる。詳細を開くたびに API を呼ばずに済み、オフラインでも表示できる。
// その代わり並び順は「定義順」ではなく「課題レスポンスに現れた順」になる。
//
// 生 JSON を解釈できない課題はカスタム属性を空にして縮退させ、
// 1 件のデータ不備で詳細表示全体を失わせない(課題抽出・Excel 出力と同じ流儀)。
func issueDetailDTOOf(is *store.Issue, parentKeys map[int64]string) IssueDetailDTO {
	dto := IssueDetailDTO{
		IssueKey:       is.IssueKey,
		Summary:        is.Summary,
		Description:    is.Description,
		StatusName:     is.StatusName,
		AssigneeName:   is.AssigneeName,
		IssueTypeName:  is.IssueTypeName,
		PriorityName:   is.PriorityName,
		Created:        is.Created,
		Updated:        is.Updated,
		DueDate:        is.DueDate,
		ParentIssueKey: parentIssueKeyOf(is.RawJSON, parentKeys),
		// フロント契約では null を返さない(常に配列)
		CustomFields: []IssueCustomFieldDTO{},
		FetchedAt:    is.FetchedAt,
		Comments:     []IssueCommentDTO{},
		Warnings:     []string{},
	}
	values, err := customfield.ParseValues(is.RawJSON)
	if err != nil {
		return dto
	}
	for _, v := range values {
		name := v.Name
		if name == "" {
			// 名前を持たない応答でも、どの定義の値かが分かるようにする
			// (値が表示から消えるのを避ける)
			name = fmt.Sprintf("(定義 ID %d)", v.ID)
		}
		dto.CustomFields = append(dto.CustomFields,
			IssueCustomFieldDTO{Name: name, Value: customfield.FormatValue(v)})
	}
	return dto
}

// newIssueDetailDTO は service の詳細(課題本体 + 親課題 + コメント)を
// 詳細 DTO へ詰め替える。GetIssueDetail / RefreshIssueDetail の共通経路。
//
// コメントは service が新しい順で返すため、並べ替えはしない(表示順の決定は
// SQL 側に寄せ、詰め替えでは順序を変えないという既存の流儀)。
func newIssueDetailDTO(d *service.IssueDetail) IssueDetailDTO {
	dto := issueDetailDTOOf(d.Issue, d.ParentKeys)
	for _, c := range d.Comments {
		dto.Comments = append(dto.Comments, IssueCommentDTO{
			AuthorName: c.AuthorName, Content: c.Content, Created: c.Created,
		})
	}
	dto.CommentsFetchedAt = d.CommentStatus.FetchedAt
	dto.CommentsHistoryOnly = d.CommentStatus.HistoryOnly
	dto.CommentsTruncated = d.CommentStatus.Truncated
	if len(d.Warnings) > 0 {
		dto.Warnings = d.Warnings
	}
	return dto
}

// GetIssueDetail は課題 1 件の詳細をローカル DB から返す(API は呼ばない)。
//
// 検索結果の課題キーをクリックしたときのポップアップ表示に使う。
// 表示はローカルへ取り込んだ時点の内容であり、Backlog 側の最新とは限らない
// (画面はその旨を FetchedAt とともに注記する。最新化は RefreshIssueDetail)。
func (a *App) GetIssueDetail(profileID string, projectID int64, issueKey string) (*IssueDetailDTO, error) {
	// 課題キー・件名は記録しない(既存のマスク方針。profileId / projectId のみ)
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	return appOp(a, "GetIssueDetail", attrs,
		func(s *service.ProfileService) (*IssueDetailDTO, []slog.Attr, error) {
			detail, err := s.GetIssueDetail(a.ctx, profileID, projectID, issueKey)
			if err != nil {
				return nil, nil, err
			}
			dto := newIssueDetailDTO(detail)
			// 記録するのは件数のみ。親の有無は「この課題が子課題である」という
			// 課題の内容そのものなので残さない(マスク方針)
			return &dto, []slog.Attr{
				slog.Int("customFields", len(dto.CustomFields)),
				slog.Int("comments", len(dto.Comments)),
			}, nil
		})
}

// RefreshIssueDetail は課題 1 件を Backlog から取得し直してローカル DB へ反映し、
// 反映後の詳細を返す(詳細ポップアップの「最新の状態を取得」)。
//
// GetIssueDetail と違い API を呼ぶ(課題本体 1 回 + コメント数回)。反映は同期と
// 同じ変換・同じ UPSERT を通すため、検索索引・親課題の引き当ても同時に最新化される。
// コメントを更新するのはこの経路だけ(同期はコメントに触れない)。
// 同期状態(最終同期時刻)は更新しない(1 件の最新化はプロジェクト同期ではない)。
func (a *App) RefreshIssueDetail(profileID string, projectID int64, issueKey string) (*IssueDetailDTO, error) {
	// 課題キー・件名は記録しない(既存のマスク方針。profileId / projectId のみ)
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	return appOp(a, "RefreshIssueDetail", attrs,
		func(s *service.ProfileService) (*IssueDetailDTO, []slog.Attr, error) {
			detail, err := s.RefreshIssue(a.ctx, profileID, projectID, issueKey)
			if err != nil {
				return nil, nil, err
			}
			dto := newIssueDetailDTO(detail)
			// コメント本文・投稿者名は記録しない(件数と取得の顛末だけを残す)
			return &dto, []slog.Attr{
				slog.Int("customFields", len(dto.CustomFields)),
				slog.Int("comments", len(dto.Comments)),
				slog.Int("commentsHistoryOnly", dto.CommentsHistoryOnly),
				slog.Bool("commentsTruncated", dto.CommentsTruncated),
				slog.Int("warnings", len(dto.Warnings)),
			}, nil
		})
}

// searchAttrs は検索条件のうち非機密なものだけをログ属性にする。
// キーワード・状態名・担当者名は課題内容や個人名を含みうるため、
// 値は記録せず「指定の有無」だけを記録する。
//
// カスタム属性の条件も同様に、入力値(顧客名等を含みうる)は記録せず
// 条件の件数だけを残す(2 段階検索が動いたかを追えるようにするため)。
func (a *App) searchAttrs(profileID string, query store.IssueFilter) []slog.Attr {
	return []slog.Attr{
		slog.String("profileId", profileID),
		slog.Int64("projectId", query.ProjectID),
		slog.Bool("hasKeyword", query.Keyword != ""),
		slog.Bool("hasStatus", query.StatusName != ""),
		slog.Bool("hasAssignee", query.AssigneeName != ""),
		slog.Bool("hasDateRange", query.UpdatedFrom != "" || query.UpdatedTo != "" ||
			query.CreatedFrom != "" || query.CreatedTo != ""),
		slog.Int("customFieldFilters", len(customfield.ActiveFilters(query.CustomFieldFilters))),
		slog.Int("limit", query.Limit),
	}
}

// FilterOptionsDTO は抽出条件の候補(フロント契約: statuses / assignees)。
type FilterOptionsDTO struct {
	Statuses  []string `json:"statuses"`
	Assignees []string `json:"assignees"`
}

// ListFilterOptions は状態・担当者の候補値をローカル DB から返す。
func (a *App) ListFilterOptions(profileID string, projectID int64) (*FilterOptionsDTO, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID), slog.Int64("projectId", projectID)}
	return appOp(a, "ListFilterOptions", attrs,
		func(s *service.ProfileService) (*FilterOptionsDTO, []slog.Attr, error) {
			opts, err := s.ListFilterOptions(a.ctx, profileID, projectID)
			if err != nil {
				return nil, nil, err
			}
			statuses := opts.StatusNames
			if statuses == nil {
				statuses = []string{}
			}
			assignees := opts.AssigneeNames
			if assignees == nil {
				assignees = []string{}
			}
			return &FilterOptionsDTO{Statuses: statuses, Assignees: assignees},
				// 候補値そのもの(状態名・担当者名)は記録せず件数のみ記録する
				[]slog.Attr{
					slog.Int("statuses", len(statuses)),
					slog.Int("assignees", len(assignees)),
				}, nil
		})
}

// SyncStateRow は同期状態画面の 1 行。
type SyncStateRow struct {
	DataKind     string `json:"dataKind"`
	ProjectID    int64  `json:"projectId"`
	LastSyncedAt string `json:"lastSyncedAt"`
}

// GetSyncState は全同期状態の一覧を返す(フロント契約に合わせ配列を返す)。
func (a *App) GetSyncState(profileID string) ([]SyncStateRow, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "GetSyncState", attrs,
		func(s *service.ProfileService) ([]SyncStateRow, []slog.Attr, error) {
			states, err := s.ListSyncStates(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]SyncStateRow, 0, len(states))
			for _, st := range states {
				rows = append(rows, SyncStateRow{DataKind: st.DataKind, ProjectID: st.ProjectID, LastSyncedAt: st.LastSyncedAt})
			}
			return rows, []slog.Attr{slog.Int("count", len(rows))}, nil
		})
}

// ---- M3: ユーザ抽出 ----------------------------------------------------------

// userAttrs はユーザ検索条件の動作ログ属性(キーワード本文は個人名を含みうるため有無のみ)。
func userAttrs(profileID string, filter store.UserFilter) []slog.Attr {
	return []slog.Attr{
		slog.String("profileId", profileID),
		slog.Bool("hasKeyword", filter.Keyword != ""),
		slog.Int("roleType", filter.RoleType),
	}
}

// SyncUsers はユーザ・チーム・プロジェクト参加情報を同期する
// (権限が無い場合はプロジェクト単位の取得へ自動縮退する)。
func (a *App) SyncUsers(profileID string) (*SyncResultDTO, error) {
	attrs := []slog.Attr{slog.String("profileId", profileID)}
	return appOp(a, "SyncUsers", attrs,
		func(s *service.ProfileService) (*SyncResultDTO, []slog.Attr, error) {
			res, err := s.SyncUsers(a.ctx, profileID)
			if err != nil {
				return nil, nil, err
			}
			warnings := res.Warnings
			if warnings == nil {
				warnings = []string{}
			}
			return &SyncResultDTO{
					Mode:       string(res.Mode),
					Fetched:    res.Fetched,
					Upserted:   res.Upserted,
					Deleted:    res.Deleted,
					Warnings:   warnings,
					DurationMs: res.DurationMs,
				},
				// 警告本文はプロジェクト名等を含みうるため件数のみ記録する
				[]slog.Attr{
					slog.String("executedMode", string(res.Mode)),
					slog.Int("fetched", res.Fetched),
					slog.Int("upserted", res.Upserted),
					slog.Int("warnings", len(warnings)),
					slog.Int64("durationMs", res.DurationMs),
				}, nil
		})
}

// UserListDTO はユーザ一覧の検索結果(フロント契約: rows / total)。
type UserListDTO struct {
	Rows  []store.UserRow `json:"rows"`
	Total int             `json:"total"`
}

// ListUsers はローカル DB からユーザ一覧を返す(所属チーム・参加プロジェクト付き)。
func (a *App) ListUsers(profileID string, query store.UserFilter) (*UserListDTO, error) {
	attrs := userAttrs(profileID, query)
	return appOp(a, "ListUsers", attrs,
		func(s *service.ProfileService) (*UserListDTO, []slog.Attr, error) {
			res, err := s.ListUsers(a.ctx, profileID, query)
			if err != nil {
				return nil, nil, err
			}
			rows := res.Users
			if rows == nil {
				rows = []store.UserRow{}
			}
			return &UserListDTO{Rows: rows, Total: res.Total},
				[]slog.Attr{slog.Int("rows", len(rows)), slog.Int("total", res.Total)}, nil
		})
}
