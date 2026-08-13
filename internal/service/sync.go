package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

// SyncProgressEvent は同期進捗の通知内容。
//
// RunID は「どの実行の進捗か」を表す識別子で、同期を開始した呼び出し元
// (UI)が採番して SyncIssues へ渡す。プロファイル ID + プロジェクト ID
// だけでは、同じ対象を続けて同期し直した場合や、失効した実行が
// まだ動いている場合に、新旧の実行を区別できないため(中 4)。
// service 側は解釈せずそのまま通知へ載せる(空文字も許す)。
type SyncProgressEvent struct {
	ProfileID string
	RunID     string
	ProjectID int64
	Progress  syncpkg.Progress
}

// SyncProgressFunc は同期進捗の通知先(app.go から Wails のイベント送信へ接続する想定)。
type SyncProgressFunc func(ev SyncProgressEvent)

// SetSyncProgressHandler は同期進捗の通知先を設定する(nil で解除)。
func (s *ProfileService) SetSyncProgressHandler(fn SyncProgressFunc) {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	s.onProgress = fn
}

// progressHandler は設定済みの通知先を返す(未設定なら nil)。
func (s *ProfileService) progressHandler() SyncProgressFunc {
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.onProgress
}

// storeForProfile はプロファイルのローカル DB を開いてキャッシュから返す。
// DB はスペースホスト名 + 認証ユーザ ID(接続テストで確認した LastUserID)で
// 決まる(設計書 2 節)。接続実績が無い(LastUserID = 0)プロファイルは
// DB を特定できないためエラーにする。
//
// この関数自身は profileMu を取らない(高 2)。呼び出し元の公開メソッドが
// 入口で必ず profileMu.RLock を取る規約とし、入れ子 RLock による
// 自己デッドロック(待機中の Lock があると 2 回目の RLock が進めない)を避ける。
func (s *ProfileService) storeForProfile(profileID string) (*store.Store, error) {
	profile, err := s.cfg.Get(profileID)
	if err != nil {
		return nil, err
	}
	host, err := backlogclient.SpaceHost(profile.SpaceURL)
	if err != nil {
		return nil, err
	}
	if profile.LastUserID == 0 {
		return nil, errors.New("接続ユーザが未確定です(接続テストを実行してプロファイルを保存し直してください)")
	}
	path, err := s.dbPathFor(host, profile.LastUserID)
	if err != nil {
		return nil, err
	}

	s.storesMu.Lock()
	defer s.storesMu.Unlock()
	if e, ok := s.stores[profileID]; ok {
		if e.path == path {
			return e.store, nil
		}
		// API キー変更等で接続ユーザが変わった場合は旧 DB を閉じて開き直す
		_ = e.store.Close()
		delete(s.stores, profileID)
	}
	st, err := s.openStore(path)
	if err != nil {
		return nil, err
	}
	s.stores[profileID] = &storeEntry{store: st, path: path}
	return st, nil
}

// closeStore はプロファイルのローカル DB 接続を閉じてキャッシュから外す。
// DB ファイルを削除する前には必ず呼ぶこと(開いたままの削除は
// Windows で失敗し、WAL/SHM も残るため)。
func (s *ProfileService) closeStore(profileID string) {
	s.storesMu.Lock()
	defer s.storesMu.Unlock()
	if e, ok := s.stores[profileID]; ok {
		_ = e.store.Close()
		delete(s.stores, profileID)
	}
}

// Close は開いているすべてのローカル DB 接続を閉じる(アプリ終了時)。
func (s *ProfileService) Close() error {
	s.storesMu.Lock()
	defer s.storesMu.Unlock()
	var firstErr error
	for id, e := range s.stores {
		if err := e.store.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.stores, id)
	}
	return firstErr
}

// engineForProfile は同期エンジン(API クライアント + ローカル DB)を組み立てる。
func (s *ProfileService) engineForProfile(ctx context.Context, profileID string) (*syncpkg.Engine, error) {
	client, err := s.clientForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return syncpkg.NewEngine(client, st), nil
}

// SyncProjects は参加プロジェクト一覧を同期する(アクセス不能になった
// プロジェクトのキャッシュ破棄を含む)。
func (s *ProfileService) SyncProjects(ctx context.Context, profileID string) (*syncpkg.Result, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	// ローカル DB への書き込みを直列化する(SQLite の接続数は 1)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	engine, err := s.engineForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return engine.SyncProjects(ctx)
}

// SyncIssues は 1 プロジェクトの課題を同期する。
// mode は "auto"(既定・sync_state から判定)/ "full" / "incremental"。
// runID は進捗通知に載せる実行識別子(SyncProgressEvent の説明を参照。空可)。
func (s *ProfileService) SyncIssues(ctx context.Context, profileID string, projectID int64, mode, runID string) (*syncpkg.Result, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	m := syncpkg.Mode(mode)
	switch m {
	case "", syncpkg.ModeAuto, syncpkg.ModeFull, syncpkg.ModeIncremental:
	default:
		return nil, fmt.Errorf("不明な同期モードです: %s", mode)
	}
	engine, err := s.engineForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	var onProgress syncpkg.ProgressFunc
	if fn := s.progressHandler(); fn != nil {
		onProgress = func(p syncpkg.Progress) {
			fn(SyncProgressEvent{ProfileID: profileID, RunID: runID, ProjectID: projectID, Progress: p})
		}
	}
	return engine.SyncIssues(ctx, projectID, m, onProgress)
}

// SyncUsers はユーザ・チーム・プロジェクト参加者を同期する(設計書 3 節)。
// 管理者権限が無い場合はプロジェクト単位取得へ自動的に縮退し、
// 結果の Mode("users-space" / "users-projects")と Warnings で UI へ伝える。
func (s *ProfileService) SyncUsers(ctx context.Context, profileID string) (*syncpkg.Result, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	// ローカル DB への書き込みを直列化する(SQLite の接続数は 1)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	engine, err := s.engineForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return engine.SyncUsers(ctx)
}

// ListUsers はローカル DB のユーザ一覧を返す(API は呼ばない。画面 4)。
// 所属チーム・参加プロジェクト・管理者プロジェクトは JOIN で解決済み。
func (s *ProfileService) ListUsers(ctx context.Context, profileID string, filter store.UserFilter) (*store.UserListResult, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListUserRows(ctx, filter)
}

// ListProjects はローカル DB のプロジェクト一覧を返す(API は呼ばない)。
func (s *ProfileService) ListProjects(ctx context.Context, profileID string) ([]store.Project, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListProjects(ctx)
}

// SearchIssues はローカル DB から課題を抽出する(設計書 4 節・画面 2)。
// Backlog API の keyword 検索とは範囲が異なる(件名 + 詳細のみ)。
func (s *ProfileService) SearchIssues(ctx context.Context, profileID string, filter store.IssueFilter) (*store.IssueSearchResult, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.SearchIssues(ctx, filter)
}

// IssueDetail は課題 1 件の詳細表示(画面 2 のポップアップ)の材料。
//
// DTO への詰め替え(表示規約・フロント契約への正規化)は呼び出し側(app)で行う。
// ここでは「ローカル DB から読める素材」だけを返す。
type IssueDetail struct {
	// Issue は対象の課題(常に非 nil)。
	Issue *store.Issue
	// ParentKeys は親課題 ID → 課題キー。親が無い場合、および親が
	// ローカルに無い(未同期・別プロジェクト)場合は空になる。
	// 呼び出し側は引き当てられない親を ID:<数値> 表記へ縮退させること。
	ParentKeys map[int64]string
}

// GetIssueDetail は課題キーで課題 1 件の詳細をローカル DB から返す(API は呼ばない)。
//
// 見つからない場合はエラーを返す(画面がポップアップに理由を表示できるようにする)。
// 親課題キーの引き当ては、必要な 1 件だけを引く(プロジェクト全体の対応表を作る
// ListIssueKeysByID は、詳細を 1 件開くたびに全課題を走査してしまうため使わない)。
//
// 課題本体と親課題キーの 2 クエリは 1 つの読み取りトランザクションで実行する
// (中 2 と同じ流儀)。間に同期の書き込みが割り込むと、親課題だけが削除・改名された
// 中途半端なスナップショットを表示してしまうため。
func (s *ProfileService) GetIssueDetail(ctx context.Context, profileID string, projectID int64, issueKey string) (*IssueDetail, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	return s.issueDetailLocked(ctx, profileID, projectID, issueKey)
}

// issueDetailLocked は profileMu 取得済みの前提で課題詳細を読む
// (公開メソッドから再度 RLock を取ると自己デッドロックしうるため分離する。
// masterDataLocked と同じ流儀)。
func (s *ProfileService) issueDetailLocked(ctx context.Context, profileID string, projectID int64, issueKey string) (*IssueDetail, error) {
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	var detail IssueDetail
	err = st.WithReadTx(ctx, func(tx *sql.Tx) error {
		issue, err := store.GetIssueByKey(ctx, tx, projectID, issueKey)
		if err != nil {
			return err
		}
		if issue == nil {
			// 課題キーはエラーメッセージに含めない(画面側が対象を表示しているため
			// 不要であり、動作ログのマスク方針とも揃える)
			return errors.New("課題がローカルに見つかりません(同期後に削除されたか、まだ同期されていません)")
		}
		keys := map[int64]string{}
		if parentID := store.ParentIssueID(issue.RawJSON); parentID != 0 {
			parentKey, err := store.GetIssueKeyByID(ctx, tx, projectID, parentID)
			if err != nil {
				return err
			}
			if parentKey != "" {
				keys[parentID] = parentKey
			}
		}
		detail = IssueDetail{Issue: issue, ParentKeys: keys}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &detail, nil
}

// RefreshIssue は課題 1 件を Backlog から取得し直してローカル DB へ反映し、
// 反映後の詳細を返す(画面 2 の課題詳細ポップアップ「最新の状態を取得」)。
//
// 反映と読み直しを 1 往復にまとめているのは、画面が「取得 → 再表示」を
// 2 回のバインディング呼び出しに分けなくて済むようにするため。
//
// ロック: ローカル DB への書き込みを伴うため、同期・一括更新と同じ
// 直列化(profileMu → syncMu)を行う。反映後の読み直しも syncMu を保持したまま
// 行い、書いた内容と表示内容が食い違わないようにする。
//
// 同期状態(sync_state)は更新しない(理由は syncpkg.Engine.RefreshIssue を参照)。
func (s *ProfileService) RefreshIssue(ctx context.Context, profileID string, projectID int64, issueKey string) (*IssueDetail, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	// ローカル DB への書き込みを直列化する(SQLite の接続数は 1)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	engine, err := s.engineForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshIssue(ctx, projectID, issueKey); err != nil {
		return nil, err
	}
	return s.issueDetailLocked(ctx, profileID, projectID, issueKey)
}

// IterateIssues はローカル DB の課題を条件に一致した順(ID 昇順)で
// 1 件ずつ visit へ渡す(R4)。Excel 出力・テンプレート出力のように
// 「条件一致全件を逐次書き出す」用途で使い、全件をメモリに保持しない。
//
// 呼び出しの約束(store.IterateIssues と同じ):
//   - visit の中からローカル DB を触らないこと(接続 1 本 + 読み取り Tx 保持中の
//     ためデッドロックする)。
//   - 件数上限は visit がエラーを返して打ち切ること(IssueFilter.Limit は無視される)。
//
// 走査中はプロファイルのライフサイクルと排他し(profileMu.RLock)、
// 読み取りトランザクションを保持し続ける。出力ファイルの書き込み時間ぶん
// 同期がブロックされるが、途中でスナップショットが変わって行が重複・欠落する
// 方が害が大きいためこの設計にしている。
func (s *ProfileService) IterateIssues(ctx context.Context, profileID string,
	filter store.IssueFilter, visit store.IssueVisitor) (store.IssueIterateResult, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return store.IssueIterateResult{}, err
	}
	return st.IterateIssues(ctx, filter, visit)
}

// ListFilterOptions は抽出条件の候補(状態・担当者)をローカル DB から返す。
func (s *ProfileService) ListFilterOptions(ctx context.Context, profileID string, projectID int64) (*store.FilterOptions, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListFilterOptions(ctx, projectID)
}

// ListSyncStates は全同期状態を返す(同期状態画面・プロジェクト一覧の鮮度表示用)。
//
// 種別・プロジェクト単位の取得メソッドは置いていない。呼び出し側(画面)は
// 一覧をまとめて受け取り、必要な行を選ぶ。プロジェクトごとに引くと
// プロジェクト数ぶんのクエリになるため(R18)。
func (s *ProfileService) ListSyncStates(ctx context.Context, profileID string) ([]store.SyncState, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListSyncStates(ctx)
}
