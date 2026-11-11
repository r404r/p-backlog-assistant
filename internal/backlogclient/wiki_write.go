package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// WikiWrite は部分更新に依存せず、名前・本文・通知指定を必ず送る。
type WikiWrite struct {
	Name       string
	Content    string
	MailNotify bool
}

func (c *Client) CreateWiki(ctx context.Context, projectID int64, in WikiWrite) (*Wiki, error) {
	if projectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	return c.writeWiki(ctx, http.MethodPost, "/api/v2/wikis", projectID, 0, in)
}
func (c *Client) UpdateWiki(ctx context.Context, id int64, in WikiWrite) (*Wiki, error) {
	if id <= 0 {
		return nil, errors.New("Wiki が指定されていません")
	}
	return c.writeWiki(ctx, http.MethodPatch, fmt.Sprintf("/api/v2/wikis/%d", id), 0, id, in)
}
func (c *Client) writeWiki(ctx context.Context, method, path string, projectID, id int64, in WikiWrite) (*Wiki, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("ページ名が入力されていません")
	}
	v := url.Values{"name": {in.Name}, "content": {in.Content}, "mailNotify": {strconv.FormatBool(in.MailNotify)}}
	if projectID > 0 {
		v.Set("projectId", strconv.FormatInt(projectID, 10))
	}
	body, err := c.rawForm(ctx, method, path, v)
	if err != nil {
		// 応答メッセージにはページ名や本文が含まれ得るため引き継がない。
		if IsUncertain(err) {
			return nil, &UncertainError{Op: "Wiki の保存", Err: errors.New("通信結果が不明です")}
		}
		if IsRejected(err) {
			return nil, &RejectedError{Method: method, Path: path, err: errors.New("Wiki の保存が拒否されました。権限・入力・同期状態を確認してください")}
		}
		return nil, errors.New("Wiki の保存に失敗しました")
	}
	p, err := parseWiki(body, true)
	if err != nil || (id > 0 && p.ID != id) || (projectID > 0 && p.ProjectID != projectID) || p.Name != in.Name || p.Content != in.Content {
		return nil, &UncertainError{Op: "Wiki の保存", Err: errors.New("保存応答を確認できません")}
	}
	return p, nil
}
