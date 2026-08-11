package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/store"
)

// TestFileExtAttrRecordsOnlyExtension は、動作ログに残すのが拡張子だけであり、
// ユーザが付けたファイル名(顧客名を含みうる)が残らないことを確認する(低 1)。
func TestFileExtAttrRecordsOnlyExtension(t *testing.T) {
	attr := fileExtAttr("/home/someuser/Documents/顧客A/2026年度_顧客A_課題一覧.XLSX")
	if attr.Key != "ext" {
		t.Errorf("キー = %q, want \"ext\"", attr.Key)
	}
	if got := attr.Value.String(); got != ".xlsx" {
		t.Errorf("値 = %q, want \".xlsx\"", got)
	}
	if strings.Contains(attr.Value.String(), "顧客A") {
		t.Errorf("ファイル名が記録されています: %q", attr.Value.String())
	}
}

// TestBulkRowActionAndStatusLabel は結果レポートの表示名解決を確認する(高 5)。
// 処理区分は payload を解析せず、行状態と課題キーの有無だけで決める。
func TestBulkRowActionAndStatusLabel(t *testing.T) {
	cases := []struct {
		row  store.JobRow
		want string
	}{
		{store.JobRow{IssueKey: "", Status: store.RowStatusDone}, "追加"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusDone}, "更新"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusSkip}, "変更なし"},
	}
	for _, c := range cases {
		if got := bulkRowAction(c.row); got != c.want {
			t.Errorf("bulkRowAction(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
	if got := bulkRowStatusLabel(store.RowStatusSending); got != "送信中(結果未確認)" {
		t.Errorf("sending の表示名 = %q", got)
	}
	// 未知の状態はそのまま返す(表示から値が消えないようにする)
	if got := bulkRowStatusLabel("unknown"); got != "unknown" {
		t.Errorf("未知の状態 = %q, want \"unknown\"", got)
	}
}

// TestMaskPathInErrorReplacesFullPathWithPlaceholder は、Excel 出力の失敗メッセージから
// 保存先のフルパスが消え、固定のプレースホルダに置換されることを確認する
// (高 2 / 2 回目 低 1)。ファイル名も顧客名・案件名を含みうるため記録しない。
func TestMaskPathInErrorReplacesFullPathWithPlaceholder(t *testing.T) {
	path := "/home/someuser/Documents/顧客A/backlog-issues.xlsx"

	t.Run("フルパスがプレースホルダに置換される", func(t *testing.T) {
		err := fmt.Errorf("ファイルを保存できません: open %s: permission denied", path)
		got := maskPathInError(err, path)
		if got == nil {
			t.Fatal("maskPathInError = nil, want 非 nil")
		}
		want := "ファイルを保存できません: open " + maskedPathPlaceholder + ": permission denied"
		if got.Error() != want {
			t.Errorf("maskPathInError = %q, want %q", got.Error(), want)
		}
	})

	t.Run("ファイル名も残らない", func(t *testing.T) {
		err := fmt.Errorf("ファイルを保存できません: open %s: permission denied", path)
		got := maskPathInError(err, path)
		if strings.Contains(got.Error(), "backlog-issues") {
			t.Errorf("ファイル名が残っています: %q", got.Error())
		}
	})

	t.Run("複数箇所すべて置換される", func(t *testing.T) {
		err := fmt.Errorf("rename %s %s: cross-device link", path, path)
		got := maskPathInError(err, path)
		if strings.Contains(got.Error(), "顧客A") || strings.Contains(got.Error(), "someuser") {
			t.Errorf("パスが残っています: %q", got.Error())
		}
	})

	t.Run("パスを含まないエラーはそのまま", func(t *testing.T) {
		err := errors.New("列の指定が不正です")
		if got := maskPathInError(err, path); got.Error() != err.Error() {
			t.Errorf("maskPathInError = %q, want %q", got.Error(), err.Error())
		}
	})

	t.Run("nil と空パスは安全", func(t *testing.T) {
		if got := maskPathInError(nil, path); got != nil {
			t.Errorf("maskPathInError(nil) = %v, want nil", got)
		}
		err := errors.New("失敗")
		if got := maskPathInError(err, ""); got != err {
			t.Errorf("空パスでエラーが差し替えられました: %v", got)
		}
	})
}

// TestNewMasterDataDTO_ConvertsCustomFields はカスタム属性定義が DTO へ写り、
// 型名がフロント側で解決不要な形(typeName)で載ることを確認する。
func TestNewMasterDataDTO_ConvertsCustomFields(t *testing.T) {
	dto := newMasterDataDTO(&bulk.MasterData{
		IssueTypes: []bulk.NamedID{{ID: 11, Name: "タスク"}},
		Priorities: []bulk.NamedID{{ID: 3, Name: "中"}},
		Statuses:   []bulk.NamedID{{ID: 1, Name: "未対応"}},
		CustomFields: []customfield.Def{
			{
				ID: 31, TypeID: customfield.TypeSingleList, Name: "重要度",
				Description: "説明", Required: true,
				ApplicableIssueTypes: []int64{11, 12}, AllowAddItem: true,
				Items: []customfield.Item{{ID: 311, Name: "高", DisplayOrder: 0}},
			},
			// 型 ID が未知でも表示から値が消えないこと
			{ID: 32, TypeID: 99, Name: "将来の型"},
		},
	})

	if len(dto.CustomFields) != 2 {
		t.Fatalf("カスタム属性 = %+v", dto.CustomFields)
	}
	first := dto.CustomFields[0]
	if first.ID != 31 || first.TypeID != customfield.TypeSingleList || first.TypeName != "単一リスト" {
		t.Errorf("customFields[0] = %+v", first)
	}
	if first.Name != "重要度" || first.Description != "説明" || !first.Required || !first.AllowAddItem {
		t.Errorf("customFields[0] = %+v", first)
	}
	if len(first.ApplicableIssueTypes) != 2 || first.ApplicableIssueTypes[1] != 12 {
		t.Errorf("customFields[0].ApplicableIssueTypes = %v", first.ApplicableIssueTypes)
	}
	if len(first.Items) != 1 || first.Items[0].ID != 311 || first.Items[0].Name != "高" {
		t.Errorf("customFields[0].Items = %+v", first.Items)
	}
	if dto.CustomFields[1].TypeName != "不明(99)" {
		t.Errorf("未知の型名 = %q", dto.CustomFields[1].TypeName)
	}
}

// TestNewMasterDataDTO_NormalizesNilSlices は nil スライスを空スライスへ
// 正規化すること(フロント契約: null を返さない)を確認する。
func TestNewMasterDataDTO_NormalizesNilSlices(t *testing.T) {
	dto := newMasterDataDTO(&bulk.MasterData{
		CustomFields: []customfield.Def{{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"}},
	})

	if dto.IssueTypes == nil || dto.Priorities == nil || dto.Statuses == nil {
		t.Errorf("マスタが nil: %+v", dto)
	}
	if dto.CustomFields[0].ApplicableIssueTypes == nil {
		t.Error("applicableIssueTypes が nil(空スライスにすること)")
	}
	if dto.CustomFields[0].Items == nil {
		t.Error("items が nil(空スライスにすること)")
	}

	// カスタム属性そのものが nil(未対応スペースでの縮退)でも空スライスを返す
	empty := newMasterDataDTO(&bulk.MasterData{})
	if empty.CustomFields == nil || len(empty.CustomFields) != 0 {
		t.Errorf("customFields = %#v, want 空スライス", empty.CustomFields)
	}
}

// TestBulkCustomFieldValues は一括更新テンプレートへプリフィルする
// カスタム属性の現在値(定義 ID → 表示文字列)を確認する(CF3)。
// 生 JSON を解釈できない課題は、テンプレート出力全体を止めないため空にする。
func TestBulkCustomFieldValues(t *testing.T) {
	values := bulkCustomFieldValues(`{"id":101,"customFields":[
		{"id":31,"fieldTypeId":1,"value":"取引先 A"},
		{"id":34,"fieldTypeId":5,"value":{"id":341,"name":"高"}},
		{"id":35,"fieldTypeId":6,"value":[{"id":351,"name":"UI"},{"id":353,"name":"DB"}]}
	]}`)
	want := map[int64]string{31: "取引先 A", 34: "高", 35: "UI, DB"}
	if len(values) != len(want) {
		t.Fatalf("カスタム属性 = %+v, want %+v", values, want)
	}
	for id, w := range want {
		if values[id] != w {
			t.Errorf("定義 %d = %q, want %q", id, values[id], w)
		}
	}
	if got := bulkCustomFieldValues(""); len(got) != 0 {
		t.Errorf("生 JSON が無い課題 = %+v, want 空", got)
	}
	if got := bulkCustomFieldValues("{壊れた JSON"); len(got) != 0 {
		t.Errorf("壊れた生 JSON = %+v, want 空", got)
	}
}
