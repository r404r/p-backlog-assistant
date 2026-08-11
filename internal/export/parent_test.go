package export

import (
	"testing"

	"backlog-assistant/internal/store"
)

// parentSampleIssues は親課題 ID を持つ課題(CF5)。
func parentSampleIssues() []store.Issue {
	return []store.Issue{
		// 同一プロジェクトの親 → 課題キーへ解決できる
		{ID: 2, IssueKey: "EX-2", Summary: "子課題", RawJSON: `{"id":2,"parentIssueId":1}`},
		// 未同期・別プロジェクトの親 → ID:<数値> へ縮退
		{ID: 3, IssueKey: "EX-3", Summary: "別プロジェクトの子", RawJSON: `{"id":3,"parentIssueId":9999}`},
		// 親なし
		{ID: 4, IssueKey: "EX-4", Summary: "親なし", RawJSON: `{"id":4,"parentIssueId":null}`},
		// 生 JSON なし(旧キャッシュ)
		{ID: 5, IssueKey: "EX-5", Summary: "生 JSON なし"},
	}
}

func TestExportIssues_ParentIssueKeyColumn(t *testing.T) {
	opts := Options{
		Columns:         []string{"issueKey", "parentIssueKey"},
		ParentIssueKeys: map[int64]string{1: "EX-1", 2: "EX-2"},
	}
	path := exportToTempFile(t, parentSampleIssues(), opts)
	f := openExported(t, path)

	got, err := f.GetRows(SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"キー", "親課題キー"},
		{"EX-2", "EX-1"},
		{"EX-3", "ID:9999"},
		{"EX-4"},
		{"EX-5"},
	}
	if len(got) != len(want) {
		t.Fatalf("行数 = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if !equalStrings(got[i], want[i]) {
			t.Errorf("行 %d = %v, want %v", i+1, got[i], want[i])
		}
	}
}

// TestDefaultColumns_ExcludesParentIssueKey は親課題キーが既定列に含まれない
// ことを確認する(利用者が明示的に選んだときだけ出力する)。
func TestDefaultColumns_ExcludesParentIssueKey(t *testing.T) {
	for _, k := range DefaultColumns() {
		if k == ParentIssueKeyColumn {
			t.Fatalf("既定列に %s が含まれている: %v", ParentIssueKeyColumn, DefaultColumns())
		}
	}
	// 指定可能な列には含まれる
	found := false
	for _, k := range AvailableColumns(nil) {
		if k == ParentIssueKeyColumn {
			found = true
		}
	}
	if !found {
		t.Errorf("指定可能な列に %s が無い: %v", ParentIssueKeyColumn, AvailableColumns(nil))
	}
	if h, ok := ColumnHeader(ParentIssueKeyColumn, nil); !ok || h != "親課題キー" {
		t.Errorf("ColumnHeader(%s) = %q, %v", ParentIssueKeyColumn, h, ok)
	}
}

func TestFormatParentIssueRef(t *testing.T) {
	keys := map[int64]string{1: "EX-1"}
	cases := []struct {
		id   int64
		want string
	}{
		{0, ""},
		{1, "EX-1"},
		{7, "ID:7"},
	}
	for _, c := range cases {
		if got := FormatParentIssueRef(c.id, keys); got != c.want {
			t.Errorf("FormatParentIssueRef(%d) = %q, want %q", c.id, got, c.want)
		}
	}
	// キー表がなくても ID: 形式へ縮退する
	if got := FormatParentIssueRef(1, nil); got != "ID:1" {
		t.Errorf("キー表なし = %q, want \"ID:1\"", got)
	}
}

// TestParseParentIssueIDRef は「ID:<数値>」形式の判定を確認する。
//
// matched は接頭辞が一致したことだけを表し、値が不正な場合は id = 0 になる。
// 「ID 形式ではない(課題キー)」と「ID 形式だが値が不正」を呼び出し側が
// 区別できないと、ID:0 や ID:abc が課題キーとして再検索されてしまう。
func TestParseParentIssueIDRef(t *testing.T) {
	cases := []struct {
		in          string
		wantID      int64
		wantMatched bool
	}{
		{"ID:123", 123, true},
		{"id: 123", 123, true}, // 大小・空白の揺れを吸収する
		{"ＩＤ:123", 123, true},  // 全角で入力された場合
		{"ID:9999999", 9999999, true},
		{"EX-1", 0, false},   // 課題キー
		{"", 0, false},       //
		{"XID:12", 0, false}, // 接頭辞が一致しない
		// 接頭辞は一致するが値が不正(呼び出し側は書式エラーにする)
		{"ID:0", 0, true},
		{"ID:-1", 0, true},
		{"ID:abc", 0, true},
		{"ID:12 34", 0, true},
		{"ID:", 0, true},
		{"ID:99999999999999999999", 0, true}, // オーバーフロー
	}
	for _, c := range cases {
		id, matched := ParseParentIssueIDRef(c.in)
		if id != c.wantID || matched != c.wantMatched {
			t.Errorf("ParseParentIssueIDRef(%q) = %d, %v; want %d, %v", c.in, id, matched, c.wantID, c.wantMatched)
		}
	}
}
