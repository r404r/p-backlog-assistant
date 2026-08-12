package bulk

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// 親課題(CF5)の取り込み検証。
//
// テンプレートの列は「親課題キー」(固定列の末尾グループ)。値の意味は
// 空欄 = 変更しない / 課題キー = その課題を親にする / ID:<数値> = ローカルに
// 無い親の指定 / #CLEAR# = 親子関係の解除。

// parentHeaders は親課題キー列を含むテンプレートヘッダ。
var parentHeaders = append(append([]string{}, templateHeaders[:12]...),
	"親課題キー", "base_updated")

// parentUpdateRow は更新行(課題キー・base_updated・親課題キー)を作る。
func parentUpdateRow(issueKey, baseUpdated, parent string) []string {
	return []string{issueKey, "", "", "", "", "", "", "", "", "", "", "", parent, baseUpdated}
}

// parentCreateRow は新規追加行(件名・種別名・親課題キー)を作る。
func parentCreateRow(summary, issueType, parent string) []string {
	return []string{"", summary, "", issueType, "", "", "", "", "", "", "", "", parent, ""}
}

// setParentRawJSON は既存課題の生 JSON を親課題 ID 付きで置き換える
// (フィクスチャの生 JSON は parentIssueId を持たず「確認不能」になるため)。
// parentID が 0 なら "parentIssueId": null(親なし)にする。
func setParentRawJSON(t *testing.T, st *store.Store, issueKey string, parentID int64) {
	t.Helper()
	ctx := context.Background()
	cur, err := st.GetIssueByKey(ctx, testProjectID, issueKey)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatalf("課題 %s がフィクスチャにありません", issueKey)
	}
	parent := "null"
	if parentID > 0 {
		parent = strconv.FormatInt(parentID, 10)
	}
	cur.RawJSON = fmt.Sprintf(`{"id":%d,"issueKey":%q,"parentIssueId":%s}`, cur.ID, cur.IssueKey, parent)
	if err := st.UpsertIssue(ctx, cur); err != nil {
		t.Fatal(err)
	}
}

// seedParentIssues は親子関係のある課題を追加する。
//
//	EXA-1〜EXA-5            : 親なし(親の有無を判定できる状態にする)
//	EXA-3(103) ← EXA-6(106) : EXA-3 は子を持つ / EXA-6 は子課題
//	EXA-7(107)              : 親なし
func seedParentIssues(t *testing.T, st *store.Store) {
	t.Helper()
	// フィクスチャの生 JSON には parentIssueId が無く、そのままでは
	// 「親の有無を確認できない課題」として検証が止まるため親なしにする
	for _, key := range []string{"EXA-1", "EXA-2", "EXA-3", "EXA-4", "EXA-5"} {
		setParentRawJSON(t, st, key, 0)
	}
	if err := st.UpsertIssues(context.Background(), []*store.Issue{
		{
			ID: 106, IssueKey: "EXA-6", ProjectID: testProjectID, Summary: "子課題",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
			Updated: "2026-08-06T00:00:00Z",
			RawJSON: `{"id":106,"issueKey":"EXA-6","parentIssueId":103}`,
		},
		{
			ID: 107, IssueKey: "EXA-7", ProjectID: testProjectID, Summary: "親なし",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
			Updated: "2026-08-07T00:00:00Z",
			RawJSON: `{"id":107,"issueKey":"EXA-7","parentIssueId":null}`,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// importParentRows は親課題キー列付きのテンプレートを取り込む(API 差し替え可)。
func importParentRows(t *testing.T, st *store.Store, api API, rows [][]string) *ImportResult {
	t.Helper()
	path := writeXLSX(t, parentHeaders, rows)
	res, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID:         testProjectID,
		FilePath:          path,
		DefaultPriorityID: 3,
		Master:            testMaster(),
		API:               api,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestImport_ParentSet は課題キーで親を設定できることを確認する。
func TestImport_ParentSet(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-1"),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionUpdate {
		t.Fatalf("プレビュー = %+v", p)
	}
	if !hasChange(p.Changes, "親課題: (未設定) → EXA-1") {
		t.Errorf("差分 = %v", p.Changes)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.ParentIssueID == nil || *payload.ParentIssueID != 101 {
		t.Errorf("payload.ParentIssueID = %v, want 101", payload.ParentIssueID)
	}
	if payload.ClearParentIssue {
		t.Errorf("クリアフラグが立っている")
	}
}

// TestImport_ParentUnchanged はプリフィルされた現在値をそのまま取り込んでも
// 変更扱いにならないことを確認する(API も呼ばない)。
func TestImport_ParentUnchanged(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI()

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-6", "2026-08-06T00:00:00Z", "EXA-3"),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionSkip {
		t.Errorf("プレビュー = %+v, want skip", p)
	}
	if len(api.getCalls) != 0 {
		t.Errorf("変更が無い行で API を呼んでいる: %v", api.getCalls)
	}
}

// TestImport_ParentClear は #CLEAR# で親子関係を解除できることを確認する。
func TestImport_ParentClear(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-6", "2026-08-06T00:00:00Z", ClearToken),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionUpdate {
		t.Fatalf("プレビュー = %+v", p)
	}
	if !hasChange(p.Changes, "親課題: EXA-3 → (クリア)") {
		t.Errorf("差分 = %v", p.Changes)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if !payload.ClearParentIssue || payload.ParentIssueID != nil {
		t.Errorf("payload = %+v", payload)
	}
}

// TestImport_ParentClearWhenNoParent は親が無い課題への #CLEAR# を
// 「変更なし」として扱うことを確認する(空の送信をしない)。
func TestImport_ParentClearWhenNoParent(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-7", "2026-08-07T00:00:00Z", ClearToken),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	if p := previewOf(res, 2); p == nil || p.Action != ActionSkip {
		t.Errorf("プレビュー = %+v, want skip", p)
	}
}

// TestImport_ParentUnknownKey は解決できない課題キーを同期案内付きで
// 行エラーにすることを確認する。
func TestImport_ParentUnknownKey(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-999"),
	})
	msg := errorMessages(res)[2]
	if !strings.Contains(msg, "EXA-999") || !strings.Contains(msg, "同期") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentSelfReference は自分自身を親に指定できないことを確認する。
func TestImport_ParentSelfReference(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-4"),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "自分自身") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentIsChild は子課題を親に指定できないことを確認する(孫の禁止)。
func TestImport_ParentIsChild(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-6"),
	})
	msg := errorMessages(res)[2]
	if !strings.Contains(msg, "EXA-6") || !strings.Contains(msg, "1 階層") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_IssueHasChildren は子課題を持つ課題に親を設定できないことを
// 確認する(2 階層化の禁止)。
func TestImport_IssueHasChildren(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-3", "2026-08-03T00:00:00Z", "EXA-1"),
	})
	msg := errorMessages(res)[2]
	if !strings.Contains(msg, "子課題") || !strings.Contains(msg, "1 階層") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentBatchTwoLevels は同一バッチ内の組み合わせで 2 階層に
// なる指定を検出することを確認する(EXA-4 → EXA-5 → EXA-1)。
func TestImport_ParentBatchTwoLevels(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-5"),
		parentUpdateRow("EXA-5", "2026-08-05T00:00:00Z", "EXA-1"),
	})
	if res.Valid {
		t.Fatalf("2 階層になる指定が受理された: %+v", res.Previews)
	}
	msgs := errorMessages(res)
	if len(msgs) != 2 {
		t.Fatalf("エラー行 = %+v, want 2 行", msgs)
	}
	for rowNo, msg := range msgs {
		if !strings.Contains(msg, "同じ取り込み") {
			t.Errorf("%d 行目のエラー = %q", rowNo, msg)
		}
	}
	// ジョブは作らない
	if res.JobID != 0 {
		t.Errorf("jobId = %d, want 0", res.JobID)
	}
}

// TestImport_ParentClearThenReparentRejected は「同じ取り込みの中で親を解除し、
// その課題を新たな親にする」操作を受理しないことを確認する。
//
// 判定は取り込み時点のローカル DB の状態に対して行う(計画 CF5)。同一ファイル
// 内の解除を先読みして許すと、行の並び順によっては解除より先に親設定を送って
// しまい、実行時に Backlog 側で拒否される。記入方法シートにも「解除だけを先に
// 取り込む」よう明記している。
func TestImport_ParentClearThenReparentRejected(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		// EXA-6 の親を解除してから、EXA-6 を EXA-4 の親にしようとする
		parentUpdateRow("EXA-6", "2026-08-06T00:00:00Z", ClearToken),
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-6"),
	})
	if res.Valid {
		t.Fatalf("解除と同一ファイルでの親指定が受理された: %+v", res.Previews)
	}
	if msg := errorMessages(res)[3]; !strings.Contains(msg, "EXA-6") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentCreateRow は新規追加行に既存課題の親を指定できることを
// 確認する(新規行どうしの親子は課題キーが無いため指定できない)。
func TestImport_ParentCreateRow(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentCreateRow("新しい課題", "タスク", "EXA-1"),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	p := previewOf(res, 2)
	if p == nil || p.Action != ActionCreate {
		t.Fatalf("プレビュー = %+v", p)
	}
	if !hasChange(p.Changes, "親課題: EXA-1") {
		t.Errorf("差分 = %v", p.Changes)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.ParentIssueID == nil || *payload.ParentIssueID != 101 {
		t.Errorf("payload.ParentIssueID = %v, want 101", payload.ParentIssueID)
	}
}

// TestImport_ParentCreateRowRejectsClear は新規追加行の #CLEAR# を拒否する。
func TestImport_ParentCreateRowRejectsClear(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentCreateRow("新しい課題", "タスク", ClearToken),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, ClearToken) {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentCreateRowAsParentRejected は同一取り込み内の新規行を
// 親に指定できないことを確認する(課題キーが未採番のため解決できない)。
func TestImport_ParentCreateRowAsParentRejected(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentCreateRow("親になる新規課題", "タスク", ""),
		parentCreateRow("子になる新規課題", "タスク", "親になる新規課題"),
	})
	if msg := errorMessages(res)[3]; !strings.Contains(msg, "親になる新規課題") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentIDRefLocal はローカルにある課題を ID:<数値> でも
// 指定できることを確認する(抽出結果をそのまま取り込めるようにする)。
func TestImport_ParentIDRefLocal(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI()

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:101"),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.ParentIssueID == nil || *payload.ParentIssueID != 101 {
		t.Errorf("payload.ParentIssueID = %v, want 101", payload.ParentIssueID)
	}
	// ローカルで解決できるため API は呼ばない
	if len(api.getCalls) != 0 {
		t.Errorf("API を呼んでいる: %v", api.getCalls)
	}
}

// TestImport_ParentIDRefRemoteOK は未同期の親を API で確認して受理することを
// 確認する(親を持たない・同一プロジェクト)。
func TestImport_ParentIDRefRemoteOK(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI()
	api.remote["900"] = backlogclient.Issue{
		ID: 900, IssueKey: "EXA-900", ProjectID: testProjectID,
	}

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
		parentUpdateRow("EXA-5", "2026-08-05T00:00:00Z", "ID:900"),
	})
	if !res.Valid {
		t.Fatalf("エラー: %+v", res.Errors)
	}
	payload := payloadOfRow(t, st, res.JobID, 2)
	if payload.ParentIssueID == nil || *payload.ParentIssueID != 900 {
		t.Errorf("payload.ParentIssueID = %v, want 900", payload.ParentIssueID)
	}
	// 同じ親を指す行が複数あっても確認は 1 回だけ
	if len(api.getCalls) != 1 || api.getCalls[0] != "900" {
		t.Errorf("API 呼び出し = %v, want [900]", api.getCalls)
	}
}

// TestImport_ParentIDRefRemoteIsChild は未同期の親が子課題だった場合に
// 行エラーにすることを確認する(孫の禁止)。
func TestImport_ParentIDRefRemoteIsChild(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI()
	api.remote["900"] = backlogclient.Issue{
		ID: 900, IssueKey: "EXA-900", ProjectID: testProjectID, ParentIssueID: 800,
	}

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "1 階層") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentIDRefRemoteOtherProject は他プロジェクトの課題を
// 親に指定できないことを確認する(本ツールの初回スコープ)。
func TestImport_ParentIDRefRemoteOtherProject(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI()
	api.remote["900"] = backlogclient.Issue{
		ID: 900, IssueKey: "EXB-1", ProjectID: testProjectID + 1,
	}

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "プロジェクト") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentIDRefRemoteUnavailable は API でも確認できない親を
// 行エラーにすることを確認する(検証不能のまま送信しない)。
func TestImport_ParentIDRefRemoteUnavailable(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	api := newFakeAPI() // remote に登録しない = 404

	res := importParentRows(t, st, api, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "900") || !strings.Contains(msg, "確認") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_ParentIDRefWithoutAPI は API を渡していない場合でも
// 検証不能のまま受理しないことを確認する。
func TestImport_ParentIDRefWithoutAPI(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
	})
	if msg := errorMessages(res)[2]; !strings.Contains(msg, "確認") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestParentPayloadCompatibility は親課題の項目を持たない旧ジョブの payload が
// そのまま復元できることを確認する(omitempty の互換)。
func TestParentPayloadCompatibility(t *testing.T) {
	encoded, err := EncodePayload(Payload{Action: ActionUpdate, Summary: ptrString("件名")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "parentIssueId") || strings.Contains(encoded, "clearParentIssue") {
		t.Errorf("未設定の親課題が payload に含まれる: %s", encoded)
	}
	p, err := DecodePayload(`{"action":"update","summary":"件名"}`)
	if err != nil {
		t.Fatal(err)
	}
	if p.ParentIssueID != nil || p.ClearParentIssue {
		t.Errorf("旧 payload の復元 = %+v", p)
	}
}

// TestParentUpdateParams は payload が送信パラメータへ正しく写ることを確認する。
func TestParentUpdateParams(t *testing.T) {
	set := updateParamsOf(&Payload{ParentIssueID: ptrInt64(101)})
	if set.ParentIssueID == nil || *set.ParentIssueID != 101 {
		t.Errorf("設定 = %v", set.ParentIssueID)
	}
	cleared := updateParamsOf(&Payload{ClearParentIssue: true})
	if cleared.ParentIssueID == nil || *cleared.ParentIssueID != 0 {
		t.Errorf("クリア = %v", cleared.ParentIssueID)
	}
	unchanged := updateParamsOf(&Payload{})
	if unchanged.ParentIssueID != nil {
		t.Errorf("未指定 = %v", unchanged.ParentIssueID)
	}
	created := createParamsOf(testProjectID, &Payload{ParentIssueID: ptrInt64(101)})
	if created.ParentIssueID == nil || *created.ParentIssueID != 101 {
		t.Errorf("新規追加 = %v", created.ParentIssueID)
	}
}

// TestSamePayload_ParentIssue は再送前突合が親課題まで見ることを確認する。
func TestSamePayload_ParentIssue(t *testing.T) {
	p := &Payload{Summary: ptrString("新規課題"), ParentIssueID: ptrInt64(101)}
	match := backlogclient.Issue{Summary: "新規課題", ParentIssueID: 101}
	if !samePayload(p, match) {
		t.Errorf("親課題が一致する課題を別課題と判定した")
	}
	other := backlogclient.Issue{Summary: "新規課題", ParentIssueID: 102}
	if samePayload(p, other) {
		t.Errorf("親課題が異なる課題を作成済みと判定した")
	}
	none := backlogclient.Issue{Summary: "新規課題"}
	if samePayload(p, none) {
		t.Errorf("親課題が未設定の課題を作成済みと判定した")
	}
	// 親を指定しなかった行は、親を持つ課題と一致しない
	if samePayload(&Payload{Summary: ptrString("新規課題")}, match) {
		t.Errorf("親課題を指定していない行が親付きの課題と一致した")
	}
}

// hasChange は差分表示に指定の文字列が含まれるかを返す。
func hasChange(changes []string, want string) bool {
	for _, c := range changes {
		if c == want {
			return true
		}
	}
	return false
}

// TestImport_ParentAcceptsExportedTemplate は出力したテンプレートを
// そのまま取り込めること(親課題キー列の出力と取り込みの契約)を確認する。
func TestImport_ParentAcceptsExportedTemplate(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	path := filepath.Join(t.TempDir(), "template.xlsx")
	err := export.ExportBulkTemplateToFile(path, testProjectID, export.BulkTemplateSlice([]export.BulkTemplateRow{{
		IssueKey: "EXA-6", Summary: "子課題",
		IssueTypeID: 11, IssueTypeName: "タスク",
		StatusID: 1, StatusName: "未対応",
		PriorityID: 3, PriorityName: "中",
		// テンプレートには現在の親がプリフィルされる
		ParentIssueKey: "EXA-3",
		BaseUpdated:    "2026-08-06T00:00:00Z",
	}}), export.BulkTemplateMasters{
		IssueTypes: []export.NamedRef{{ID: 11, Name: "タスク"}},
		Statuses:   []export.NamedRef{{ID: 1, Name: "未対応"}},
		Priorities: []export.NamedRef{{ID: 3, Name: "中"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	importExported := func() *ImportResult {
		res, ierr := NewImporter(st).Import(context.Background(), ImportOptions{
			ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3, Master: testMaster(),
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

	// 親課題キー列(M 列)を編集すると更新になる
	editTemplateCells(t, path, map[string]string{"M2": "EXA-1"})
	edited := importExported()
	if !edited.Valid {
		t.Fatalf("親課題の編集がエラーになった: %+v", edited.Errors)
	}
	payload := payloadOfRow(t, st, edited.JobID, 2)
	if payload.ParentIssueID == nil || *payload.ParentIssueID != 101 {
		t.Errorf("payload.ParentIssueID = %v, want 101", payload.ParentIssueID)
	}
}

// --- 親の有無を確認できない課題(生 JSON が古い・壊れている)の扱い ---
//
// 「確認不能」を「親なし」として扱うと 1 階層検証を素通りしてしまうため、
// 関わる行はすべて行エラー + フル同期案内にする(検証不能のまま送信しない)。

// unknownParentIssue は親の有無を判定できない課題を追加する(生 JSON なし)。
func unknownParentIssue(t *testing.T, st *store.Store, id int64, issueKey string) {
	t.Helper()
	if err := st.UpsertIssues(context.Background(), []*store.Issue{{
		ID: id, IssueKey: issueKey, ProjectID: testProjectID, Summary: "生 JSON なし",
		StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
		Updated: "2026-08-09T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
}

// TestImport_ParentCandidateUnverifiable は親候補の親子関係を判定できない場合に
// 行エラーにすることを確認する(孫の禁止を検証できないため)。
func TestImport_ParentCandidateUnverifiable(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	unknownParentIssue(t, st, 108, "EXA-8")

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-8"),
	})
	msg := errorMessages(res)[2]
	if !strings.Contains(msg, "EXA-8") || !strings.Contains(msg, "同期") {
		t.Errorf("エラー = %q", msg)
	}
}

// TestImport_CurrentParentUnverifiable は更新対象の課題の現在の親を
// 判定できない場合に行エラーにすることを確認する(差分判定ができないため)。
func TestImport_CurrentParentUnverifiable(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	unknownParentIssue(t, st, 108, "EXA-8")

	for _, value := range []string{"EXA-1", ClearToken} {
		res := importParentRows(t, st, nil, [][]string{
			parentUpdateRow("EXA-8", "2026-08-09T00:00:00Z", value),
		})
		msg := errorMessages(res)[2]
		if !strings.Contains(msg, "EXA-8") || !strings.Contains(msg, "同期") {
			t.Errorf("%s のエラー = %q", value, msg)
		}
	}
}

// TestImport_HasChildUnverifiable は「子を持つか」を判定できない課題が
// プロジェクトに残っている間は親の設定を受理しないことを確認する
// (判定できない課題がその行の子である可能性を否定できないため)。
func TestImport_HasChildUnverifiable(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)
	unknownParentIssue(t, st, 108, "EXA-8")

	res := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "EXA-1"),
	})
	msg := errorMessages(res)[2]
	if !strings.Contains(msg, "同期") {
		t.Errorf("エラー = %q", msg)
	}
	// 解除は階層を深くしないため、判定できない課題があっても受理する
	cleared := importParentRows(t, st, nil, [][]string{
		parentUpdateRow("EXA-6", "2026-08-06T00:00:00Z", ClearToken),
	})
	if !cleared.Valid {
		t.Errorf("解除がエラーになった: %+v", cleared.Errors)
	}
	// 新規追加行は子を持ちようがないため、判定できない課題があっても受理する
	created := importParentRows(t, st, nil, [][]string{
		parentCreateRow("新しい課題", "タスク", "EXA-1"),
	})
	if !created.Valid {
		t.Errorf("新規追加がエラーになった: %+v", created.Errors)
	}
}

// TestImport_ParentInvalidIDRef は ID:<数値> 形式の値が不正な場合に
// 課題キーとして再解釈せず、書式エラーにすることを確認する。
func TestImport_ParentInvalidIDRef(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	for _, value := range []string{"ID:0", "ID:abc", "ID:-1", "ID:99999999999999999999"} {
		res := importParentRows(t, st, nil, [][]string{
			parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", value),
		})
		msg := errorMessages(res)[2]
		if !strings.Contains(msg, "形式") {
			t.Errorf("%s のエラー = %q", value, msg)
		}
	}
}

// TestImport_ParentRemoteNotFoundIsRowError は 404 / 403 を確定的な行エラーとして
// 扱うことを確認する(取り込み全体は止めない)。
func TestImport_ParentRemoteNotFoundIsRowError(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	for _, apiErr := range []error{backlogclient.ErrNotFound, backlogclient.ErrPermissionDenied} {
		api := newFakeAPI()
		api.getErr = apiErr
		res := importParentRows(t, st, api, [][]string{
			parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
			parentUpdateRow("EXA-5", "2026-08-05T00:00:00Z", "ID:900"),
		})
		if len(res.Errors) != 2 {
			t.Fatalf("%v: エラー = %+v, want 2 行", apiErr, res.Errors)
		}
		// 確定的な失敗のため結果をキャッシュし、同じ親を 2 度確認しない
		if len(api.getCalls) != 1 {
			t.Errorf("%v: API 呼び出し = %v, want 1 回", apiErr, api.getCalls)
		}
	}
}

// TestImport_ParentRemoteFailureAbortsImport は成否不明な失敗
// (認証・レート制限・通信障害・中断)で取り込み全体を止めることを確認する。
// 行エラーにすると「親が居ないから通らなかった」と誤解させるため。
func TestImport_ParentRemoteFailureAbortsImport(t *testing.T) {
	st := openTestStore(t)
	seedParentIssues(t, st)

	for _, apiErr := range []error{
		backlogclient.ErrRateLimitExceeded,
		backlogclient.ErrUnauthorized,
		context.Canceled,
		errors.New("接続できません"),
	} {
		api := newFakeAPI()
		api.getErr = apiErr
		path := writeXLSX(t, parentHeaders, [][]string{
			parentUpdateRow("EXA-4", "2026-08-04T00:00:00Z", "ID:900"),
		})
		_, err := NewImporter(st).Import(context.Background(), ImportOptions{
			ProjectID: testProjectID, FilePath: path, DefaultPriorityID: 3,
			Master: testMaster(), API: api,
		})
		if err == nil {
			t.Errorf("%v: 取り込みが成功してしまった", apiErr)
		}
	}
}
