package bulk

// customfield.go は一括更新・追加におけるカスタム属性(CF3)の
// 入力検証・差分判定・送信値の組み立てを担う。
//
// 入力規約(記入方法シートと同じ内容。食い違わせないこと):
//   - 列ヘッダは「属性:{定義名}」(固定 13 列の後ろに定義順で並ぶ)。
//   - 文字列 / 文章はそのまま、数値は数値、日付は yyyy-MM-dd。
//   - 単一リスト / ラジオは選択肢名、複数リスト / チェックボックスは
//     選択肢名のカンマ区切り(「,」「、」の両方を受理する)。
//   - 空セル = 変更しない(新規追加行では未設定)、#CLEAR# = クリア。
//
// 「その他」の直接入力(customField_{id}_otherValue)は非対応。
// 定義取得 API が allowInput を返すかが実機未検証で、入力可否を判定できないため
// (customfield.Def.AllowInput のコメントを参照)。選択肢に無い名前はエラーにし、
// メッセージでその旨を案内する。

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// customItemSeparators は複数リスト / チェックボックスの区切り文字。
// 日本語入力では読点になりがちなので「、」も受理する。
const customItemSeparators = ",、"

// customItems は 1 定義の選択肢索引。
type customItems struct {
	// byName は正規化した選択肢名 → 選択肢 ID。
	// 同名の選択肢を検知するためスライスで持つ(曖昧な入力はエラーにする)。
	byName map[string][]int64
	// order は定義順の選択肢 ID(送信値・表示を定義順へ正規化するために使う)。
	order []int64
	// nameByID は選択肢 ID → 表示名。
	nameByID map[int64]string
	// commaInName は選択肢名にカンマ(区切り文字)を含むか。
	// 複数選択では区切りと区別できないため、値が入力されたらエラーにする。
	commaInName bool
}

// newCustomItems は定義から選択肢索引を作る。
func newCustomItems(def customfield.Def) *customItems {
	items := &customItems{
		byName:   map[string][]int64{},
		order:    make([]int64, 0, len(def.Items)),
		nameByID: map[int64]string{},
	}
	for _, it := range def.Items {
		items.order = append(items.order, it.ID)
		items.nameByID[it.ID] = it.Name
		if key := normalizeHeader(it.Name); key != "" {
			items.byName[key] = append(items.byName[key], it.ID)
		}
		if strings.ContainsAny(it.Name, customItemSeparators) {
			items.commaInName = true
		}
	}
	return items
}

// names は選択肢 ID の集合を定義順の表示名へ整形する
// (表示は customfield.FormatValue と同じ ", " 区切り)。
func (c *customItems) names(ids []int64) string {
	set := map[int64]bool{}
	for _, id := range ids {
		set[id] = true
	}
	out := make([]string, 0, len(ids))
	for _, id := range c.order {
		if set[id] {
			out = append(out, c.nameByID[id])
		}
	}
	return strings.Join(out, ", ")
}

// isListType はリスト系(選択肢から選ぶ)の型かを返す。
func isListType(typeID int) bool {
	switch typeID {
	case customfield.TypeSingleList, customfield.TypeMultipleList,
		customfield.TypeCheckBox, customfield.TypeRadio:
		return true
	}
	return false
}

// applicableTo は定義が課題種別に適用されるかを返す。
// ApplicableIssueTypes が空の場合は全課題種別に適用される(API の仕様)。
func applicableTo(def customfield.Def, issueTypeID int64) bool {
	if len(def.ApplicableIssueTypes) == 0 {
		return true
	}
	for _, id := range def.ApplicableIssueTypes {
		if id == issueTypeID {
			return true
		}
	}
	return false
}

// planCustomFieldsCreate は新規追加行のカスタム属性を検証し、送信値を組み立てる。
//
// 必須チェックはこの行の課題種別に適用される定義だけに行う
// (適用外の属性は API 側でも要求されない)。
func (v *validator) planCustomFieldsCreate(r rawRow, issueTypeID int64, plan *rowPlan) error {
	for _, def := range v.idx.customDefs {
		raw := r.cell(customColKey(def.ID))
		if raw == "" {
			if def.Required && applicableTo(def, issueTypeID) {
				return fmt.Errorf("カスタム属性「%s」が入力されていません(新規追加には必須です)", def.Name)
			}
			continue
		}
		if err := v.checkApplicable(def, issueTypeID); err != nil {
			return err
		}
		if raw == ClearToken {
			// 既存フィールドと同じ扱い(クリアすべき既存値が無い)
			return fmt.Errorf("新規追加行では %s を指定できません", ClearToken)
		}
		in, display, err := v.customInput(def, raw)
		if err != nil {
			return err
		}
		plan.payload.CustomFields = append(plan.payload.CustomFields, in)
		plan.changes = append(plan.changes, fmt.Sprintf("%s: %s", def.Name, display))
	}
	return nil
}

// planCustomFieldsUpdate は更新行のカスタム属性を検証し、現在値との差分だけを
// 送信値へ載せる(空セル = 変更しない)。
func (v *validator) planCustomFieldsUpdate(r rawRow, cur *store.Issue, plan *rowPlan) error {
	if !v.hasCustomInput(r) {
		return nil // カスタム属性の記入が無い行では現在値の解析も行わない
	}
	current, err := currentCustomValues(cur)
	if err != nil {
		return err
	}
	issueTypeID := v.effectiveIssueTypeID(plan, cur)
	for _, def := range v.idx.customDefs {
		raw := r.cell(customColKey(def.ID))
		if raw == "" {
			continue // 空欄 = 変更しない
		}
		if err := v.checkApplicable(def, issueTypeID); err != nil {
			return err
		}
		curValue, hasCurrent := current[def.ID]
		curDisplay := ""
		if hasCurrent {
			curDisplay = customfield.FormatValue(curValue)
		}
		// テンプレートへプリフィルした現在値は、取り込み時にセルが前後空白を
		// 落とされた状態で戻ってくる。比較も同じ正規化で行い、未編集の行が
		// 変更扱いにならないようにする(前後の空白だけを足し引きする編集は
		// Excel 経由では表現できない。記入方法シートにも明記している)。
		curCompare := strings.TrimSpace(curDisplay)
		if raw == ClearToken {
			if curCompare == "" {
				continue // 既に未設定(空の送信をしない)
			}
			if curCompare == ClearToken {
				// 現在値そのものが文字列「#CLEAR#」の課題。プリフィルを未編集で
				// 取り込んだだけの行をクリアと解釈しない(変更なしとして扱う)。
				// 逆に、値として #CLEAR# を設定し直す操作は非対応。
				continue
			}
			plan.payload.CustomFields = append(plan.payload.CustomFields,
				customfield.InputValue{ID: def.ID, TypeID: def.TypeID, Clear: true})
			plan.changes = append(plan.changes, change(def.Name, curDisplay, "(クリア)"))
			continue
		}
		in, display, err := v.customInput(def, raw)
		if err != nil {
			return err
		}
		if sameCustomValue(def, in, curValue, curCompare) {
			continue
		}
		plan.payload.CustomFields = append(plan.payload.CustomFields, in)
		plan.changes = append(plan.changes, change(def.Name, curDisplay, display))
	}
	return nil
}

// effectiveIssueTypeID は行の適用先となる課題種別 ID を返す(判定できなければ 0)。
//
// 同じ行で種別を変更する場合は変更後の種別で判定する(実行時に適用される種別と
// 一致させるため)。種別を変更しない行はローカルの現在値から求めるが、
// store.Issue は種別名しか保持していないため、名前がマスタで一意に決まらない
// 場合(改名・削除された種別、同名の種別)は 0 を返す。
// 判定できないことを理由に取り込みを止めると正当な更新まで弾いてしまうため、
// 呼び出し側は 0 のとき適用可否を検査しない(安全側)。
//
// なお 0 が返るのは種別列を空にした行だけである点に注意。通常の出力テンプレート
// では種別名がプリフィルされており、その名前がマスタで解決できない場合は
// ここに到達する前の名前解決(planUpdate の resolveNamed)で行エラーになる。
func (v *validator) effectiveIssueTypeID(plan *rowPlan, cur *store.Issue) int64 {
	if plan.payload.IssueTypeID != nil {
		return *plan.payload.IssueTypeID
	}
	if ids := v.idx.issueTypeByName[normalizeHeader(cur.IssueTypeName)]; len(ids) == 1 {
		return ids[0]
	}
	return 0
}

// checkApplicable は課題種別に適用されない定義への入力を弾く。
//
// 適用外の属性は API が拒否するため、dry-run では成功したのに実行時に
// 失敗する(しかも 1 件ずつ送るので途中まで反映される)ことを防ぐ。
// issueTypeID が 0(判定できない)の場合は検査しない。
func (v *validator) checkApplicable(def customfield.Def, issueTypeID int64) error {
	if issueTypeID == 0 || applicableTo(def, issueTypeID) {
		return nil
	}
	return fmt.Errorf("カスタム属性「%s」は課題種別「%s」には設定できません(この種別には適用されない属性です)",
		def.Name, v.idx.issueTypeByID[issueTypeID])
}

// hasCustomInput はカスタム属性の列に 1 つでも記入があるかを返す。
func (v *validator) hasCustomInput(r rawRow) bool {
	for _, def := range v.idx.customDefs {
		if r.has(customColKey(def.ID)) {
			return true
		}
	}
	return false
}

// currentCustomValues はローカルの課題からカスタム属性の現在値を取り出す。
// 生 JSON を保持していない古いキャッシュは「値なし」として扱う(課題出力と同じ流儀)。
func currentCustomValues(cur *store.Issue) (map[int64]customfield.Value, error) {
	out := map[int64]customfield.Value{}
	if cur.RawJSON == "" {
		return out, nil
	}
	values, err := customfield.ParseValues(cur.RawJSON)
	if err != nil {
		return nil, fmt.Errorf("課題 %s のカスタム属性の現在値を読み取れません: %w", cur.IssueKey, err)
	}
	for _, value := range values {
		out[value.ID] = value
	}
	return out, nil
}

// sameCustomValue は入力値が現在値と同じかを返す(同じなら送信しない)。
//
// リスト系は選択肢 ID の集合で比較する(表示名は変更されうるため)。
// それ以外は正規化済みの文字列(customInput が返す送信値)と、前後空白を
// 落とした現在値(curCompare)を比較する。どちらも同じ規約で整形されている。
func sameCustomValue(def customfield.Def, in customfield.InputValue,
	curValue customfield.Value, curCompare string) bool {

	if isListType(def.TypeID) {
		return sameInt64Set(in.ItemIDs, customfield.ItemIDs(curValue))
	}
	return in.Text == curCompare
}

// sameInt64Set は 2 つの ID 集合が等しいかを返す(順序・重複は無視する)。
func sameInt64Set(a, b []int64) bool {
	set := map[int64]bool{}
	for _, id := range a {
		set[id] = true
	}
	other := map[int64]bool{}
	for _, id := range b {
		other[id] = true
	}
	if len(set) != len(other) {
		return false
	}
	for id := range set {
		if !other[id] {
			return false
		}
	}
	return true
}

// customInput はセルの入力値を送信値と表示文字列へ変換する。
// 型ごとの書式・選択肢の解決に失敗した場合はエラー(行のエラーとして報告する)。
func (v *validator) customInput(def customfield.Def, raw string) (customfield.InputValue, string, error) {
	in := customfield.InputValue{ID: def.ID, TypeID: def.TypeID}
	switch def.TypeID {
	case customfield.TypeText, customfield.TypeTextArea:
		in.Text = raw
	case customfield.TypeNumeric:
		// NaN / Inf は ParseFloat が受理するが数値としては送れない
		// (API へ "NaN" 等の文字列を送ることになる)ため書式エラーにする
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return in, "", fmt.Errorf("カスタム属性「%s」は数値で入力してください(%q)", def.Name, raw)
		}
		// 表示・比較の規約を customfield.FormatValue と揃える(整数は小数点なし)
		in.Text = strconv.FormatFloat(n, 'f', -1, 64)
	case customfield.TypeDate:
		normalized, ok := parseDateValue(raw)
		if !ok {
			return in, "", fmt.Errorf("カスタム属性「%s」の日付が不正です(%q)。yyyy-MM-dd 形式で入力してください",
				def.Name, raw)
		}
		in.Text = normalized
	case customfield.TypeSingleList, customfield.TypeRadio:
		// 単一選択は区切り文字で分割しない(選択肢名にカンマを含んでも一意に決まる)
		id, err := v.resolveCustomItem(def, raw)
		if err != nil {
			return in, "", err
		}
		in.ItemIDs = []int64{id}
	case customfield.TypeMultipleList, customfield.TypeCheckBox:
		ids, err := v.resolveCustomItems(def, raw)
		if err != nil {
			return in, "", err
		}
		in.ItemIDs = ids
	default:
		// 未知の型は送信書式を決められない。黙って落とすと記入が反映されないため、
		// エラーにして利用者に気付かせる(Backlog に型が追加された場合の保険)。
		return in, "", fmt.Errorf("カスタム属性「%s」の型(%s)には対応していません",
			def.Name, customfield.TypeName(def.TypeID))
	}
	if isListType(def.TypeID) {
		return in, v.idx.customItems[def.ID].names(in.ItemIDs), nil
	}
	return in, in.Text, nil
}

// resolveCustomItems は複数選択の入力(選択肢名のカンマ区切り)を選択肢 ID へ解決する。
// 並びは定義順へ正規化し、重複指定は 1 つにまとめる(入力順で差分が出ないようにする)。
func (v *validator) resolveCustomItems(def customfield.Def, raw string) ([]int64, error) {
	items := v.idx.customItems[def.ID]
	if items.commaInName {
		return nil, fmt.Errorf("カスタム属性「%s」は選択肢名にカンマを含むため取り込めません(複数選択の区切りと区別できません)",
			def.Name)
	}
	selected := map[int64]bool{}
	for _, name := range strings.FieldsFunc(raw, func(r rune) bool {
		return strings.ContainsRune(customItemSeparators, r)
	}) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		id, err := v.resolveCustomItem(def, name)
		if err != nil {
			return nil, err
		}
		selected[id] = true
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("カスタム属性「%s」の選択肢が入力されていません(%q)", def.Name, raw)
	}
	out := make([]int64, 0, len(selected))
	for _, id := range items.order {
		if selected[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// resolveCustomItem は選択肢名 1 件を選択肢 ID へ解決する(完全一致)。
//
// 見つからない場合は「その他」の直接入力が未対応であることも案内する
// (利用者が「入力すれば通るはず」と誤解しないようにする)。
func (v *validator) resolveCustomItem(def customfield.Def, name string) (int64, error) {
	ids := v.idx.customItems[def.ID].byName[normalizeHeader(name)]
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return 0, fmt.Errorf("カスタム属性「%s」の選択肢「%s」が見つかりません(「%s」シートの候補から選んでください)。"+
			"選択肢に無い値(「その他」の直接入力)は現在未対応です",
			def.Name, name, export.SheetBulkMaster)
	default:
		return 0, fmt.Errorf("カスタム属性「%s」の選択肢「%s」は複数あり一意に決められません(Backlog 側で選択肢名を変更してください)",
			def.Name, name)
	}
}
