package store

import "context"

// ListSyncStates は全同期状態を data_kind, project_id の昇順で返す(同期状態画面用)。
func (s *Store) ListSyncStates(ctx context.Context) ([]SyncState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT data_kind, project_id, last_synced_at, last_sync_date,
		       activity_cursor, activity_start_pending
		FROM sync_state
		ORDER BY data_kind, project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var states []SyncState
	for rows.Next() {
		var st SyncState
		if err := rows.Scan(&st.DataKind, &st.ProjectID, &st.LastSyncedAt,
			&st.LastSyncDate, &st.ActivityCursor, &st.ActivityStartPending); err != nil {
			return nil, err
		}
		states = append(states, st)
	}
	return states, rows.Err()
}
