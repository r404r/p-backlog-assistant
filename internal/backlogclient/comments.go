package backlogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// Comment は GET /issues/:issueIdOrKey/comments の 1 件。
//
// コメントは詳細表示専用(Excel 出力等の対象外)のため、
// Issue / Project のような RawJSON(未知フィールドの保持)は持たない。
type Comment struct {
	ID int64 `json:"id"`
	// Content は本文。API が null を返す項目(状態変更等の変更履歴のみの項目)は空文字になる。
	Content    string `json:"content"`
	AuthorName string `json:"authorName"` // createdUser.name
	Created    string `json:"created"`
	Updated    string `json:"updated"`
}

// CommentQuery はコメント取得のページングパラメータ。
type CommentQuery struct {
	MinID int64  // minId(0 なら送信しない)
	MaxID int64  // maxId(0 なら送信しない)
	Order string // order(asc / desc。空なら送信しない)
	Count int    // count(1〜100。0 なら送信しない)
}

// values はクエリパラメータへ変換する(ActivityQuery と同じ流儀)。
func (q CommentQuery) values() url.Values {
	v := url.Values{}
	if q.MinID > 0 {
		v.Set("minId", strconv.FormatInt(q.MinID, 10))
	}
	if q.MaxID > 0 {
		v.Set("maxId", strconv.FormatInt(q.MaxID, 10))
	}
	setIfNotEmpty(v, "order", q.Order)
	if q.Count > 0 {
		v.Set("count", strconv.Itoa(q.Count))
	}
	return v
}

// GetIssueComments は課題のコメント一覧(GET /api/v2/issues/:issueIdOrKey/comments)を取得する。
// 課題が存在しない場合は ErrNotFound(errors.Is で判定可能)を返す。
//
// 応答の扱い:
//   - JSON 配列でない応答(null・エラーオブジェクト等)はエラー
//   - 空配列([])は「コメント 0 件」として正常
//   - id が無い・0 以下の要素はエラー(読み飛ばさない)。GetProjects と同じく
//     「破壊的な判断の根拠になる応答は厳格に扱う」方針による。呼び出し側
//     (internal/sync)は取得できたコメントでローカルを全入れ替えし、
//     ページ終端も「戻り件数 < count」で判定するため、1 件でも黙って落とすと
//     古いページを未取得のまま「全件取得できた」と誤認して既存コメントを
//     消してしまう。id はページング(maxId 遡り)にも必須である。
//     この関数は「応答の要素数 = 戻り件数」を呼び出し側への契約とする。
//     なお、この失敗で課題詳細が失われることはない(呼び出し側はコメント取得の
//     失敗を警告として扱い、課題本体の最新化と既存コメントの表示を維持する)。
func (c *Client) GetIssueComments(ctx context.Context, issueIDOrKey string, q CommentQuery) ([]Comment, error) {
	body, err := c.rawGet(ctx, "/api/v2/issues/"+url.PathEscape(issueIDOrKey)+"/comments", q.values())
	if err != nil {
		return nil, err
	}
	elems, err := decodeArray(body, "コメント一覧")
	if err != nil {
		return nil, err
	}
	if elems == nil {
		// json.Unmarshal は JSON null をスライス未設定(nil)にする。
		// 空配列([])は長さ 0 の非 nil スライスになるため区別できる。
		return nil, errors.New("コメント一覧の応答が不正です(JSON 配列ではありません)")
	}
	out := make([]Comment, 0, len(elems))
	for _, e := range elems {
		var a struct {
			ID *int64 `json:"id"`
			// content は変更履歴のみの項目で null になる
			Content     *string   `json:"content"`
			CreatedUser *apiNamed `json:"createdUser"`
			Created     *string   `json:"created"`
			Updated     *string   `json:"updated"`
		}
		if err := json.Unmarshal(e, &a); err != nil {
			return nil, fmt.Errorf("コメントを解析できません: %w", err)
		}
		id := derefInt64(a.ID)
		if id <= 0 {
			// 上記のとおり読み飛ばさない(件数がずれると呼び出し側の
			// ページ終端判定が壊れ、既存コメントを誤って消しうる)
			return nil, errors.New("コメント一覧の応答が不正です(id が無いコメントが含まれています)")
		}
		cm := Comment{
			ID:      id,
			Content: derefString(a.Content),
			Created: derefString(a.Created),
			Updated: derefString(a.Updated),
		}
		if a.CreatedUser != nil {
			cm.AuthorName = derefString(a.CreatedUser.Name)
		}
		out = append(out, cm)
	}
	return out, nil
}
