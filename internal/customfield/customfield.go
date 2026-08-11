// Package customfield は Backlog のカスタム属性(custom fields)を扱う。
//
// カスタム属性はプロジェクトごとに定義され、課題レスポンスの customFields に
// 「定義 ID + 型 ID + 値」の形で現れる。値の JSON 表現は型ごとに異なる
// (文字列・数値・オブジェクト・配列)ため、取り出し(ParseValues)と
// 表示用の文字列化(FormatValue)をこのパッケージに集約し、
// Excel 出力・画面表示・一括更新が同じ規約を共有できるようにする。
//
// 値は表示・出力のためのものであり、入力の実在性判定には使わない。
// そのため、個々の値の形の揺れ(null・想定外の型)は極力エラーにせず空文字へ
// 縮退するが、customFields 自体が配列でない等の構造の破損は、出力が黙って
// 全て空欄になるのを防ぐためエラーとして検知させる。
package customfield

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// カスタム属性の型 ID(Backlog API の typeId / fieldTypeId)。
const (
	TypeText         = 1 // 文字列
	TypeTextArea     = 2 // 文章
	TypeNumeric      = 3 // 数値
	TypeDate         = 4 // 日付
	TypeSingleList   = 5 // 単一リスト
	TypeMultipleList = 6 // 複数リスト
	TypeCheckBox     = 7 // チェックボックス
	TypeRadio        = 8 // ラジオ
)

// TypeName は型 ID の日本語名を返す。
// 未知の型 ID は「不明(N)」として、値(型 ID)が表示から消えないようにする。
func TypeName(typeID int) string {
	switch typeID {
	case TypeText:
		return "文字列"
	case TypeTextArea:
		return "文章"
	case TypeNumeric:
		return "数値"
	case TypeDate:
		return "日付"
	case TypeSingleList:
		return "単一リスト"
	case TypeMultipleList:
		return "複数リスト"
	case TypeCheckBox:
		return "チェックボックス"
	case TypeRadio:
		return "ラジオ"
	default:
		return fmt.Sprintf("不明(%d)", typeID)
	}
}

// Item はリスト系(単一リスト・複数リスト・チェックボックス・ラジオ)の選択肢。
type Item struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	DisplayOrder int    `json:"displayOrder"`
}

// Def はカスタム属性の定義(GET /projects/:id/customFields の 1 件)。
type Def struct {
	ID          int64  `json:"id"`
	TypeID      int    `json:"typeId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	// ApplicableIssueTypes は適用対象の課題種別 ID。
	// 空の場合は「全課題種別に適用」を意味する(API の仕様)。
	ApplicableIssueTypes []int64 `json:"applicableIssueTypes"`
	// AllowInput はリスト系で「その他」の直接入力を許すか(customField_{id}_otherValue)。
	//
	// 注意: 定義取得 API の公式レスポンス例に allowInput は記載が無く、
	// 実際に返るかは実機検証中。応答に無い場合は false になり「不明」と
	// 区別できないため、入力可否の判定(バリデーション)には使わず、
	// 画面表示・ガイドの案内のみに使うこと。
	AllowInput bool `json:"allowInput"`
	// AllowAddItem はリスト系で課題登録時に選択肢自体の追加を許すか。
	// 「その他」直接入力(AllowInput)とは別の機能なので混同しないこと。
	AllowAddItem bool   `json:"allowAddItem"`
	Items        []Item `json:"items"`
}

// Value は課題レスポンス内のカスタム属性の値(customFields の 1 件)。
//
// Value フィールドは型ごとに形が異なるため、生の JSON 値
// (string / float64 / map[string]any / []any / nil)をそのまま保持する。
// 表示用の文字列は FormatValue で得る。
type Value struct {
	ID          int64  `json:"id"`
	FieldTypeID int    `json:"fieldTypeId"`
	Name        string `json:"name"`
	Value       any    `json:"value"`
	// OtherValue はリスト系の「その他」直接入力の値。
	// 複数リスト / チェックボックスでは選択肢と併存し得るため表示では末尾に連結し、
	// 単一リスト / ラジオでは選択肢名が得られない場合のフォールバックとして使う。
	OtherValue string `json:"otherValue"`
}

// ParseValues は課題 1 件の生 JSON からカスタム属性の値を取り出す。
//
// customFields が無い・null の場合(カスタム属性を使わないプロジェクト、
// またはプランが未対応のスペース)は空スライスを返す。
// 課題の JSON 自体が壊れている場合のみエラーを返す。
func ParseValues(rawJSON string) ([]Value, error) {
	var envelope struct {
		CustomFields json.RawMessage `json:"customFields"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &envelope); err != nil {
		return nil, fmt.Errorf("カスタム属性を取り出せません(課題の JSON を解析できません): %w", err)
	}
	if len(envelope.CustomFields) == 0 || string(envelope.CustomFields) == "null" {
		return []Value{}, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(envelope.CustomFields, &elems); err != nil {
		// 配列以外(オブジェクト等)は API 仕様の変化かデータ破損。
		// 黙って「カスタム属性なし」にすると Excel 出力が全て空欄になり
		// 異常に気づけないため、エラーとして検知させる。
		return nil, fmt.Errorf("カスタム属性を取り出せません(customFields が配列ではありません): %w", err)
	}
	out := make([]Value, 0, len(elems))
	for _, e := range elems {
		var v struct {
			ID          *int64  `json:"id"`
			FieldTypeID *int    `json:"fieldTypeId"`
			Name        *string `json:"name"`
			Value       any     `json:"value"`
			OtherValue  any     `json:"otherValue"`
		}
		if err := json.Unmarshal(e, &v); err != nil {
			return nil, fmt.Errorf("カスタム属性の値を解析できません: %w", err)
		}
		val := Value{Value: v.Value, OtherValue: otherValueString(v.OtherValue)}
		if v.ID != nil {
			val.ID = *v.ID
		}
		if v.FieldTypeID != nil {
			val.FieldTypeID = *v.FieldTypeID
		}
		if v.Name != nil {
			val.Name = *v.Name
		}
		out = append(out, val)
	}
	return out, nil
}

// FormatValue は値を表示・Excel 出力用の文字列にする。
//
// 型ごとの規約:
//   - 文字列 / 文章 …… そのまま
//   - 数値 …………………… 整数は小数点なし、それ以外は最短表現
//   - 日付 …………………… yyyy-MM-dd(日時付きで返った場合も日付部分だけ)
//   - 単一リスト / ラジオ … 選択肢名(名前が得られない場合は「その他」入力)
//   - 複数リスト / チェックボックス … 選択肢名を ", " 区切りで連結し、
//     「その他」入力があれば末尾に連結(選択肢と併存し得るため)
//   - 値なし ………………… 空文字
func FormatValue(v Value) string {
	switch v.FieldTypeID {
	case TypeText, TypeTextArea:
		return formatText(v.Value)
	case TypeNumeric:
		return formatNumber(v.Value)
	case TypeDate:
		return formatDate(v.Value)
	case TypeSingleList, TypeRadio:
		return orOtherValue(formatSingleList(v.Value), v.OtherValue)
	case TypeMultipleList, TypeCheckBox:
		return appendOtherValue(formatMultipleList(v.Value), v.OtherValue)
	default:
		// 未知の型は値の形から推測して表示する(Backlog に型が追加されても空にしない)
		return appendOtherValue(formatAny(v.Value), v.OtherValue)
	}
}

// orOtherValue は表示文字列が空のときだけ「その他」入力へフォールバックする。
func orOtherValue(s, otherValue string) string {
	if s != "" {
		return s
	}
	return otherValue
}

// appendOtherValue は「その他」入力を末尾に連結する(複数選択では選択肢と併存するため)。
func appendOtherValue(s, otherValue string) string {
	if otherValue == "" {
		return s
	}
	if s == "" {
		return otherValue
	}
	return s + ", " + otherValue
}

// formatText は文字列 / 文章の値を文字列にする。
func formatText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return formatAny(v)
}

// formatNumber は数値の値を文字列にする。
// JSON の数値は float64 で入るため、整数は小数点なし(12)、
// それ以外は最短表現(12.5)になるよう 'f' / -1 で整形する。
func formatNumber(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case string:
		return n
	default:
		return formatAny(v)
	}
}

// formatDate は日付の値を yyyy-MM-dd にする。
// API が "yyyy-MM-ddTHH:mm:ssZ" を返す可能性に備え、日付部分だけを使う。
func formatDate(v any) string {
	s, ok := v.(string)
	if !ok {
		return formatAny(v)
	}
	if i := strings.IndexAny(s, "T "); i >= 0 {
		return s[:i]
	}
	return s
}

// formatSingleList は単一リスト / ラジオの値({id, name})を選択肢名にする。
func formatSingleList(v any) string {
	if m, ok := v.(map[string]any); ok {
		return itemName(m)
	}
	// 応答の揺れ(配列で返る等)にも耐える
	if _, ok := v.([]any); ok {
		return formatMultipleList(v)
	}
	return ""
}

// formatMultipleList は複数リスト / チェックボックスの値([{id, name}])を
// 選択肢名の ", " 区切りにする。名前が取れない要素は連結から除く。
func formatMultipleList(v any) string {
	switch t := v.(type) {
	case []any:
		names := make([]string, 0, len(t))
		for _, e := range t {
			if name := elementName(e); name != "" {
				names = append(names, name)
			}
		}
		return strings.Join(names, ", ")
	case map[string]any:
		return itemName(t)
	default:
		return ""
	}
}

// formatAny は型が判別できない値を、JSON 上の形から表示用の文字列にする。
func formatAny(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case map[string]any:
		return itemName(t)
	case []any:
		return formatMultipleList(t)
	default:
		return fmt.Sprint(t)
	}
}

// elementName は配列要素({id, name} または素の文字列)から表示名を取り出す。
func elementName(e any) string {
	switch t := e.(type) {
	case map[string]any:
		return itemName(t)
	case string:
		return t
	default:
		return ""
	}
}

// itemName は {id, name} オブジェクトの name を返す(無ければ空文字)。
func itemName(m map[string]any) string {
	if name, ok := m["name"].(string); ok {
		return name
	}
	return ""
}

// otherValueString は otherValue(通常は文字列 / null)を表示用の文字列にする。
func otherValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return formatAny(t)
	}
}
