package customfield

import (
	"strings"
	"testing"
)

// TestTypeName は型 ID から型名を解決できること、未知の ID でも
// 値が消えない表記(「不明(N)」)になることを確認する。
func TestTypeName(t *testing.T) {
	cases := map[int]string{
		TypeText:         "文字列",
		TypeTextArea:     "文章",
		TypeNumeric:      "数値",
		TypeDate:         "日付",
		TypeSingleList:   "単一リスト",
		TypeMultipleList: "複数リスト",
		TypeCheckBox:     "チェックボックス",
		TypeRadio:        "ラジオ",
		9:                "不明(9)",
		0:                "不明(0)",
		-1:               "不明(-1)",
	}
	for typeID, want := range cases {
		if got := TypeName(typeID); got != want {
			t.Errorf("TypeName(%d) = %q, want %q", typeID, got, want)
		}
	}
}

// sampleIssueJSON は各型のカスタム属性を 1 件ずつ持つ課題の生 JSON(検証用の作りもの)。
const sampleIssueJSON = `{
	"id":101,"issueKey":"EXA-1","summary":"件名",
	"customFields":[
		{"id":1,"fieldTypeId":1,"name":"文字列項目","value":"あいう"},
		{"id":2,"fieldTypeId":2,"name":"文章項目","value":"1 行目\n2 行目"},
		{"id":3,"fieldTypeId":3,"name":"数値項目","value":12},
		{"id":4,"fieldTypeId":4,"name":"日付項目","value":"2026-08-12"},
		{"id":5,"fieldTypeId":5,"name":"単一リスト項目","value":{"id":51,"name":"選択肢A"},"otherValue":null},
		{"id":6,"fieldTypeId":6,"name":"複数リスト項目","value":[{"id":61,"name":"選択肢B"},{"id":62,"name":"選択肢C"}]},
		{"id":7,"fieldTypeId":7,"name":"チェックボックス項目","value":[{"id":71,"name":"チェック1"}]},
		{"id":8,"fieldTypeId":8,"name":"ラジオ項目","value":{"id":81,"name":"ラジオA"}}
	]
}`

// TestParseValues_AllTypes は各型の値を取り出し、表示用文字列に変換できることを確認する。
func TestParseValues_AllTypes(t *testing.T) {
	values, err := ParseValues(sampleIssueJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 8 {
		t.Fatalf("件数 = %d, want 8", len(values))
	}

	wantFormatted := []string{
		"あいう",
		"1 行目\n2 行目",
		"12",
		"2026-08-12",
		"選択肢A",
		"選択肢B, 選択肢C",
		"チェック1",
		"ラジオA",
	}
	for i, v := range values {
		if v.ID != int64(i+1) {
			t.Errorf("values[%d].ID = %d, want %d", i, v.ID, i+1)
		}
		if v.FieldTypeID != i+1 {
			t.Errorf("values[%d].FieldTypeID = %d, want %d", i, v.FieldTypeID, i+1)
		}
		if v.Name == "" {
			t.Errorf("values[%d].Name が空", i)
		}
		if got := FormatValue(v); got != wantFormatted[i] {
			t.Errorf("FormatValue(%s) = %q, want %q", v.Name, got, wantFormatted[i])
		}
	}
}

// TestParseValues_MissingOrNullCustomFields は customFields が無い・null の課題でも
// エラーにせず空スライスを返すことを確認する(カスタム属性を使わないプロジェクト)。
func TestParseValues_MissingOrNullCustomFields(t *testing.T) {
	cases := map[string]string{
		"customFields が無い":    `{"id":101,"issueKey":"EXA-1"}`,
		"customFields が null": `{"id":101,"customFields":null}`,
		"customFields が空配列":   `{"id":101,"customFields":[]}`,
		"トップレベルに他の項目しか無い":     `{}`,
		"customFields の要素が空":  `{"customFields":[{}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			values, err := ParseValues(raw)
			if err != nil {
				t.Fatalf("エラーになった: %v", err)
			}
			if values == nil {
				t.Fatal("nil が返った(空スライスを返すこと)")
			}
			// 「要素が空」だけは 1 件返る(値なし = 空文字)
			if name == "customFields の要素が空" {
				if len(values) != 1 || FormatValue(values[0]) != "" {
					t.Errorf("values = %+v", values)
				}
				return
			}
			if len(values) != 0 {
				t.Errorf("件数 = %d, want 0", len(values))
			}
		})
	}
}

// TestParseValues_NullValues は値が null の属性でも落ちず、空文字になることを確認する。
func TestParseValues_NullValues(t *testing.T) {
	raw := `{"customFields":[
		{"id":1,"fieldTypeId":1,"name":"文字列項目","value":null},
		{"id":3,"fieldTypeId":3,"name":"数値項目","value":null},
		{"id":4,"fieldTypeId":4,"name":"日付項目","value":null},
		{"id":5,"fieldTypeId":5,"name":"単一リスト項目","value":null},
		{"id":6,"fieldTypeId":6,"name":"複数リスト項目","value":null}
	]}`
	values, err := ParseValues(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 5 {
		t.Fatalf("件数 = %d, want 5", len(values))
	}
	for _, v := range values {
		if got := FormatValue(v); got != "" {
			t.Errorf("%s の表示 = %q, want \"\"", v.Name, got)
		}
	}
}

// TestParseValues_BrokenJSON は壊れた JSON をエラーにすることを確認する
// (空文字・配列など、課題 1 件のオブジェクトではない入力も含む)。
func TestParseValues_BrokenJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"途中で切れている":             `{"customFields":[{"id":1,`,
		"空文字":                  ``,
		"オブジェクトでない":            `[]`,
		"customFields が配列ではない": `{"customFields":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseValues(raw); err == nil {
				t.Fatal("壊れた JSON がエラーにならなかった")
			} else if !strings.Contains(err.Error(), "カスタム属性") {
				t.Errorf("エラーメッセージ = %q", err.Error())
			}
		})
	}
}

// TestFormatValue_DateWithTime は日付型が日時付きで返ってきた場合でも
// 日付部分だけを使うことを確認する(API 応答の揺れへの耐性)。
func TestFormatValue_DateWithTime(t *testing.T) {
	values, err := ParseValues(`{"customFields":[
		{"id":4,"fieldTypeId":4,"name":"日付項目","value":"2026-08-12T00:00:00Z"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("件数 = %d", len(values))
	}
	// 生の値はそのまま保持する(取り出しでは加工しない)
	if s, ok := values[0].Value.(string); !ok || s != "2026-08-12T00:00:00Z" {
		t.Errorf("Value = %#v, want \"2026-08-12T00:00:00Z\"", values[0].Value)
	}
	if got := FormatValue(values[0]); got != "2026-08-12" {
		t.Errorf("FormatValue = %q, want \"2026-08-12\"", got)
	}
}

// TestFormatValue_Numeric は数値の文字列化(整数は小数点なし・それ以外は最短表現)を確認する。
func TestFormatValue_Numeric(t *testing.T) {
	cases := map[string]string{
		"12":      "12",
		"12.0":    "12",
		"12.5":    "12.5",
		"0":       "0",
		"-3.25":   "-3.25",
		"0.1":     "0.1",
		"1000000": "1000000",
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			values, err := ParseValues(`{"customFields":[{"id":3,"fieldTypeId":3,"name":"数値項目","value":` + raw + `}]}`)
			if err != nil {
				t.Fatal(err)
			}
			if got := FormatValue(values[0]); got != want {
				t.Errorf("FormatValue = %q, want %q", got, want)
			}
		})
	}
}

// TestFormatValue_OtherValue は「その他」入力の扱いを確認する。
// 単一リスト / ラジオは選択肢名が無い場合のフォールバック、
// 複数リスト / チェックボックスは選択肢名の末尾への連結(併存し得るため)。
func TestFormatValue_OtherValue(t *testing.T) {
	t.Run("選択肢名がある場合は選択肢名", func(t *testing.T) {
		values, err := ParseValues(`{"customFields":[
			{"id":5,"fieldTypeId":5,"name":"単一リスト項目","value":{"id":51,"name":"選択肢A"},"otherValue":"自由入力"}
		]}`)
		if err != nil {
			t.Fatal(err)
		}
		if values[0].OtherValue != "自由入力" {
			t.Errorf("OtherValue = %q", values[0].OtherValue)
		}
		if got := FormatValue(values[0]); got != "選択肢A" {
			t.Errorf("FormatValue = %q, want \"選択肢A\"", got)
		}
	})

	t.Run("選択肢名が無い場合は otherValue", func(t *testing.T) {
		values, err := ParseValues(`{"customFields":[
			{"id":5,"fieldTypeId":5,"name":"単一リスト項目","value":null,"otherValue":"自由入力"}
		]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatValue(values[0]); got != "自由入力" {
			t.Errorf("FormatValue = %q, want \"自由入力\"", got)
		}
	})

	t.Run("複数リストで選択肢が無ければ otherValue のみ", func(t *testing.T) {
		values, err := ParseValues(`{"customFields":[
			{"id":6,"fieldTypeId":6,"name":"複数リスト項目","value":[],"otherValue":"自由入力"}
		]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatValue(values[0]); got != "自由入力" {
			t.Errorf("FormatValue = %q, want \"自由入力\"", got)
		}
	})

	// 複数選択では「その他」は選択肢と併存するため、フォールバックではなく末尾へ連結する
	// (選択肢だけ表示すると Excel 出力で自由入力分が欠落する)。
	t.Run("複数リストでは選択肢と otherValue を併記", func(t *testing.T) {
		values, err := ParseValues(`{"customFields":[
			{"id":6,"fieldTypeId":6,"name":"複数リスト項目",
			 "value":[{"id":61,"name":"選択肢A"},{"id":62,"name":"選択肢B"}],"otherValue":"自由入力"}
		]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatValue(values[0]); got != "選択肢A, 選択肢B, 自由入力" {
			t.Errorf("FormatValue = %q, want \"選択肢A, 選択肢B, 自由入力\"", got)
		}
	})

	t.Run("チェックボックスも併記", func(t *testing.T) {
		values, err := ParseValues(`{"customFields":[
			{"id":7,"fieldTypeId":7,"name":"チェックボックス項目",
			 "value":[{"id":71,"name":"選択肢A"}],"otherValue":"自由入力"}
		]}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := FormatValue(values[0]); got != "選択肢A, 自由入力" {
			t.Errorf("FormatValue = %q, want \"選択肢A, 自由入力\"", got)
		}
	})
}

// TestFormatValue_UnknownType は未知の型 ID でも値の形から表示できることを確認する
// (Backlog 側に型が追加されても表示が空にならないようにするため)。
func TestFormatValue_UnknownType(t *testing.T) {
	values, err := ParseValues(`{"customFields":[
		{"id":90,"fieldTypeId":99,"name":"未知1","value":"文字"},
		{"id":91,"fieldTypeId":99,"name":"未知2","value":7},
		{"id":92,"fieldTypeId":99,"name":"未知3","value":{"id":1,"name":"選択肢X"}},
		{"id":93,"fieldTypeId":99,"name":"未知4","value":[{"id":1,"name":"X"},{"id":2,"name":"Y"}]},
		{"id":94,"fieldTypeId":99,"name":"未知5","value":true}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"文字", "7", "選択肢X", "X, Y", "true"}
	for i, v := range values {
		if got := FormatValue(v); got != want[i] {
			t.Errorf("FormatValue(%s) = %q, want %q", v.Name, got, want[i])
		}
	}
}

// TestFormatValue_ListWithoutNames は選択肢名が欠けた応答でも落ちないことを確認する。
func TestFormatValue_ListWithoutNames(t *testing.T) {
	values, err := ParseValues(`{"customFields":[
		{"id":5,"fieldTypeId":5,"name":"単一リスト項目","value":{"id":51}},
		{"id":6,"fieldTypeId":6,"name":"複数リスト項目","value":[{"id":61},{"id":62,"name":"選択肢C"}]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatValue(values[0]); got != "" {
		t.Errorf("単一リスト = %q, want \"\"", got)
	}
	// 名前が取れた選択肢だけを連結する(空要素で区切りが崩れないこと)
	if got := FormatValue(values[1]); got != "選択肢C" {
		t.Errorf("複数リスト = %q, want \"選択肢C\"", got)
	}
}
