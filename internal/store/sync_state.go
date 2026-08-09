package store

import (
	"context"
	"database/sql"
)

// データ種別(sync_state.data_kind)。
const (
	DataKindIssues   = "issues"
	DataKindProjects = "projects"
	DataKindUsers    = "users"
	DataKindTeams    = "teams"
)

// ProjectScopeAll はプロジェクトに紐づかないデータ種別で使う project_id。
const ProjectScopeAll int64 = 0

// SyncState は同期状態 1 行(設計書 2 節)。
//
// ActivityCursor と ActivityStartPending は必ず分離して扱う:
//   - ActivityCursor: 確定済みカーソル。削除反映と同一トランザクションでのみ前進する。
//   - ActivityStartPending: 実行中フル同期の開始境界。完了時に ActivityCursor へ昇格する。
//
// 途中で異常終了した場合は ActivityCursor を前進させない(次回フル同期をやり直す)。
type SyncState struct {
	DataKind             string `json:"dataKind"`
	ProjectID            int64  `json:"projectId"`
	LastSyncedAt         string `json:"lastSyncedAt"`
	LastSyncDate         string `json:"lastSyncDate"`
	ActivityCursor       int64  `json:"activityCursor"`
	ActivityStartPending int64  `json:"activityStartPending"`
}

// GetSyncState は同期状態を返す。未記録の場合は (nil, nil) を返す。
func GetSyncState(ctx context.Context, q dbtx, dataKind string, projectID int64) (*SyncState, error) {
	var (
		lastSyncedAt sql.NullString
		lastSyncDate sql.NullString
		cursor       sql.NullInt64
		pending      sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `
		SELECT last_synced_at, last_sync_date, activity_cursor, activity_start_pending
		FROM sync_state WHERE data_kind = ? AND project_id = ?`,
		dataKind, projectID).Scan(&lastSyncedAt, &lastSyncDate, &cursor, &pending)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &SyncState{
		DataKind:             dataKind,
		ProjectID:            projectID,
		LastSyncedAt:         lastSyncedAt.String,
		LastSyncDate:         lastSyncDate.String,
		ActivityCursor:       cursor.Int64,
		ActivityStartPending: pending.Int64,
	}, nil
}

// GetSyncState は Store 直接実行版。
func (s *Store) GetSyncState(ctx context.Context, dataKind string, projectID int64) (*SyncState, error) {
	return GetSyncState(ctx, s.db, dataKind, projectID)
}

// UpsertSyncState は同期状態を全項目まとめて UPSERT する。
func UpsertSyncState(ctx context.Context, q dbtx, st *SyncState) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO sync_state (
			data_kind, project_id, last_synced_at, last_sync_date,
			activity_cursor, activity_start_pending)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(data_kind, project_id) DO UPDATE SET
			last_synced_at = excluded.last_synced_at,
			last_sync_date = excluded.last_sync_date,
			activity_cursor = excluded.activity_cursor,
			activity_start_pending = excluded.activity_start_pending`,
		st.DataKind, st.ProjectID, st.LastSyncedAt, st.LastSyncDate,
		st.ActivityCursor, st.ActivityStartPending)
	return err
}

// UpsertSyncState は Store 直接実行版。
func (s *Store) UpsertSyncState(ctx context.Context, st *SyncState) error {
	return UpsertSyncState(ctx, s.db, st)
}

// ensureSyncStateRow は行が無ければ既定値で作成する(部分更新の前提を作る)。
func ensureSyncStateRow(ctx context.Context, q dbtx, dataKind string, projectID int64) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO sync_state (data_kind, project_id, last_synced_at, last_sync_date,
			activity_cursor, activity_start_pending)
		VALUES (?, ?, '', '', 0, 0)
		ON CONFLICT(data_kind, project_id) DO NOTHING`, dataKind, projectID)
	return err
}

// SetActivityStartPending はフル同期の開始境界を保存する。
// 確定済みカーソル(activity_cursor)は変更しない。
func SetActivityStartPending(ctx context.Context, q dbtx, dataKind string, projectID, activityID int64) error {
	if err := ensureSyncStateRow(ctx, q, dataKind, projectID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`UPDATE sync_state SET activity_start_pending = ? WHERE data_kind = ? AND project_id = ?`,
		activityID, dataKind, projectID)
	return err
}

// SetActivityStartPending は Store 直接実行版。
func (s *Store) SetActivityStartPending(ctx context.Context, dataKind string, projectID, activityID int64) error {
	return SetActivityStartPending(ctx, s.db, dataKind, projectID, activityID)
}

// PromotePendingCursor は activity_start_pending を activity_cursor へ昇格し、
// pending をクリアする(フル同期の正常完了時)。
// pending が 0(未取得・取得失敗)の場合はカーソルを変更しない。
func PromotePendingCursor(ctx context.Context, q dbtx, dataKind string, projectID int64) error {
	_, err := q.ExecContext(ctx, `
		UPDATE sync_state
		SET activity_cursor = activity_start_pending, activity_start_pending = 0
		WHERE data_kind = ? AND project_id = ?
		  AND activity_start_pending IS NOT NULL AND activity_start_pending > 0`,
		dataKind, projectID)
	return err
}

// PromotePendingCursor は Store 直接実行版。
func (s *Store) PromotePendingCursor(ctx context.Context, dataKind string, projectID int64) error {
	return PromotePendingCursor(ctx, s.db, dataKind, projectID)
}

// SetActivityCursor は確定済みカーソルを設定する。
// 差分同期では削除反映と同一トランザクション内で呼ぶこと(カーソルだけ先行すると
// 異常終了時に未反映の削除イベントを永久に飛ばすため。設計書 3 節)。
func SetActivityCursor(ctx context.Context, q dbtx, dataKind string, projectID, cursor int64) error {
	if err := ensureSyncStateRow(ctx, q, dataKind, projectID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`UPDATE sync_state SET activity_cursor = ? WHERE data_kind = ? AND project_id = ?`,
		cursor, dataKind, projectID)
	return err
}

// SetActivityCursor は Store 直接実行版。
func (s *Store) SetActivityCursor(ctx context.Context, dataKind string, projectID, cursor int64) error {
	return SetActivityCursor(ctx, s.db, dataKind, projectID, cursor)
}

// SetSyncCompleted は同期完了時刻と次回 updatedSince 用の日付を保存する。
// カーソル類は変更しない。
func SetSyncCompleted(ctx context.Context, q dbtx, dataKind string, projectID int64, syncedAt, syncDate string) error {
	if err := ensureSyncStateRow(ctx, q, dataKind, projectID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		`UPDATE sync_state SET last_synced_at = ?, last_sync_date = ?
		 WHERE data_kind = ? AND project_id = ?`,
		syncedAt, syncDate, dataKind, projectID)
	return err
}

// SetSyncCompleted は Store 直接実行版。
func (s *Store) SetSyncCompleted(ctx context.Context, dataKind string, projectID int64, syncedAt, syncDate string) error {
	return SetSyncCompleted(ctx, s.db, dataKind, projectID, syncedAt, syncDate)
}
