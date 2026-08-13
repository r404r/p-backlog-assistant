package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestNormalizeSearchText(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// ASCII 小文字化
		{"ABC", "abc"},
		// 全角英字(ＡＢＣ)→ NFKC で半角化 + フォールド
		{"ＡＢＣ", "abc"},
		// 全角数字(１２３)→ 半角
		{"１２３", "123"},
		// 半角カナ(ﾊﾞｸﾞ)→ 全角(バグ)
		{"ﾊﾞｸﾞ", "バグ"},
		// 日本語はそのまま
		{"テスト", "テスト"},
		// 組文字(㈱)の展開 → (株)
		{"㈱テスト", "(株)テスト"},
		// 混在
		{"Bug ﾊﾞｸﾞ １２３", "bug バグ 123"},
		// Unicode ケースフォールド: ß → ss(strings.ToLower では変換されない)
		{"ß", "ss"},
		{"STRAßE", "strasse"},
		// 大文字エスツェット(ẞ)も ss
		{"ẞ", "ss"},
		// ギリシャ文字の語末シグマ(ς)は σ に統一される
		{"Σ ς", "σ σ"},
	}
	for _, c := range cases {
		if got := NormalizeSearchText(c.in); got != c.want {
			t.Errorf("NormalizeSearchText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUpsertIssue_GeneratesSearchText(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	issue := &Issue{
		ID:          101,
		IssueKey:    "EX-1",
		ProjectID:   1,
		Summary:     "ログイン画面のバグ",
		Description: "IE11 で ＴＩＭＥＯＵＴ が発生する", // 全角 ＴＩＭＥＯＵＴ
		StatusName:  "処理中",
		Updated:     "2026-01-01T00:00:00Z",
		RawJSON:     `{"id":101}`,
		FetchedAt:   "2026-01-02T00:00:00Z",
	}
	if err := s.UpsertIssue(ctx, issue); err != nil {
		t.Fatal(err)
	}

	got, err := issueSearchText(ctx, s.DB(), 101)
	if err != nil {
		t.Fatal(err)
	}
	// 先頭に課題キー(小文字化済み)が入り、
	// 全角 ＴＩＭＥＯＵＴ → 半角小文字 timeout に正規化されている
	want := "ex-1\nログイン画面のバグ\nie11 で timeout が発生する"
	if got != want {
		t.Errorf("search_text = %q, want %q", got, want)
	}
}

func TestUpsertIssue_UpdateRegeneratesSearchText(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	i := &Issue{ID: 201, IssueKey: "EX-2", ProjectID: 1, Summary: "旧タイトル"}
	if err := s.UpsertIssue(ctx, i); err != nil {
		t.Fatal(err)
	}
	i.Summary = "新タイトル ABC"
	if err := s.UpsertIssue(ctx, i); err != nil {
		t.Fatal(err)
	}

	got, err := issueSearchText(ctx, s.DB(), 201)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ex-2\n新タイトル abc\n" {
		t.Errorf("更新後の search_text = %q", got)
	}
	// UPSERT なので行は 1 行のまま
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM issues WHERE issue_key = 'EX-2'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("行数 = %d, want 1", count)
	}
}

func TestSearchIssueIDs_NormalizedMatch(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	issues := []*Issue{
		// 全角大文字 ＴＩＭＥＯＵＴ
		{ID: 1, IssueKey: "EX-10", ProjectID: 1, Summary: "ＴＩＭＥＯＵＴ エラー"},
		// 半角カナ ﾊﾞｸﾞ
		{ID: 2, IssueKey: "EX-11", ProjectID: 1, Summary: "ﾊﾞｸﾞ修正"},
		{ID: 3, IssueKey: "EX-12", ProjectID: 1, Summary: "関係ない課題"},
	}
	for _, i := range issues {
		if err := s.UpsertIssue(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	// 半角小文字の検索語が全角大文字由来の行に一致する
	ids, err := s.SearchIssueIDs(ctx, "timeout")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("timeout の検索結果 = %v, want [1]", ids)
	}

	// 全角カナ(バグ)の検索語が半角カナ由来の行に一致する
	ids, err = s.SearchIssueIDs(ctx, "バグ")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("バグ の検索結果 = %v, want [2]", ids)
	}

	// LIKE メタ文字はエスケープされる(% で全件ヒットしない)
	ids, err = s.SearchIssueIDs(ctx, "%")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("%% の検索結果 = %v, want []", ids)
	}
}

// TestSearchIssueIDs_MatchesIssueKey は課題キーがキーワード検索の対象に
// 含まれること(スペース横断検索でも同じ規則が効くこと)を確認する。
func TestSearchIssueIDs_MatchesIssueKey(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	issues := []*Issue{
		{ID: 1, IssueKey: "EXA-10", ProjectID: 1, Summary: "ログイン不具合"},
		{ID: 2, IssueKey: "EXB-10", ProjectID: 2, Summary: "画面崩れ"},
	}
	for _, i := range issues {
		if err := s.UpsertIssue(ctx, i); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name    string
		keyword string
		want    []int64
	}{
		{"課題キーの完全一致", "EXA-10", []int64{1}},
		{"小文字入力でも一致する", "exa-10", []int64{1}},
		{"課題キーの部分一致", "a-10", []int64{1}},
		{"プロジェクトキー部分は同一プロジェクトの全課題に一致", "exb", []int64{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.SearchIssueIDs(ctx, tc.keyword)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("キーワード %q の結果 = %v, want %v", tc.keyword, got, tc.want)
			}
		})
	}
}

func TestWithTx_CommitAndRollback(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	// コミット: トランザクション内の複数 UPSERT が反映される
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := UpsertIssue(ctx, tx, &Issue{ID: 1, IssueKey: "EX-1", ProjectID: 1, Summary: "a"}); err != nil {
			return err
		}
		return UpsertIssue(ctx, tx, &Issue{ID: 2, IssueKey: "EX-2", ProjectID: 1, Summary: "b"})
	})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := s.SearchIssueIDs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("コミット後の件数 = %d, want 2", len(ids))
	}

	// ロールバック: fn がエラーを返したら変更は反映されない
	wantErr := errors.New("故意の失敗")
	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := UpsertIssue(ctx, tx, &Issue{ID: 3, IssueKey: "EX-3", ProjectID: 1, Summary: "c"}); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithTx のエラー = %v, want %v", err, wantErr)
	}
	ids, err = s.SearchIssueIDs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("ロールバック後の件数 = %d, want 2(ID=3 は反映されない)", len(ids))
	}
}
