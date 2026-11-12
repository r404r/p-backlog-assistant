package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type wikiDownload struct{ cancel context.CancelFunc }

type missingWikiDownload struct{ calls []int64 }

func (d *missingWikiDownload) DownloadWikiAttachment(_ context.Context, _, id int64, dst io.Writer) error {
	d.calls = append(d.calls, id)
	if id == 1 {
		return backlogclient.ErrNotFound
	}
	_, err := io.WriteString(dst, "data")
	return err
}

func TestWikiExportMissingAttachmentKeepsFailureTextAndContinues(t *testing.T) {
	pages := []WikiExportPage{
		{Wiki: store.Wiki{ID: 1, Name: "Guide", Content: "本文", ContentFetchedAt: "now"}, Attachments: []store.WikiAttachment{
			{ID: 1, Name: "missing.png", Size: 4},
			{ID: 2, Name: "later.txt", Size: 4},
		}},
		{Wiki: store.Wiki{ID: 2, Name: "Later", Content: "次頁", ContentFetchedAt: "now"}},
	}
	download := &missingWikiDownload{}
	path, err := ExportWiki(context.Background(), WikiExportOptions{Parent: t.TempDir(), ProjectKey: "SAMPLE", TextFormattingRule: "markdown", Pages: pages}, download)
	if err != nil {
		t.Fatal(err)
	}
	if len(download.calls) != 2 || download.calls[0] != 1 || download.calls[1] != 2 {
		t.Fatal("後続の添付が取得されていません", download.calls)
	}
	b, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest WikiManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	const reason = "添付の取得に失敗しました(HTTP 404)"
	if len(manifest.Pages) != 2 || len(manifest.Pages[0].Attachments) != 2 {
		t.Fatal(manifest)
	}
	first, later := manifest.Pages[0].Attachments[0], manifest.Pages[0].Attachments[1]
	if first.Error != reason || later.Error != "" {
		t.Fatalf("添付の結果 = %q / %q", first.Error, later.Error)
	}
	if _, err := os.Stat(filepath.Join(path, filepath.FromSlash(first.Path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失敗した添付が残っています", err)
	}
	if data, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(later.Path))); err != nil || string(data) != "data" {
		t.Fatal("後続添付", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(manifest.Pages[1].Path))); err != nil || string(data) != "次頁" {
		t.Fatal("後続ページ", string(data), err)
	}
	book, err := excelize.OpenFile(filepath.Join(path, "index.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	rows, err := book.GetRows("Sheet1")
	if err != nil || len(rows) != 3 || len(rows[1]) < 9 || rows[1][8] != "1: "+reason {
		t.Fatal(rows, err)
	}
}

func (d wikiDownload) DownloadWikiAttachment(ctx context.Context, w, a int64, dst io.Writer) error {
	io.WriteString(dst, "data")
	if d.cancel != nil {
		d.cancel()
		return ctx.Err()
	}
	if a == 2 {
		return errors.New("HTTP 302")
	}
	return nil
}
func TestWikiPathAllocation(t *testing.T) {
	names := []string{"a", "a.md/b", "A", "../CON/x?y", "a//b", "a/b", "A/B", "é/x", "e\u0301/x", strings.Repeat("日", 150), "a~3"}
	pages := []WikiExportPage{}
	for i, n := range names {
		pages = append(pages, WikiExportPage{Wiki: store.Wiki{ID: int64(i + 1), Name: n, Content: fmt.Sprint(i + 1), ContentFetchedAt: "now"}})
	}
	one, err := AllocateWikiPaths(pages, ".md")
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(pages)-1; i < j; i, j = i+1, j-1 {
		pages[i], pages[j] = pages[j], pages[i]
	}
	two, err := AllocateWikiPaths(pages, ".md")
	if err != nil {
		t.Fatal(err)
	}
	for id, p := range one {
		if two[id] != p {
			t.Fatal("order-dependent", id, p, two[id])
		}
		for _, s := range strings.Split(p, "/") {
			if len(s) > 255 || !utf8.ValidString(s) || s == ".." {
				t.Fatal(p)
			}
		}
	}
	if one[1] != "pages/a.md" || one[2] == "pages/a.md/b.md" {
		t.Fatal(one)
	}
	root := t.TempDir()
	if _, err := safeWikiPath(root, "../escape"); err == nil {
		t.Fatal("traversal accepted")
	}
	for _, seq := range [][]WikiExportPage{pages, append([]WikiExportPage(nil), pages...)} {
		parent := t.TempDir()
		path, err := ExportWiki(context.Background(), WikiExportOptions{Parent: parent, ProjectKey: "../CON", TextFormattingRule: "markdown", Pages: seq}, wikiDownload{})
		if err != nil {
			t.Fatal(err)
		}
		for id, p := range one {
			b, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(p)))
			if err != nil || string(b) != fmt.Sprint(id) {
				t.Fatal(id, p, err)
			}
		}
	}
}
func TestWikiExportManifestAttachmentsAndCancel(t *testing.T) {
	parent := t.TempDir()
	pages := []WikiExportPage{{Wiki: store.Wiki{ID: 1, Name: "例", Content: "本文", ContentFetchedAt: "now"}, Attachments: []store.WikiAttachment{{ID: 1, Name: "../CON", Size: 4}, {ID: 2, Name: "fail", Size: 4}}}, {Wiki: store.Wiki{ID: 2, Name: "未取得"}}}
	path, err := ExportWiki(context.Background(), WikiExportOptions{Parent: parent, ProjectKey: "SAMPLE", TextFormattingRule: "markdown", Pages: pages}, wikiDownload{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m WikiManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Pages) != 2 || m.Pages[0].AttachmentFailures == "" || m.Pages[1].BodyStatus != "本文未取得" {
		t.Fatal(m)
	}
	for _, p := range m.Pages {
		if _, err := os.Stat(filepath.Join(path, filepath.FromSlash(p.Path))); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range m.Pages[0].Attachments {
		_, err := os.Stat(filepath.Join(path, filepath.FromSlash(a.Path)))
		if (err == nil) != (a.Error == "") {
			t.Fatal(a, err)
		}
	}
	book, err := excelize.OpenFile(filepath.Join(path, "index.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer book.Close()
	rows, err := book.GetRows("Sheet1")
	if err != nil || len(rows) != 3 || !strings.Contains(rows[1][8], "302") {
		t.Fatal(rows, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	_, err = ExportWiki(ctx, WikiExportOptions{Parent: dir, ProjectKey: "SAMPLE", Pages: pages}, wikiDownload{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal(entries)
	}
}

func TestWikiComponentSafety(t *testing.T) {
	for _, name := range []string{"CON", "con.txt", "PRN", "AUX", "NUL", "COM1", "lpt9.log", ".", "..", "a:*?<>|\\\x00", "trailing. ", strings.Repeat("日", 150)} {
		got := fitWikiComponent(cleanWikiComponent(name), "", "")
		if len(got) > 255 || strings.ContainsAny(got, `:*?"<>|\\/`) || strings.HasSuffix(got, ".") || strings.HasSuffix(got, " ") || got == ".." || !utf8.ValidString(got) {
			t.Fatal(name, got)
		}
	}
}
func TestWikiExportSubtreeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{{"Guide", true}, {"Guide/Start", true}, {"Guides", false}, {"Guides/Start", false}} {
		if WikiSubtree(tc.name, "Guide") != tc.want {
			t.Fatal(tc)
		}
	}
}

func TestWikiSubtreeIgnoresEmptyComponents(t *testing.T) {
	for _, tc := range []struct {
		name, subtree string
		want          bool
	}{
		{"a//b", "a/b", true},
		{"a//b/c", "a/b", true},
		{"/x/y", "x", true},
		{"a/b", "/a//b/", true},
		{"a/bc", "a/b", false},
		{"a//bc", "a/b", false},
		{"a", "a/b", false},
		{"a/b", "/", true},
	} {
		t.Run(tc.name+" in "+tc.subtree, func(t *testing.T) {
			if got := WikiSubtree(tc.name, tc.subtree); got != tc.want {
				t.Fatalf("WikiSubtree(%q, %q) = %v, want %v", tc.name, tc.subtree, got, tc.want)
			}
		})
	}
}
