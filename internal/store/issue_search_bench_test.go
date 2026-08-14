package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"backlog-assistant/internal/customfield"
)

// issue_search_bench_test.go は検索結果ページネーション(OFFSET 方式)の
// 性能を裏付けるためのベンチマーク(設計書「課題抽出の検索結果
// ページネーション」5 節)。想定最大規模は 1 万件、許容目標は 1 ページ 200ms。
//
// 深い OFFSET はスキャンコストが増えるため、先頭・中間・最終ページを測る。
// カスタム属性経路は 1 ページ取得のたびに候補行全件の JSON 解析が走るため
// 最も重く、最優先の測定対象になる。

// benchIssueCount はベンチマークで投入する課題件数(想定最大規模)。
const benchIssueCount = 10000

// benchPageSize は画面 1 ページの件数(フロントの PAGE_SIZE と同じ)。
const benchPageSize = 200

// openBenchStore は課題を benchIssueCount 件投入した一時 DB を開く。
//
// 課題の内容はカスタム属性経路も測れるよう生 JSON つきにし、
// 3 件に 1 件だけカスタム属性条件に一致させる(絞り込みが効く状況を作る)。
// キーワード検索(FTS 経路)用に、全課題の件名へ共通の語を含める。
func openBenchStore(b *testing.B) *Store {
	b.Helper()
	dir := b.TempDir()
	s, err := Open(filepath.Join(dir, "example.backlog.jp_12345.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })

	ctx := context.Background()
	// 1 トランザクションあたりの件数を抑えつつ投入する(UpsertIssues は
	// 呼び出しごとに 1 トランザクション)。
	const chunk = 1000
	for start := int64(1); start <= benchIssueCount; start += chunk {
		issues := make([]*Issue, 0, chunk)
		for i := start; i < start+chunk && i <= benchIssueCount; i++ {
			customer := "対象外"
			if i%3 == 0 {
				customer = "ABC商事"
			}
			issues = append(issues, &Issue{
				ID: i, IssueKey: fmt.Sprintf("EXA-%d", i), ProjectID: 1,
				Summary:      fmt.Sprintf("ログイン不具合の調査 %d 件目", i),
				Description:  "画面から操作するとタイムアウトする",
				StatusName:   "未対応",
				AssigneeName: "担当 太郎",
				Created:      "2026-01-10T00:00:00Z",
				Updated:      "2026-02-10T00:00:00Z",
				RawJSON:      cfRawJSON(i, customer, 8, "2026-08-01", 51),
			})
		}
		if err := s.UpsertIssues(ctx, issues); err != nil {
			b.Fatal(err)
		}
	}
	return s
}

// benchPage は測定するページ位置(名前と offset)。
type benchPage struct {
	name   string
	offset int
}

// benchPagesAt は先頭・中間・最終ページの位置を組み立てる
// (matched は条件に一致する総件数)。
func benchPagesAt(matched int) []benchPage {
	return []benchPage{
		{"先頭ページ", 0},
		{"中間ページ", matched / 2},
		{"最終ページ", matched - benchPageSize},
	}
}

// benchSearchPages は指定条件で pages 各位置の 1 ページ取得を測る。
func benchSearchPages(b *testing.B, f IssueFilter, pages []benchPage) {
	s := openBenchStore(b)
	ctx := context.Background()
	for _, p := range pages {
		b.Run(p.name, func(b *testing.B) {
			q := f
			q.Limit = benchPageSize
			q.Offset = p.offset
			// 測定前に 1 回実行し、結果が空でない(= 測る意味がある)ことを確かめる
			res, err := s.SearchIssues(ctx, q)
			if err != nil {
				b.Fatal(err)
			}
			if len(res.Issues) == 0 {
				b.Fatalf("offset %d の結果が 0 件(total %d)", p.offset, res.Total)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.SearchIssues(ctx, q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSearchIssues_SQLOnlyPages は SQL だけで完結する経路の
// 先頭・中間・最終ページを測る(深い OFFSET のコストを見る)。
func BenchmarkSearchIssues_SQLOnlyPages(b *testing.B) {
	benchSearchPages(b, IssueFilter{ProjectID: 1}, benchPagesAt(benchIssueCount))
}

// BenchmarkSearchIssues_FTSPages はキーワードあり(FTS 索引経路)のページ取得を測る。
func BenchmarkSearchIssues_FTSPages(b *testing.B) {
	benchSearchPages(b, IssueFilter{ProjectID: 1, Keyword: "ログイン"}, benchPagesAt(benchIssueCount))
}

// BenchmarkSearchIssues_CustomFieldPages はカスタム属性経路(2 段階検索)の
// ページ取得を測る。1 ページごとに候補行全件の JSON 解析が走るため最も重い。
func BenchmarkSearchIssues_CustomFieldPages(b *testing.B) {
	// 一致するのは 3 件に 1 件なので、一致件数は約 benchIssueCount/3
	matched := benchIssueCount / 3
	benchSearchPages(b, IssueFilter{
		ProjectID: 1,
		CustomFieldFilters: []customfield.Filter{
			{DefID: 1, TypeID: customfield.TypeText, Text: "ABC商事"},
		},
	}, benchPagesAt(matched))
}
