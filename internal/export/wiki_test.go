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

	"backlog-assistant/internal/store"
)

type wikiDownload struct{ cancel context.CancelFunc }

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
