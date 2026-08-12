package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/bulk"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
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
// 表示名は画面のプレビュー(bulk.ActionLabel)と同じ値を使う(R14)。
func TestBulkRowActionAndStatusLabel(t *testing.T) {
	cases := []struct {
		row  store.JobRow
		want string
	}{
		{store.JobRow{IssueKey: "", Status: store.RowStatusDone}, "新規追加"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusDone}, "更新"},
		{store.JobRow{IssueKey: "EXA-1", Status: store.RowStatusSkip}, "変更なし"},
		// 成功した新規追加行には作成された課題のキーが入る(結果レポートの
		// issueKey 列を埋めるため)。処理区分は作成された課題 ID の有無で判断し、
		// 課題キーが入ったことで「更新」と表示しないようにする
		{store.JobRow{IssueKey: "EXA-9", ResultIssueID: 9, Status: store.RowStatusDone}, "新規追加"},
	}
	for _, c := range cases {
		if got := bulkRowAction(c.row); got != c.want {
			t.Errorf("bulkRowAction(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
	// 行状態の表示名も画面・Excel で共通の 1 か所(bulkRowStatusLabels)から引く
	statusWant := map[string]string{
		store.RowStatusPending:  "未処理",
		store.RowStatusSending:  "送信中(結果未確認)",
		store.RowStatusDone:     "完了",
		store.RowStatusError:    "失敗",
		store.RowStatusConflict: "競合",
		store.RowStatusSkip:     "変更なし",
	}
	for status, want := range statusWant {
		if got := bulkRowStatusLabel(status); got != want {
			t.Errorf("bulkRowStatusLabel(%q) = %q, want %q", status, got, want)
		}
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

// TestIssueRowDTOOf_CustomFields は検索結果 1 行の DTO が、
// 要求された列(cf_{定義ID})のカスタム属性値だけを表示文字列で持つことを確認する(CF4)。
// 全属性を詰めると、列を選んでいない利用者にも解析コストと転送量が掛かるため。
func TestIssueRowDTOOf_CustomFields(t *testing.T) {
	issue := store.Issue{
		IssueKey: "EXA-1", Summary: "件名", StatusName: "未対応",
		RawJSON: `{"id":101,"customFields":[
			{"id":31,"fieldTypeId":1,"value":"取引先 A"},
			{"id":34,"fieldTypeId":5,"value":{"id":341,"name":"高"}},
			{"id":35,"fieldTypeId":3,"value":12.5}
		]}`,
	}

	t.Run("要求した列だけを詰める", func(t *testing.T) {
		row := issueRowDTOOf(&issue, []int64{31, 35})
		if row.IssueKey != "EXA-1" || row.Summary != "件名" || row.StatusName != "未対応" {
			t.Errorf("固定項目 = %+v", row)
		}
		want := map[string]string{"cf_31": "取引先 A", "cf_35": "12.5"}
		if len(row.CustomFields) != len(want) {
			t.Fatalf("customFields = %+v, want %+v", row.CustomFields, want)
		}
		for k, w := range want {
			if row.CustomFields[k] != w {
				t.Errorf("%s = %q, want %q", k, row.CustomFields[k], w)
			}
		}
	})

	t.Run("値を持たない列は空文字で埋める", func(t *testing.T) {
		row := issueRowDTOOf(&issue, []int64{99})
		if got, ok := row.CustomFields["cf_99"]; !ok || got != "" {
			t.Errorf("cf_99 = %q (ok=%v), want 空文字", got, ok)
		}
	})

	t.Run("列を要求しなければ空のマップ", func(t *testing.T) {
		row := issueRowDTOOf(&issue, nil)
		// フロント契約で null を返さない(常にオブジェクト)
		if row.CustomFields == nil {
			t.Fatal("customFields = nil, want 空のマップ")
		}
		if len(row.CustomFields) != 0 {
			t.Errorf("customFields = %+v, want 空", row.CustomFields)
		}
	})

	t.Run("生 JSON が壊れていても行は返る", func(t *testing.T) {
		broken := store.Issue{IssueKey: "EXA-2", RawJSON: "{壊れた JSON"}
		row := issueRowDTOOf(&broken, []int64{31})
		if row.IssueKey != "EXA-2" {
			t.Errorf("行が壊れた: %+v", row)
		}
		if row.CustomFields["cf_31"] != "" {
			t.Errorf("cf_31 = %q, want 空文字", row.CustomFields["cf_31"])
		}
	})
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

// TestParentIssueKeyOf は一括更新テンプレート・課題抽出へプリフィルする
// 親課題の表記(CF5)を確認する。
//
// 同一プロジェクトの親は課題キー、ローカルに無い親(未同期・別プロジェクト)は
// 課題キーと取り違えようのない ID:<数値> 形式にし、取り込み側と往復できるようにする。
func TestParentIssueKeyOf(t *testing.T) {
	keys := map[int64]string{100: "EXA-1"}
	cases := []struct {
		name    string
		rawJSON string
		want    string
	}{
		{"ローカルにある親", `{"id":101,"parentIssueId":100}`, "EXA-1"},
		{"ローカルに無い親", `{"id":101,"parentIssueId":9999}`, "ID:9999"},
		{"親なし", `{"id":101,"parentIssueId":null}`, ""},
		{"生 JSON なし", "", ""},
		{"壊れた生 JSON", "{壊れた JSON", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parentIssueKeyOf(c.rawJSON, keys); got != c.want {
				t.Errorf("parentIssueKeyOf = %q, want %q", got, c.want)
			}
		})
	}
}

// TestIssueDetailDTOOf は課題詳細ポップアップ(画面 2)へ渡す DTO の組み立てを確認する。
//
// カスタム属性は定義取得(API)を行わず、生 JSON の name と表示規約だけで作る。
// 並びは課題レスポンスの順(定義順ではない)。
func TestIssueDetailDTOOf(t *testing.T) {
	issue := &store.Issue{
		IssueKey: "EXA-2", Summary: "件名", Description: "詳細本文",
		StatusName: "処理中", AssigneeName: "山田 太郎",
		IssueTypeName: "タスク", PriorityName: "中",
		Created: "2026-02-01T04:05:06Z", Updated: "2026-02-03T04:05:06Z",
		DueDate: "2026-03-04", FetchedAt: "2026-02-04T00:00:00Z",
		RawJSON: `{"id":102,"parentIssueId":100,"customFields":[
			{"id":34,"fieldTypeId":5,"name":"重要度","value":{"id":341,"name":"高"}},
			{"id":31,"fieldTypeId":1,"name":"顧客名","value":"取引先 A"},
			{"id":35,"fieldTypeId":6,"name":"影響環境","value":[{"id":351,"name":"UI"},{"id":353,"name":"DB"}]}
		]}`,
	}

	t.Run("固定項目と親課題キー", func(t *testing.T) {
		dto := issueDetailDTOOf(issue, map[int64]string{100: "EXA-1"})
		if dto.IssueKey != "EXA-2" || dto.Summary != "件名" || dto.Description != "詳細本文" {
			t.Errorf("固定項目 = %+v", dto)
		}
		if dto.StatusName != "処理中" || dto.AssigneeName != "山田 太郎" ||
			dto.IssueTypeName != "タスク" || dto.PriorityName != "中" {
			t.Errorf("固定項目 = %+v", dto)
		}
		if dto.Created != "2026-02-01T04:05:06Z" || dto.Updated != "2026-02-03T04:05:06Z" ||
			dto.DueDate != "2026-03-04" || dto.FetchedAt != "2026-02-04T00:00:00Z" {
			t.Errorf("日時項目 = %+v", dto)
		}
		if dto.ParentIssueKey != "EXA-1" {
			t.Errorf("親課題キー = %q, want EXA-1", dto.ParentIssueKey)
		}
	})

	t.Run("ローカルに無い親は ID 表記へ縮退する", func(t *testing.T) {
		dto := issueDetailDTOOf(issue, nil)
		if dto.ParentIssueKey != "ID:100" {
			t.Errorf("親課題キー = %q, want ID:100", dto.ParentIssueKey)
		}
	})

	t.Run("カスタム属性は生 JSON の名前と表示規約で作る", func(t *testing.T) {
		dto := issueDetailDTOOf(issue, nil)
		want := []IssueCustomFieldDTO{
			// 定義順ではなく課題レスポンスの順に並ぶ(定義取得を行わないため)
			{Name: "重要度", Value: "高"},
			{Name: "顧客名", Value: "取引先 A"},
			{Name: "影響環境", Value: "UI, DB"},
		}
		if len(dto.CustomFields) != len(want) {
			t.Fatalf("カスタム属性 = %+v, want %+v", dto.CustomFields, want)
		}
		for i, w := range want {
			if dto.CustomFields[i] != w {
				t.Errorf("customFields[%d] = %+v, want %+v", i, dto.CustomFields[i], w)
			}
		}
	})

	t.Run("名前を持たない属性も値が消えないようにする", func(t *testing.T) {
		noName := &store.Issue{
			IssueKey: "EXA-3",
			RawJSON:  `{"id":103,"customFields":[{"id":31,"fieldTypeId":1,"value":"取引先 A"}]}`,
		}
		dto := issueDetailDTOOf(noName, nil)
		if len(dto.CustomFields) != 1 {
			t.Fatalf("カスタム属性 = %+v", dto.CustomFields)
		}
		if dto.CustomFields[0].Name != "(定義 ID 31)" || dto.CustomFields[0].Value != "取引先 A" {
			t.Errorf("customFields[0] = %+v", dto.CustomFields[0])
		}
	})

	t.Run("生 JSON が無い・壊れていても詳細は返る", func(t *testing.T) {
		for _, raw := range []string{"", "{壊れた JSON"} {
			broken := &store.Issue{IssueKey: "EXA-4", Summary: "件名", RawJSON: raw}
			dto := issueDetailDTOOf(broken, map[int64]string{100: "EXA-1"})
			if dto.IssueKey != "EXA-4" || dto.Summary != "件名" {
				t.Errorf("縮退時の固定項目 = %+v", dto)
			}
			if dto.ParentIssueKey != "" {
				t.Errorf("親課題キー = %q, want 空", dto.ParentIssueKey)
			}
			// フロント契約で null を返さない(常に配列)
			if dto.CustomFields == nil {
				t.Error("customFields = nil, want 空スライス")
			}
			if len(dto.CustomFields) != 0 {
				t.Errorf("customFields = %+v, want 空", dto.CustomFields)
			}
		}
	})
}

// TestHasColumn は列選択に特定の列キーが含まれるかの判定を確認する
// (親課題キー列を選んだときだけ課題キーの対応表を作るため)。
func TestHasColumn(t *testing.T) {
	columns := []string{"issueKey", "summary", export.ParentIssueKeyColumn}
	if !hasColumn(columns, export.ParentIssueKeyColumn) {
		t.Errorf("選択済みの列を検出できない: %v", columns)
	}
	if hasColumn([]string{"issueKey"}, export.ParentIssueKeyColumn) {
		t.Errorf("選択していない列を検出した")
	}
}

// TestLimitedIssueVisitor は Excel 出力の件数上限が「逐次書き出しの途中で
// 打ち切る」形で働くことを確認する(R4)。全件をメモリに溜めなくなったため、
// 上限判定は走査中に行い、超えた時点で errExportRowLimit で中断する。
func TestLimitedIssueVisitor(t *testing.T) {
	written := 0
	visit := limitedIssueVisitor(2, func(*store.Issue) error {
		written++
		return nil
	})
	for i := 0; i < 2; i++ {
		if err := visit(&store.Issue{}); err != nil {
			t.Fatalf("%d 件目でエラー: %v", i+1, err)
		}
	}
	err := visit(&store.Issue{})
	if !errors.Is(err, errExportRowLimit) {
		t.Fatalf("上限超過時のエラー = %v, want errExportRowLimit", err)
	}
	if written != 2 {
		t.Errorf("書き出した件数 = %d, want 2(上限を超えた行は書き出さない)", written)
	}
}

// TestLimitedIssueVisitor_PropagatesWriteError は書き出し側のエラーが
// そのまま伝わることを確認する(上限超過と取り違えないため)。
func TestLimitedIssueVisitor_PropagatesWriteError(t *testing.T) {
	want := errors.New("書き出し失敗")
	visit := limitedIssueVisitor(10, func(*store.Issue) error { return want })
	if err := visit(&store.Issue{}); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// TestBulkTemplateRowOf は課題 1 件からテンプレート行への詰め替えを確認する。
// 逐次書き出しでは課題を保持しないため、この変換は 1 件単位で完結する必要がある。
func TestBulkTemplateRowOf(t *testing.T) {
	issue := &store.Issue{
		IssueKey: "EXA-2", Summary: "件名",
		StatusID: 1, StatusName: "未対応",
		AssigneeID: 501, AssigneeName: "山田 太郎",
		IssueTypeName: "タスク", PriorityName: "中",
		DueDate: "2026-03-04T00:00:00Z", Description: "詳細",
		Updated: "2026-02-03T04:05:06Z",
		RawJSON: `{"id":102,"parentIssueId":100,
			"issueType":{"id":11},"priority":{"id":3},
			"customFields":[{"id":31,"fieldTypeId":1,"value":"取引先 A"}]}`,
	}
	row := bulkTemplateRowOf(issue, map[int64]string{100: "EXA-1"})

	if row.IssueKey != "EXA-2" || row.Summary != "件名" {
		t.Errorf("固定項目 = %+v", row)
	}
	// 種別 ID・優先度 ID は生 JSON から補完する(store.Issue は名前しか持たない)
	if row.IssueTypeID != 11 || row.PriorityID != 3 {
		t.Errorf("ID 列 = 種別 %d / 優先度 %d, want 11 / 3", row.IssueTypeID, row.PriorityID)
	}
	if row.ParentIssueKey != "EXA-1" {
		t.Errorf("親課題キー = %q, want EXA-1", row.ParentIssueKey)
	}
	if row.BaseUpdated != "2026-02-03T04:05:06Z" {
		t.Errorf("base_updated = %q(整形せず生値を使うこと)", row.BaseUpdated)
	}
	if row.CustomFields[31] != "取引先 A" {
		t.Errorf("カスタム属性 = %+v", row.CustomFields)
	}
}

// storeWithIssues は課題を投入した一時ローカル DB を返す(結合確認用)。
func storeWithIssues(t *testing.T, n int) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	issues := make([]*store.Issue, 0, n)
	for i := 1; i <= n; i++ {
		issues = append(issues, &store.Issue{
			ID: int64(i), IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1,
			Summary: fmt.Sprintf("件名 %d", i),
			Updated: "2026-02-03T04:05:06Z",
			RawJSON: fmt.Sprintf(`{"id":%d,"parentIssueId":1,
				"customFields":[{"id":31,"fieldTypeId":1,"value":"取引先 %d"}]}`, i, i),
		})
	}
	if err := st.UpsertIssues(context.Background(), issues); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestExportIssuesToFile_StreamsFromStoreCursor は「ローカル DB のカーソル →
// Excel の StreamWriter」という R4 の経路が、全件をスライスに載せずに
// 従来と同じ列(固定列・カスタム属性列・親課題キー列)を出力することを確認する。
// 保存ダイアログ(Wails ランタイム結合)だけを除いた結合確認。
func TestExportIssuesToFile_StreamsFromStoreCursor(t *testing.T) {
	const n = 50
	st := storeWithIssues(t, n)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "issues.xlsx")
	opts := export.Options{
		Columns:         []string{"issueKey", export.ParentIssueKeyColumn, export.CustomColumnKey(31)},
		CustomFields:    []customfield.Def{{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"}},
		ParentIssueKeys: map[int64]string{1: "EXA-1"},
	}
	var res store.IssueIterateResult
	seq := func(yield func(*store.Issue) error) error {
		var err error
		res, err = st.IterateIssues(ctx, store.IssueFilter{ProjectID: 1}, yield)
		return err
	}
	if err := export.ExportIssuesToFile(path, seq, opts); err != nil {
		t.Fatalf("ExportIssuesToFile: %v", err)
	}
	if res.Total != n {
		t.Errorf("走査件数 = %d, want %d", res.Total, n)
	}

	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows(export.SheetIssues)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n+1 {
		t.Fatalf("行数 = %d, want %d", len(rows), n+1)
	}
	for i := 1; i <= n; i++ {
		want := []string{fmt.Sprintf("EXA-%d", i), "EXA-1", fmt.Sprintf("取引先 %d", i)}
		for c, w := range want {
			if rows[i][c] != w {
				t.Errorf("%d 行 %d 列 = %q, want %q", i+1, c+1, rows[i][c], w)
			}
		}
	}
}

// TestExportIssuesToFile_RowLimitLeavesExistingFile は、件数上限で打ち切った
// 出力が既存ファイルを壊さないことを確認する(R4 の打ち切りと R5 の
// 一時ファイル置換の組み合わせ)。
func TestExportIssuesToFile_RowLimitLeavesExistingFile(t *testing.T) {
	st := storeWithIssues(t, 5)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "issues.xlsx")
	const existing = "既存の内容"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	seq := func(yield func(*store.Issue) error) error {
		_, err := st.IterateIssues(ctx, store.IssueFilter{ProjectID: 1},
			limitedIssueVisitor(2, yield)) // 上限 2 件 < 5 件
		return err
	}
	err := export.ExportIssuesToFile(path, seq, export.Options{})
	if !errors.Is(err, errExportRowLimit) {
		t.Fatalf("err = %v, want errExportRowLimit", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Errorf("既存ファイルが変更された: %q", string(got))
	}
}

// TestNewProjectRows は、プロジェクト一覧と同期状態一覧(1 回の取得)の
// 突き合わせが、プロジェクトごとに同期状態を引いていた頃と同じ結果になることを
// 確認する(R18: N+1 解消)。
func TestNewProjectRows(t *testing.T) {
	projects := []store.Project{
		{ID: 1, ProjectKey: "EXA", Name: "検証用"},
		{ID: 2, ProjectKey: "SUB", Name: "未同期"},
	}
	states := []store.SyncState{
		// 課題以外の種別・別プロジェクトの行が混ざっていても取り違えない
		{DataKind: store.DataKindUsers, ProjectID: store.ProjectScopeAll, LastSyncedAt: "2026-08-10T00:00:00Z"},
		{DataKind: store.DataKindIssues, ProjectID: 1, LastSyncedAt: "2026-08-12T09:00:00Z"},
		{DataKind: store.DataKindProjects, ProjectID: 2, LastSyncedAt: "2026-08-11T00:00:00Z"},
	}

	got := newProjectRows(projects, states, false)
	want := []ProjectRow{
		{ID: 1, ProjectKey: "EXA", Name: "検証用", LastSyncedAt: "2026-08-12T09:00:00Z"},
		{ID: 2, ProjectKey: "SUB", Name: "未同期", LastSyncedAt: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("行数 = %d, want %d(%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("行 %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestNewProjectRows_UnknownSyncState は、同期状態を取得できなかった場合に
// 全行が「不明」になり、最終同期時刻を「未同期」と断定しないことを確認する(中 1)。
func TestNewProjectRows_UnknownSyncState(t *testing.T) {
	projects := []store.Project{{ID: 1, ProjectKey: "EXA", Name: "検証用"}}

	got := newProjectRows(projects, nil, true)
	if len(got) != 1 {
		t.Fatalf("行数 = %d, want 1", len(got))
	}
	if !got[0].SyncStateUnknown {
		t.Errorf("SyncStateUnknown = false, want true(%+v)", got[0])
	}
	if got[0].LastSyncedAt != "" {
		t.Errorf("LastSyncedAt = %q, want \"\"", got[0].LastSyncedAt)
	}
}

// TestNewProjectRows_EmptyIsNotNil は、プロジェクトが 0 件でも JSON が
// null にならない(フロントが配列として扱える)ことを確認する。
func TestNewProjectRows_EmptyIsNotNil(t *testing.T) {
	if got := newProjectRows(nil, nil, false); got == nil {
		t.Error("newProjectRows = nil, want 空スライス")
	}
}

// TestSyncProgressPayload は課題同期の進捗イベント(sync:progress)の
// ペイロードが、フロントエンド(backend.ts の SyncProgress)の期待どおりの
// キー・型で組み立てられることを確認する。
// イベント送信そのものは Wails ランタイム結合のため手動確認とする(TDD 例外)。
func TestSyncProgressPayload(t *testing.T) {
	got := syncProgressPayload(service.SyncProgressEvent{
		ProfileID: "profile-1",
		RunID:     "run-7",
		ProjectID: 42,
		Progress:  syncpkg.Progress{Phase: syncpkg.PhaseFetch, Fetched: 300, Total: 1200},
	})
	want := map[string]any{
		"profileId": "profile-1",
		"runId":     "run-7",
		"projectId": int64(42),
		"phase":     "fetch",
		"fetched":   300,
		"total":     1200,
	}
	if len(got) != len(want) {
		t.Fatalf("キー数 = %d, want %d(%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
}
