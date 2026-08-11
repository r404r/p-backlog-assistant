package bulk

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// testCustomFields は検証用のカスタム属性定義(型ごとの扱いを網羅する)。
// 実データではない。
func testCustomFields() []customfield.Def {
	return []customfield.Def{
		{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"},
		{ID: 32, TypeID: customfield.TypeNumeric, Name: "見積工数"},
		{ID: 33, TypeID: customfield.TypeDate, Name: "開始日"},
		{
			ID: 34, TypeID: customfield.TypeSingleList, Name: "重要度",
			Items: []customfield.Item{{ID: 341, Name: "高"}, {ID: 342, Name: "低"}},
		},
		{
			ID: 35, TypeID: customfield.TypeMultipleList, Name: "タグ",
			Items: []customfield.Item{{ID: 351, Name: "UI"}, {ID: 352, Name: "API"}, {ID: 353, Name: "DB"}},
		},
		{
			ID: 36, TypeID: customfield.TypeCheckBox, Name: "対象OS",
			Items: []customfield.Item{{ID: 361, Name: "Windows"}, {ID: 362, Name: "macOS"}},
		},
		{
			ID: 37, TypeID: customfield.TypeRadio, Name: "区分",
			Items: []customfield.Item{{ID: 371, Name: "社内"}, {ID: 372, Name: "社外"}},
		},
		// 種別「タスク」(11)にだけ適用される必須属性
		{
			ID: 38, TypeID: customfield.TypeText, Name: "必須メモ",
			Required: true, ApplicableIssueTypes: []int64{11},
		},
	}
}

// testMasterWithCustomFields はカスタム属性付きのマスタ。
func testMasterWithCustomFields() MasterData {
	m := testMaster()
	m.CustomFields = testCustomFields()
	return m
}

// customTemplateHeaders は固定列 + カスタム属性列(定義順)のヘッダ。
// 列はヘッダ名で解決するため、この検証で使わない親課題キー列は省いている。
var customTemplateHeaders = append(append([]string{}, templateHeaders...),
	"属性:顧客名", "属性:見積工数", "属性:開始日", "属性:重要度",
	"属性:タグ", "属性:対象OS", "属性:区分", "属性:必須メモ")

// cfColumn はカスタム属性列の位置(0 始まり)を返す。
func cfColumn(name string) int {
	for i, h := range customTemplateHeaders {
		if h == export.BulkCustomHeader(name) {
			return i
		}
	}
	panic("未知のカスタム属性: " + name)
}

// cfUpdateRow は既存課題の更新行を作る(固定列は課題キーと base_updated のみ)。
func cfUpdateRow(issueKey, baseUpdated string, cf map[string]string) []string {
	row := make([]string, len(customTemplateHeaders))
	row[0] = issueKey
	row[12] = baseUpdated
	return fillCustomCells(row, cf)
}

// cfCreateRow は新規追加行を作る(件名・種別名を指定)。
func cfCreateRow(summary, issueTypeName string, cf map[string]string) []string {
	row := make([]string, len(customTemplateHeaders))
	row[1] = summary
	row[3] = issueTypeName
	return fillCustomCells(row, cf)
}

func fillCustomCells(row []string, cf map[string]string) []string {
	for name, v := range cf {
		row[cfColumn(name)] = v
	}
	return row
}

// importCustomFile はカスタム属性付きテンプレートを取り込む。
func importCustomFile(t *testing.T, st *store.Store, rows [][]string) *ImportResult {
	t.Helper()
	return importFileWithMaster(t, st, customTemplateHeaders, rows, 3, testMasterWithCustomFields())
}

// TestImport_CustomField_ResolvesHeaderByName は「属性:{定義名}」列を
// 定義名で解決し、差分・payload に反映することを確認する。
func TestImport_CustomField_ResolvesHeaderByName(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{"顧客名": "取引先 B"}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionUpdate {
		t.Fatalf("preview = %+v", p)
	}
	if !containsSubstring(p.Changes, "顧客名: 取引先 A → 取引先 B") {
		t.Errorf("差分 = %v", p.Changes)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if len(payload.CustomFields) != 1 {
		t.Fatalf("カスタム属性 = %+v", payload.CustomFields)
	}
	got := payload.CustomFields[0]
	if got.ID != 31 || got.TypeID != customfield.TypeText || got.Text != "取引先 B" || got.Clear {
		t.Errorf("カスタム属性 = %+v", got)
	}
}

// TestImport_CustomField_UnknownHeader は定義に無い「属性:」列を
// エラーにする(黙殺すると記入した内容が反映されないまま実行される)。
func TestImport_CustomField_UnknownHeader(t *testing.T) {
	st := openTestStore(t)
	headers := append(append([]string{}, templateHeaders...), "属性:存在しない属性")
	path := writeXLSX(t, headers, [][]string{
		{"EXA-1", "", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z", "値"},
	})
	_, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3,
		Master: testMasterWithCustomFields(),
	})
	if err == nil {
		t.Fatal("未知のカスタム属性列が受理された")
	}
	if !strings.Contains(err.Error(), "存在しない属性") {
		t.Errorf("メッセージ = %q", err.Error())
	}
}

// TestImport_CustomField_DuplicateDefinitionName は定義名が重複する
// プロジェクトの取り込みを拒否することを確認する(どちらの定義か決められない)。
func TestImport_CustomField_DuplicateDefinitionName(t *testing.T) {
	st := openTestStore(t)
	master := testMaster()
	master.CustomFields = []customfield.Def{
		{ID: 31, TypeID: customfield.TypeText, Name: "顧客名"},
		{ID: 39, TypeID: customfield.TypeText, Name: "顧客名"},
	}
	path := writeXLSX(t, templateHeaders, [][]string{
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
	})
	_, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: master,
	})
	if err == nil {
		t.Fatal("定義名の重複が受理された")
	}
	if !strings.Contains(err.Error(), "顧客名") {
		t.Errorf("メッセージ = %q", err.Error())
	}
}

// TestImport_CustomField_TypeValidation は型ごとの入力検証を確認する。
func TestImport_CustomField_TypeValidation(t *testing.T) {
	t.Run("正常値", func(t *testing.T) {
		st := openTestStore(t)
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{
				"見積工数": "12.5",
				"開始日":  "2026-05-06",
				"重要度":  "低",
				"タグ":   "UI, DB",
				"対象OS": "Windows、macOS",
				"区分":   "社外",
			}),
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		got := map[int64]customfield.InputValue{}
		for _, v := range payload.CustomFields {
			got[v.ID] = v
		}
		if v := got[32]; v.Text != "12.5" {
			t.Errorf("見積工数 = %+v", v)
		}
		if v := got[33]; v.Text != "2026-05-06" {
			t.Errorf("開始日 = %+v", v)
		}
		if v := got[34]; len(v.ItemIDs) != 1 || v.ItemIDs[0] != 342 {
			t.Errorf("重要度 = %+v", v)
		}
		// 選択肢の並びは定義順に正規化する(入力順に依存しない)
		if v := got[35]; len(v.ItemIDs) != 2 || v.ItemIDs[0] != 351 || v.ItemIDs[1] != 353 {
			t.Errorf("タグ = %+v", v)
		}
		// 「、」区切りも受理する
		if v := got[36]; len(v.ItemIDs) != 2 || v.ItemIDs[0] != 361 || v.ItemIDs[1] != 362 {
			t.Errorf("対象OS = %+v", v)
		}
		if v := got[37]; len(v.ItemIDs) != 1 || v.ItemIDs[0] != 372 {
			t.Errorf("区分 = %+v", v)
		}
	})

	t.Run("数値の書式エラー", func(t *testing.T) {
		st := openTestStore(t)
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"見積工数": "たくさん"}),
		})
		if res.Valid {
			t.Fatal("数値でない値が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "見積工数") || !strings.Contains(msg, "数値") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("日付の書式エラー", func(t *testing.T) {
		st := openTestStore(t)
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"開始日": "2026年5月6日"}),
		})
		if res.Valid {
			t.Fatal("不正な日付が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "開始日") || !strings.Contains(msg, "yyyy-MM-dd") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("選択肢に無い名前はエラー", func(t *testing.T) {
		st := openTestStore(t)
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"重要度": "最高"}),
		})
		if res.Valid {
			t.Fatal("選択肢に無い名前が受理された")
		}
		msg := errorMessages(res)[2]
		if !strings.Contains(msg, "重要度") || !strings.Contains(msg, "最高") {
			t.Errorf("メッセージ = %q", msg)
		}
		// 「その他」の直接入力(otherValue)は未対応であることを明示する
		if !strings.Contains(msg, "その他") || !strings.Contains(msg, "未対応") {
			t.Errorf("「その他」未対応の案内が無い: %q", msg)
		}
	})

	t.Run("複数リストの一部が解決できない場合もエラー", func(t *testing.T) {
		st := openTestStore(t)
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"タグ": "UI, 未知"}),
		})
		if res.Valid {
			t.Fatal("解決できない選択肢が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "未知") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("選択肢名が重複する定義はエラー", func(t *testing.T) {
		st := openTestStore(t)
		master := testMasterWithCustomFields()
		master.CustomFields = append(master.CustomFields, customfield.Def{
			ID: 40, TypeID: customfield.TypeSingleList, Name: "曖昧",
			Items: []customfield.Item{{ID: 401, Name: "同名"}, {ID: 402, Name: "同名"}},
		})
		headers := append(append([]string{}, customTemplateHeaders...), "属性:曖昧")
		row := cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", nil)
		row = append(row, "同名")
		res := importFileWithMaster(t, st, headers, [][]string{row}, 3, master)
		if res.Valid {
			t.Fatal("曖昧な選択肢が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "一意に決められません") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("選択肢名にカンマを含む複数リストはエラー", func(t *testing.T) {
		st := openTestStore(t)
		master := testMasterWithCustomFields()
		master.CustomFields = append(master.CustomFields, customfield.Def{
			ID: 41, TypeID: customfield.TypeMultipleList, Name: "紛らわしい",
			Items: []customfield.Item{{ID: 411, Name: "A, B"}, {ID: 412, Name: "C"}},
		})
		headers := append(append([]string{}, customTemplateHeaders...), "属性:紛らわしい")
		row := cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", nil)
		row = append(row, "C")
		res := importFileWithMaster(t, st, headers, [][]string{row}, 3, master)
		if res.Valid {
			t.Fatal("カンマを含む選択肢の定義が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "カンマ") {
			t.Errorf("メッセージ = %q", msg)
		}
	})
}

// TestImport_CustomField_NoChangeIsSkip は現在値と同じ入力で
// 差分を作らない(空の PATCH を送らない)ことを確認する。
func TestImport_CustomField_NoChangeIsSkip(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{
			"顧客名": "取引先 A",
			"開始日": "2026-05-06",
			"重要度": "高",
			// 現在値は「UI, DB」。並び順が違っても変更なしと判定する
			"タグ": "DB, UI",
		}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionSkip {
		t.Fatalf("preview = %+v, want skip", p)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if len(payload.CustomFields) != 0 {
		t.Errorf("変更が無いのに送信対象になった: %+v", payload.CustomFields)
	}
}

// TestImport_CustomField_ListDiff はリスト系の差分表示を確認する。
func TestImport_CustomField_ListDiff(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{
			"重要度": "低",
			"タグ":  "API",
		}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil {
		t.Fatal("プレビューが無い")
	}
	for _, want := range []string{"重要度: 高 → 低", "タグ: UI, DB → API"} {
		if !containsSubstring(p.Changes, want) {
			t.Errorf("差分に %q が無い: %v", want, p.Changes)
		}
	}
}

// TestImport_CustomField_Clear は #CLEAR# の扱いを確認する。
func TestImport_CustomField_Clear(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		// 現在値あり → クリア
		cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{"顧客名": ClearToken}),
		// 現在値なし → 変更なし(空の送信をしない)
		cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"顧客名": ClearToken}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || !containsSubstring(p.Changes, "顧客名: 取引先 A → (クリア)") {
		t.Errorf("差分 = %+v", p)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if len(payload.CustomFields) != 1 || !payload.CustomFields[0].Clear {
		t.Errorf("カスタム属性 = %+v", payload.CustomFields)
	}
	if p := previewOf(res, 3); p == nil || p.Action != ActionSkip {
		t.Errorf("現在値が無い行 = %+v, want skip", p)
	}
}

// TestImport_CustomField_ClearNotAllowedOnCreate は新規追加行の #CLEAR# を
// 既存フィールドと同じく拒否することを確認する(クリアすべき既存値が無い)。
func TestImport_CustomField_ClearNotAllowedOnCreate(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		cfCreateRow("新規課題", "バグ", map[string]string{"顧客名": ClearToken}),
	})
	if res.Valid {
		t.Fatal("新規追加行の #CLEAR# が受理された")
	}
	if msg := errorMessages(res)[2]; !strings.Contains(msg, ClearToken) {
		t.Errorf("メッセージ = %q", msg)
	}
}

// TestImport_CustomField_RequiredOnCreate は新規追加行の必須チェックを確認する。
// 必須判定は「required かつ行の課題種別に適用される定義」に限る。
func TestImport_CustomField_RequiredOnCreate(t *testing.T) {
	st := openTestStore(t)

	t.Run("適用種別の新規行で未入力はエラー", func(t *testing.T) {
		res := importCustomFile(t, st, [][]string{
			cfCreateRow("新規課題", "タスク", nil),
		})
		if res.Valid {
			t.Fatal("必須のカスタム属性が未入力なのに受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "必須メモ") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("適用外の種別なら未入力でもよい", func(t *testing.T) {
		res := importCustomFile(t, st, [][]string{
			cfCreateRow("新規課題", "バグ", nil),
		})
		if !res.Valid {
			t.Fatalf("適用外の種別でエラーになった: %+v", res.Errors)
		}
	})

	t.Run("入力があれば新規行の payload に載る", func(t *testing.T) {
		res := importCustomFile(t, st, [][]string{
			cfCreateRow("新規課題", "タスク", map[string]string{"必須メモ": "メモ", "重要度": "高"}),
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if len(payload.CustomFields) != 2 {
			t.Fatalf("カスタム属性 = %+v", payload.CustomFields)
		}
		p := previewOf(res, 2)
		if !containsSubstring(p.Changes, "重要度: 高") || !containsSubstring(p.Changes, "必須メモ: メモ") {
			t.Errorf("差分 = %v", p.Changes)
		}
	})

	t.Run("更新行では必須チェックしない", func(t *testing.T) {
		// 空欄 = 変更しない のため、未入力でも既存の値が消えるわけではない
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{"顧客名": "取引先 B"}),
		})
		if !res.Valid {
			t.Fatalf("更新行で必須チェックが働いた: %+v", res.Errors)
		}
	})
}

// TestImport_CustomField_AcceptsExportedTemplate は出力したテンプレートを
// そのまま取り込めること(出力と取り込みの列ヘッダ契約)を確認する。
func TestImport_CustomField_AcceptsExportedTemplate(t *testing.T) {
	st := openTestStore(t)
	master := testMasterWithCustomFields()
	path := filepath.Join(t.TempDir(), "template.xlsx")
	err := export.ExportBulkTemplateToFile(path, testProjectID, []export.BulkTemplateRow{{
		IssueKey: "EXA-1", Summary: "ログイン不具合",
		IssueTypeID: 11, IssueTypeName: "タスク",
		StatusID: 1, StatusName: "未対応",
		PriorityID: 3, PriorityName: "中",
		AssigneeID: 501, AssigneeName: "山田 太郎",
		BaseUpdated: "2026-08-01T00:00:00Z",
		// テンプレートには現在値がプリフィルされる
		CustomFields: map[int64]string{31: "取引先 A", 33: "2026-05-06", 34: "高", 35: "UI, DB"},
	}}, export.BulkTemplateMasters{
		IssueTypes:   []export.NamedRef{{ID: 11, Name: "タスク"}},
		Statuses:     []export.NamedRef{{ID: 1, Name: "未対応"}},
		Priorities:   []export.NamedRef{{ID: 3, Name: "中"}},
		Assignees:    []export.NamedRef{{ID: 501, Name: "山田 太郎"}},
		CustomFields: master.CustomFields,
	})
	if err != nil {
		t.Fatal(err)
	}

	importExported := func() *ImportResult {
		res, ierr := NewImporter(st).Import(context.Background(), ImportOptions{
			ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: master,
		})
		if ierr != nil {
			t.Fatal(ierr)
		}
		return res
	}

	// 未編集なら「変更なし」(プリフィルした現在値がそのまま解決できる)
	res := importExported()
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionSkip {
		t.Fatalf("未編集の行 = %+v, want skip", p)
	}

	// 属性列(R 列 = 属性:重要度)を編集すると更新になる
	editTemplateCells(t, path, map[string]string{"R2": "低"})
	edited := importExported()
	if !edited.Valid {
		t.Fatalf("カスタム属性の編集がエラーになった: %+v", edited.Errors)
	}
	payload := payloadOfRow(t, st, edited.JobID, 2)
	if len(payload.CustomFields) != 1 || payload.CustomFields[0].ID != 34 ||
		len(payload.CustomFields[0].ItemIDs) != 1 || payload.CustomFields[0].ItemIDs[0] != 342 {
		t.Errorf("カスタム属性 = %+v", payload.CustomFields)
	}
}

// TestPayload_CustomFields_EncodeDecode は送信内容の永続化(job_rows.payload)が
// カスタム属性を往復できること、カスタム属性の無い旧ジョブも復元できることを確認する。
func TestPayload_CustomFields_EncodeDecode(t *testing.T) {
	p := Payload{
		Action:  ActionUpdate,
		Summary: ptrString("件名"),
		CustomFields: []customfield.InputValue{
			{ID: 31, TypeID: customfield.TypeText, Text: "取引先 A"},
			{ID: 35, TypeID: customfield.TypeMultipleList, ItemIDs: []int64{351, 353}},
			{ID: 33, TypeID: customfield.TypeDate, Clear: true},
		},
	}
	encoded, err := EncodePayload(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePayload(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CustomFields) != 3 {
		t.Fatalf("カスタム属性 = %+v", got.CustomFields)
	}
	if got.CustomFields[1].ID != 35 || len(got.CustomFields[1].ItemIDs) != 2 ||
		got.CustomFields[1].ItemIDs[0] != 351 {
		t.Errorf("選択肢 = %+v", got.CustomFields[1])
	}
	if !got.CustomFields[2].Clear {
		t.Errorf("クリア指定が失われた: %+v", got.CustomFields[2])
	}

	// 後方互換: カスタム属性を持たない旧ジョブの payload
	legacy, err := DecodePayload(`{"action":"update","summary":"件名","assigneeId":0}`)
	if err != nil {
		t.Fatalf("旧 payload を復元できない: %v", err)
	}
	if legacy.CustomFields != nil {
		t.Errorf("カスタム属性 = %+v, want nil", legacy.CustomFields)
	}
	if legacy.Summary == nil || *legacy.Summary != "件名" {
		t.Errorf("旧 payload の件名 = %+v", legacy.Summary)
	}
}

// TestRun_CustomField_SendsParameters は取り込んだカスタム属性が
// 実行時のリクエストへ渡ることを確認する(更新・新規追加)。
func TestRun_CustomField_SendsParameters(t *testing.T) {
	st := openTestStore(t)
	res := importCustomFile(t, st, [][]string{
		cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{"重要度": "低"}),
		cfCreateRow("新規課題", "バグ", map[string]string{"顧客名": "取引先 C"}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	api := newFakeAPI()
	api.remote["EXA-1"] = backlogclient.Issue{
		ID: 101, IssueKey: "EXA-1", ProjectID: testProjectID, Updated: "2026-08-01T00:00:00Z",
	}
	if _, err := newTestEngine(api, st).Run(context.Background(), res.JobID, RunOptions{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 1 || len(api.updates[0].in.CustomFields) != 1 {
		t.Fatalf("更新リクエスト = %+v", api.updates)
	}
	if got := api.updates[0].in.CustomFields[0]; got.ID != 34 || len(got.ItemIDs) != 1 || got.ItemIDs[0] != 342 {
		t.Errorf("更新のカスタム属性 = %+v", got)
	}
	if len(api.creates) != 1 || len(api.creates[0].CustomFields) != 1 {
		t.Fatalf("追加リクエスト = %+v", api.creates)
	}
	if got := api.creates[0].CustomFields[0]; got.ID != 31 || got.Text != "取引先 C" {
		t.Errorf("追加のカスタム属性 = %+v", got)
	}
}

// TestSamePayload_CustomFields は再送前突合(高 3)がカスタム属性まで
// 突き合わせることを確認する。件名等が同じでもカスタム属性が違えば
// 「作成済み」と断定してはならない(別課題を掴んで取りこぼす)。
func TestSamePayload_CustomFields(t *testing.T) {
	payload := &Payload{
		Summary: ptrString("新規課題"),
		CustomFields: []customfield.InputValue{
			{ID: 31, TypeID: customfield.TypeText, Text: "取引先 A"},
			{ID: 35, TypeID: customfield.TypeMultipleList, ItemIDs: []int64{351, 353}},
		},
	}
	match := backlogclient.Issue{Summary: "新規課題", RawJSON: `{"customFields":[
		{"id":31,"fieldTypeId":1,"value":"取引先 A"},
		{"id":35,"fieldTypeId":6,"value":[{"id":353,"name":"DB"},{"id":351,"name":"UI"}]}
	]}`}
	if !samePayload(payload, match) {
		t.Errorf("同一のカスタム属性が不一致と判定された")
	}

	differs := backlogclient.Issue{Summary: "新規課題", RawJSON: `{"customFields":[
		{"id":31,"fieldTypeId":1,"value":"別の取引先"},
		{"id":35,"fieldTypeId":6,"value":[{"id":351,"name":"UI"},{"id":353,"name":"DB"}]}
	]}`}
	if samePayload(payload, differs) {
		t.Errorf("値が違うのに一致と判定された")
	}

	// 「その他」の直接入力が残っている候補は、こちらが送った内容の結果とは
	// 断定できない(otherValue は送信非対応のため。レビュー 1 回目 高 2)
	withOther := backlogclient.Issue{Summary: "新規課題", RawJSON: `{"customFields":[
		{"id":31,"fieldTypeId":1,"value":"取引先 A"},
		{"id":35,"fieldTypeId":6,"value":[{"id":351,"name":"UI"},{"id":353,"name":"DB"}],
		 "otherValue":"その他の値"}
	]}`}
	if samePayload(payload, withOther) {
		t.Errorf("otherValue が残っている候補を一致と判定した")
	}

	// 生 JSON を確認できない応答は「一致」と断定しない(二重作成より取りこぼしを避ける)
	if samePayload(payload, backlogclient.Issue{Summary: "新規課題"}) {
		t.Errorf("カスタム属性を確認できないのに一致と判定された")
	}
	// カスタム属性を送っていない行は従来どおり(生 JSON を要求しない)
	if !samePayload(&Payload{Summary: ptrString("新規課題")}, backlogclient.Issue{Summary: "新規課題"}) {
		t.Errorf("カスタム属性なしの突合が壊れた")
	}
}

// upsertTestIssue は検証用の課題を 1 件投入する。
func upsertTestIssue(t *testing.T, st *store.Store, issue *store.Issue) {
	t.Helper()
	if err := st.UpsertIssues(context.Background(), []*store.Issue{issue}); err != nil {
		t.Fatal(err)
	}
}

// TestImport_CustomField_UneditedPrefillIsNoChange は、テンプレートへ
// プリフィルした現在値を編集せずに取り込んだ行が「変更なし」になることを確認する
// (レビュー 1 回目 高 1)。プリフィル値が化けて更新・クリアされてはならない。
func TestImport_CustomField_UneditedPrefillIsNoChange(t *testing.T) {
	st := openTestStore(t)
	upsertTestIssue(t, st, &store.Issue{
		ID: 106, IssueKey: "EXA-6", ProjectID: testProjectID, Summary: "予備 6",
		StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
		Updated: "2026-08-06T00:00:00Z",
		RawJSON: `{"customFields":[
			{"id":31,"fieldTypeId":1,"name":"顧客名","value":"#CLEAR#"},
			{"id":38,"fieldTypeId":1,"name":"必須メモ","value":"  前後に空白  "}
		]}`,
	})
	res := importCustomFile(t, st, [][]string{
		cfUpdateRow("EXA-6", "2026-08-06T00:00:00Z", map[string]string{
			// 現在値が文字列としての「#CLEAR#」の行。プリフィルをそのまま取り込んでも
			// クリア指示と解釈してはならない
			"顧客名": ClearToken,
			// 取り込み時にセルは前後空白が落ちる。未編集でも変更扱いにしない
			"必須メモ": "前後に空白",
		}),
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionSkip {
		t.Fatalf("preview = %+v, want skip", p)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if len(payload.CustomFields) != 0 {
		t.Errorf("未編集なのに送信対象になった: %+v", payload.CustomFields)
	}
}

// TestImport_CustomField_RejectsInapplicableIssueType は、行の課題種別に
// 適用されないカスタム属性への入力を取り込み時に弾くことを確認する
// (レビュー 1 回目 中 1。dry-run 成功 → 実行時に API 拒否、を防ぐ)。
func TestImport_CustomField_RejectsInapplicableIssueType(t *testing.T) {
	st := openTestStore(t)

	t.Run("新規行は指定した種別で判定する", func(t *testing.T) {
		// 必須メモ(38)はタスク(11)専用
		res := importCustomFile(t, st, [][]string{
			cfCreateRow("新規課題", "バグ", map[string]string{"必須メモ": "メモ"}),
		})
		if res.Valid {
			t.Fatal("適用外の種別への入力が受理された")
		}
		msg := errorMessages(res)[2]
		if !strings.Contains(msg, "必須メモ") || !strings.Contains(msg, "バグ") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("更新行は現在の種別で判定する", func(t *testing.T) {
		// EXA-2 の現在の種別は「バグ」
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"必須メモ": "メモ"}),
		})
		if res.Valid {
			t.Fatal("適用外の種別への入力が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "必須メモ") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("#CLEAR# も同じく弾く", func(t *testing.T) {
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"必須メモ": ClearToken}),
		})
		if res.Valid {
			t.Fatal("適用外の種別への #CLEAR# が受理された")
		}
	})

	t.Run("同じ行で種別を変更する場合は変更後の種別で判定する", func(t *testing.T) {
		row := cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"必須メモ": "メモ"})
		row[3] = "タスク" // 種別名を変更
		res := importFileWithMaster(t, st, customTemplateHeaders, [][]string{row}, 3,
			testMasterWithCustomFields())
		if !res.Valid {
			t.Fatalf("変更後の種別では適用されるのにエラーになった: %+v", res.Errors)
		}
	})

	t.Run("適用される種別なら受理する", func(t *testing.T) {
		// EXA-1 の現在の種別は「タスク」
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-1", "2026-08-01T00:00:00Z", map[string]string{"必須メモ": "メモ"}),
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
	})

	// 通常の出力テンプレートは種別名をプリフィルするため、その名前がマスタで
	// 解決できない場合は適用可否の判定(effectiveIssueTypeID)に到達する前に、
	// 既存の名前解決で行エラーになる(レビュー 2 回目 中 1: この経路の明文化)。
	t.Run("プリフィルされた種別名が解決できない行は名前解決でエラーになる", func(t *testing.T) {
		upsertTestIssue(t, st, &store.Issue{
			ID: 108, IssueKey: "EXA-8", ProjectID: testProjectID, Summary: "予備 8",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "廃止された種別", PriorityName: "中",
			Updated: "2026-08-08T00:00:00Z",
		})
		row := cfUpdateRow("EXA-8", "2026-08-08T00:00:00Z", map[string]string{"必須メモ": "メモ"})
		row[3] = "廃止された種別" // テンプレート出力と同じく種別名がプリフィルされている状態
		res := importFileWithMaster(t, st, customTemplateHeaders, [][]string{row}, 3,
			testMasterWithCustomFields())
		if res.Valid {
			t.Fatal("解決できない種別名の行が受理された")
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "種別") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("種別を特定できない課題では弾かない", func(t *testing.T) {
		// ローカルには種別名しか無いため、マスタに無い名前では適用可否を判定できない。
		// 判定できない場合に弾くと、正当な更新まで止めてしまうため通す(安全側)
		upsertTestIssue(t, st, &store.Issue{
			ID: 107, IssueKey: "EXA-7", ProjectID: testProjectID, Summary: "予備 7",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "廃止された種別", PriorityName: "中",
			Updated: "2026-08-07T00:00:00Z",
		})
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-7", "2026-08-07T00:00:00Z", map[string]string{"必須メモ": "メモ"}),
		})
		if !res.Valid {
			t.Fatalf("判定できない種別で弾かれた: %+v", res.Errors)
		}
	})
}

// TestImport_CustomField_RejectsNaNAndInf は数値属性が NaN / Inf を
// 受け付けないことを確認する(レビュー 1 回目 中 2)。
func TestImport_CustomField_RejectsNaNAndInf(t *testing.T) {
	st := openTestStore(t)
	for _, input := range []string{"NaN", "Inf", "+Inf", "-inf", "infinity"} {
		res := importCustomFile(t, st, [][]string{
			cfUpdateRow("EXA-2", "2026-08-02T00:00:00Z", map[string]string{"見積工数": input}),
		})
		if res.Valid {
			t.Errorf("%q が受理された", input)
			continue
		}
		if msg := errorMessages(res)[2]; !strings.Contains(msg, "数値") {
			t.Errorf("%q: メッセージ = %q", input, msg)
		}
	}
}

// TestImport_CustomField_UnsupportedType は未知の型に値が入力された場合に
// 黙って送らずエラーにすることを確認する(送信書式が決められないため)。
func TestImport_CustomField_UnsupportedType(t *testing.T) {
	st := openTestStore(t)
	master := testMaster()
	master.CustomFields = []customfield.Def{{ID: 42, TypeID: 99, Name: "未知型"}}
	headers := append(append([]string{}, templateHeaders...), "属性:未知型")
	row := []string{"EXA-1", "", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z", "値"}
	res := importFileWithMaster(t, st, headers, [][]string{row}, 3, master)
	if res.Valid {
		t.Fatal("未対応の型が受理された")
	}
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "未知型") {
		t.Errorf("メッセージ = %q", msg)
	}
}
