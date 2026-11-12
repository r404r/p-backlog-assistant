package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

type WikiDownloader interface {
	DownloadWikiAttachment(context.Context, int64, int64, io.Writer) error
}
type WikiExportPage struct {
	Wiki        store.Wiki
	Attachments []store.WikiAttachment
}
type WikiExportOptions struct {
	Parent, ProjectKey, TextFormattingRule, AppVersion string
	Pages                                              []WikiExportPage
	OnProgress                                         func(int, int)
}
type WikiManifest struct {
	SchemaVersion      int                `json:"schemaVersion"`
	ExportedAt         string             `json:"exportedAt"`
	ProjectKey         string             `json:"projectKey"`
	TextFormattingRule string             `json:"textFormattingRule"`
	AppVersion         string             `json:"appVersion"`
	Pages              []WikiManifestPage `json:"pages"`
}
type WikiManifestAttachment struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Path  string `json:"path"`
	Error string `json:"error"`
}
type WikiManifestPage struct {
	ID                 int64                    `json:"wikiId"`
	Name               string                   `json:"name"`
	Path               string                   `json:"path"`
	Tags               string                   `json:"tags"`
	Updated            string                   `json:"updated"`
	AttachmentCount    int                      `json:"attachmentCount"`
	AttachmentSize     int64                    `json:"attachmentSize"`
	BodyStatus         string                   `json:"bodyStatus"`
	AttachmentFailures string                   `json:"attachmentFailures"`
	Attachments        []WikiManifestAttachment `json:"attachments"`
}

// ExportWiki は同じ親の一時ディレクトリに出力し、完了時にだけ公開する。
func ExportWiki(ctx context.Context, o WikiExportOptions, downloader WikiDownloader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(o.Parent)
	if err != nil {
		return "", err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	ext := ".txt"
	if o.TextFormattingRule == "markdown" {
		ext = ".md"
	}
	pages := append([]WikiExportPage(nil), o.Pages...)
	sort.Slice(pages, func(i, j int) bool { return pages[i].Wiki.ID < pages[j].Wiki.ID })
	active := pages[:0]
	for _, p := range pages {
		if !p.Wiki.Deleted {
			active = append(active, p)
		}
	}
	pages = active
	paths, err := AllocateWikiPaths(pages, ext)
	if err != nil {
		return "", err
	}
	name := fitWikiComponent(cleanWikiComponent(o.ProjectKey), "", "-wiki-"+time.Now().Format("20060102-150405"))
	final := filepath.Join(parent, name)
	for n := 2; ; n++ {
		_, err := os.Lstat(final)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		final = filepath.Join(parent, fitWikiComponent(name, fmt.Sprintf("~%d", n), ""))
	}
	tmp, err := os.MkdirTemp(parent, ".wiki-export-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	tmp, err = filepath.EvalSymlinks(tmp)
	if err != nil {
		return "", err
	}
	// os.Root は書き込み時のシンボリックリンク経由の越境も防ぐ。
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return "", err
	}
	defer root.Close()
	write := func(rel string, body []byte) error {
		if _, err := safeWikiPath(tmp, rel); err != nil {
			return err
		}
		local := filepath.FromSlash(rel)
		if err := root.MkdirAll(filepath.Dir(local), 0o700); err != nil {
			return err
		}
		f, err := root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, werr := f.Write(body)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		return cerr
	}
	manifest := WikiManifest{SchemaVersion: 1, ExportedAt: time.Now().UTC().Format(time.RFC3339), ProjectKey: o.ProjectKey, TextFormattingRule: o.TextFormattingRule, AppVersion: o.AppVersion, Pages: []WikiManifestPage{}}
	for i, p := range pages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		m := WikiManifestPage{ID: p.Wiki.ID, Name: p.Wiki.Name, Path: paths[p.Wiki.ID], Tags: p.Wiki.TagsJSON, Updated: p.Wiki.Updated, BodyStatus: "成功", AttachmentCount: len(p.Attachments), Attachments: []WikiManifestAttachment{}}
		if p.Wiki.ContentFetchedAt == "" {
			m.BodyStatus = "本文未取得"
		}
		body := p.Wiki.Content
		if p.Wiki.ContentFetchedAt == "" {
			body = ""
		}
		if err := write(m.Path, []byte(body)); err != nil {
			m.BodyStatus = "本文の出力に失敗しました"
		}
		used := map[string]bool{}
		attachments := append([]store.WikiAttachment(nil), p.Attachments...)
		sort.Slice(attachments, func(i, j int) bool { return attachments[i].ID < attachments[j].ID })
		for _, a := range attachments {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			m.AttachmentSize += a.Size
			base := fmt.Sprintf("%d_%s", a.ID, cleanWikiComponent(a.Name))
			file := fitWikiComponent(base, "", "")
			for n := 1; used[wikiNameKey(file)]; n++ {
				file = fitWikiComponent(base, fmt.Sprintf("~%d~%d", a.ID, n), "")
			}
			used[wikiNameKey(file)] = true
			rel := fmt.Sprintf("attachments/%d/%s", p.Wiki.ID, file)
			ma := WikiManifestAttachment{ID: a.ID, Name: a.Name, Size: a.Size, Path: rel}
			dlErr := func() error {
				if _, err := safeWikiPath(tmp, rel); err != nil {
					return err
				}
				local := filepath.FromSlash(rel)
				if err := root.MkdirAll(filepath.Dir(local), 0o700); err != nil {
					return errors.New("添付ディレクトリを作成できません")
				}
				f, err := root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return errors.New("添付ファイルを作成できません")
				}
				if downloader == nil {
					err = errors.New("添付ダウンロードが利用できません")
				} else {
					err = downloader.DownloadWikiAttachment(ctx, p.Wiki.ID, a.ID, f)
				}
				if closeErr := f.Close(); err == nil {
					err = closeErr
				}
				if err == nil {
					info, statErr := root.Stat(local)
					if statErr != nil {
						err = errors.New("添付を確認できません")
					} else if info.Size() != a.Size {
						err = errors.New("添付サイズが一致しません")
					}
				}
				if err != nil {
					_ = root.Remove(local)
				}
				return err
			}()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if dlErr != nil {
				ma.Error = dlErr.Error()
				// 画像用の分類を出力せず、従来の添付404の理由を保持する。
				if errors.Is(dlErr, backlogclient.ErrNotFound) {
					ma.Error = "添付の取得に失敗しました(HTTP 404)"
				}
				if m.AttachmentFailures != "" {
					m.AttachmentFailures += "; "
				}
				m.AttachmentFailures += fmt.Sprintf("%d: %s", a.ID, ma.Error)
			}
			m.Attachments = append(m.Attachments, ma)
		}
		manifest.Pages = append(manifest.Pages, m)
		if o.OnProgress != nil {
			o.OnProgress(i+1, len(pages))
		}
	}
	book := excelize.NewFile()
	defer book.Close()
	headers := []string{"wikiId", "ページ名", "出力パス", "タグ", "updated", "添付数", "添付合計サイズ", "本文出力の成否", "添付失敗理由"}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := book.SetCellStr("Sheet1", cell, h); err != nil {
			return "", err
		}
	}
	for i, p := range manifest.Pages {
		vals := []any{p.ID, p.Name, p.Path, p.Tags, p.Updated, p.AttachmentCount, p.AttachmentSize, p.BodyStatus, p.AttachmentFailures}
		for j, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
			if err := book.SetCellValue("Sheet1", cell, v); err != nil {
				return "", err
			}
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		return "", err
	}
	if err := write("index.xlsx", buffer.Bytes()); err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	if err := write("manifest.json", body); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := os.Lstat(final); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("出力先が既に存在します")
	}
	// Windows では開いたルートハンドルがディレクトリの rename を妨げる。
	// 異常経路の defer は残し、公開前に明示的に閉じる。
	if err := root.Close(); err != nil {
		return "", fmt.Errorf("一時ディレクトリを閉じられません: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	return final, nil
}

// WikiSubtree はページ名の成分境界で選択する。類似プレフィックスは含めない。
func WikiSubtree(name, subtree string) bool {
	// ツリーと同じく空成分だけを無視する。「.」「..」等の論理名は保持する。
	split := func(path string) []string { return strings.FieldsFunc(path, func(r rune) bool { return r == '/' }) }
	pageParts, subtreeParts := split(name), split(subtree)
	if len(subtreeParts) > len(pageParts) {
		return false
	}
	for i, part := range subtreeParts {
		if pageParts[i] != part {
			return false
		}
	}
	return true
}
