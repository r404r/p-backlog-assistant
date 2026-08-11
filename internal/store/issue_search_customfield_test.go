package store

import (
	"context"
	"fmt"
	"testing"

	"backlog-assistant/internal/customfield"
)

// cfRawJSON はカスタム属性つきの課題の生 JSON を組み立てる(検証用の作りもの)。
//
//	定義 1(文字列) = customer
//	定義 3(数値)   = hours
//	定義 4(日付)   = date
//	定義 5(単一リスト) = 選択肢 ID itemID(0 なら値なし)
func cfRawJSON(id int64, customer string, hours float64, date string, itemID int64) string {
	item := "null"
	if itemID != 0 {
		item = fmt.Sprintf(`{"id":%d,"name":"選択肢%d"}`, itemID, itemID)
	}
	return fmt.Sprintf(`{"id":%d,"customFields":[
		{"id":1,"fieldTypeId":1,"name":"顧客名","value":%q},
		{"id":3,"fieldTypeId":3,"name":"見積工数","value":%v},
		{"id":4,"fieldTypeId":4,"name":"リリース日","value":%q},
		{"id":5,"fieldTypeId":5,"name":"影響範囲","value":%s}
	]}`, id, customer, hours, date, item)
}

// seedCustomFieldIssues はカスタム属性の絞り込み検証用データを投入する。
func seedCustomFieldIssues(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	issues := []*Issue{
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "一件目",
			Created: "2026-01-10T00:00:00Z", Updated: "2026-02-10T00:00:00Z",
			RawJSON: cfRawJSON(1, "ＡＢＣ商事", 8, "2026-08-01", 51)},
		{ID: 2, IssueKey: "EXA-2", ProjectID: 1, Summary: "二件目",
			Created: "2026-01-11T00:00:00Z", Updated: "2026-02-11T00:00:00Z",
			RawJSON: cfRawJSON(2, "XYZ工業", 20, "2026-09-15", 52)},
		{ID: 3, IssueKey: "EXA-3", ProjectID: 1, Summary: "三件目",
			Created: "2026-01-12T00:00:00Z", Updated: "2026-02-12T00:00:00Z",
			RawJSON: cfRawJSON(3, "ABC商事", 3, "2026-07-01", 51)},
		// 生 JSON を持たない課題(旧バージョンで同期した課題を模す)
		{ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "生 JSON なし",
			Created: "2026-01-13T00:00:00Z", Updated: "2026-02-13T00:00:00Z"},
		// 別プロジェクト(条件を満たしても混入してはならない)
		{ID: 5, IssueKey: "EXB-1", ProjectID: 2, Summary: "別プロジェクト",
			RawJSON: cfRawJSON(5, "ABC商事", 8, "2026-08-01", 51)},
	}
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
}

// issueKeysOf は結果の課題キーを取り出す(比較のため)。
func issueKeysOf(res *IssueSearchResult) []string {
	out := make([]string, 0, len(res.Issues))
	for _, i := range res.Issues {
		out = append(out, i.IssueKey)
	}
	return out
}

// equalKeys は課題キーの並びが一致するかを返す(検索は ID 昇順)。
func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSearchIssues_CustomFieldText はテキスト系の部分一致で絞り込めることと、
// 正規化(全角・大文字小文字の同一視)がキーワード検索と同じ規則であることを確認する。
func TestSearchIssues_CustomFieldText(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "abc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// EXA-1 は全角 ＡＢＣ、EXA-3 は半角 ABC。どちらも一致する
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1", "EXA-3"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1 EXA-3]", got)
	}
	if res.Total != 2 {
		t.Errorf("total = %d, want 2", res.Total)
	}
}

// TestSearchIssues_CustomFieldNumericRange は数値の範囲で絞り込めることを確認する。
func TestSearchIssues_CustomFieldNumericRange(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 3, TypeID: customfield.TypeNumeric, Min: "5", Max: "10"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1]", got)
	}
}

// TestSearchIssues_CustomFieldDateRange は日付の範囲で絞り込めることを確認する。
func TestSearchIssues_CustomFieldDateRange(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 4, TypeID: customfield.TypeDate, Min: "2026-08-01"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1", "EXA-2"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1 EXA-2]", got)
	}
}

// TestSearchIssues_CustomFieldItemIDs はリスト系の選択肢 ID(いずれか一致)で
// 絞り込めることを確認する。
func TestSearchIssues_CustomFieldItemIDs(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 5, TypeID: customfield.TypeSingleList, ItemIDs: []int64{52, 51}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1", "EXA-2", "EXA-3"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1 EXA-2 EXA-3]", got)
	}
}

// TestSearchIssues_CustomFieldMultipleAreAnded は複数条件が AND で効くこと、
// SQL 側の条件(状態・期間等)とも AND で組み合わさることを確認する。
func TestSearchIssues_CustomFieldMultipleAreAnded(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "abc"},
			{DefID: 3, TypeID: customfield.TypeNumeric, Max: "5"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-3"}) {
		t.Errorf("一致した課題 = %v, want [EXA-3]", got)
	}

	// SQL 条件との組み合わせ(作成日で EXA-3 を除外する)
	res, err = s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CreatedTo: "2026-01-11",
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "abc"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1"}) {
		t.Errorf("SQL 条件と併用: 一致した課題 = %v, want [EXA-1]", got)
	}
}

// TestSearchIssues_CustomFieldMissingRawJSON は生 JSON を持たない課題が
// カスタム属性条件に一致しないこと(条件を確認できない行を通さないこと)、
// かつ「判定できなかった件数」として数え上げられることを確認する。
// 黙って除外すると、利用者は結果を「条件に合う全件」と誤解してしまう。
func TestSearchIssues_CustomFieldMissingRawJSON(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	// EXA-4 は raw_json が空。上限だけの条件でも拾われてはならない
	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 3, TypeID: customfield.TypeNumeric, Max: "1000"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range issueKeysOf(res) {
		if k == "EXA-4" {
			t.Error("生 JSON を持たない課題がカスタム属性条件に一致した")
		}
	}
	if res.Total != 3 {
		t.Errorf("total = %d, want 3", res.Total)
	}
	if res.Unverifiable != 1 {
		t.Errorf("Unverifiable = %d, want 1(生 JSON が無く判定できなかった課題)", res.Unverifiable)
	}
}

// TestSearchIssues_CustomFieldBrokenRawJSON は生 JSON が壊れている課題が
// 検索全体を失敗させず、その行だけ除外され、判定不能として数えられることを確認する。
func TestSearchIssues_CustomFieldBrokenRawJSON(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	seedCustomFieldIssues(t, s)
	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 6, IssueKey: "EXA-6", ProjectID: 1, Summary: "壊れた JSON", RawJSON: `{"customFields":`},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchIssues(ctx, IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "商事"},
		},
	})
	if err != nil {
		t.Fatalf("壊れた行があるだけで検索が失敗した: %v", err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1", "EXA-3"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1 EXA-3]", got)
	}
	// 生 JSON なし(EXA-4)+ 壊れた JSON(EXA-6)
	if res.Unverifiable != 2 {
		t.Errorf("Unverifiable = %d, want 2", res.Unverifiable)
	}
}

// TestSearchIssues_CustomFieldsKeyMissingIsUnverifiable は、生 JSON に
// customFields キーが無い / null の課題を判定不能として数えることを確認する。
//
// 空配列 [] は「属性 0 件」の正常応答なので判定不能にはせず、値なしとして
// 通常どおり不一致にする。両者を取り違えると、警告が出っぱなしになる
// (前者を判定不能にした場合)か、同期不足を見逃す(後者を値なしにした場合)。
func TestSearchIssues_CustomFieldsKeyMissingIsUnverifiable(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	issues := []*Issue{
		// 条件を満たす課題
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "対象",
			RawJSON: cfRawJSON(1, "ABC商事", 8, "2026-08-01", 51)},
		// customFields キーが無い(古い同期データ)
		{ID: 2, IssueKey: "EXA-2", ProjectID: 1, Summary: "キーなし",
			RawJSON: `{"id":2,"issueKey":"EXA-2","summary":"キーなし"}`},
		// customFields が null
		{ID: 3, IssueKey: "EXA-3", ProjectID: 1, Summary: "null",
			RawJSON: `{"id":3,"customFields":null}`},
		// customFields が空配列(属性 0 件の正常応答 = 値なしとして不一致)
		{ID: 4, IssueKey: "EXA-4", ProjectID: 1, Summary: "空配列",
			RawJSON: `{"id":4,"customFields":[]}`},
	}
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchIssues(ctx, IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "ABC商事"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-1"}) {
		t.Errorf("一致した課題 = %v, want [EXA-1]", got)
	}
	// キーなし(EXA-2)と null(EXA-3)の 2 件。空配列(EXA-4)は含めない
	if res.Unverifiable != 2 {
		t.Errorf("Unverifiable = %d, want 2(空配列は判定不能に含めない)", res.Unverifiable)
	}
}

// TestSearchIssues_UnverifiableIsZeroWithoutCustomFilters は、カスタム属性条件が
// 無い検索では生 JSON を読まないため判定不能が発生しないことを確認する
// (「判定できなかった」警告が条件と無関係に出ないこと)。
func TestSearchIssues_UnverifiableIsZeroWithoutCustomFilters(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 {
		t.Errorf("total = %d, want 4", res.Total)
	}
	if res.Unverifiable != 0 {
		t.Errorf("Unverifiable = %d, want 0(カスタム属性条件なし)", res.Unverifiable)
	}
}

// TestSearchIssues_UnverifiableCountedBeyondLimit は、判定不能件数が表示上限に
// 影響されないこと(上限で切っても全件数える)を確認する。
func TestSearchIssues_UnverifiableCountedBeyondLimit(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	issues := make([]*Issue, 0, 10)
	for i := int64(1); i <= 5; i++ {
		issues = append(issues, &Issue{
			ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "対象",
			RawJSON: cfRawJSON(i, "ABC商事", 8, "2026-08-01", 51),
		})
	}
	// 上限より後ろに、生 JSON を持たない課題を並べる
	for i := int64(6); i <= 10; i++ {
		issues = append(issues, &Issue{
			ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "生 JSON なし",
		})
	}
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchIssues(ctx, IssueFilter{
		ProjectID: 1,
		Limit:     2,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "ABC商事"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 || len(res.Issues) != 2 {
		t.Errorf("total = %d / 行数 = %d, want 5 / 2", res.Total, len(res.Issues))
	}
	if res.Unverifiable != 5 {
		t.Errorf("Unverifiable = %d, want 5(上限に関わらず全件数える)", res.Unverifiable)
	}
}

// TestSearchIssues_CustomFieldLimitAppliedAfterFilter は上限がカスタム属性条件の
// 適用後に効くこと、total は条件に一致した全件数であることを確認する。
// (SQL 側で先に LIMIT すると、条件を満たす課題が上限の外に落ちて消えてしまう)
func TestSearchIssues_CustomFieldLimitAppliedAfterFilter(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	// 先頭 50 件は条件に一致しない課題で埋め、後ろの 5 件だけを一致させる
	issues := make([]*Issue, 0, 55)
	for i := int64(1); i <= 50; i++ {
		issues = append(issues, &Issue{
			ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "対象外",
			RawJSON: cfRawJSON(i, "対象外", 1, "2026-01-01", 52),
		})
	}
	for i := int64(51); i <= 55; i++ {
		issues = append(issues, &Issue{
			ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "対象",
			RawJSON: cfRawJSON(i, "ABC商事", 8, "2026-08-01", 51),
		})
	}
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchIssues(ctx, IssueFilter{
		ProjectID: 1,
		Limit:     2,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "ABC商事"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-51", "EXA-52"}) {
		t.Errorf("一致した課題 = %v, want [EXA-51 EXA-52](上限はカスタム条件の適用後に効く)", got)
	}
	if res.Total != 5 {
		t.Errorf("total = %d, want 5(上限で切っても一致した総件数)", res.Total)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

// TestSearchIssues_CustomFieldEmptyFilterIsIgnored は「条件なし」の要素だけを
// 渡した場合、絞り込みが行われず(生 JSON を持たない課題も含めて)全件返ることを確認する。
func TestSearchIssues_CustomFieldEmptyFilterIsIgnored(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText},
			{DefID: 5, TypeID: customfield.TypeSingleList, ItemIDs: []int64{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 {
		t.Errorf("total = %d, want 4(条件なしなので絞り込まない)", res.Total)
	}
}

// TestSearchIssues_CustomFieldInvalidFilterFails は評価できない条件が
// 黙って無視されず、エラーになることを確認する。
func TestSearchIssues_CustomFieldInvalidFilterFails(t *testing.T) {
	s := openTempStore(t)
	seedCustomFieldIssues(t, s)

	_, err := s.SearchIssues(context.Background(), IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 3, TypeID: customfield.TypeNumeric, Min: "abc"},
		},
	})
	if err == nil {
		t.Fatal("数値でない下限を指定してもエラーにならなかった")
	}
}

// TestSearchIssues_CustomFieldExcludesDeleted は論理削除済みの課題が
// カスタム属性条件の対象にもならないことを確認する。
func TestSearchIssues_CustomFieldExcludesDeleted(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	seedCustomFieldIssues(t, s)
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{1}); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchIssues(ctx, IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "商事"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := issueKeysOf(res); !equalKeys(got, []string{"EXA-3"}) {
		t.Errorf("一致した課題 = %v, want [EXA-3]", got)
	}
}
