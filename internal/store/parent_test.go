package store

import (
	"context"
	"testing"
)

// TestParentIssueRef は親課題の 3 状態(親なし / 親あり / 確認不能)を確認する。
//
// 「確認不能」を「親なし」に潰すと、一括更新の 1 階層検証が黙って素通りして
// しまうため(孫の許可・子の見落とし・#CLEAR# の黙殺)、状態として区別する。
func TestParentIssueRef(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantID    int64
		wantState ParentState
	}{
		{"親あり", `{"id":101,"parentIssueId":100}`, 100, ParentSet},
		{"親なし(null)", `{"id":101,"parentIssueId":null}`, 0, ParentNone},
		{"親なし(0)", `{"id":101,"parentIssueId":0}`, 0, ParentNone},
		{"項目なし", `{"id":101}`, 0, ParentUnknown},
		{"空文字", "", 0, ParentUnknown},
		{"壊れた JSON", "{壊れた", 0, ParentUnknown},
		{"数値でない", `{"parentIssueId":"100"}`, 0, ParentUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, state := ParentIssueRef(c.raw)
			if id != c.wantID || state != c.wantState {
				t.Errorf("ParentIssueRef(%q) = %d, %v; want %d, %v", c.raw, id, state, c.wantID, c.wantState)
			}
			// 表示用の縮退版は確認不能・親なしをどちらも 0 にする
			if got := ParentIssueID(c.raw); got != c.wantID {
				t.Errorf("ParentIssueID(%q) = %d, want %d", c.raw, got, c.wantID)
			}
		})
	}
}

func TestListIssueParents(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 100, IssueKey: "EXA-1", ProjectID: 1, Summary: "親", RawJSON: `{"id":100,"parentIssueId":null}`},
		{ID: 101, IssueKey: "EXA-2", ProjectID: 1, Summary: "子", RawJSON: `{"id":101,"parentIssueId":100}`},
		// 生 JSON を持たない古いキャッシュは親なしとして扱う
		{ID: 102, IssueKey: "EXA-3", ProjectID: 1, Summary: "生 JSON なし"},
		// 別プロジェクトは含めない
		{ID: 200, IssueKey: "EXB-1", ProjectID: 2, Summary: "別件", RawJSON: `{"id":200}`},
	}); err != nil {
		t.Fatal(err)
	}
	// 論理削除済みは含めない
	if err := s.UpsertIssue(ctx, &Issue{ID: 103, IssueKey: "EXA-4", ProjectID: 1, Summary: "削除"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkIssueDeletedByID(ctx, 1, 103); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListIssueParents(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []IssueParent{
		{ID: 100, IssueKey: "EXA-1", ParentIssueID: 0, State: ParentNone},
		{ID: 101, IssueKey: "EXA-2", ParentIssueID: 100, State: ParentSet},
		// 生 JSON が無い課題は「親なし」ではなく「確認不能」
		{ID: 102, IssueKey: "EXA-3", ParentIssueID: 0, State: ParentUnknown},
	}
	if len(got) != len(want) {
		t.Fatalf("件数 = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
