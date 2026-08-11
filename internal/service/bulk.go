package service

import (
	"context"
	"sync/atomic"

	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/store"
)

// 一括更新・追加(画面 3)のユースケース。
//
// ロック規約は同期系と同じ(profileMu → syncMu)。実行はローカル DB への
// 書き込みを伴うため syncMu を取り、同期処理と直列化する。
// CancelBulkRun だけは実行中に呼ばれるため、どのロックも取らない
// (取ると実行完了まで待たされ、キャンセルの意味が無くなる)。

// GetMasterData は種別・優先度・状態のマスタを取得する(取り込み前の検証用)。
func (s *ProfileService) GetMasterData(ctx context.Context, profileID string, projectID int64) (*bulk.MasterData, error) {
	// プロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	return s.masterDataLocked(ctx, profileID, projectID)
}

// masterDataLocked は profileMu 取得済みの前提でマスタを取得する
// (公開メソッドから再度 RLock を取ると自己デッドロックしうるため分離する)。
func (s *ProfileService) masterDataLocked(ctx context.Context, profileID string, projectID int64) (*bulk.MasterData, error) {
	client, err := s.clientForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return bulk.FetchMasterData(ctx, client, projectID)
}

// ListAssigneeCandidates は担当者として指定できるユーザを返す
// (テンプレートの「マスタ」シートに載せる候補。取り込み時の検証と同じ集合)。
// 対象プロジェクトの参加者が未同期(0 件)の場合はスペース全体のユーザを返す。
func (s *ProfileService) ListAssigneeCandidates(ctx context.Context, profileID string, projectID int64) ([]store.UserRef, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	users, _, err := bulk.AssigneeCandidates(ctx, st, projectID)
	return users, err
}

// ImportBulkFile は記入済み Excel を取り込み、検証・dry-run 差分を行って
// ジョブを作成する(設計書 5 節)。エラー行がある場合はジョブを作らず、
// 行番号付きのエラー一覧を返す。
//
// defaultPriorityID は優先度未入力の新規追加行に適用する既定値
// (取り込み時のダイアログでプロジェクト単位に指定する)。
func (s *ProfileService) ImportBulkFile(ctx context.Context, profileID string, projectID int64, filePath string, defaultPriorityID int64) (*bulk.ImportResult, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	// ジョブ行の書き込みを同期処理と直列化する(SQLite の接続数は 1)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	master, err := s.masterDataLocked(ctx, profileID, projectID)
	if err != nil {
		return nil, err
	}
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	// 親課題の状態確認(CF5。ローカルに無い ID:<数値> の親)に API を使う
	client, err := s.clientForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return bulk.NewImporter(st).Import(ctx, bulk.ImportOptions{
		ProjectID:         projectID,
		FilePath:          filePath,
		DefaultPriorityID: defaultPriorityID,
		Master:            *master,
		API:               client,
	})
}

// ListIssueKeysByID は指定プロジェクトの「課題 ID → 課題キー」を返す(CF5)。
//
// 親課題 ID を課題キーへ引き当てるために、課題抽出・テンプレート出力の両方で使う
// (引き当てられない親は ID:<数値> 形式で出力する)。API は呼ばない。
func (s *ProfileService) ListIssueKeysByID(ctx context.Context, profileID string, projectID int64) (map[int64]string, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	refs, err := st.ListIssueRefs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys := make(map[int64]string, len(refs))
	for _, r := range refs {
		keys[r.ID] = r.IssueKey
	}
	return keys, nil
}

// RunBulkJob はジョブの未処理行を実行する(進捗は onProgress へ通知)。
// 中断・再開のため、行ごとの状態は実行中に逐次 DB へ記録される。
func (s *ProfileService) RunBulkJob(ctx context.Context, profileID string, jobID int64, opts bulk.RunOptions, onProgress bulk.ProgressFunc) (*bulk.RunResult, error) {
	// プロファイルの削除・保存と排他する(高 2。ロック順序 profileMu → syncMu)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	// 書き込み処理のため同期と直列化する
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	client, err := s.clientForProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	key := bulkRunKey{profileID: profileID, jobID: jobID}
	canceled := s.registerCancel(key)
	defer s.clearCancel(key)

	return bulk.NewEngine(client, st).Run(ctx, jobID, opts, canceled.Load, onProgress)
}

// CancelBulkRun は実行中のジョブへ中断を指示する。
// 実行エンジンは行と行の間でこのフラグを確認し、送信済みの行を
// 中途半端な状態にせず停止する(未処理行は pending のまま残る)。
//
// 対象はプロファイル ID + ジョブ ID で特定する(中 2)。ジョブ ID は
// プロファイルごとの採番のため、ID だけで指示すると別プロファイルで実行中の
// 同番ジョブを巻き添えで中断しうる。
//
// 実行開始前に呼ばれた場合もフラグは保持され、その実行が即座に中断される
// (フラグは実行終了時に破棄されるため、次の実行には持ち越さない)。
func (s *ProfileService) CancelBulkRun(profileID string, jobID int64) {
	s.registerCancel(bulkRunKey{profileID: profileID, jobID: jobID}).Store(true)
}

// registerCancel は実行用のキャンセルフラグを用意する
// (実行開始前に届いた指示を取りこぼさないよう、既存エントリがあれば流用する)。
func (s *ProfileService) registerCancel(key bulkRunKey) *atomic.Bool {
	s.bulkMu.Lock()
	defer s.bulkMu.Unlock()
	if s.bulkCancels == nil {
		s.bulkCancels = map[bulkRunKey]*atomic.Bool{}
	}
	if flag, ok := s.bulkCancels[key]; ok {
		return flag
	}
	flag := &atomic.Bool{}
	s.bulkCancels[key] = flag
	return flag
}

// clearCancel は実行終了後にキャンセルフラグを破棄する。
func (s *ProfileService) clearCancel(key bulkRunKey) {
	s.bulkMu.Lock()
	defer s.bulkMu.Unlock()
	delete(s.bulkCancels, key)
}

// ListBulkJobs はジョブ一覧を新しい順に返す(行数集計付き。API は呼ばない)。
func (s *ProfileService) ListBulkJobs(ctx context.Context, profileID string) ([]store.JobSummary, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListJobs(ctx)
}

// GetBulkJobRows はジョブの行明細を返す(結果レポートの作成用)。
func (s *ProfileService) GetBulkJobRows(ctx context.Context, profileID string, jobID int64) ([]store.JobRow, error) {
	// store を使う操作はプロファイルの削除・保存と排他する(高 2)
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	st, err := s.storeForProfile(profileID)
	if err != nil {
		return nil, err
	}
	return st.ListJobRows(ctx, jobID)
}
