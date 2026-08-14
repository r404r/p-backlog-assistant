package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"backlog-assistant/internal/customfield"
)

// visitKeys は IterateIssues が渡してきた課題キーを順に集める。
func visitKeys(out *[]string) IssueVisitor {
	return func(i *Issue) error {
		*out = append(*out, i.IssueKey)
		return nil
	}
}

// TestIterateIssues_RequiresProjectID は検索と同じくプロジェクト必須であることを確認する。
func TestIterateIssues_RequiresProjectID(t *testing.T) {
	s := openTempStore(t)
	if _, err := s.IterateIssues(context.Background(), IssueFilter{}, func(*Issue) error { return nil }); err == nil {
		t.Fatal("projectID 未指定でもエラーにならなかった")
	}
}

// TestValidateIssueFilter は、走査を始める前(= 保存ダイアログを出す前)に
// 条件の不備を検出できることを確認する。
func TestValidateIssueFilter(t *testing.T) {
	if err := ValidateIssueFilter(IssueFilter{ProjectID: 1, Keyword: "ログイン"}); err != nil {
		t.Errorf("正常な条件でエラー: %v", err)
	}
	if err := ValidateIssueFilter(IssueFilter{}); err == nil {
		t.Error("プロジェクト未指定でもエラーにならなかった")
	}
	// 数値型に範囲を逆転して指定した条件(customfield 側の検証に委ねる)
	err := ValidateIssueFilter(IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 3, TypeID: customfield.TypeNumeric, Min: "10", Max: "1"},
		},
	})
	if err == nil {
		t.Error("不正なカスタム属性条件でもエラーにならなかった")
	}
}

// TestIterateIssues_VisitsMatchingRowsInIDOrder は条件に一致する課題が
// ID 昇順で 1 件ずつ渡され、total が渡した件数と一致することを確認する。
func TestIterateIssues_VisitsMatchingRowsInIDOrder(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	var keys []string
	res, err := s.IterateIssues(context.Background(),
		IssueFilter{ProjectID: 1, Keyword: "ログイン"}, visitKeys(&keys))
	if err != nil {
		t.Fatal(err)
	}
	if !equalKeys(keys, []string{"EXA-1", "EXA-3"}) {
		t.Errorf("渡された課題 = %v, want [EXA-1 EXA-3]", keys)
	}
	if res.Total != 2 || res.Unverifiable != 0 {
		t.Errorf("結果 = %+v, want {Total:2 Unverifiable:0}", res)
	}
}

// TestIterateIssues_IgnoresLimit は Limit が走査を打ち切らないことを確認する。
// Excel 出力は「条件一致全件」を書き出すため、件数上限の判定は呼び出し側
// (visit の中)で行う契約になっている。
func TestIterateIssues_IgnoresLimit(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	var keys []string
	res, err := s.IterateIssues(context.Background(),
		IssueFilter{ProjectID: 1, Limit: 1}, visitKeys(&keys))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || res.Total != 3 {
		t.Errorf("渡された課題 = %v / total = %d, want 3 件", keys, res.Total)
	}
}

// TestIterateIssues_IgnoresOffset は Offset が走査の開始位置を動かさないことを
// 確認する(Limit 無視と同じ契約)。Excel 出力・一括更新テンプレートは
// 「条件一致全件」を書き出すため、画面のページングで付く Offset が
// 出力に漏れても先頭から全件が出る。
func TestIterateIssues_IgnoresOffset(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	var keys []string
	res, err := s.IterateIssues(context.Background(),
		IssueFilter{ProjectID: 1, Offset: 2, Limit: 1}, visitKeys(&keys))
	if err != nil {
		t.Fatal(err)
	}
	if !equalKeys(keys, []string{"EXA-1", "EXA-2", "EXA-3"}) || res.Total != 3 {
		t.Errorf("渡された課題 = %v / total = %d, want [EXA-1 EXA-2 EXA-3] / 3", keys, res.Total)
	}
}

// TestIterateIssues_VisitorErrorStopsIteration は visit が返したエラーで
// 走査が直ちに打ち切られ、そのエラーがそのまま返ることを確認する
// (件数上限の打ち切りがこの経路で行われるため、errors.Is で判定できること)。
func TestIterateIssues_VisitorErrorStopsIteration(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	stop := errors.New("打ち切り")
	visited := 0
	_, err := s.IterateIssues(context.Background(), IssueFilter{ProjectID: 1},
		func(*Issue) error {
			visited++
			return stop
		})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want %v", err, stop)
	}
	if visited != 1 {
		t.Errorf("走査した件数 = %d, want 1(1 件目で打ち切る)", visited)
	}
}

// TestIterateIssues_CustomFieldFilter はカスタム属性条件の 2 段階判定が
// 走査経路でも働き、判定不能な課題が Unverifiable として数えられることを確認する。
func TestIterateIssues_CustomFieldFilter(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	filter := IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "abc"},
		},
	}
	var keys []string
	res, err := s.IterateIssues(context.Background(), filter, visitKeys(&keys))
	if err != nil {
		t.Fatal(err)
	}
	if !equalKeys(keys, []string{"EXA-1", "EXA-3"}) {
		t.Errorf("渡された課題 = %v, want [EXA-1 EXA-3]", keys)
	}
	// 生 JSON を持たない EXA-4 は判定不能として数える(結果には含めない)
	if res.Total != 2 || res.Unverifiable != 1 {
		t.Errorf("結果 = %+v, want {Total:2 Unverifiable:1}", res)
	}
}

// TestIterateIssues_MatchesSearchIssues は走査経路と検索経路の結果
// (課題キーの並び・total・unverifiable)が一致することを確認する。
// Excel 出力を走査経路へ切り替えても、画面プレビューと同じ集合が出力されること
// を担保する。
func TestIterateIssues_MatchesSearchIssues(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)
	ctx := context.Background()

	filters := []IssueFilter{
		{ProjectID: 1},
		{ProjectID: 1, Keyword: "件目"},
		{ProjectID: 1, CustomFieldFilters: []customfield.Filter{
			{DefID: 5, TypeID: customfield.TypeSingleList, ItemIDs: []int64{51}},
		}},
	}
	for _, f := range filters {
		want, err := s.SearchIssues(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		got, err := s.IterateIssues(ctx, f, visitKeys(&keys))
		if err != nil {
			t.Fatal(err)
		}
		if !equalKeys(keys, issueKeysOf(want)) {
			t.Errorf("条件 %+v: 走査 = %v, 検索 = %v", f, keys, issueKeysOf(want))
		}
		if got.Total != want.Total || got.Unverifiable != want.Unverifiable {
			t.Errorf("条件 %+v: 走査 = %+v, 検索 total=%d unverifiable=%d",
				f, got, want.Total, want.Unverifiable)
		}
	}
}

// TestIterateIssues_AllowsQueryWhileCursorOpen は、読み取り Tx でカーソルを
// 開いたまま同じ Tx で別クエリを発行できることを確認する
// (modernc.org/sqlite + 接続 1 本という構成でカーソル走査が成り立つ前提。
// これが崩れる場合は「ID を先に集めてからチャンク読み」へ切り替える必要がある)。
func TestIterateIssues_AllowsQueryWhileCursorOpen(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	err := s.WithReadTx(ctx, func(tx *sql.Tx) error {
		_, err := IterateIssues(ctx, tx, IssueFilter{ProjectID: 1}, func(i *Issue) error {
			var n int
			return tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM issues WHERE id = ?`, i.ID).Scan(&n)
		})
		return err
	})
	if err != nil {
		t.Fatalf("カーソル保持中の別クエリに失敗: %v", err)
	}
}
