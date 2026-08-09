package store

import (
	"context"
	"testing"
)

func TestSyncState_GetMissingReturnsNil(t *testing.T) {
	s := openTempStore(t)
	got, err := s.GetSyncState(context.Background(), DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("未記録の sync_state = %+v, want nil", got)
	}
}

func TestSyncState_Upsert(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	st := &SyncState{
		DataKind: DataKindIssues, ProjectID: 1,
		LastSyncedAt: "2026-08-09T00:00:00Z", LastSyncDate: "2026-08-08",
		ActivityCursor: 100, ActivityStartPending: 200,
	}
	if err := s.UpsertSyncState(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *st {
		t.Errorf("got = %+v, want %+v", got, st)
	}

	// プロジェクトごとに独立していること
	if other, err := s.GetSyncState(ctx, DataKindIssues, 2); err != nil || other != nil {
		t.Errorf("別プロジェクトの状態 = %+v, %v", other, err)
	}
}

// TestSetActivityStartPending_KeepsCursor は開始境界の保存が
// 確定済みカーソルを前進させないことを確認する(異常終了時のやり直しのため)。
func TestSetActivityStartPending_KeepsCursor(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertSyncState(ctx, &SyncState{
		DataKind: DataKindIssues, ProjectID: 1, ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetActivityStartPending(ctx, DataKindIssues, 1, 500); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActivityCursor != 100 {
		t.Errorf("ActivityCursor = %d, want 100(前進してはいけない)", got.ActivityCursor)
	}
	if got.ActivityStartPending != 500 {
		t.Errorf("ActivityStartPending = %d, want 500", got.ActivityStartPending)
	}
}

func TestPromotePendingCursor(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertSyncState(ctx, &SyncState{
		DataKind: DataKindIssues, ProjectID: 1, ActivityCursor: 100, ActivityStartPending: 500,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PromotePendingCursor(ctx, DataKindIssues, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActivityCursor != 500 || got.ActivityStartPending != 0 {
		t.Errorf("昇格後 = cursor %d / pending %d, want 500 / 0", got.ActivityCursor, got.ActivityStartPending)
	}

	// pending が無い場合はカーソルを変更しない(取得失敗時の巻き戻し防止)
	if err := s.PromotePendingCursor(ctx, DataKindIssues, 1); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSyncState(ctx, DataKindIssues, 1)
	if got.ActivityCursor != 500 {
		t.Errorf("pending 無しで cursor が変化した: %d", got.ActivityCursor)
	}
}

func TestSetActivityCursor(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.SetActivityCursor(ctx, DataKindIssues, 1, 700); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ActivityCursor != 700 {
		t.Errorf("ActivityCursor = %+v, want 700", got)
	}
}

func TestSetSyncCompleted(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertSyncState(ctx, &SyncState{
		DataKind: DataKindIssues, ProjectID: 1, ActivityCursor: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSyncCompleted(ctx, DataKindIssues, 1, "2026-08-09T10:00:00Z", "2026-08-09"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSyncState(ctx, DataKindIssues, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSyncedAt != "2026-08-09T10:00:00Z" || got.LastSyncDate != "2026-08-09" {
		t.Errorf("got = %+v", got)
	}
	if got.ActivityCursor != 100 {
		t.Errorf("完了時刻の保存でカーソルが変化した: %d", got.ActivityCursor)
	}
}
