package customfield

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Filter はカスタム属性 1 定義に対する絞り込み条件(CF4)。
//
// カスタム属性の値は課題の生 JSON(issues.raw_json)にしか無く、SQL では
// 絞り込めない。そのため検索は 2 段階になり、SQL で絞った行に対して
// この条件を Go 側で適用する(store.SearchIssues 参照)。
//
// 1 定義につき 1 条件で、型ごとに使うフィールドが決まっている:
//   - 文字列 / 文章 …………………………… Text(部分一致)
//   - 数値 / 日付 ……………………………… Min / Max(範囲。境界を含む)
//   - 単一リスト / 複数リスト /
//     チェックボックス / ラジオ ………… ItemIDs(選択肢 ID のいずれか一致)
//
// 型に対応しないフィールドを使うこと自体は禁止しない(例: リスト系に Text を
// 指定すると選択肢名の部分一致になる)。同一 Filter 内の複数条件、および
// 複数 Filter は AND で連結する。
//
// JSON タグはフロント契約(frontend/src/lib/backend.ts の CustomFieldFilter)と対。
type Filter struct {
	// DefID は対象のカスタム属性定義 ID(必須)。
	DefID int64 `json:"defId"`
	// TypeID は定義の型 ID。比較方法(数値比較か文字列比較か)の判断に使う。
	TypeID int `json:"typeId"`
	// Text はテキスト系の部分一致(空白のみは条件なし扱い)。
	Text string `json:"text"`
	// Min / Max は数値・日付の範囲(境界を含む。空なら無制限)。
	Min string `json:"min"`
	Max string `json:"max"`
	// ItemIDs はリスト系で選択された選択肢 ID(いずれか 1 つに一致すれば真)。
	ItemIDs []int64 `json:"itemIds"`
}

// IsEmpty は「条件が何も指定されていない」ことを表す。
//
// テキストが空白のみの場合も条件なしとして扱う。空文字を部分一致に使うと
// 全件一致になり、利用者から見て「条件を指定したのに絞られない」状態になるため
// (キーワード検索側の splitKeywords と同じ考え方)。
func (f Filter) IsEmpty() bool {
	return strings.TrimSpace(f.Text) == "" && f.Min == "" && f.Max == "" && len(f.ItemIDs) == 0
}

// ActiveFilters は条件が指定されているものだけを返す。
// 呼び出し側はこれが空なら 2 段階目(生 JSON の解析)を丸ごと省略できる。
// 入力が nil / 全て空なら nil を返す(「条件なし」を len() で判定できる)。
func ActiveFilters(filters []Filter) []Filter {
	var out []Filter
	for _, f := range filters {
		if f.IsEmpty() {
			continue
		}
		out = append(out, f)
	}
	return out
}

// dateLayout は日付条件・日付値の書式(表示・出力と共通の yyyy-MM-dd)。
const dateLayout = "2006-01-02"

// isRangeType / isListType は型が範囲条件・選択肢条件を扱えるかを返す。
func isRangeType(typeID int) bool { return typeID == TypeNumeric || typeID == TypeDate }
func isListType(typeID int) bool {
	switch typeID {
	case TypeSingleList, TypeMultipleList, TypeCheckBox, TypeRadio:
		return true
	}
	return false
}

// isKnownType は Backlog が定義する型 ID かを返す。
func isKnownType(typeID int) bool {
	return typeID == TypeText || typeID == TypeTextArea || isRangeType(typeID) || isListType(typeID)
}

// ValidateFilters は評価できない条件を検索前にエラーにする。
//
// 不正な条件を黙って無視すると「条件を指定したのに全件出る」ことになり、
// 利用者が誤った抽出結果を正しいものと信じてしまうため、明示的に失敗させる。
//
// 同一定義に対する条件が重複している場合もエラーにする。MatchValues は
// 定義 ID で値を引くので技術的には両方が AND で効くが、画面の契約は
// 「1 定義 1 条件」であり、重複はフロント・呼び出し側の組み立て誤りを意味する。
func ValidateFilters(filters []Filter) error {
	seen := make(map[int64]bool, len(filters))
	for _, f := range filters {
		if err := f.Validate(); err != nil {
			return err
		}
		if seen[f.DefID] {
			return fmt.Errorf("カスタム属性(定義 ID %d)の絞り込み条件が重複しています", f.DefID)
		}
		seen[f.DefID] = true
	}
	return nil
}

// Validate は条件 1 件の妥当性を検証する。
//
// 型ごとに使えるフィールドを固定し、対応しない組み合わせ(テキスト型への範囲指定等)は
// 判定に進める前に弾く。特に非数値・非日付への範囲指定を通すと、比較が
// 「意味の無い辞書順」に流れて誤った抽出結果になるため、必ずここで塞ぐ。
func (f Filter) Validate() error {
	if f.DefID <= 0 {
		return fmt.Errorf("カスタム属性の絞り込み条件の定義 ID が不正です: %d", f.DefID)
	}
	if !isKnownType(f.TypeID) {
		return fmt.Errorf("カスタム属性(定義 ID %d)の型 ID が不明です: %d。アプリを更新するか、条件を外してください",
			f.DefID, f.TypeID)
	}
	if err := f.validateFieldsForType(); err != nil {
		return err
	}
	for _, id := range f.ItemIDs {
		if id <= 0 {
			return fmt.Errorf("カスタム属性(定義 ID %d)の選択肢 ID が不正です: %d", f.DefID, id)
		}
	}
	switch f.TypeID {
	case TypeNumeric:
		return f.validateNumericRange()
	case TypeDate:
		return f.validateDateRange()
	}
	return nil
}

// validateFieldsForType は型に対応しない条件フィールドが使われていないかを検証する。
//
//	文字列 / 文章 …………… Text のみ
//	数値 / 日付 ……………… Min / Max のみ
//	リスト系 ………………… ItemIDs、および Text
//	  (選択肢を取得できない定義では画面が部分一致へ縮退するため Text も許す)
func (f Filter) validateFieldsForType() error {
	hasRange := f.Min != "" || f.Max != ""
	hasItems := len(f.ItemIDs) > 0
	hasText := strings.TrimSpace(f.Text) != ""

	if hasRange && !isRangeType(f.TypeID) {
		return fmt.Errorf("カスタム属性(定義 ID %d)は%s型のため範囲では絞り込めません",
			f.DefID, TypeName(f.TypeID))
	}
	if hasItems && !isListType(f.TypeID) {
		return fmt.Errorf("カスタム属性(定義 ID %d)は%s型のため選択肢では絞り込めません",
			f.DefID, TypeName(f.TypeID))
	}
	if hasText && isRangeType(f.TypeID) {
		return fmt.Errorf("カスタム属性(定義 ID %d)は%s型のため部分一致では絞り込めません(範囲を指定してください)",
			f.DefID, TypeName(f.TypeID))
	}
	return nil
}

// validateNumericRange は数値範囲の境界を検証する。
func (f Filter) validateNumericRange() error {
	min, hasMin, err := parseBoundFloat(f, f.Min, "下限")
	if err != nil {
		return err
	}
	max, hasMax, err := parseBoundFloat(f, f.Max, "上限")
	if err != nil {
		return err
	}
	if hasMin && hasMax && min > max {
		return fmt.Errorf("カスタム属性(定義 ID %d)の数値範囲が逆転しています(下限 %s > 上限 %s)",
			f.DefID, f.Min, f.Max)
	}
	return nil
}

// validateDateRange は日付範囲の境界を検証する。
func (f Filter) validateDateRange() error {
	if err := validateBoundDate(f, f.Min, "下限"); err != nil {
		return err
	}
	if err := validateBoundDate(f, f.Max, "上限"); err != nil {
		return err
	}
	// 書式が yyyy-MM-dd に揃っているため、前後関係は辞書順比較で判定できる
	if f.Min != "" && f.Max != "" && f.Min > f.Max {
		return fmt.Errorf("カスタム属性(定義 ID %d)の日付範囲が逆転しています(下限 %s > 上限 %s)",
			f.DefID, f.Min, f.Max)
	}
	return nil
}

// parseBoundFloat は数値範囲の境界値を解釈する(空なら未指定)。
//
// NaN・±Inf は ParseFloat が受理してしまうが、比較が常に偽になる
// (= 条件が無視されたのと同じ結果になる)ため拒否する。
func parseBoundFloat(f Filter, bound, label string) (float64, bool, error) {
	if bound == "" {
		return 0, false, nil
	}
	v, err := strconv.ParseFloat(bound, 64)
	if err != nil {
		return 0, false, fmt.Errorf("カスタム属性(定義 ID %d)の%sが数値ではありません: %s", f.DefID, label, bound)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, fmt.Errorf("カスタム属性(定義 ID %d)の%sに使えない数値が指定されています: %s",
			f.DefID, label, bound)
	}
	return v, true, nil
}

// validateBoundDate は日付範囲の境界値が「実在する yyyy-MM-dd」かを検証する(空なら未指定)。
// 書式(桁・区切り)と実在(2026-02-31 等でないこと)の両方を見る。
func validateBoundDate(f Filter, bound, label string) error {
	if bound == "" {
		return nil
	}
	if !isDateOnly(bound) {
		return fmt.Errorf("カスタム属性(定義 ID %d)の%sが日付(yyyy-MM-dd)ではありません: %s",
			f.DefID, label, bound)
	}
	if _, err := time.Parse(dateLayout, bound); err != nil {
		return fmt.Errorf("カスタム属性(定義 ID %d)の%sに存在しない日付が指定されています: %s",
			f.DefID, label, bound)
	}
	return nil
}

// isDateOnly は文字列が yyyy-MM-dd の形(桁・区切りが揃っている)かを返す。
// 日付の値・境界はどちらも yyyy-MM-dd に揃えるため、比較は文字列の辞書順で足りる。
// time.Parse は "2026-8-1" のような桁の欠けも受理するため、書式は自前で見る。
func isDateOnly(v string) bool {
	if len(v) != 10 {
		return false
	}
	for i, r := range v {
		if i == 4 || i == 7 {
			if r != '-' {
				return false
			}
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MatchValues は課題 1 件のカスタム属性値が全条件(AND)を満たすかを返す。
//
// values は ParseValues の結果(課題 1 件ぶん)。定義に対応する値を持たない課題は、
// 空の値を持つものとして扱う(= 何らかの条件が指定されていれば一致しない)。
//
// normalize にはテキストの部分一致に使う正規化関数を渡す(検索本文と同じ規則に
// 揃えるため。store.NormalizeSearchText を想定)。nil の場合は素の部分一致になる。
func MatchValues(values []Value, filters []Filter, normalize func(string) string) bool {
	if len(filters) == 0 {
		return true
	}
	if normalize == nil {
		normalize = func(s string) string { return s }
	}
	byDef := make(map[int64]Value, len(values))
	for _, v := range values {
		// 同じ定義 ID が複数現れることは想定しないが、先勝ちで安定させる
		if _, dup := byDef[v.ID]; !dup {
			byDef[v.ID] = v
		}
	}
	for _, f := range filters {
		if f.IsEmpty() {
			continue
		}
		v, ok := byDef[f.DefID]
		if !ok {
			// 値を持たない課題。表示文字列は空・選択肢も無いため、
			// 下の判定はいずれも偽になる(型だけ条件に合わせておく)。
			v = Value{ID: f.DefID, FieldTypeID: f.TypeID}
		} else if v.FieldTypeID != f.TypeID {
			// 保存されている値の型が、条件が想定する現在の定義の型と食い違う
			// (Backlog 側で定義の型を変更した後、まだ再同期していない課題や、
			// 生 JSON が部分的に壊れている場合)。
			//
			// 型が違う値を現在の型の規約で判定すると、文字列型で保存された "5" が
			// 数値範囲 1〜10 に入る、といった誤一致を生む。かといって「判定不能」
			// (= 同期不足の警告)にもしない。行のデータが欠けているわけではなく、
			// 「現在の定義の条件を満たすとは言えない」だけなので、不一致として扱う。
			//
			// 未知・未設定の fieldTypeId(0 や将来の型)も、検証済みの条件の型
			// (1〜8)と一致しないためここで不一致になる。
			return false
		}
		if !matchOne(v, f, normalize) {
			return false
		}
	}
	return true
}

// matchOne は条件 1 件を値 1 件に適用する(条件内の複数指定は AND)。
func matchOne(v Value, f Filter, normalize func(string) string) bool {
	// 表示用の文字列化はテキスト一致・範囲比較の双方で使うため 1 回で済ませる
	display := FormatValue(v)
	if needle := strings.TrimSpace(f.Text); needle != "" {
		if !strings.Contains(normalize(display), normalize(needle)) {
			return false
		}
	}
	if f.Min != "" || f.Max != "" {
		if !matchRange(display, f) {
			return false
		}
	}
	if len(f.ItemIDs) > 0 {
		if !matchItemIDs(ItemIDs(v), f.ItemIDs) {
			return false
		}
	}
	return true
}

// matchRange は表示文字列を範囲条件と突き合わせる。
//
// 値が空(未入力)の課題は、どの範囲条件にも一致しない。上限だけを指定したときに
// 未入力の課題まで拾ってしまうと、利用者の意図(値が範囲内のものを見たい)から外れるため。
func matchRange(display string, f Filter) bool {
	// 範囲を扱えない型(テキスト・リスト系)への範囲指定は ValidateFilters で
	// 弾かれるが、検証を通さない呼び出しに備えてここでも一致させない。
	// 辞書順比較へ流すと、意味の無い比較結果を「絞り込めた」ように見せてしまう。
	if !isRangeType(f.TypeID) {
		return false
	}
	if display == "" {
		return false
	}
	if f.TypeID == TypeNumeric {
		return matchNumericRange(display, f)
	}
	// 日付は yyyy-MM-dd で桁が揃っているため、辞書順 = 時系列順になる
	if f.Min != "" && display < f.Min {
		return false
	}
	if f.Max != "" && display > f.Max {
		return false
	}
	return true
}

// matchNumericRange は数値として範囲を判定する(辞書順では 9 > 12.5 になるため)。
// 値が数値として読めない場合は一致しない。
func matchNumericRange(display string, f Filter) bool {
	n, err := strconv.ParseFloat(display, 64)
	if err != nil {
		return false
	}
	// 境界は ValidateFilters で検証済みのため、ここでの解析失敗は
	// 「条件なし」と同じ扱いにして判定を止めない。
	if f.Min != "" {
		if min, err := strconv.ParseFloat(f.Min, 64); err == nil && n < min {
			return false
		}
	}
	if f.Max != "" {
		if max, err := strconv.ParseFloat(f.Max, 64); err == nil && n > max {
			return false
		}
	}
	return true
}

// matchItemIDs は選択済みの選択肢 ID が条件のいずれかに一致するかを返す。
func matchItemIDs(selected, wanted []int64) bool {
	for _, s := range selected {
		for _, w := range wanted {
			if s == w {
				return true
			}
		}
	}
	return false
}
