package bulk

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/customfield"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// testProjectID は検証用のプロジェクト ID(実データではない)。
const testProjectID = int64(1)

// fakeAPI は Backlog API のフェイク(書き込み・マスタ取得)。
type fakeAPI struct {
	// remote は課題キー → リモートの課題(競合検知の再取得で返す)。
	remote map[string]backlogclient.Issue

	issueTypes   []backlogclient.IssueType
	priorities   []backlogclient.Priority
	statuses     []backlogclient.Status
	customFields []customfield.Def

	// listed は課題一覧(再送前突合で返す既存課題)。
	listed []backlogclient.Issue

	// 呼び出し記録
	creates          []backlogclient.IssueCreate
	updates          []updateCall
	getCalls         []string
	listCalls        []backlogclient.IssueQuery
	customFieldCalls []string
	nextIssue        int64
	createErr        error
	updateErr        error
	getErr           error
	listErr          error
	customFieldsErr  error
	beforeCall       func() // 各書き込みの直前フック(キャンセル検証用)
}

type updateCall struct {
	key string
	in  backlogclient.IssueUpdate
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		remote: map[string]backlogclient.Issue{},
		issueTypes: []backlogclient.IssueType{
			{ID: 11, Name: "タスク", ProjectID: testProjectID},
			{ID: 12, Name: "バグ", ProjectID: testProjectID},
		},
		priorities: []backlogclient.Priority{
			{ID: 2, Name: "高"}, {ID: 3, Name: "中"}, {ID: 4, Name: "低"},
		},
		statuses: []backlogclient.Status{
			{ID: 1, Name: "未対応"}, {ID: 2, Name: "処理中"}, {ID: 4, Name: "完了"},
		},
		customFields: []customfield.Def{
			{
				ID: 31, TypeID: customfield.TypeText, Name: "顧客名", Required: true,
				ApplicableIssueTypes: []int64{}, Items: []customfield.Item{},
			},
			{
				ID: 32, TypeID: customfield.TypeSingleList, Name: "重要度",
				ApplicableIssueTypes: []int64{11},
				Items: []customfield.Item{
					{ID: 321, Name: "高", DisplayOrder: 0},
					{ID: 322, Name: "低", DisplayOrder: 1},
				},
			},
		},
		nextIssue: 1000,
	}
}

func (f *fakeAPI) GetIssue(ctx context.Context, issueIDOrKey string) (*backlogclient.Issue, error) {
	f.getCalls = append(f.getCalls, issueIDOrKey)
	if f.getErr != nil {
		return nil, f.getErr
	}
	issue, ok := f.remote[issueIDOrKey]
	if !ok {
		return nil, backlogclient.ErrNotFound
	}
	return &issue, nil
}

// GetIssues は再送前突合(高 3)で呼ばれる課題一覧。
// listed の内容を 1 ページで返す(件数上限に満たないため最終ページ扱いになる)。
func (f *fakeAPI) GetIssues(ctx context.Context, q backlogclient.IssueQuery) ([]backlogclient.Issue, error) {
	f.listCalls = append(f.listCalls, q)
	if f.listErr != nil {
		return nil, f.listErr
	}
	if q.Offset > 0 {
		return nil, nil
	}
	return f.listed, nil
}

func (f *fakeAPI) CreateIssue(ctx context.Context, in backlogclient.IssueCreate) (*backlogclient.Issue, error) {
	if f.beforeCall != nil {
		f.beforeCall()
	}
	f.creates = append(f.creates, in)
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextIssue++
	return &backlogclient.Issue{
		ID: f.nextIssue, IssueKey: fmt.Sprintf("EXA-%d", f.nextIssue),
		ProjectID: in.ProjectID, Summary: in.Summary,
	}, nil
}

func (f *fakeAPI) UpdateIssue(ctx context.Context, issueIDOrKey string, in backlogclient.IssueUpdate) (*backlogclient.Issue, error) {
	if f.beforeCall != nil {
		f.beforeCall()
	}
	f.updates = append(f.updates, updateCall{key: issueIDOrKey, in: in})
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	issue := f.remote[issueIDOrKey]
	return &issue, nil
}

func (f *fakeAPI) GetProjectIssueTypes(ctx context.Context, projectID int64) ([]backlogclient.IssueType, error) {
	return f.issueTypes, nil
}

func (f *fakeAPI) GetPriorities(ctx context.Context) ([]backlogclient.Priority, error) {
	return f.priorities, nil
}

func (f *fakeAPI) GetProjectStatuses(ctx context.Context, projectID int64) ([]backlogclient.Status, error) {
	return f.statuses, nil
}

func (f *fakeAPI) GetProjectCustomFields(ctx context.Context, projectIDOrKey string) ([]customfield.Def, error) {
	f.customFieldCalls = append(f.customFieldCalls, projectIDOrKey)
	if f.customFieldsErr != nil {
		return nil, f.customFieldsErr
	}
	return f.customFields, nil
}

// testMaster は検証用のマスタ(fakeAPI と同じ内容)。
func testMaster() MasterData {
	return MasterData{
		IssueTypes: []NamedID{{ID: 11, Name: "タスク"}, {ID: 12, Name: "バグ"}},
		Priorities: []NamedID{{ID: 2, Name: "高"}, {ID: 3, Name: "中"}, {ID: 4, Name: "低"}},
		Statuses:   []NamedID{{ID: 1, Name: "未対応"}, {ID: 2, Name: "処理中"}, {ID: 4, Name: "完了"}},
	}
}

// testIssueRawJSON は EXA-1 の生 JSON(カスタム属性の現在値を含む)。
// 定義 ID は testCustomFields に対応する。実データではない。
const testIssueRawJSON = `{"id":101,"issueKey":"EXA-1","customFields":[
	{"id":31,"fieldTypeId":1,"name":"顧客名","value":"取引先 A"},
	{"id":33,"fieldTypeId":4,"name":"開始日","value":"2026-05-06"},
	{"id":34,"fieldTypeId":5,"name":"重要度","value":{"id":341,"name":"高"}},
	{"id":35,"fieldTypeId":6,"name":"タグ","value":[{"id":351,"name":"UI"},{"id":353,"name":"DB"}]}
]}`

// openTestStore は一時ディレクトリの DB を開き、検証用の課題・ユーザを投入する。
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "example.backlog.jp_1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	if err := st.UpsertIssues(ctx, []*store.Issue{
		{
			ID: 101, IssueKey: "EXA-1", ProjectID: testProjectID,
			Summary: "ログイン不具合", Description: "旧説明",
			StatusID: 1, StatusName: "未対応",
			AssigneeID: 501, AssigneeName: "山田 太郎",
			IssueTypeName: "タスク", PriorityName: "中",
			DueDate: "2026-09-01T00:00:00Z", Updated: "2026-08-01T00:00:00Z",
			// カスタム属性の現在値(差分判定に使う。testCustomFields と対応)
			RawJSON: testIssueRawJSON,
		},
		{
			ID: 102, IssueKey: "EXA-2", ProjectID: testProjectID,
			Summary: "画面崩れ", StatusID: 2, StatusName: "処理中",
			IssueTypeName: "バグ", PriorityName: "高", Updated: "2026-08-02T00:00:00Z",
		},
		// 検証テストで 1 行 1 課題を割り当てるための予備(内容は EXA-1 と同形)
		{
			ID: 103, IssueKey: "EXA-3", ProjectID: testProjectID, Summary: "予備 3",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
			Updated: "2026-08-03T00:00:00Z",
		},
		{
			ID: 104, IssueKey: "EXA-4", ProjectID: testProjectID, Summary: "予備 4",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
			Updated: "2026-08-04T00:00:00Z",
		},
		{
			ID: 105, IssueKey: "EXA-5", ProjectID: testProjectID, Summary: "予備 5",
			StatusID: 1, StatusName: "未対応", IssueTypeName: "タスク", PriorityName: "中",
			Updated: "2026-08-05T00:00:00Z",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceUsers(ctx, []*store.User{
		{ID: 501, UserCode: "taro", Name: "山田 太郎"},
		{ID: 502, UserCode: "hanako", Name: "山田 花子"},
		{ID: 503, UserCode: "jiro", Name: "重複 名前"},
		{ID: 504, UserCode: "saburo", Name: "重複 名前"},
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// テンプレートの標準ヘッダ(設計書 5 節)。
var templateHeaders = []string{
	"issueKey", "件名", "種別ID", "種別名", "状態ID", "状態名",
	"優先度ID", "優先度名", "担当者ID", "担当者名", "期限", "詳細", "base_updated",
}

// writeXLSX はヘッダと行から xlsx を作り、そのパスを返す。
func writeXLSX(t *testing.T, headers []string, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	sheet := f.GetSheetName(0)
	for i, h := range headers {
		cell, err := excelize.CoordinatesToCellName(i+1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.SetCellStr(sheet, cell, h); err != nil {
			t.Fatal(err)
		}
	}
	for r, row := range rows {
		for i, v := range row {
			cell, err := excelize.CoordinatesToCellName(i+1, r+2)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeTemplateXLSX はテンプレート出力と同じ体裁(データシート +
// プロジェクト ID を埋め込んだ「記入方法」シート)の xlsx を作り、パスを返す。
// 見出し・シート名は export の定数を使い、出力側との契約を共有する。
func writeTemplateXLSX(t *testing.T, projectID int64, rows [][]string) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := f.SetSheetName(f.GetSheetName(0), export.SheetBulkTemplate); err != nil {
		t.Fatal(err)
	}
	writeSheetRows(t, f, export.SheetBulkTemplate, append([][]string{templateHeaders}, rows...))
	if _, err := f.NewSheet(export.SheetBulkGuide); err != nil {
		t.Fatal(err)
	}
	writeSheetRows(t, f, export.SheetBulkGuide, [][]string{
		{"項目", "説明"},
		{export.BulkProjectIDLabel, strconv.FormatInt(projectID, 10)},
	})

	path := filepath.Join(t.TempDir(), "bulk.xlsx")
	if err := f.SaveAs(path); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeSheetRows はシートへ行を書き込む(左上から順に)。
func writeSheetRows(t *testing.T, f *excelize.File, sheet string, rows [][]string) {
	t.Helper()
	for r, row := range rows {
		for i, v := range row {
			cell, err := excelize.CoordinatesToCellName(i+1, r+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.SetCellStr(sheet, cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// importFile はテンプレート列の行を取り込む(既定優先度は「中」= 3)。
func importFile(t *testing.T, st *store.Store, rows [][]string) *ImportResult {
	t.Helper()
	return importFileWith(t, st, templateHeaders, rows, 3)
}

func importFileWith(t *testing.T, st *store.Store, headers []string, rows [][]string, defaultPriorityID int64) *ImportResult {
	t.Helper()
	return importFileWithMaster(t, st, headers, rows, defaultPriorityID, testMaster())
}

// importFileWithMaster はマスタを指定して取り込む(カスタム属性の検証用)。
func importFileWithMaster(t *testing.T, st *store.Store, headers []string, rows [][]string,
	defaultPriorityID int64, master MasterData) *ImportResult {
	t.Helper()
	path := writeXLSX(t, headers, rows)
	res, err := NewImporter(st).Import(context.Background(), ImportOptions{
		ProjectID:         testProjectID,
		FilePath:          path,
		DefaultPriorityID: defaultPriorityID,
		Master:            master,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// errorMessages は行番号 → メッセージのマップを返す(検証用)。
func errorMessages(res *ImportResult) map[int]string {
	m := map[int]string{}
	for _, e := range res.Errors {
		m[e.RowNo] = e.Message
	}
	return m
}

// previewOf は行番号のプレビューを返す(無ければ nil)。
func previewOf(res *ImportResult, rowNo int) *RowPreview {
	for i := range res.Previews {
		if res.Previews[i].RowNo == rowNo {
			return &res.Previews[i]
		}
	}
	return nil
}
