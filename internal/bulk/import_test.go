package bulk

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// 列順: issueKey / 件名 / 種別ID / 種別名 / 状態ID / 状態名 /
//       優先度ID / 優先度名 / 担当者ID / 担当者名 / 期限 / 詳細 / base_updated

func TestImport_ClassifiesCreateUpdateAndSkip(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		// 更新(件名変更)
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		// 新規追加
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
		// 更新だが変更なし(現在値と同じ)
		{"EXA-2", "画面崩れ", "", "", "", "", "", "", "", "", "", "", "2026-08-02T00:00:00Z"},
	})

	if !res.Valid || len(res.Errors) != 0 {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if res.TotalRows != 3 || res.Creates != 1 || res.Updates != 1 || res.Skips != 1 {
		t.Errorf("集計 = %+v", res)
	}
	if res.JobID == 0 {
		t.Fatal("ジョブが作成されていない")
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionUpdate || p.IssueKey != "EXA-1" {
		t.Errorf("preview(2) = %+v", p)
	}
	if p := previewOf(res, 3); p == nil || p.Action != ActionCreate || p.Summary != "新規課題" {
		t.Errorf("preview(3) = %+v", p)
	}
	if p := previewOf(res, 4); p == nil || p.Action != ActionSkip || len(p.Changes) != 0 {
		t.Errorf("preview(4) = %+v", p)
	}

	// ジョブ行が永続化され、skip 行も記録されていること
	rows, err := st.ListJobRows(context.Background(), res.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("ジョブ行数 = %d, want 3", len(rows))
	}
	if rows[0].IssueKey != "EXA-1" || rows[0].BaseUpdated != "2026-08-01T00:00:00Z" ||
		rows[0].Status != store.RowStatusPending {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	if rows[2].Status != store.RowStatusSkip {
		t.Errorf("変更なし行の状態 = %q, want skip", rows[2].Status)
	}
	job, err := st.GetJob(context.Background(), res.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != store.JobKindMixed || job.SourceHash == "" || job.SourceFile != "bulk.xlsx" {
		t.Errorf("job = %+v", job)
	}
}

// TestImport_ColumnOrderIndependent は列順を入れ替えても
// ヘッダ名で解決できることを確認する。
func TestImport_ColumnOrderIndependent(t *testing.T) {
	st := openTestStore(t)
	res := importFileWith(t, st,
		[]string{"件名", "base_updated", "担当者名", "issueKey"},
		[][]string{{"新しい件名", "2026-08-01T00:00:00Z", "山田 花子", "EXA-1"}}, 3)

	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionUpdate {
		t.Fatalf("preview = %+v", p)
	}
	if len(p.Changes) != 2 {
		t.Errorf("変更 = %v(件名・担当者の 2 件を期待)", p.Changes)
	}
}

// TestImport_RequiresIssueKeyColumn は issueKey 列が無いファイルを拒否することを確認する
// (全行が新規追加と誤解釈されるのを防ぐ)。
func TestImport_RequiresIssueKeyColumn(t *testing.T) {
	st := openTestStore(t)
	path := writeXLSX(t, []string{"件名", "種別ID"}, [][]string{{"新規", "11"}})
	_, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
	})
	if err == nil {
		t.Fatal("issueKey 列が無いのにエラーにならなかった")
	}
	if !strings.Contains(err.Error(), "issueKey") {
		t.Errorf("エラーメッセージ = %q", err.Error())
	}
}

// TestImport_ResolvesNamesOnly は名前のみの入力を一意に解決できる場合に
// ID を補完することを確認する(設計書 5 節)。
func TestImport_ResolvesNamesOnly(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"", "新規課題", "", "バグ", "", "", "", "低", "", "山田 花子", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil {
		t.Fatal("プレビューが無い")
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.IssueTypeID == nil || *payload.IssueTypeID != 12 {
		t.Errorf("種別 = %+v, want 12", payload.IssueTypeID)
	}
	if payload.PriorityID == nil || *payload.PriorityID != 4 {
		t.Errorf("優先度 = %+v, want 4", payload.PriorityID)
	}
	if payload.AssigneeID == nil || *payload.AssigneeID != 502 {
		t.Errorf("担当者 = %+v, want 502", payload.AssigneeID)
	}
}

// TestImport_UsesIDWhenNameEmpty は名前列が空の行で ID 列を使うことを確認する
// (旧テンプレート・ID だけを記入した Excel の後方互換)。
func TestImport_UsesIDWhenNameEmpty(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.IssueTypeID == nil || *payload.IssueTypeID != 11 {
		t.Errorf("種別 = %+v, want 11", payload.IssueTypeID)
	}
	if payload.PriorityID == nil || *payload.PriorityID != 3 {
		t.Errorf("優先度 = %+v, want 3", payload.PriorityID)
	}
}

// TestImport_NameTakesPrecedenceOverStaleID は、名前列だけをドロップダウンで
// 編集した行(ID 列は出力時のまま)で名前列を採用することを確認する。
// テンプレートは ID 列も出力するため、この扱いができないと名前での編集が成立しない。
func TestImport_NameTakesPrecedenceOverStaleID(t *testing.T) {
	st := openTestStore(t)
	// EXA-1 の現在値は 種別 タスク(11)/ 状態 未対応(1)/ 優先度 中(3)/ 担当者 山田 太郎(501)
	res := importFile(t, st, [][]string{
		{"EXA-1", "", "11", "バグ", "1", "完了", "3", "高", "501", "山田 花子 (502)", "", "", "2026-08-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.IssueTypeID == nil || *payload.IssueTypeID != 12 {
		t.Errorf("種別 = %+v, want 12(名前列「バグ」を採用)", payload.IssueTypeID)
	}
	if payload.StatusID == nil || *payload.StatusID != 4 {
		t.Errorf("状態 = %+v, want 4(名前列「完了」を採用)", payload.StatusID)
	}
	if payload.PriorityID == nil || *payload.PriorityID != 2 {
		t.Errorf("優先度 = %+v, want 2(名前列「高」を採用)", payload.PriorityID)
	}
	if payload.AssigneeID == nil || *payload.AssigneeID != 502 {
		t.Errorf("担当者 = %+v, want 502(名前列を採用)", payload.AssigneeID)
	}
	// 食い違った ID 列は無視した旨を 4 列ぶん警告する(エラーにはしない)
	for _, label := range []string{"種別ID", "状態ID", "優先度ID", "担当者ID"} {
		if !containsSubstring(res.Warnings, "2 行目: "+label+" 列は名前列と食い違うため無視しました") {
			t.Errorf("%s の警告がない: %v", label, res.Warnings)
		}
	}
}

// TestImport_IgnoresEditedIDWhenNamePresent は、ID 列だけを書き換えた行
// (名前列は出力時のまま)でも名前列を採用し、ID 列の編集を警告付きで
// 無視することを確認する。
// 「どちらが編集されたか」を現在値から推測すると、出力後にリモートが変化した
// 場合に古い ID を採用してしまうため、名前列を常に優先する。
func TestImport_IgnoresEditedIDWhenNamePresent(t *testing.T) {
	st := openTestStore(t)
	// EXA-1 の現在値は 種別 タスク(11)/ 状態 未対応(1)/ 優先度 中(3)/ 担当者 山田 太郎(501)。
	// 名前列は出力時のまま(= 現在値と同じ)なので、変更は 1 件も生じない。
	res := importFile(t, st, [][]string{
		{"EXA-1", "", "12", "タスク", "4", "未対応", "2", "中", "502", "山田 太郎 (501)", "", "", "2026-08-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionSkip {
		t.Fatalf("preview = %+v, want skip(名前列は現在値のまま)", p)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.IssueTypeID != nil || payload.StatusID != nil ||
		payload.PriorityID != nil || payload.AssigneeID != nil {
		t.Errorf("ID 列の編集が採用された: %+v", payload)
	}
	for _, label := range []string{"種別ID", "状態ID", "優先度ID", "担当者ID"} {
		if !containsSubstring(res.Warnings, "2 行目: "+label+" 列は名前列と食い違うため無視しました") {
			t.Errorf("%s の警告がない: %v", label, res.Warnings)
		}
	}
}

// TestImport_NameWinsWhenCurrentMatchesName は、出力後にリモート(ローカルの
// 現在値)が名前列と同じ値へ変化していても、ID 列(出力時の古い値)を採用しない
// ことを確認する。現在値から編集箇所を推測していた頃は、この行で古い ID 側へ
// 巻き戻す誤更新が発生していた。
func TestImport_NameWinsWhenCurrentMatchesName(t *testing.T) {
	st := openTestStore(t)
	// EXA-2 の現在値は 種別 バグ(12)。出力時は タスク(11)だったとして
	// 種別ID に 11 が残り、名前列は現在値と同じ「バグ」。
	res := importFile(t, st, [][]string{
		{"EXA-2", "", "11", "バグ", "", "", "", "", "", "", "", "", "2026-08-02T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.IssueTypeID != nil {
		t.Errorf("種別 = %v, want なし(名前列は現在値と同じなので更新しない)", *payload.IssueTypeID)
	}
	if !containsSubstring(res.Warnings, "2 行目: 種別ID 列は名前列と食い違うため無視しました") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestImport_WarnsOnNameIDMismatch は、名前列と ID 列が食い違う行を
// 名前列の指定で受理し、無視した ID 列を警告として報告することを確認する
// (取り込みは止めない。名前列が唯一の正)。
func TestImport_WarnsOnNameIDMismatch(t *testing.T) {
	st := openTestStore(t)

	t.Run("新規追加行も名前列を優先する", func(t *testing.T) {
		res := importFile(t, st, [][]string{
			{"", "新規課題", "11", "バグ", "", "", "3", "", "", "", "", "", ""},
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.IssueTypeID == nil || *payload.IssueTypeID != 12 {
			t.Errorf("種別 = %+v, want 12(名前列「バグ」を採用)", payload.IssueTypeID)
		}
		if !containsSubstring(res.Warnings, "2 行目: 種別ID 列は名前列と食い違うため無視しました") {
			t.Errorf("警告 = %v", res.Warnings)
		}
	})

	t.Run("担当者も名前列を優先する", func(t *testing.T) {
		// EXA-2 の現在の担当者は未設定
		res := importFile(t, st, [][]string{
			{"EXA-2", "", "", "", "", "", "", "", "502", "山田 太郎 (501)", "", "", "2026-08-02T00:00:00Z"},
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.AssigneeID == nil || *payload.AssigneeID != 501 {
			t.Errorf("担当者 = %+v, want 501(名前列を採用)", payload.AssigneeID)
		}
		if !containsSubstring(res.Warnings, "2 行目: 担当者ID 列は名前列と食い違うため無視しました") {
			t.Errorf("警告 = %v", res.Warnings)
		}
	})

	t.Run("名前が解決できない場合は従来どおりエラー", func(t *testing.T) {
		res := importFile(t, st, [][]string{
			// 解決できない名前
			{"EXA-1", "", "12", "存在しない種別", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
			// 曖昧な名前(ID 列を足しても解決しない)
			{"EXA-3", "", "", "", "", "", "", "", "503", "重複 名前", "", "", "2026-08-03T00:00:00Z"},
		})
		if res.Valid {
			t.Fatal("解決できない名前が受理された")
		}
		msgs := errorMessages(res)
		if !strings.Contains(msgs[2], "見つかりません") {
			t.Errorf("行 2 = %q", msgs[2])
		}
		if !strings.Contains(msgs[3], "一意に決められません") {
			t.Errorf("行 3 = %q", msgs[3])
		}
	})

	t.Run("エラー行の警告は残さない", func(t *testing.T) {
		res := importFile(t, st, [][]string{
			// 種別は食い違うが、行自体は期限の書式でエラーになる
			{"EXA-1", "", "12", "タスク", "", "", "", "", "", "", "2026年9月1日", "", "2026-08-01T00:00:00Z"},
		})
		if res.Valid {
			t.Fatal("不正な期限が受理された")
		}
		if containsSubstring(res.Warnings, "食い違う") {
			t.Errorf("エラー行の警告が残った: %v", res.Warnings)
		}
	})
}

// TestImport_AcceptsExportedTemplate は、テンプレート出力そのものを
// 名前列だけ書き換えて取り込めることを確認する(出力と取り込みの契約)。
func TestImport_AcceptsExportedTemplate(t *testing.T) {
	st := openTestStore(t)
	path := filepath.Join(t.TempDir(), "template.xlsx")
	err := export.ExportBulkTemplateToFile(path, testProjectID, export.BulkTemplateSlice([]export.BulkTemplateRow{{
		IssueKey: "EXA-1", Summary: "ログイン不具合",
		IssueTypeID: 11, IssueTypeName: "タスク",
		StatusID: 1, StatusName: "未対応",
		PriorityID: 3, PriorityName: "中",
		AssigneeID: 501, AssigneeName: "山田 太郎",
		BaseUpdated: "2026-08-01T00:00:00Z",
	}}), export.BulkTemplateMasters{
		IssueTypes: []export.NamedRef{{ID: 11, Name: "タスク"}, {ID: 12, Name: "バグ"}},
		Statuses:   []export.NamedRef{{ID: 1, Name: "未対応"}, {ID: 4, Name: "完了"}},
		Priorities: []export.NamedRef{{ID: 2, Name: "高"}, {ID: 3, Name: "中"}},
		Assignees:  []export.NamedRef{{ID: 501, Name: "山田 太郎"}, {ID: 502, Name: "山田 花子"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 未編集の状態では「変更なし」になること(出力値がそのまま解決できる)
	res := importTemplate(t, st, path)
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionSkip {
		t.Fatalf("未編集の行 = %+v, want skip", p)
	}

	// 名前列だけをドロップダウンの候補に書き換える(ID 列は出力時のまま)
	editTemplateCells(t, path, map[string]string{
		"D2": "バグ", "F2": "完了", "H2": "高", "J2": "山田 花子 (502)",
	})
	edited := importTemplate(t, st, path)
	if !edited.Valid {
		t.Fatalf("名前列の編集がエラーになった: %+v", edited.Errors)
	}
	payload := payloadOfRow(t, st, edited.JobID, 2)
	if payload.IssueTypeID == nil || *payload.IssueTypeID != 12 {
		t.Errorf("種別 = %+v, want 12", payload.IssueTypeID)
	}
	if payload.StatusID == nil || *payload.StatusID != 4 {
		t.Errorf("状態 = %+v, want 4", payload.StatusID)
	}
	if payload.PriorityID == nil || *payload.PriorityID != 2 {
		t.Errorf("優先度 = %+v, want 2", payload.PriorityID)
	}
	if payload.AssigneeID == nil || *payload.AssigneeID != 502 {
		t.Errorf("担当者 = %+v, want 502", payload.AssigneeID)
	}
}

// importTemplate は出力済みテンプレートをそのまま取り込む。
func importTemplate(t *testing.T, st *store.Store, path string) *ImportResult {
	t.Helper()
	res, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// editTemplateCells は出力済みテンプレートのセルを書き換える(利用者の編集を模す)。
func editTemplateCells(t *testing.T, path string, cells map[string]string) {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for cell, value := range cells {
		if err := f.SetCellStr(export.SheetBulkTemplate, cell, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
}

// TestImport_ResolvesAssigneeLabel は担当者名の「表示名 (ID)」形式を
// ID で解決することを確認する(同名ユーザを区別する唯一の手段)。
func TestImport_ResolvesAssigneeLabel(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		// 同名(503 / 504)でも括弧内の ID で一意に決まる
		{"EXA-1", "", "", "", "", "", "", "", "", "重複 名前 (504)", "", "", "2026-08-01T00:00:00Z"},
		// 新規追加行でも同様
		{"", "新規課題", "11", "", "", "", "3", "", "", "山田 花子 (502)", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if p := payloadOfRow(t, st, res.JobID, 2); p.AssigneeID == nil || *p.AssigneeID != 504 {
		t.Errorf("担当者 = %+v, want 504", p.AssigneeID)
	}
	if p := payloadOfRow(t, st, res.JobID, 3); p.AssigneeID == nil || *p.AssigneeID != 502 {
		t.Errorf("担当者 = %+v, want 502", p.AssigneeID)
	}

	// 括弧内の ID が候補に無い場合はエラー(存在しないユーザを黙って無視しない)
	invalid := importFile(t, st, [][]string{
		{"EXA-1", "", "", "", "", "", "", "", "", "山田 太郎 (999)", "", "", "2026-08-01T00:00:00Z"},
	})
	if invalid.Valid {
		t.Fatal("存在しない担当者 ID が受理された")
	}
	if msg := errorMessages(invalid)[2]; !strings.Contains(msg, "担当者") {
		t.Errorf("メッセージ = %q", msg)
	}
}

// TestImport_DefaultPriorityApplied は優先度未入力の新規行に既定値を適用することを確認する。
func TestImport_DefaultPriorityApplied(t *testing.T) {
	st := openTestStore(t)
	res := importFileWith(t, st, templateHeaders, [][]string{
		{"", "新規課題", "11", "", "", "", "", "", "", "", "", "", ""},
	}, 2)
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.PriorityID == nil || *payload.PriorityID != 2 {
		t.Errorf("優先度 = %+v, want 2(既定値)", payload.PriorityID)
	}

	// 既定値も未指定ならエラー行にする
	invalid := importFileWith(t, st, templateHeaders, [][]string{
		{"", "新規課題", "11", "", "", "", "", "", "", "", "", "", ""},
	}, 0)
	if invalid.Valid {
		t.Fatal("既定優先度が無いのにエラーにならなかった")
	}
	if msg := errorMessages(invalid)[2]; !strings.Contains(msg, "優先度") {
		t.Errorf("メッセージ = %q", msg)
	}
}

func TestImport_ValidationErrors(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		// 2: ローカルに存在しない課題キー
		{"EXA-99", "件名", "", "", "", "", "", "", "", "", "", "", ""},
		// 3: 数値でない種別 ID
		{"", "新規", "abc", "", "", "", "3", "", "", "", "", "", ""},
		// 4: 存在しない優先度 ID
		{"", "新規", "11", "", "", "", "99", "", "", "", "", "", ""},
		// 5: 新規行に件名が無い
		{"", "", "11", "", "", "", "3", "", "", "", "", "", ""},
		// 6: 新規行に種別が無い
		{"", "新規", "", "", "", "", "3", "", "", "", "", "", ""},
		// 7: 新規行に状態を指定
		{"", "新規", "11", "", "1", "", "3", "", "", "", "", "", ""},
		// 8: 曖昧な担当者名
		{"EXA-1", "", "", "", "", "", "", "", "", "重複 名前", "", "", "2026-08-01T00:00:00Z"},
		// 9: 存在しない種別名
		{"EXA-3", "", "", "存在しない種別", "", "", "", "", "", "", "", "", "2026-08-03T00:00:00Z"},
		// 10: 日付書式が不正
		{"EXA-4", "", "", "", "", "", "", "", "", "", "2026年9月1日", "", "2026-08-04T00:00:00Z"},
		// 11: クリア不可の列への #CLEAR#
		{"EXA-5", "#CLEAR#", "", "", "", "", "", "", "", "", "", "", "2026-08-05T00:00:00Z"},
		// 12: 新規行での #CLEAR#
		{"", "新規", "11", "", "", "", "3", "", "", "", "#CLEAR#", "", ""},
		// 13: 存在しない担当者 ID
		{"EXA-2", "", "", "", "", "", "", "", "999", "", "", "", "2026-08-02T00:00:00Z"},
	})

	if res.Valid {
		t.Fatal("エラーがあるのに valid になった")
	}
	// エラーがある場合はジョブを作らない(実行できない入力を残さない)
	if res.JobID != 0 {
		t.Errorf("JobID = %d, want 0", res.JobID)
	}
	msgs := errorMessages(res)
	for _, rowNo := range []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13} {
		if msgs[rowNo] == "" {
			t.Errorf("行 %d のエラーが報告されていない", rowNo)
		}
	}
	if !strings.Contains(msgs[2], "EXA-99") {
		t.Errorf("行 2 = %q", msgs[2])
	}
	if !strings.Contains(msgs[8], "担当者") {
		t.Errorf("行 8 = %q", msgs[8])
	}
	if !strings.Contains(msgs[11], "#CLEAR#") {
		t.Errorf("行 11 = %q", msgs[11])
	}
	if !strings.Contains(msgs[12], "#CLEAR#") {
		t.Errorf("行 12 = %q", msgs[12])
	}
}

// TestImport_RequiresBaseUpdatedOnUpdateRows は更新行の base_updated 欠落・不正を
// 検証エラーにすることを確認する(高 1)。
// base_updated が無いと実行時の競合検知を素通りし、他者の変更を黙って上書きしうる。
func TestImport_RequiresBaseUpdatedOnUpdateRows(t *testing.T) {
	st := openTestStore(t)

	t.Run("欠落はエラー", func(t *testing.T) {
		res := importFile(t, st, [][]string{
			{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", ""},
		})
		if res.Valid {
			t.Fatal("base_updated が無いのに valid になった")
		}
		if res.JobID != 0 {
			t.Errorf("JobID = %d, want 0", res.JobID)
		}
		msg := errorMessages(res)[2]
		if !strings.Contains(msg, "base_updated がありません") || !strings.Contains(msg, "テンプレート") {
			t.Errorf("メッセージ = %q", msg)
		}
	})

	t.Run("RFC3339 でない値はエラー", func(t *testing.T) {
		for _, invalid := range []string{"2026-08-01", "2026/08/01 00:00:00", "不正な日時"} {
			res := importFile(t, st, [][]string{
				{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", invalid},
			})
			if res.Valid {
				t.Fatalf("%q: 不正な base_updated が受理された", invalid)
			}
			if msg := errorMessages(res)[2]; !strings.Contains(msg, "base_updated が不正です") {
				t.Errorf("%q: メッセージ = %q", invalid, msg)
			}
		}
	})

	t.Run("新規追加行は不要", func(t *testing.T) {
		res := importFile(t, st, [][]string{
			{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
		})
		if !res.Valid {
			t.Errorf("新規追加行が base_updated 無しで拒否された: %+v", res.Errors)
		}
	})

	t.Run("base_updated 列が無いファイルもエラー", func(t *testing.T) {
		res := importFileWith(t, st,
			[]string{"issueKey", "件名"},
			[][]string{{"EXA-1", "新しい件名"}}, 3)
		if res.Valid {
			t.Fatal("base_updated 列が無いのに valid になった")
		}
	})
}

// TestImport_RejectsTemplateOfAnotherProject はテンプレートに埋め込まれた
// プロジェクト ID と選択中のプロジェクトが異なる場合に取り込みを拒否することを
// 確認する(高 2。別プロジェクトへの誤書き込み防止)。
func TestImport_RejectsTemplateOfAnotherProject(t *testing.T) {
	st := openTestStore(t)
	path := writeTemplateXLSX(t, 999, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})

	_, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
	})
	if err == nil {
		t.Fatal("別プロジェクトのテンプレートが受理された")
	}
	if !strings.Contains(err.Error(), "プロジェクト ID 999") {
		t.Errorf("メッセージ = %q", err.Error())
	}
}

// TestImport_AcceptsTemplateOfSameProject は一致するテンプレートを警告なしで
// 受理することを確認する(高 2)。
func TestImport_AcceptsTemplateOfSameProject(t *testing.T) {
	st := openTestStore(t)
	path := writeTemplateXLSX(t, testProjectID, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})

	res, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "対象プロジェクトの情報がありません") {
			t.Errorf("一致しているのにメタ情報の警告が出た: %q", w)
		}
	}
}

// TestImport_WarnsOnTemplateWithoutProjectID はメタ情報の無い旧テンプレートを
// 警告付きで受理する(続行する)ことを確認する(高 2)。
func TestImport_WarnsOnTemplateWithoutProjectID(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if !containsSubstring(res.Warnings, "対象プロジェクトの情報がありません") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestImport_AssigneeLimitedToProjectMembers は担当者の候補を
// 対象プロジェクトの参加者に限定することを確認する(中 1)。
func TestImport_AssigneeLimitedToProjectMembers(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	// 山田 太郎(501)のみ参加。山田 花子(502)は別プロジェクトの参加者。
	// project_users は projects への FK を持つ(v4)ため、先に行を作る。
	for _, p := range []store.Project{
		{ID: testProjectID, ProjectKey: "EXA", Name: "検証用 A"},
		{ID: 2, ProjectKey: "EXB", Name: "検証用 B"},
	} {
		if err := st.UpsertProject(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceProjectUsers(ctx, testProjectID, []store.ProjectUser{{UserID: 501}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceProjectUsers(ctx, 2, []store.ProjectUser{{UserID: 502}}); err != nil {
		t.Fatal(err)
	}

	res := importFile(t, st, [][]string{
		// 参加者は解決できる
		{"EXA-1", "", "", "", "", "", "", "", "", "山田 太郎", "", "", "2026-08-01T00:00:00Z"},
		// 未参加ユーザは名前でも ID でも解決できない
		{"EXA-2", "", "", "", "", "", "", "", "", "山田 花子", "", "", "2026-08-02T00:00:00Z"},
		{"EXA-3", "", "", "", "", "", "", "", "502", "", "", "", "2026-08-03T00:00:00Z"},
	})
	if res.Valid {
		t.Fatal("プロジェクト未参加の担当者が受理された")
	}
	msgs := errorMessages(res)
	if msgs[2] != "" {
		t.Errorf("参加者の行がエラーになった: %q", msgs[2])
	}
	if !strings.Contains(msgs[3], "担当者") || !strings.Contains(msgs[4], "担当者") {
		t.Errorf("未参加ユーザのエラー = %q / %q", msgs[3], msgs[4])
	}
	// 参加者が登録されている場合はフォールバックの警告を出さない
	if containsSubstring(res.Warnings, "ユーザ同期が未実施") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// TestImport_AssigneeFallsBackWhenNoProjectUsers はプロジェクト参加者が
// 未同期(0 件)の場合にスペース全体へフォールバックし、警告を出すことを確認する(中 1)。
func TestImport_AssigneeFallsBackWhenNoProjectUsers(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "", "", "", "", "", "", "", "", "山田 花子", "", "", "2026-08-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("フォールバックで解決できなかった: %+v", res.Errors)
	}
	if !containsSubstring(res.Warnings, "ユーザ同期が未実施") {
		t.Errorf("警告 = %v", res.Warnings)
	}
}

// containsSubstring は要素のいずれかが sub を含むかを返す。
func containsSubstring(values []string, sub string) bool {
	for _, v := range values {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

// TestImport_RejectsDuplicateIssueKey は同じ課題を複数行で更新する入力を弾く
// (後勝ちで意図しない結果になるため)。
func TestImport_RejectsDuplicateIssueKey(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "件名A", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		{"EXA-1", "件名B", "", "", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
	})
	if res.Valid {
		t.Fatal("重複した issueKey がエラーにならなかった")
	}
	if msg := errorMessages(res)[3]; !strings.Contains(msg, "EXA-1") {
		t.Errorf("メッセージ = %q", msg)
	}
}

// TestImport_DryRunDiff は更新行の差分表示(変更前 → 変更後)を確認する。
func TestImport_DryRunDiff(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "新しい件名", "12", "", "4", "", "2", "", "#CLEAR#", "", "#CLEAR#", "新しい説明", "2026-08-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionUpdate {
		t.Fatalf("preview = %+v", p)
	}
	joined := strings.Join(p.Changes, "\n")
	wants := []string{
		"件名: ログイン不具合 → 新しい件名",
		"種別: タスク → バグ",
		"状態: 未対応 → 完了",
		"優先度: 中 → 高",
		"担当者: 山田 太郎 → (クリア)",
		"期限: 2026-09-01 → (クリア)",
		"詳細:",
	}
	for _, want := range wants {
		if !strings.Contains(joined, want) {
			t.Errorf("差分に %q が含まれない:\n%s", want, joined)
		}
	}
	if p.ConflictWarning {
		t.Error("base_updated が一致しているのに競合警告が出た")
	}
}

// TestImport_ConflictWarning は base_updated とローカルの updated の
// 不一致を事前警告することを確認する(競合の予兆)。
func TestImport_ConflictWarning(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"EXA-1", "新しい件名", "", "", "", "", "", "", "", "", "", "", "2026-07-01T00:00:00Z"},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || !p.ConflictWarning {
		t.Errorf("preview = %+v(競合警告を期待)", p)
	}
}

// TestImport_EmptyRowsIgnored は空行を無視することを確認する。
func TestImport_EmptyRowsIgnored(t *testing.T) {
	st := openTestStore(t)
	res := importFile(t, st, [][]string{
		{"", "新規課題", "11", "", "", "", "3", "", "", "", "", "", ""},
		{"", "", "", "", "", "", "", "", "", "", "", "", ""},
	})
	if !res.Valid {
		t.Fatalf("エラー = %+v", res.Errors)
	}
	if res.TotalRows != 1 {
		t.Errorf("TotalRows = %d, want 1", res.TotalRows)
	}
}

// TestImport_AcceptsDateFormats は期限の許容書式を確認する。
func TestImport_AcceptsDateFormats(t *testing.T) {
	st := openTestStore(t)
	for _, input := range []string{"2026-10-01", "2026/10/1", "2026-10-01T00:00:00Z"} {
		res := importFile(t, st, [][]string{
			{"EXA-1", "", "", "", "", "", "", "", "", "", input, "", "2026-08-01T00:00:00Z"},
		})
		if !res.Valid {
			t.Fatalf("%q: エラー = %+v", input, res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.DueDate == nil || *payload.DueDate != "2026-10-01" {
			t.Errorf("%q → %+v, want 2026-10-01", input, payload.DueDate)
		}
	}
}

// TestImport_NoDataRows はデータ行が無いファイルを拒否することを確認する。
func TestImport_NoDataRows(t *testing.T) {
	st := openTestStore(t)
	path := writeXLSX(t, templateHeaders, nil)
	if _, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
	}); err == nil {
		t.Fatal("データ行が無いのにエラーにならなかった")
	}
}

// payloadOfRow はジョブ行の payload を解析して返す。
func payloadOfRow(t *testing.T, st *store.Store, jobID int64, rowNo int) Payload {
	t.Helper()
	rows, err := st.ListJobRows(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.RowNo == rowNo {
			p, err := DecodePayload(r.Payload)
			if err != nil {
				t.Fatal(err)
			}
			return *p
		}
	}
	t.Fatalf("行 %d が見つからない", rowNo)
	return Payload{}
}

// ID 列の #CLEAR# は名前列に値がある場合は無視されること(名前列が常に正。2 回目レビュー高 1)。
func TestImport_NamePriorityBeatsClearInIDColumn(t *testing.T) {
	st := openTestStore(t)

	t.Run("担当者ID の #CLEAR# は名前列があれば無視して警告", func(t *testing.T) {
		// EXA-2 は担当者未設定。名前列の指定(501)が採用され、クリアされないこと
		res := importFile(t, st, [][]string{
			{"EXA-2", "", "", "", "", "", "", "", "#CLEAR#", "山田 太郎 (501)", "", "", "2026-08-02T00:00:00Z"},
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.AssigneeID == nil || *payload.AssigneeID != 501 {
			t.Errorf("担当者 = %+v, want 501(名前列を採用。#CLEAR# は無視)", payload.AssigneeID)
		}
		if !containsSubstring(res.Warnings, "2 行目: 担当者ID 列は名前列と食い違うため無視しました") {
			t.Errorf("警告 = %v", res.Warnings)
		}
	})

	t.Run("名前列の #CLEAR# は ID 列の値より優先してクリア", func(t *testing.T) {
		// EXA-1 の現在の担当者は 501。名前列 #CLEAR# が優先されクリア(payload.AssigneeID=0)
		res := importFile(t, st, [][]string{
			{"EXA-1", "", "", "", "", "", "", "", "502", "#CLEAR#", "", "", "2026-08-01T00:00:00Z"},
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.AssigneeID == nil || *payload.AssigneeID != 0 {
			t.Errorf("担当者 = %+v, want 0(クリア)", payload.AssigneeID)
		}
		if !containsSubstring(res.Warnings, "2 行目: 担当者ID 列は名前列と食い違うため無視しました") {
			t.Errorf("警告 = %v", res.Warnings)
		}
	})

	t.Run("種別ID の #CLEAR# は名前列があれば無視(クリア不可列でもエラーにしない)", func(t *testing.T) {
		// EXA-1 の現在種別は「タスク」。名前列「バグ」(12)への変更が採用されること
		res := importFile(t, st, [][]string{
			{"EXA-1", "", "#CLEAR#", "バグ", "", "", "", "", "", "", "", "", "2026-08-01T00:00:00Z"},
		})
		if !res.Valid {
			t.Fatalf("エラー = %+v", res.Errors)
		}
		payload := payloadOfRow(t, st, res.JobID, 2)
		if payload.IssueTypeID == nil || *payload.IssueTypeID != 12 {
			t.Errorf("種別 = %+v, want 12(名前列「バグ」を採用)", payload.IssueTypeID)
		}
		if !containsSubstring(res.Warnings, "2 行目: 種別ID 列は名前列と食い違うため無視しました") {
			t.Errorf("警告 = %v", res.Warnings)
		}
	})
}
