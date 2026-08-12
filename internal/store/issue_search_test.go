package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedIssues は検索テスト用のデータを投入する。
func seedIssues(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	issues := []*Issue{
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "ログイン不具合", Description: "TIMEOUT",
			StatusName: "未対応", AssigneeName: "担当 太郎",
			Created: "2026-01-10T00:00:00Z", Updated: "2026-02-10T00:00:00Z"},
		{ID: 2, IssueKey: "EXA-2", ProjectID: 1, Summary: "画面崩れ", Description: "レイアウト",
			StatusName: "処理中", AssigneeName: "担当 花子",
			Created: "2026-03-10T00:00:00Z", Updated: "2026-04-10T00:00:00Z"},
		{ID: 3, IssueKey: "EXA-3", ProjectID: 1, Summary: "ログイン改善", Description: "",
			StatusName: "完了", AssigneeName: "",
			Created: "2026-05-10T00:00:00Z", Updated: "2026-06-10T00:00:00Z"},
		// 別プロジェクトの課題(projectID 必須のフィルタで除外される)
		{ID: 4, IssueKey: "EXB-1", ProjectID: 2, Summary: "ログイン他プロジェクト",
			StatusName: "未対応", AssigneeName: "担当 太郎",
			Created: "2026-01-10T00:00:00Z", Updated: "2026-02-10T00:00:00Z"},
	}
	if err := s.UpsertIssues(ctx, issues); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertIssues_BulkGeneratesSearchText(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	if err := s.UpsertIssues(ctx, []*Issue{
		{ID: 1, IssueKey: "EXA-1", ProjectID: 1, Summary: "ＴＩＭＥＯＵＴ"},
		{ID: 2, IssueKey: "EXA-2", ProjectID: 1, Summary: "ﾊﾞｸﾞ"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := issueSearchText(ctx, s.DB(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != "timeout\n" {
		t.Errorf("search_text = %q", got)
	}
}

func TestSearchIssues_RequiresProjectID(t *testing.T) {
	s := openTempStore(t)
	if _, err := s.SearchIssues(context.Background(), IssueFilter{}); err == nil {
		t.Fatal("projectID 未指定でもエラーにならなかった")
	}
}

func TestSearchIssues_Keyword(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{ProjectID: 1, Keyword: "ログイン"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || len(res.Issues) != 2 {
		t.Fatalf("件数 = %d / total %d, want 2", len(res.Issues), res.Total)
	}
	for _, i := range res.Issues {
		if i.ProjectID != 1 {
			t.Errorf("別プロジェクトの課題が混入: %+v", i)
		}
	}
	// LIKE メタ文字はエスケープされる
	res, err = s.SearchIssues(context.Background(), IssueFilter{ProjectID: 1, Keyword: "%"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Errorf("%% の検索結果 = %d 件, want 0", res.Total)
	}
}

// TestBuildFilter_KeywordSplit はキーワードが半角・全角スペースで分割され、
// 語ごとに LIKE 条件が組み立てられること(既定は AND)を確認する。
func TestBuildFilter_KeywordSplit(t *testing.T) {
	spec, err := IssueFilter{ProjectID: 1, Keyword: "ログイン　timeout 改善"}.buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(spec.where, "issues.search_text LIKE ?"); n != 3 {
		t.Errorf("LIKE 条件の数 = %d, want 3 (where = %q)", n, spec.where)
	}
	if strings.Contains(spec.where, " OR ") {
		t.Errorf("既定モードで OR が使われている: %q", spec.where)
	}
	// project_id・FTS の MATCH 式に続いて語ごとのパターンが並ぶ。
	// 「改善」は 2 文字なので trigram 索引を使えず MATCH 式から外れるが、
	// LIKE 条件は全語ぶん残るため結果集合は変わらない(AND モード)。
	want := []any{int64(1), `"ログイン" AND "timeout"`, "%ログイン%", "%timeout%", "%改善%"}
	if len(spec.args) != len(want) {
		t.Fatalf("引数 = %v, want %v", spec.args, want)
	}
	for i := range want {
		if spec.args[i] != want[i] {
			t.Errorf("args[%d] = %v, want %v", i, spec.args[i], want[i])
		}
	}
}

// TestBuildFilter_KeywordOrIsParenthesized は OR 連結が括弧で括られ、
// project_id 等の他条件と正しく AND 結合されることを確認する
// (括弧が無いと OR が全体に広がり、別プロジェクトの課題が混入する)。
func TestBuildFilter_KeywordOrIsParenthesized(t *testing.T) {
	spec, err := IssueFilter{ProjectID: 1, Keyword: "ログイン 改善", KeywordMode: "or"}.buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.where, `(issues.search_text LIKE ? ESCAPE '\' OR issues.search_text LIKE ? ESCAPE '\')`) {
		t.Errorf("OR 条件が括弧で括られていない: %q", spec.where)
	}
	if !strings.HasPrefix(spec.where, "issues.deleted = 0 AND issues.project_id = ? AND ") {
		t.Errorf("他条件との結合が AND になっていない: %q", spec.where)
	}
}

// TestBuildFilter_KeywordBlank は空文字・スペースのみのキーワードで
// LIKE 条件が付かないことを確認する。
func TestBuildFilter_KeywordBlank(t *testing.T) {
	for _, kw := range []string{"", "   ", "　", " 　 "} {
		spec, err := IssueFilter{ProjectID: 1, Keyword: kw, KeywordMode: "or"}.buildFilter()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(spec.where, "search_text") {
			t.Errorf("キーワード %q で LIKE 条件が付いた: %q", kw, spec.where)
		}
		if strings.Contains(spec.from, "issues_fts") {
			t.Errorf("キーワード %q で FTS が使われた: %q", kw, spec.from)
		}
		if len(spec.args) != 1 {
			t.Errorf("キーワード %q の引数 = %v, want [1]", kw, spec.args)
		}
	}
}

// TestSearchIssues_KeywordAndOr は複数キーワードの AND / OR の結果件数を確認する。
func TestSearchIssues_KeywordAndOr(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	tests := []struct {
		name    string
		keyword string
		mode    string
		want    int
	}{
		// 課題 1 = 「ログイン不具合 / TIMEOUT」、課題 3 = 「ログイン改善」
		{"AND(既定)は全語を含む課題だけ", "ログイン timeout", "", 1},
		{"AND で一致しない組み合わせ", "ログイン レイアウト", "and", 0},
		{"全角スペースでも分割される", "ログイン　timeout", "", 1},
		{"OR はいずれかを含む課題", "ログイン レイアウト", "or", 3},
		{"未知のモードは AND に縮退する", "ログイン レイアウト", "xyz", 0},
		{"語ごとに LIKE メタ文字をエスケープする", "% _", "or", 0},
		{"1 語のみの OR も動作する", "ログイン", "or", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: tt.keyword, KeywordMode: tt.mode})
			if err != nil {
				t.Fatal(err)
			}
			if res.Total != tt.want || len(res.Issues) != tt.want {
				t.Errorf("件数 = %d / total %d, want %d", len(res.Issues), res.Total, tt.want)
			}
			for _, i := range res.Issues {
				if i.ProjectID != 1 {
					t.Errorf("別プロジェクトの課題が混入: %+v", i)
				}
			}
		})
	}
}

func TestSearchIssues_Ranges(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	// created 範囲(日付指定。終端は当日いっぱいを含む)
	res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, CreatedFrom: "2026-03-01", CreatedTo: "2026-05-10"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Errorf("created 範囲の件数 = %d, want 2", res.Total)
	}
	// updated 範囲
	res, err = s.SearchIssues(ctx, IssueFilter{ProjectID: 1, UpdatedFrom: "2026-06-01"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Issues[0].ID != 3 {
		t.Errorf("updated 範囲の結果 = %+v", res.Issues)
	}
}

func TestSearchIssues_StatusAndAssignee(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, StatusName: "処理中"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Issues[0].ID != 2 {
		t.Errorf("状態フィルタの結果 = %+v", res.Issues)
	}
	res, err = s.SearchIssues(ctx, IssueFilter{ProjectID: 1, AssigneeName: "担当 太郎"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || res.Issues[0].ID != 1 {
		t.Errorf("担当者フィルタの結果 = %+v", res.Issues)
	}
}

func TestSearchIssues_ExcludesDeleted(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	if err := s.MarkIssuesDeleted(ctx, 1, []int64{1}); err != nil {
		t.Fatal(err)
	}
	res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Errorf("削除済み除外後の件数 = %d, want 2", res.Total)
	}
	for _, i := range res.Issues {
		if i.ID == 1 {
			t.Error("削除済みの課題が返された")
		}
	}
}

func TestSearchIssues_LimitReturnsTotal(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	res, err := s.SearchIssues(context.Background(), IssueFilter{ProjectID: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 {
		t.Errorf("返却件数 = %d, want 1", len(res.Issues))
	}
	if res.Total != 3 {
		t.Errorf("総件数 = %d, want 3(上限で切っても総数を返す)", res.Total)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

// TestSearchIssues_SnapshotIsConsistentUnderConcurrentWrite は、件数取得と
// 行取得の間に同期の書き込みが割り込んでも結果が食い違わないこと(中 2)を確認する。
// 両クエリを 1 つの読み取りトランザクションで実行していないと、
// total と実際の行数がずれる(「N 件中 M 件」の表示が壊れる)。
func TestSearchIssues_SnapshotIsConsistentUnderConcurrentWrite(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	seedIssues(t, s)

	done := make(chan struct{})
	writeErr := make(chan error, 1)
	go func() {
		defer close(writeErr)
		// 同期の書き込みを模して、検索と並行に課題を投入し続ける
		for i := int64(100); i < 1100; i++ {
			select {
			case <-done:
				return
			default:
			}
			if err := s.UpsertIssues(ctx, []*Issue{{
				ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1, Summary: "並行投入",
			}}); err != nil {
				writeErr <- err
				return
			}
		}
	}()

	for i := 0; i < 300; i++ {
		res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1})
		if err != nil {
			close(done)
			t.Fatal(err)
		}
		if res.Total != len(res.Issues) {
			close(done)
			t.Fatalf("検索スナップショットが不整合: total = %d, 行数 = %d", res.Total, len(res.Issues))
		}
	}
	close(done)
	for err := range writeErr {
		t.Fatalf("並行書き込みが失敗した: %v", err)
	}
}

func TestListFilterOptions(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	opts, err := s.ListFilterOptions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.StatusNames) != 3 {
		t.Errorf("状態候補 = %v, want 3 件", opts.StatusNames)
	}
	// 空の担当者名は候補に含めない
	for _, a := range opts.AssigneeNames {
		if a == "" {
			t.Error("空の担当者名が候補に含まれている")
		}
	}
	if len(opts.AssigneeNames) != 2 {
		t.Errorf("担当者候補 = %v, want 2 件", opts.AssigneeNames)
	}

	// 削除済みは候補から外れる
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{2}); err != nil {
		t.Fatal(err)
	}
	opts, err = s.ListFilterOptions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range opts.StatusNames {
		if st == "処理中" {
			t.Error("削除済み課題の状態が候補に残っている")
		}
	}
}

func TestListIssueIDs(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	ids, err := s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("ID 数 = %d, want 3", len(ids))
	}
	// 削除済みは対象外
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{3}); err != nil {
		t.Fatal(err)
	}
	ids, err = s.ListIssueIDs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Errorf("削除後の ID 数 = %d, want 2", len(ids))
	}
}

func TestListIssueRefs(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	refs, err := s.ListIssueRefs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 || refs[0].IssueKey != "EXA-1" {
		t.Fatalf("refs = %+v", refs)
	}
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{1}); err != nil {
		t.Fatal(err)
	}
	refs, err = s.ListIssueRefs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Errorf("削除後の refs = %+v", refs)
	}
}

func TestGetIssueUpdatedMap(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)

	m, err := s.GetIssueUpdatedMap(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if m[1] != "2026-02-10T00:00:00Z" {
		t.Errorf("updated[1] = %q", m[1])
	}
	if _, ok := m[4]; ok {
		t.Error("別プロジェクトの課題が含まれている")
	}
}

func TestMarkIssueDeletedByKey(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	ok, err := s.MarkIssueDeletedByKey(ctx, 1, "EXA-2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("既存の課題キーで false が返った")
	}
	ok, err = s.MarkIssueDeletedByKey(ctx, 1, "EXA-999")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("存在しない課題キーで true が返った")
	}
}

// TestMarkIssueDeleted_ScopedToProject は論理削除が指定プロジェクトの課題に
// 限定されること(中 1: 削除 SQL に project_id 条件を必ず付ける)を確認する。
func TestMarkIssueDeleted_ScopedToProject(t *testing.T) {
	s := openTempStore(t)
	seedIssues(t, s)
	ctx := context.Background()

	// 課題 4(EXB-1)はプロジェクト 2 の課題なので、プロジェクト 1 では消えない
	ok, err := s.MarkIssueDeletedByID(ctx, 1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("別プロジェクトの課題が ID で削除された")
	}
	ok, err = s.MarkIssueDeletedByKey(ctx, 1, "EXB-1")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("別プロジェクトの課題が課題キーで削除された")
	}
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{4}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListIssueIDs(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 4 {
		t.Errorf("プロジェクト 2 の課題 = %v, want [4](巻き添え削除しない)", ids)
	}

	// 自プロジェクトの課題は削除できる
	ok, err = s.MarkIssueDeletedByID(ctx, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("自プロジェクトの課題が削除できない")
	}
}
