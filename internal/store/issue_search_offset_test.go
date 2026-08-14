package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"backlog-assistant/internal/customfield"
)

// seedNumberedIssues は ID 1..n の課題を投入する(ページングの並び確認用)。
// summary には keyword をそのまま含め、FTS 経路の検証にも使えるようにする。
func seedNumberedIssues(t *testing.T, s *Store, n int, keyword string) {
	t.Helper()
	issues := make([]*Issue, 0, n)
	for i := int64(1); i <= int64(n); i++ {
		issues = append(issues, &Issue{
			ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1,
			Summary: fmt.Sprintf("%s %d 件目", keyword, i),
		})
	}
	if err := s.UpsertIssues(context.Background(), issues); err != nil {
		t.Fatal(err)
	}
}

// issueIDsOf は結果の課題 ID を取り出す(比較のため)。
func issueIDsOf(res *IssueSearchResult) []int64 {
	out := make([]int64, 0, len(res.Issues))
	for _, i := range res.Issues {
		out = append(out, i.ID)
	}
	return out
}

// TestSearchIssues_OffsetSQLPath は SQL だけで完結する経路(カスタム属性条件なし)で
// Offset がページングとして働くことを確認する。
// Truncated は「この応答より後ろに一致行が残っているか」を意味する。
func TestSearchIssues_OffsetSQLPath(t *testing.T) {
	s := openTempStore(t)
	seedNumberedIssues(t, s, 10, "対象")
	ctx := context.Background()

	tests := []struct {
		name      string
		offset    int
		limit     int
		want      []int64
		truncated bool
	}{
		{"先頭ページ", 0, 3, []int64{1, 2, 3}, true},
		{"中間ページ", 3, 3, []int64{4, 5, 6}, true},
		{"最終ページ(半端)", 9, 3, []int64{10}, false},
		{"最終ページ(ちょうど)", 6, 4, []int64{7, 8, 9, 10}, false},
		{"total と同じ offset は空", 10, 3, nil, false},
		{"total を超える offset は空", 100, 3, nil, false},
		{"負の offset は 0 に丸める", -5, 3, []int64{1, 2, 3}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.SearchIssues(ctx,
				IssueFilter{ProjectID: 1, Limit: tt.limit, Offset: tt.offset})
			if err != nil {
				t.Fatal(err)
			}
			if got := issueIDsOf(res); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("課題 ID = %v, want %v", got, tt.want)
			}
			// 総件数は offset に影響されない
			if res.Total != 10 {
				t.Errorf("total = %d, want 10(offset に影響されない)", res.Total)
			}
			if res.Truncated != tt.truncated {
				t.Errorf("Truncated = %v, want %v(total %d / offset %d / 行数 %d)",
					res.Truncated, tt.truncated, res.Total, tt.offset, len(res.Issues))
			}
		})
	}
}

// TestSearchIssues_OffsetPagesCoverAllRows は連続したページをつなぐと
// 重複・欠落なく全件になることを確認する(ページ送りの一貫性)。
func TestSearchIssues_OffsetPagesCoverAllRows(t *testing.T) {
	s := openTempStore(t)
	seedNumberedIssues(t, s, 10, "対象")
	ctx := context.Background()

	var got []int64
	for offset := 0; offset < 10; offset += 4 {
		res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Limit: 4, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, issueIDsOf(res)...)
	}
	want := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ページを連結した結果 = %v, want %v", got, want)
	}
}

// seedOffsetCustomFieldIssues はカスタム属性経路の offset 検証用データを投入する。
// 一致・不一致・判定不能を交互に並べ、「一致行を基準に読み飛ばす」ことを
// SQL の行番号による読み飛ばしと区別できるようにする。
//
// ID の割り当て(3 件周期で 12 件):
//
//	ID % 3 == 1 … 対象外(不一致)
//	ID % 3 == 2 … 生 JSON なし(判定不能)
//	ID % 3 == 0 … ABC商事(一致)
func seedOffsetCustomFieldIssues(t *testing.T, s *Store) {
	t.Helper()
	issues := make([]*Issue, 0, 12)
	for i := int64(1); i <= 12; i++ {
		switch i % 3 {
		case 1:
			issues = append(issues, &Issue{
				ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "対象外",
				RawJSON: cfRawJSON(i, "対象外", 1, "2026-01-01", 52),
			})
		case 2:
			// 生 JSON なし = 判定不能
			issues = append(issues, &Issue{
				ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "生 JSON なし",
			})
		default:
			issues = append(issues, &Issue{
				ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "対象",
				RawJSON: cfRawJSON(i, "ABC商事", 8, "2026-08-01", 51),
			})
		}
	}
	if err := s.UpsertIssues(context.Background(), issues); err != nil {
		t.Fatal(err)
	}
}

// TestSearchIssues_OffsetCustomFieldPath はカスタム属性経路(2 段階検索)で
// Offset が「SQL の行」ではなく「一致行」を基準に読み飛ばすこと、
// Total / Unverifiable が offset に関わらず全走査で数えられることを確認する。
func TestSearchIssues_OffsetCustomFieldPath(t *testing.T) {
	s := openTempStore(t)
	seedOffsetCustomFieldIssues(t, s)
	ctx := context.Background()

	// 一致するのは ID 3, 6, 9, 12 の 4 件、判定不能は ID 2, 5, 8, 11 の 4 件
	tests := []struct {
		name      string
		offset    int
		limit     int
		want      []int64
		truncated bool
	}{
		{"先頭ページ", 0, 2, []int64{3, 6}, true},
		{"中間ページ(一致行を基準に読み飛ばす)", 1, 2, []int64{6, 9}, true},
		{"最終ページ(半端)", 3, 2, []int64{12}, false},
		{"total と同じ offset は空", 4, 2, nil, false},
		{"total を超える offset は空", 50, 2, nil, false},
		{"負の offset は 0 に丸める", -3, 2, []int64{3, 6}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.SearchIssues(ctx, IssueFilter{
				ProjectID: 1,
				Limit:     tt.limit,
				Offset:    tt.offset,
				CustomFieldFilters: []customfield.Filter{
					{DefID: 1, TypeID: customfield.TypeText, Text: "ABC商事"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := issueIDsOf(res); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("課題 ID = %v, want %v", got, tt.want)
			}
			if res.Total != 4 {
				t.Errorf("total = %d, want 4(offset に関わらず全走査で数える)", res.Total)
			}
			if res.Unverifiable != 4 {
				t.Errorf("Unverifiable = %d, want 4(offset に関わらず全走査で数える)", res.Unverifiable)
			}
			if res.Truncated != tt.truncated {
				t.Errorf("Truncated = %v, want %v(total %d / offset %d / 行数 %d)",
					res.Truncated, tt.truncated, res.Total, tt.offset, len(res.Issues))
			}
		})
	}
}

// TestSearchIssues_OffsetFTSPathMatchesSQLPath は FTS 索引を使う経路
// (3 文字以上のキーワード)でも offset の意味・並びが SQL 経路と一致することを
// 確認する。FTS 経路は FROM 句と ORDER BY が変わるため、別経路として検証する。
func TestSearchIssues_OffsetFTSPathMatchesSQLPath(t *testing.T) {
	s := openTempStore(t)
	// 全課題が「ログイン」を含むため、キーワードあり / なしで同じ集合になる
	seedNumberedIssues(t, s, 10, "ログイン")
	ctx := context.Background()

	// キーワードが FTS 索引経路に乗ることを確かめる(乗らないと検証にならない)
	spec, err := IssueFilter{ProjectID: 1, Keyword: "ログイン"}.buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.from, "issues_fts") {
		t.Fatalf("FTS 経路になっていない: from = %q", spec.from)
	}

	for offset := 0; offset <= 10; offset += 3 {
		sqlRes, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Limit: 3, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		ftsRes, err := s.SearchIssues(ctx,
			IssueFilter{ProjectID: 1, Keyword: "ログイン", Limit: 3, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := issueIDsOf(ftsRes), issueIDsOf(sqlRes); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("offset %d: FTS 経路の課題 ID = %v, want %v(SQL 経路と一致)", offset, got, want)
		}
		if ftsRes.Total != sqlRes.Total || ftsRes.Truncated != sqlRes.Truncated {
			t.Errorf("offset %d: FTS 経路 total/truncated = %d/%v, want %d/%v",
				offset, ftsRes.Total, ftsRes.Truncated, sqlRes.Total, sqlRes.Truncated)
		}
	}
}
