package backlogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Wiki は一覧・詳細の共通メタデータ。本文は詳細応答のみが持つ。
type Wiki struct {
	ID            int64            `json:"id"`
	ProjectID     int64            `json:"projectId"`
	Name          string           `json:"name"`
	Content       string           `json:"content"`
	Created       string           `json:"created"`
	Updated       string           `json:"updated"`
	TagsJSON      string           `json:"tagsJson"`
	RawJSON       string           `json:"rawJson"`
	CreatedUserID int64            `json:"createdUserId"`
	Attachments   []WikiAttachment `json:"attachments"`
}
type WikiAttachment struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created string `json:"created"`
	RawJSON string `json:"rawJson"`
}

func parseWiki(body []byte, detail bool) (*Wiki, error) {
	var raw struct {
		ID          int64             `json:"id"`
		ProjectID   int64             `json:"projectId"`
		Name        *string           `json:"name"`
		Content     *string           `json:"content"`
		Created     string            `json:"created"`
		Updated     string            `json:"updated"`
		Tags        json.RawMessage   `json:"tags"`
		Attachments []json.RawMessage `json:"attachments"`
		CreatedUser struct {
			ID int64 `json:"id"`
		} `json:"createdUser"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("Wiki 応答を解析できません")
	}
	if raw.ID <= 0 || raw.ProjectID <= 0 || raw.Name == nil || raw.Updated == "" || (detail && raw.Content == nil) {
		return nil, errors.New("Wiki 応答が不正です")
	}
	p := &Wiki{ID: raw.ID, ProjectID: raw.ProjectID, Name: *raw.Name, Created: raw.Created, Updated: raw.Updated, TagsJSON: string(raw.Tags), RawJSON: string(body), Attachments: []WikiAttachment{}}
	p.CreatedUserID = raw.CreatedUser.ID
	if raw.Content != nil {
		p.Content = *raw.Content
	}
	if len(raw.Tags) == 0 || string(raw.Tags) == "null" {
		p.TagsJSON = "[]"
	}
	seen := map[int64]bool{}
	for _, r := range raw.Attachments {
		var a WikiAttachment
		if json.Unmarshal(r, &a) != nil || a.ID <= 0 || a.Size < 0 || seen[a.ID] {
			return nil, errors.New("Wiki 添付応答が不正です")
		}
		seen[a.ID] = true
		a.RawJSON = string(r)
		p.Attachments = append(p.Attachments, a)
	}
	return p, nil
}
func (c *Client) GetWikis(ctx context.Context, projectID int64) ([]Wiki, error) {
	if projectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	body, err := c.rawGetChecked(ctx, "/api/v2/wikis", url.Values{"projectIdOrKey": {strconv.FormatInt(projectID, 10)}}, true)
	if err != nil {
		return nil, err
	}
	elems, err := decodeArray(body, "Wiki 一覧")
	if err != nil || elems == nil {
		return nil, errors.New("Wiki 一覧の応答が不正です")
	}
	out := make([]Wiki, 0, len(elems))
	seen := map[int64]bool{}
	for _, e := range elems {
		p, err := parseWiki(e, false)
		if err != nil {
			return nil, err
		}
		if p.ProjectID != projectID || seen[p.ID] {
			return nil, errors.New("Wiki 一覧のプロジェクトまたは ID が不正です")
		}
		seen[p.ID] = true
		out = append(out, *p)
	}
	return out, nil
}
func (c *Client) GetWiki(ctx context.Context, id int64) (*Wiki, error) {
	if id <= 0 {
		return nil, errors.New("Wiki が指定されていません")
	}
	body, err := c.rawGetChecked(ctx, fmt.Sprintf("/api/v2/wikis/%d", id), nil, true)
	if err != nil {
		return nil, err
	}
	p, err := parseWiki(body, true)
	if err == nil && p.ID != id {
		return nil, errors.New("Wiki の ID が一致しません")
	}
	return p, err
}

// DownloadWikiAttachment は全体の時間上限を設けずストリームを取得する。
// 接続・ヘッダには上限を設け、本文は呼び出し側の context で中断する。
// エラー応答本文と URL は返さない。書きかけファイルの破棄は呼び出し側が行う。
func (c *Client) DownloadWikiAttachment(ctx context.Context, wikiID, attachmentID int64, dst io.Writer) error {
	if wikiID <= 0 || attachmentID <= 0 {
		return errors.New("Wiki または添付が指定されていません")
	}
	ic := newInterceptor(c.limiter)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	defer transport.CloseIdleConnections()
	ic.base.Transport = transport
	ic.base.Timeout = 0
	q := url.Values{"apiKey": {c.apiKey}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/wikis/%d/attachments/%d?%s", c.spaceURL, wikiID, attachmentID, q.Encode()), nil)
	if err != nil {
		return errors.New("添付リクエストを作成できません")
	}
	resp, err := ic.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// transport のエラー文には URL が含まれるため、分類だけを引き継ぐ。
		switch {
		case errors.Is(err, ErrUnauthorized):
			return fmt.Errorf("添付の取得に失敗しました(HTTP 401): %w", ErrUnauthorized)
		case errors.Is(err, ErrPermissionDenied):
			return fmt.Errorf("添付の取得に失敗しました(HTTP 403): %w", ErrPermissionDenied)
		}
		return errors.New("添付の通信に失敗しました")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("添付の取得に失敗しました(HTTP %d)", resp.StatusCode)
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("添付の読み書きが中断されました")
	}
	return ctx.Err()
}
