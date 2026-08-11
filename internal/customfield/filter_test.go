package customfield

import (
	"strings"
	"testing"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// testNormalize は store.NormalizeSearchText と同じ規則(NFKC → ケースフォールド)。
// store を import すると customfield → store の循環になるため、
// 検証用にここで同じ処理を組み立てる(本番では store 側が関数を渡す)。
func testNormalize(s string) string {
	return cases.Fold().String(norm.NFKC.String(s))
}

// filterTestValues は各型のカスタム属性値を 1 件ずつ持つ検証用データ。
func filterTestValues() []Value {
	return []Value{
		{ID: 1, FieldTypeID: TypeText, Name: "文字列項目", Value: "ＡＢＣ株式会社"},
		{ID: 2, FieldTypeID: TypeTextArea, Name: "文章項目", Value: "1 行目\n備考テキスト"},
		{ID: 3, FieldTypeID: TypeNumeric, Name: "数値項目", Value: float64(12.5)},
		{ID: 4, FieldTypeID: TypeDate, Name: "日付項目", Value: "2026-08-12"},
		{ID: 5, FieldTypeID: TypeSingleList, Name: "単一リスト項目",
			Value: map[string]any{"id": float64(51), "name": "選択肢A"}},
		{ID: 6, FieldTypeID: TypeMultipleList, Name: "複数リスト項目",
			Value: []any{
				map[string]any{"id": float64(61), "name": "選択肢B"},
				map[string]any{"id": float64(62), "name": "選択肢C"},
			}},
	}
}

// TestFilter_IsEmpty は「条件なし」の判定を確認する。
// 空白のみのテキストは条件として扱わない(全件一致になるのを防ぐため)。
func TestFilter_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"未設定", Filter{DefID: 1, TypeID: TypeText}, true},
		{"空白のみ", Filter{DefID: 1, TypeID: TypeText, Text: "  　"}, true},
		{"テキストあり", Filter{DefID: 1, TypeID: TypeText, Text: "会社"}, false},
		{"下限のみ", Filter{DefID: 3, TypeID: TypeNumeric, Min: "1"}, false},
		{"上限のみ", Filter{DefID: 3, TypeID: TypeNumeric, Max: "1"}, false},
		{"選択肢あり", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{51}}, false},
		{"選択肢が空スライス", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{}}, true},
	}
	for _, c := range cases {
		if got := c.f.IsEmpty(); got != c.want {
			t.Errorf("%s: IsEmpty() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestActiveFilters は条件なしの要素が除かれることを確認する
// (2 段階検索の 2 段階目を「本当に条件があるときだけ」実行するため)。
func TestActiveFilters(t *testing.T) {
	got := ActiveFilters([]Filter{
		{DefID: 1, TypeID: TypeText},
		{DefID: 2, TypeID: TypeText, Text: "備考"},
		{DefID: 3, TypeID: TypeNumeric, Min: "1"},
	})
	if len(got) != 2 {
		t.Fatalf("件数 = %d, want 2 (%+v)", len(got), got)
	}
	if got[0].DefID != 2 || got[1].DefID != 3 {
		t.Errorf("残った条件 = %+v", got)
	}
	if ActiveFilters(nil) != nil {
		t.Error("nil は nil のままであってほしい")
	}
}

// TestValidateFilters は評価できない条件を検索前にエラーにすることを確認する。
// 黙って無視すると「条件を指定したのに全件出る」という誤解を招くため。
func TestValidateFilters(t *testing.T) {
	cases := []struct {
		name    string
		f       Filter
		wantErr bool
	}{
		{"定義 ID なし", Filter{TypeID: TypeText, Text: "a"}, true},
		{"数値の下限が数値でない", Filter{DefID: 3, TypeID: TypeNumeric, Min: "abc"}, true},
		{"数値の上限が数値でない", Filter{DefID: 3, TypeID: TypeNumeric, Max: "1,000"}, true},
		{"数値の範囲が正しい", Filter{DefID: 3, TypeID: TypeNumeric, Min: "-1.5", Max: "10"}, false},
		{"日付の形式が不正", Filter{DefID: 4, TypeID: TypeDate, Min: "2026/08/12"}, true},
		{"日付の形式が正しい", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-08-01", Max: "2026-08-31"}, false},
		{"下限が上限を超える(数値)", Filter{DefID: 3, TypeID: TypeNumeric, Min: "10", Max: "1"}, true},
		{"下限が上限を超える(日付)", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-09-01", Max: "2026-08-01"}, true},

		// 識別子の非正値(画面・契約の破損。黙って全件一致させない)
		{"定義 ID が負", Filter{DefID: -1, TypeID: TypeText, Text: "a"}, true},
		{"選択肢 ID が 0", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{0}}, true},
		{"選択肢 ID が負", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{-3}}, true},

		// 未知の型 ID(比較方法を決められないため、判定せずエラーにする)
		{"型 ID が未知", Filter{DefID: 1, TypeID: 99, Text: "a"}, true},
		{"型 ID が未設定", Filter{DefID: 1, TypeID: 0, Text: "a"}, true},

		// 型と条件フィールドの不整合。
		// 特に非数値・非日付への範囲指定は辞書順比較に流れて誤結果になるため必ず塞ぐ
		{"テキスト型に範囲", Filter{DefID: 1, TypeID: TypeText, Min: "1"}, true},
		{"文章型に範囲", Filter{DefID: 2, TypeID: TypeTextArea, Max: "9"}, true},
		{"リスト型に範囲", Filter{DefID: 5, TypeID: TypeSingleList, Min: "1"}, true},
		{"テキスト型に選択肢", Filter{DefID: 1, TypeID: TypeText, ItemIDs: []int64{51}}, true},
		{"数値型に選択肢", Filter{DefID: 3, TypeID: TypeNumeric, ItemIDs: []int64{51}}, true},
		{"数値型に部分一致", Filter{DefID: 3, TypeID: TypeNumeric, Text: "12"}, true},
		{"日付型に部分一致", Filter{DefID: 4, TypeID: TypeDate, Text: "2026"}, true},
		// リスト系のテキスト指定は許す(選択肢が取れない定義で画面が縮退させる経路)
		{"リスト型に部分一致", Filter{DefID: 5, TypeID: TypeSingleList, Text: "選択肢"}, false},
		{"チェックボックスの選択肢", Filter{DefID: 7, TypeID: TypeCheckBox, ItemIDs: []int64{71}}, false},

		// 数値境界の異常値(比較が常に偽になり、条件を無視したのと同じになる)
		{"下限が NaN", Filter{DefID: 3, TypeID: TypeNumeric, Min: "NaN"}, true},
		{"上限が Inf", Filter{DefID: 3, TypeID: TypeNumeric, Max: "Inf"}, true},
		{"上限が +Inf", Filter{DefID: 3, TypeID: TypeNumeric, Max: "+Inf"}, true},
		{"下限が -Infinity", Filter{DefID: 3, TypeID: TypeNumeric, Min: "-Infinity"}, true},

		// 実在しない日付(形が合っていても比較の意味を成さない)
		{"実在しない日付", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-02-31"}, true},
		{"存在しない月", Filter{DefID: 4, TypeID: TypeDate, Max: "2026-13-01"}, true},
		{"閏日は実在する", Filter{DefID: 4, TypeID: TypeDate, Min: "2024-02-29"}, false},
		{"平年の 2/29 は実在しない", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-02-29"}, true},
	}
	for _, c := range cases {
		err := ValidateFilters([]Filter{c.f})
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", c.name, err, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "カスタム属性") {
			t.Errorf("%s: エラーメッセージが日本語の文脈を欠く: %v", c.name, err)
		}
	}
}

// TestValidateFilters_DuplicateDefID は同じ定義に対する条件が 2 つ来た場合に
// エラーになることを確認する。片方だけが適用される(MatchValues は定義 ID で
// 値を引くため両方が AND で効くが、画面の意図としては 1 定義 1 条件)状態を
// 黙って通すと、利用者は指定した条件のうちどれが効いたか分からなくなる。
func TestValidateFilters_DuplicateDefID(t *testing.T) {
	err := ValidateFilters([]Filter{
		{DefID: 1, TypeID: TypeText, Text: "あ"},
		{DefID: 1, TypeID: TypeText, Text: "い"},
	})
	if err == nil {
		t.Fatal("同一定義の条件が重複してもエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "カスタム属性") {
		t.Errorf("エラーメッセージが日本語の文脈を欠く: %v", err)
	}
	// 定義が異なれば正常
	if err := ValidateFilters([]Filter{
		{DefID: 1, TypeID: TypeText, Text: "あ"},
		{DefID: 2, TypeID: TypeTextArea, Text: "い"},
	}); err != nil {
		t.Errorf("異なる定義の条件でエラーになった: %v", err)
	}
}

// TestMatchValues_TypeMismatchDoesNotMatch は、保存されている値の型
// (生 JSON の fieldTypeId)が条件の型と食い違う場合に一致しないことを確認する。
//
// 定義の型を Backlog 側で変更した後、まだ再同期していない課題では、
// 文字列型で保存された "5" が数値範囲 1〜10 に入ってしまう等の誤一致が起きる。
func TestMatchValues_TypeMismatchDoesNotMatch(t *testing.T) {
	cases := []struct {
		name   string
		values []Value
		f      Filter
		want   bool
	}{
		{
			// 生 JSON は文字列型で "5"、条件は数値範囲。数値として解釈してはならない
			name:   "文字列型の値に数値範囲",
			values: []Value{{ID: 3, FieldTypeID: TypeText, Value: "5"}},
			f:      Filter{DefID: 3, TypeID: TypeNumeric, Min: "1", Max: "10"},
			want:   false,
		},
		{
			name:   "数値型の値にテキスト条件",
			values: []Value{{ID: 1, FieldTypeID: TypeNumeric, Value: float64(5)}},
			f:      Filter{DefID: 1, TypeID: TypeText, Text: "5"},
			want:   false,
		},
		{
			name:   "日付型の値に文字列条件",
			values: []Value{{ID: 4, FieldTypeID: TypeDate, Value: "2026-08-12"}},
			f:      Filter{DefID: 4, TypeID: TypeText, Text: "2026"},
			want:   false,
		},
		{
			// リスト系どうしでも型が違えば一致させない(単一リスト → 複数リストへの定義変更)
			name: "単一リストの条件に複数リストの値",
			values: []Value{{ID: 5, FieldTypeID: TypeMultipleList,
				Value: []any{map[string]any{"id": float64(51), "name": "選択肢A"}}}},
			f:    Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{51}},
			want: false,
		},
		{
			// 未知・未設定の fieldTypeId(0)はどの条件の型とも一致しない
			name:   "型 ID が未設定の値",
			values: []Value{{ID: 1, FieldTypeID: 0, Value: "abc"}},
			f:      Filter{DefID: 1, TypeID: TypeText, Text: "abc"},
			want:   false,
		},
		{
			name:   "未知の型 ID の値",
			values: []Value{{ID: 1, FieldTypeID: 99, Value: "abc"}},
			f:      Filter{DefID: 1, TypeID: TypeText, Text: "abc"},
			want:   false,
		},
		{
			// 型が一致していれば従来どおり判定する
			name:   "型が一致すれば一致する",
			values: []Value{{ID: 3, FieldTypeID: TypeNumeric, Value: float64(5)}},
			f:      Filter{DefID: 3, TypeID: TypeNumeric, Min: "1", Max: "10"},
			want:   true,
		},
	}
	for _, c := range cases {
		if got := MatchValues(c.values, []Filter{c.f}, testNormalize); got != c.want {
			t.Errorf("%s: MatchValues = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchValues_RangeOnUnsupportedTypeDoesNotMatch は、検証を通らない条件
// (テキスト型への範囲指定)が万一 MatchValues に届いても、辞書順比較で
// 誤って一致しないことを確認する(ValidateFilters と二重の防御)。
func TestMatchValues_RangeOnUnsupportedTypeDoesNotMatch(t *testing.T) {
	values := []Value{{ID: 1, FieldTypeID: TypeText, Value: "b"}}
	f := Filter{DefID: 1, TypeID: TypeText, Min: "a", Max: "c"}
	if MatchValues(values, []Filter{f}, testNormalize) {
		t.Error("テキスト型への範囲指定が辞書順比較で一致してしまった")
	}
}

// TestMatchValues_Text はテキスト系の部分一致を確認する。
// 正規化(NFKC + ケースフォールド)を通すため、全角・半角や大文字小文字は同一視される。
func TestMatchValues_Text(t *testing.T) {
	values := filterTestValues()
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"部分一致", Filter{DefID: 1, TypeID: TypeText, Text: "株式会社"}, true},
		{"全角半角の同一視", Filter{DefID: 1, TypeID: TypeText, Text: "ABC"}, true},
		{"一致しない", Filter{DefID: 1, TypeID: TypeText, Text: "有限会社"}, false},
		{"文章も部分一致", Filter{DefID: 2, TypeID: TypeTextArea, Text: "備考"}, true},
		{"値を持たない定義は一致しない", Filter{DefID: 99, TypeID: TypeText, Text: "備考"}, false},
	}
	for _, c := range cases {
		if got := MatchValues(values, []Filter{c.f}, testNormalize); got != c.want {
			t.Errorf("%s: MatchValues = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchValues_NumericRange は数値の範囲一致を確認する
// (辞書順ではなく数値として比較されること)。
func TestMatchValues_NumericRange(t *testing.T) {
	values := filterTestValues() // 数値項目 = 12.5
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"下限のみ(境界を含む)", Filter{DefID: 3, TypeID: TypeNumeric, Min: "12.5"}, true},
		{"上限のみ(境界を含む)", Filter{DefID: 3, TypeID: TypeNumeric, Max: "12.5"}, true},
		{"範囲内", Filter{DefID: 3, TypeID: TypeNumeric, Min: "10", Max: "20"}, true},
		{"下限未満", Filter{DefID: 3, TypeID: TypeNumeric, Min: "13"}, false},
		{"上限超過", Filter{DefID: 3, TypeID: TypeNumeric, Max: "12"}, false},
		// 辞書順なら "12.5" < "9" となり一致してしまう。数値比較であることの確認。
		{"数値として比較する", Filter{DefID: 3, TypeID: TypeNumeric, Min: "9"}, true},
		{"値を持たない定義は一致しない", Filter{DefID: 98, TypeID: TypeNumeric, Min: "0"}, false},
	}
	for _, c := range cases {
		if got := MatchValues(values, []Filter{c.f}, testNormalize); got != c.want {
			t.Errorf("%s: MatchValues = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchValues_DateRange は日付の範囲一致を確認する。
func TestMatchValues_DateRange(t *testing.T) {
	values := filterTestValues() // 日付項目 = 2026-08-12
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"範囲内", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-08-01", Max: "2026-08-31"}, true},
		{"下限と同日", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-08-12"}, true},
		{"上限と同日", Filter{DefID: 4, TypeID: TypeDate, Max: "2026-08-12"}, true},
		{"範囲外", Filter{DefID: 4, TypeID: TypeDate, Min: "2026-09-01"}, false},
		{"値を持たない定義は上限だけでも一致しない",
			Filter{DefID: 97, TypeID: TypeDate, Max: "2099-12-31"}, false},
	}
	for _, c := range cases {
		if got := MatchValues(values, []Filter{c.f}, testNormalize); got != c.want {
			t.Errorf("%s: MatchValues = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchValues_MissingDateWithTime は日時付きで返った日付でも
// 日付部分だけで比較されることを確認する。
func TestMatchValues_DateWithTime(t *testing.T) {
	values := []Value{{ID: 4, FieldTypeID: TypeDate, Value: "2026-08-12T00:00:00Z"}}
	f := Filter{DefID: 4, TypeID: TypeDate, Min: "2026-08-12", Max: "2026-08-12"}
	if !MatchValues(values, []Filter{f}, testNormalize) {
		t.Error("日時付きの日付が範囲に一致しなかった")
	}
}

// TestMatchValues_ItemIDs はリスト系の「選択肢 ID のいずれか一致」を確認する。
func TestMatchValues_ItemIDs(t *testing.T) {
	values := filterTestValues()
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"単一リストが一致", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{51}}, true},
		{"単一リストの候補違い", Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{52, 53}}, false},
		{"単一リストはいずれか一致で真",
			Filter{DefID: 5, TypeID: TypeSingleList, ItemIDs: []int64{52, 51}}, true},
		{"複数リストのいずれか一致",
			Filter{DefID: 6, TypeID: TypeMultipleList, ItemIDs: []int64{62}}, true},
		{"複数リストの候補違い",
			Filter{DefID: 6, TypeID: TypeMultipleList, ItemIDs: []int64{63}}, false},
		{"値を持たない定義は一致しない",
			Filter{DefID: 96, TypeID: TypeSingleList, ItemIDs: []int64{51}}, false},
	}
	for _, c := range cases {
		if got := MatchValues(values, []Filter{c.f}, testNormalize); got != c.want {
			t.Errorf("%s: MatchValues = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestMatchValues_MultipleFiltersAreAnded は複数条件が AND で連結されることを確認する。
func TestMatchValues_MultipleFiltersAreAnded(t *testing.T) {
	values := filterTestValues()
	all := []Filter{
		{DefID: 1, TypeID: TypeText, Text: "株式会社"},
		{DefID: 3, TypeID: TypeNumeric, Min: "10", Max: "20"},
		{DefID: 6, TypeID: TypeMultipleList, ItemIDs: []int64{61}},
	}
	if !MatchValues(values, all, testNormalize) {
		t.Error("すべて満たす課題が一致しなかった")
	}
	// 1 つでも外れれば不一致
	ng := append([]Filter{}, all...)
	ng = append(ng, Filter{DefID: 4, TypeID: TypeDate, Min: "2027-01-01"})
	if MatchValues(values, ng, testNormalize) {
		t.Error("満たさない条件があるのに一致した")
	}
}

// TestMatchValues_NoFilters は条件が無ければ常に一致することを確認する。
func TestMatchValues_NoFilters(t *testing.T) {
	if !MatchValues(nil, nil, testNormalize) {
		t.Error("条件なしで一致しなかった")
	}
	if !MatchValues(nil, []Filter{{DefID: 1, TypeID: TypeText}}, testNormalize) {
		t.Error("空の条件だけなら一致してほしい")
	}
}

// TestMatchValues_EmptyValueDoesNotMatch は値が空文字の属性が
// 範囲条件・テキスト条件のいずれにも一致しないことを確認する
// (未入力の課題が「上限だけ指定」で拾われるのを防ぐ)。
func TestMatchValues_EmptyValueDoesNotMatch(t *testing.T) {
	values := []Value{
		{ID: 3, FieldTypeID: TypeNumeric, Value: nil},
		{ID: 4, FieldTypeID: TypeDate, Value: nil},
		{ID: 1, FieldTypeID: TypeText, Value: ""},
	}
	cases := []Filter{
		{DefID: 3, TypeID: TypeNumeric, Max: "100"},
		{DefID: 4, TypeID: TypeDate, Max: "2099-12-31"},
		{DefID: 1, TypeID: TypeText, Text: "a"},
	}
	for _, f := range cases {
		if MatchValues(values, []Filter{f}, testNormalize) {
			t.Errorf("未入力の値が条件 %+v に一致した", f)
		}
	}
}

// TestMatchValues_OtherValue はリスト系の「その他」入力もテキスト条件の
// 対象になることを確認する(表示文字列に含まれるため)。
func TestMatchValues_OtherValue(t *testing.T) {
	values := []Value{
		{ID: 5, FieldTypeID: TypeSingleList, Value: nil, OtherValue: "その他の値"},
	}
	if !MatchValues(values, []Filter{{DefID: 5, TypeID: TypeSingleList, Text: "その他"}}, testNormalize) {
		t.Error("その他入力がテキスト条件に一致しなかった")
	}
}

// TestMatchValues_NilNormalize は正規化関数を渡さなくても
// 落ちずに素の部分一致で動くことを確認する(呼び出し漏れの保険)。
func TestMatchValues_NilNormalize(t *testing.T) {
	values := []Value{{ID: 1, FieldTypeID: TypeText, Value: "ABC"}}
	if !MatchValues(values, []Filter{{DefID: 1, TypeID: TypeText, Text: "BC"}}, nil) {
		t.Error("normalize が nil でも素の部分一致で動いてほしい")
	}
}
