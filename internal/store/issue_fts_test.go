package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// ftsIntegrityCheck は FTS 索引と issues 本体の整合性を SQLite に検証させる
// (不整合があればエラーになる)。
// external content 方式では rank=1 を指定しないと索引内部の検査だけで終わり、
// content テーブル(issues)との突合(古い posting・rebuild 漏れの検出)が
// 行われない(SQLite 公式の integrity-check 仕様)。
func ftsIntegrityCheck(t *testing.T, q dbtx) {
	t.Helper()
	if _, err := q.ExecContext(context.Background(),
		`INSERT INTO issues_fts(issues_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		t.Fatalf("FTS 索引の整合性検証に失敗しました: %v", err)
	}
}

// TestMigrate_CreatesFTSIndex は v3 マイグレーションで FTS5 仮想テーブルと
// 同期トリガーが作られることを確認する。
func TestMigrate_CreatesFTSIndex(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()

	var sqlText string
	if err := s.DB().QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'issues_fts'`).Scan(&sqlText); err != nil {
		t.Fatalf("issues_fts が作られていません: %v", err)
	}
	for _, want := range []string{"fts5", "content='issues'", "content_rowid='id'", "trigram"} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("issues_fts の定義に %q が含まれていません: %s", want, sqlText)
		}
	}
	for _, trg := range []string{"issues_fts_ai", "issues_fts_au", "issues_fts_ad"} {
		var n int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, trg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("トリガー %s が作られていません", trg)
		}
	}
}

// TestMigrate_V2ToV3RebuildsFTSIndex は v2 の既存 DB を開いたときに、
// 既存行が FTS 索引へ一括投入(rebuild)されることを確認する。
func TestMigrate_V2ToV3RebuildsFTSIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "example.backlog.jp_1.db")

	// v2 相当の DB を手で作る(v1 + v2 のマイグレーションのみ適用)。
	func() {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
			t.Fatal(err)
		}
		for _, stmts := range migrations[:2] {
			for _, st := range stmts {
				if _, err := db.Exec(st); err != nil {
					t.Fatalf("v2 スキーマの作成に失敗: %v (%s)", err, st)
				}
			}
		}
		if _, err := db.Exec(
			`INSERT INTO issues (id, issue_key, project_id, summary, description,
				status_id, status_name, assignee_id, assignee_name, issue_type_name,
				priority_name, created, updated, due_date, raw_json, search_text, fetched_at, deleted)
			 VALUES (1, 'EXA-1', 1, 'ログイン不具合', '再現手順',
				1, '未対応', 0, '', 'バグ',
				'中', '2026-01-01T00:00:00Z', '2026-01-02T00:00:00Z', '', '', ?, '', 0)`,
			NormalizeSearchText("ログイン不具合\n再現手順")); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO meta(key, value) VALUES('schema_version', '2')`); err != nil {
			t.Fatal(err)
		}
	}()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("v2 の DB を開けませんでした: %v", err)
	}
	defer s.Close()

	if v, err := s.SchemaVersion(); err != nil || v != LatestSchemaVersion() {
		t.Fatalf("schema_version = %d (err %v), want %d", v, err, LatestSchemaVersion())
	}
	ftsIntegrityCheck(t, s.DB())

	// rebuild されていれば、既存行が FTS 経由の検索で見つかる。
	res, err := s.SearchIssues(context.Background(), IssueFilter{ProjectID: 1, Keyword: "再現手順"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 {
		t.Errorf("v2 既存行の検索件数 = %d, want 1(rebuild されていない)", res.Total)
	}
}

// TestFTSIndex_StaysInSync は追加・更新・論理削除・物理削除のいずれでも
// FTS 索引が issues 本体と同期し続けることを確認する。
func TestFTSIndex_StaysInSync(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	search := func(kw string) int {
		t.Helper()
		res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: kw})
		if err != nil {
			t.Fatal(err)
		}
		return res.Total
	}

	// 追加(INSERT トリガー)
	if err := s.UpsertIssue(ctx, &Issue{ID: 1, IssueKey: "EXA-1", ProjectID: 1,
		Summary: "ログイン不具合", Description: "タイムアウト"}); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
	if n := search("タイムアウト"); n != 1 {
		t.Errorf("追加直後の検索 = %d, want 1", n)
	}

	// 更新(UPSERT → UPDATE トリガー)。旧テキストは索引から消えること。
	if err := s.UpsertIssue(ctx, &Issue{ID: 1, IssueKey: "EXA-1", ProjectID: 1,
		Summary: "ログアウト不具合", Description: "セッション切れ"}); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
	if n := search("タイムアウト"); n != 0 {
		t.Errorf("更新後に旧テキストが残っている = %d, want 0", n)
	}
	if n := search("セッション切れ"); n != 1 {
		t.Errorf("更新後の新テキストの検索 = %d, want 1", n)
	}

	// 論理削除(UPDATE トリガー。索引には残るが deleted = 0 条件で除外される)
	if err := s.MarkIssuesDeleted(ctx, 1, []int64{1}); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
	if n := search("セッション切れ"); n != 0 {
		t.Errorf("論理削除後の検索 = %d, want 0", n)
	}

	// 物理削除(DELETE トリガー)
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM issues WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
}

// TestFTSIndex_NullSearchText は search_text が NULL の行(想定外だが
// 直接 INSERT された場合)でもトリガー・整合性検証が壊れないことを確認する。
func TestFTSIndex_NullSearchText(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO issues (id, issue_key, project_id, summary, deleted) VALUES (1, 'EXA-1', 1, 'ヌル', 0)`); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
	if _, err := s.DB().ExecContext(ctx, `UPDATE issues SET summary = '更新' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
}

// TestFTSMatchExpr は FTS5 の MATCH 式の組み立て(語のクォート・AND/OR・
// 短い語の除外)を確認する。
func TestFTSMatchExpr(t *testing.T) {
	tests := []struct {
		name  string
		terms []string
		or    bool
		want  string
		ok    bool
	}{
		{"語なし", nil, false, "", false},
		{"1 語", []string{"ログイン"}, false, `"ログイン"`, true},
		{"AND 連結", []string{"ログイン", "timeout"}, false, `"ログイン" AND "timeout"`, true},
		{"OR 連結", []string{"ログイン", "timeout"}, true, `"ログイン" OR "timeout"`, true},
		{"二重引用符はエスケープする", []string{`a"b"c`}, false, `"a""b""c"`, true},
		{"AND は短い語だけ索引から外す", []string{"ab", "ログイン"}, false, `"ログイン"`, true},
		{"AND で全語が短ければ索引を使わない", []string{"ab", "c"}, false, "", false},
		{"OR は短い語が 1 つでもあれば索引を使わない", []string{"ab", "ログイン"}, true, "", false},
		{"3 文字ちょうどは索引を使う", []string{"abc"}, false, `"abc"`, true},
		{"3 文字は Unicode の文字数で数える", []string{"あいう"}, false, `"あいう"`, true},
		// NUL は FTS5 のクエリ解析を途中で打ち切らせる(unterminated string)
		{"AND は NUL を含む語を索引から外す", []string{"ログ\x00イン", "timeout"}, false, `"timeout"`, true},
		{"OR は NUL を含む語があれば索引を使わない", []string{"ログ\x00イン", "timeout"}, true, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ftsMatchExpr(tt.terms, tt.or)
			if got != tt.want || ok != tt.ok {
				t.Errorf("ftsMatchExpr(%q, %v) = (%q, %v), want (%q, %v)",
					tt.terms, tt.or, got, ok, tt.want, tt.ok)
			}
		})
	}

	// 語数の上限(SQLite の式木の深さ制限を超えないため)
	many := make([]string, ftsMaxTerms+10)
	for i := range many {
		many[i] = fmt.Sprintf("語%03d", i)
	}
	t.Run("AND は上限を超えた語を切り捨てる", func(t *testing.T) {
		got, ok := ftsMatchExpr(many, false)
		if !ok {
			t.Fatal("FTS が使われなかった")
		}
		if n := strings.Count(got, " AND ") + 1; n != ftsMaxTerms {
			t.Errorf("語数 = %d, want %d", n, ftsMaxTerms)
		}
	})
	t.Run("OR は上限を超えたら索引を使わない", func(t *testing.T) {
		if got, ok := ftsMatchExpr(many, true); ok {
			t.Errorf("ftsMatchExpr = (%q, true), want (\"\", false)", got)
		}
	})
}

// TestBuildFilter_UsesFTSPrefilter は 3 文字以上のキーワードで FTS の
// 事前絞り込み(CROSS JOIN + MATCH)が使われ、LIKE による再判定も
// 残ることを確認する(結果集合を LIKE と一致させるため)。
func TestBuildFilter_UsesFTSPrefilter(t *testing.T) {
	spec, err := IssueFilter{ProjectID: 1, Keyword: "ログイン timeout"}.buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.from, "issues_fts") || !strings.Contains(spec.from, "CROSS JOIN") {
		t.Errorf("FROM 句が FTS 結合になっていない: %q", spec.from)
	}
	if !strings.Contains(spec.where, "issues_fts MATCH ?") {
		t.Errorf("MATCH 条件が無い: %q", spec.where)
	}
	if n := strings.Count(spec.where, "issues.search_text LIKE ?"); n != 2 {
		t.Errorf("LIKE 再判定の数 = %d, want 2: %q", n, spec.where)
	}
	want := []any{int64(1), `"ログイン" AND "timeout"`, "%ログイン%", "%timeout%"}
	if fmt.Sprint(spec.args) != fmt.Sprint(want) {
		t.Errorf("引数 = %v, want %v", spec.args, want)
	}
	if spec.orderBy != "issues_fts.rowid" {
		t.Errorf("ORDER BY = %q, want issues_fts.rowid", spec.orderBy)
	}
}

// TestBuildFilter_ShortKeywordFallsBackToLike は 3 文字未満のキーワードで
// FTS を使わず LIKE のみになることを確認する(trigram は 3 文字未満を
// 索引できないため)。
func TestBuildFilter_ShortKeywordFallsBackToLike(t *testing.T) {
	for _, tt := range []struct {
		kw   string
		mode string
	}{
		{"ab", ""},
		{"あ", ""},
		{"ab c", "and"},
		{"ab ログイン", "or"}, // OR は 1 語でも短ければ索引を使えない
	} {
		spec, err := IssueFilter{ProjectID: 1, Keyword: tt.kw, KeywordMode: tt.mode}.buildFilter()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(spec.from, "issues_fts") || strings.Contains(spec.where, "MATCH") {
			t.Errorf("キーワード %q(mode %q)で FTS が使われた: from=%q where=%q",
				tt.kw, tt.mode, spec.from, spec.where)
		}
		if spec.orderBy != "issues.id" {
			t.Errorf("ORDER BY = %q, want issues.id", spec.orderBy)
		}
	}
}

// ftsCompatIssues は LIKE と FTS の結果一致を確かめるための多様なデータ。
func ftsCompatIssues() []*Issue {
	texts := []struct {
		summary string
		desc    string
	}{
		{"ログイン不具合", "TIMEOUT が発生する"},
		{"ログイン改善", "画面のレイアウトを直す"},
		{"ログアウトできない", "セッション切れ"},
		{"帳票出力の 100% 失敗", "under_score と % を含む"},
		{"ＴＩＭＥＯＵＴ(全角)", "ﾊﾞｸﾞ(半角カナ)"},
		{"a\"b\"c という引用符", "FTS のクォート境界"},
		{"C++ / C# のビルド", "記号 * ^ : - を含む"},
		{"ABC123 abc456", "英数字混在 Abc789"},
		{"日本語ノ全角カタカナ", "ひらがな・漢字混在"},
		{"連続  空白\tタブ", "改行\n入り"},
		{"", ""},
		{"短い語 ab c", "1 文字 あ の扱い"},
		{"straße", "ドイツ語のエスツェット"},
		{"emoji 🙂 入り", "サロゲートペア"},
	}
	out := make([]*Issue, 0, len(texts))
	for i, tx := range texts {
		out = append(out, &Issue{
			ID: int64(i + 1), IssueKey: fmt.Sprintf("EXA-%d", i+1), ProjectID: 1,
			Summary: tx.summary, Description: tx.desc,
			Created: "2026-01-01T00:00:00Z", Updated: "2026-01-02T00:00:00Z",
		})
	}
	return out
}

// likeReferenceIDs は FTS を使わない素の LIKE 検索(従来実装と同じ意味)で
// 一致する課題 ID を返す。比較の基準として使う。
func likeReferenceIDs(t *testing.T, s *Store, keyword, mode string) []int64 {
	t.Helper()
	terms := splitKeywords(keyword)
	conds := make([]string, 0, len(terms))
	args := []any{int64(1)}
	for _, term := range terms {
		conds = append(conds, `search_text LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(term)+"%")
	}
	q := `SELECT id FROM issues WHERE deleted = 0 AND project_id = ?`
	if len(conds) > 0 {
		if mode == KeywordModeOr {
			q += " AND (" + strings.Join(conds, " OR ") + ")"
		} else {
			q += " AND " + strings.Join(conds, " AND ")
		}
	}
	q += " ORDER BY id"
	rows, err := s.DB().QueryContext(context.Background(), q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// TestSearchIssues_FTSMatchesLike は FTS 化した検索が、従来の LIKE 検索と
// 完全に同じ課題集合を返すことを確認する(日本語・記号・英数字混在)。
func TestSearchIssues_FTSMatchesLike(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, ftsCompatIssues()); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())

	keywords := []string{
		"ログイン", "ログ", "ログア", "不具合", "timeout", "TIMEOUT", "ＴＩＭＥＯＵＴ",
		"ﾊﾞｸﾞ", "バグ", "レイアウト", "%", "_", "100%", "under_score",
		`a"b"c`, `"`, "c++", "C#", "abc", "abc1", "ABC123", "abc456",
		"あ", "ab", "日本語", "ひらがな・漢字", "straße", "STRASSE", "strasse",
		"🙂", "emoji 🙂", "空白", "タブ", "存在しない語", "ログイン 改善",
		"ログイン timeout", "ab ログイン", "ログイン レイアウト", "  ",
	}
	modes := []string{"", "and", "or"}
	for _, kw := range keywords {
		for _, mode := range modes {
			name := fmt.Sprintf("%q/%s", kw, mode)
			t.Run(name, func(t *testing.T) {
				res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: kw, KeywordMode: mode})
				if err != nil {
					t.Fatalf("検索に失敗: %v", err)
				}
				got := make([]int64, 0, len(res.Issues))
				for _, i := range res.Issues {
					got = append(got, i.ID)
				}
				want := likeReferenceIDs(t, s, kw, mode)
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("結果 = %v, want %v(LIKE 基準)", got, want)
				}
				if res.Total != len(want) {
					t.Errorf("total = %d, want %d", res.Total, len(want))
				}
			})
		}
	}
}

// TestSearchIssues_FTSEdgeKeywords は FTS5 のクエリ解析を壊しかねない入力
// (FTS5 の構文記号・ダブルクォート・NUL・多数の語・極端に長い語)でも
// 検索がエラーにならず、LIKE 基準と同じ結果になることを確認する。
//
// キーワードは利用者が自由に入力するため、ここが崩れると検索そのものが
// 失敗する(遅くなるだけの従来実装より悪化する)。
func TestSearchIssues_FTSEdgeKeywords(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, ftsCompatIssues()); err != nil {
		t.Fatal(err)
	}
	keywords := map[string]string{
		"FTS5 の構文記号":  `NEAR(a b) OR NOT c* : ^d`,
		"ダブルクォート 1 つ": `"`,
		"連続ダブルクォート":   `""""`,
		"括弧のみ":        `(((`,
		"NUL を含む語":    "ログ\x00イン",
		"NUL のみ":      "\x00\x00\x00",
		"多数の語(AND)":   strings.TrimSpace(strings.Repeat("ログイン ", ftsMaxTerms+10)),
		"多数の語(OR)":    strings.TrimSpace(strings.Repeat("ログイン ", ftsMaxTerms+10)),
		"極端に長い語":      strings.Repeat("あ", 5000),
	}
	for name, kw := range keywords {
		for _, mode := range []string{"", "or"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: kw, KeywordMode: mode})
				if err != nil {
					t.Fatalf("検索に失敗: %v", err)
				}
				if want := len(likeReferenceIDs(t, s, kw, mode)); res.Total != want {
					t.Errorf("total = %d, want %d(LIKE 基準)", res.Total, want)
				}
			})
		}
	}
}

// TestSearchIssues_NulInSearchText は本文に NUL が含まれる課題でも
// 結果が LIKE 基準と一致することを確認する。
//
// FTS5 は NUL 以降も索引するのに対し、SQLite の LIKE は NUL で比較を
// 打ち切る。FTS の結果は LIKE の上位集合になるため、LIKE による再判定で
// 従来と同じ結果に収まる(この非対称性が逆向きだと取りこぼしになる)。
func TestSearchIssues_NulInSearchText(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssue(ctx, &Issue{ID: 1, IssueKey: "EXA-1", ProjectID: 1,
		Summary: "先頭テキスト\x00後半キーワード", Description: "説明ノ本文"}); err != nil {
		t.Fatal(err)
	}
	ftsIntegrityCheck(t, s.DB())
	for _, kw := range []string{"先頭テキスト", "後半キーワード", "説明ノ本文"} {
		res, err := s.SearchIssues(ctx, IssueFilter{ProjectID: 1, Keyword: kw})
		if err != nil {
			t.Fatalf("キーワード %q: %v", kw, err)
		}
		if want := len(likeReferenceIDs(t, s, kw, "")); res.Total != want {
			t.Errorf("キーワード %q: total = %d, want %d(LIKE 基準)", kw, res.Total, want)
		}
	}
}

// TestIterateIssues_FTSMatchesLike は逐次走査(R4)でも FTS 化後の結果が
// LIKE 基準と一致することを確認する。
func TestIterateIssues_FTSMatchesLike(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, ftsCompatIssues()); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"ログイン", "ログ", "あ", "100%", "ログイン 改善"} {
		for _, mode := range []string{"", "or"} {
			got := []int64{}
			if _, err := s.IterateIssues(ctx,
				IssueFilter{ProjectID: 1, Keyword: kw, KeywordMode: mode},
				func(i *Issue) error { got = append(got, i.ID); return nil }); err != nil {
				t.Fatal(err)
			}
			want := likeReferenceIDs(t, s, kw, mode)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("キーワード %q(mode %q): 結果 = %v, want %v", kw, mode, got, want)
			}
		}
	}
}

// TestSearchIssueIDs_FTSMatchesLike は SearchIssueIDs(全プロジェクト横断の
// キーワード検索)も FTS 化後に LIKE と同じ結果を返すことを確認する。
func TestSearchIssueIDs_FTSMatchesLike(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	if err := s.UpsertIssues(ctx, ftsCompatIssues()); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"ログイン", "ログ", "あ", "100%", `a"b"c`, "存在しない語", ""} {
		got, err := s.SearchIssueIDs(ctx, kw)
		if err != nil {
			t.Fatalf("キーワード %q: %v", kw, err)
		}
		rows, err := s.DB().QueryContext(ctx,
			`SELECT id FROM issues WHERE deleted = 0 AND search_text LIKE ? ESCAPE '\' ORDER BY id`,
			"%"+escapeLike(NormalizeSearchText(kw))+"%")
		if err != nil {
			t.Fatal(err)
		}
		want := []int64{}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			want = append(want, id)
		}
		rows.Close()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("キーワード %q: 結果 = %v, want %v", kw, got, want)
		}
	}
}

// TestSearchIssues_QueryPlanUsesFTS は検索 SQL が FTS 索引を経由し、
// issues の全走査にならないことを EXPLAIN QUERY PLAN で確認する。
func TestSearchIssues_QueryPlanUsesFTS(t *testing.T) {
	s := openTempStore(t)
	spec, err := IssueFilter{ProjectID: 1, Keyword: "ログイン"}.buildFilter()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{spec.selectQuery(), spec.countQuery()} {
		rows, err := s.DB().QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, spec.args...)
		if err != nil {
			t.Fatalf("EXPLAIN QUERY PLAN に失敗: %v (%s)", err, q)
		}
		plan := ""
		for rows.Next() {
			var a, b, c int
			var detail string
			if err := rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail + "\n"
		}
		rows.Close()
		if !strings.Contains(plan, "issues_fts VIRTUAL TABLE INDEX") {
			t.Errorf("FTS 索引が使われていない:\n%s", plan)
		}
		if !strings.Contains(plan, "SEARCH issues USING INTEGER PRIMARY KEY") {
			t.Errorf("issues が rowid 索引で引かれていない:\n%s", plan)
		}
		if strings.Contains(plan, "SCAN issues\n") {
			t.Errorf("issues が全走査になっている:\n%s", plan)
		}
	}
	// 一覧取得は ORDER BY を FTS 側の rowid に合わせ、並べ替え用の
	// 一時 B-Tree(全件のメモリ・一時ファイル展開)を避ける。
	rows, err := s.DB().QueryContext(context.Background(),
		"EXPLAIN QUERY PLAN "+spec.selectQuery(), spec.args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "TEMP B-TREE FOR ORDER BY") {
			t.Errorf("並べ替えに一時 B-Tree が使われている: %s", detail)
		}
	}
}
