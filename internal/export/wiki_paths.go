package export

import (
	"backlog-assistant/internal/fileutil"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func wikiNameKey(s string) string        { return cases.Fold().String(norm.NFC.String(s)) }
func cleanWikiComponent(s string) string { return fileutil.CleanComponent(s) }
func fitWikiComponent(base, suffix, ext string) string {
	return fileutil.FitComponent(base, suffix, ext)
}

type wikiPathNode struct {
	raw      string
	id       int64
	file     bool
	children map[string]*wikiPathNode
	pageID   int64
}

// AllocateWikiPaths はファイルとディレクトリを同じ名前空間で事前割当する。
// 衝突は ID、次に元名のバイト順で決める。正規化は比較キーだけに使用する。
func AllocateWikiPaths(pages []WikiExportPage, ext string) (map[int64]string, error) {
	root := &wikiPathNode{children: map[string]*wikiPathNode{}}
	seen := map[int64]bool{}
	for _, p := range pages {
		if p.Wiki.ID <= 0 || seen[p.Wiki.ID] {
			return nil, errors.New("Wiki ID が不正または重複しています")
		}
		seen[p.Wiki.ID] = true
		parts := []string{}
		for _, part := range strings.Split(p.Wiki.Name, "/") {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			parts = []string{"_"}
		}
		node := root
		for i, part := range parts {
			file := i == len(parts)-1
			key := "d:" + part
			if file {
				key = fmt.Sprintf("f:%d", p.Wiki.ID)
			}
			child := node.children[key]
			if child == nil {
				child = &wikiPathNode{raw: part, id: p.Wiki.ID, file: file, children: map[string]*wikiPathNode{}, pageID: p.Wiki.ID}
				node.children[key] = child
			}
			if p.Wiki.ID < child.id {
				child.id = p.Wiki.ID
			}
			node = child
		}
	}
	result := map[int64]string{}
	var walk func(*wikiPathNode, string)
	walk = func(node *wikiPathNode, parent string) {
		children := make([]*wikiPathNode, 0, len(node.children))
		for _, n := range node.children {
			children = append(children, n)
		}
		sort.Slice(children, func(i, j int) bool {
			if children[i].id != children[j].id {
				return children[i].id < children[j].id
			}
			return children[i].raw < children[j].raw
		})
		used := map[string]bool{}
		for _, n := range children {
			base := cleanWikiComponent(n.raw)
			ending := ""
			if n.file {
				ending = ext
			}
			suffix := ""
			name := fitWikiComponent(base, suffix, ending)
			for count := 1; used[wikiNameKey(name)]; count++ {
				suffix = fmt.Sprintf("~%d", n.id)
				if count > 1 {
					suffix += fmt.Sprintf("~%d", count)
				}
				name = fitWikiComponent(base, suffix, ending)
			}
			used[wikiNameKey(name)] = true
			path := parent + "/" + name
			if n.file {
				result[n.pageID] = path
			} else {
				walk(n, path)
			}
		}
	}
	walk(root, "pages")
	return result, nil
}
func safeWikiPath(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", errors.New("出力ルート外のパスです")
	}
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	r, err := filepath.Rel(root, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errors.New("出力ルート外のパスです")
	}
	return abs, nil
}
