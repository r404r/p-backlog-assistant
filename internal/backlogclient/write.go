package backlogclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 一括更新・追加(設計書 5 節)で使う書き込み API。
//
// 送信は取得系(rawGet)と同じ interceptor を経由するため、
// update 区分のレート制限・429 リトライ・401/403 の正規化・
// エラーメッセージの apiKey マスクが自動的に適用される。

// maxAPIErrorMessageLen は API のエラーメッセージを結果へ載せる際の上限。
// 行ごとの失敗理由はユーザに提示する必要があるが、応答本文を丸ごと載せない。
const maxAPIErrorMessageLen = 200

// 書き込みエラーの 2 分類(高 4)。
//
// 一括更新・追加では「送信したが結果が分からない」状態を失敗と断定してはならない
// (実際には作成・更新済みなのに error として再送すると二重更新になる)。
// そのため書き込みのエラーを次の 2 つに分ける:
//
//	RejectedError  : リクエストの内容・権限を理由に拒否された(サーバは反映していない)。
//	                 呼び出し側は失敗として確定してよい。
//	UncertainError : 送信後に応答が得られなかった・応答を解釈できなかった、あるいは
//	                 サーバ側の障害(408 / 5xx)で結果を確認できなかった。
//	                 成否不明であり、呼び出し側は失敗として確定してはならない。
//
// どちらにも該当しないエラー(送信前の入力検証など)は、送信していないことが
// 明らかなため通常のエラーとして扱う。

// RejectedError は Backlog がリクエストを確定的に拒否したことを表す。
// クライアント側の誤り(408 を除く 4xx)であり、サーバ側で書き込みが
// 行われていないことが保証される。
//
// 5xx と 408 は含まない: サーバが書き込みを終えた後にタイムアウト・障害が
// 起きた可能性があり、「反映されていない」と断定できないため
// UncertainError として扱う(2 回目 高 2)。
type RejectedError struct {
	// StatusCode は受信した HTTP ステータス。
	// transport 側で正規化されたエラー(401 / 403 / 429)経由の場合は 0 になる
	// (ステータス受信済みであることは err の種類で判別できる)。
	StatusCode int
	Method     string
	Path       string
	// Message は応答の errors[].message(取得できた場合のみ)。
	Message string
	// err は正規化済みエラー(ErrNotFound / ErrUnauthorized 等)。
	// errors.Is による判定を保つためラップする。
	err error
}

func (e *RejectedError) Error() string {
	switch {
	case e.err != nil:
		return e.err.Error()
	case e.Message != "":
		return fmt.Sprintf("Backlog API がエラーを返しました(HTTP %d): %s", e.StatusCode, e.Message)
	default:
		return fmt.Sprintf("Backlog API がエラーを返しました(HTTP %d): %s %s", e.StatusCode, e.Method, e.Path)
	}
}

// Unwrap は正規化済みエラー(ErrNotFound 等)を返し、errors.Is の判定を保つ。
func (e *RejectedError) Unwrap() error { return e.err }

// UncertainError は書き込みを送信した後に結果を確認できなかったことを表す
// (応答欠落・ネットワークエラー・応答の JSON 解析失敗)。
// 反映されたかどうかが不明なため、呼び出し側は失敗として確定せず、
// 送信中(sending)のまま残して利用者へ確認を促すこと。
type UncertainError struct {
	// Op は確認できなかった操作の説明(「課題の追加」等)。
	Op  string
	Err error
}

func (e *UncertainError) Error() string {
	return fmt.Sprintf("%sの結果を確認できませんでした(反映されている可能性があります): %s", e.Op, e.Err.Error())
}

func (e *UncertainError) Unwrap() error { return e.Err }

// IsRejected はエラーが確定的拒否(RejectedError)かを返す。
func IsRejected(err error) bool {
	var re *RejectedError
	return errors.As(err, &re)
}

// IsUncertain はエラーが成否不明(UncertainError)かを返す。
func IsUncertain(err error) bool {
	var ue *UncertainError
	return errors.As(err, &ue)
}

// IssueCreate は課題追加(POST /api/v2/issues)のパラメータ。
//
// ProjectID・Summary・IssueTypeID・PriorityID は API 仕様上の必須項目
// (設計書 5 節)。任意項目はポインタで表し、nil は「送信しない」を意味する。
// 新規追加ではクリア指定(#CLEAR#)は使えないため、空値の特別扱いはしない。
type IssueCreate struct {
	ProjectID   int64
	Summary     string
	IssueTypeID int64
	PriorityID  int64
	Description *string // nil = 送信しない
	AssigneeID  *int64  // nil = 送信しない
	DueDate     *string // nil = 送信しない(yyyy-MM-dd)
}

// IssueUpdate は課題更新(PATCH /api/v2/issues/:issueIdOrKey)のパラメータ。
//
// すべてポインタで表し、意味は次のとおり(設計書 5 節):
//   - nil        : 変更しない(パラメータを送信しない)
//   - 値あり      : その値へ更新する
//   - 空値       : フィールドのクリア(*int64 が 0 / *string が "")。
//     API へは空文字パラメータ(assigneeId= / dueDate= / description=)を送る。
//
// クリアを許可するのは担当者・期限・詳細のみ(呼び出し側 internal/bulk で検証する)。
type IssueUpdate struct {
	Summary     *string
	Description *string // "" = クリア
	IssueTypeID *int64
	PriorityID  *int64
	StatusID    *int64 // 更新のみ(新規追加では指定できない)
	AssigneeID  *int64 // 0 = クリア(未割当にする)
	DueDate     *string
}

// values は更新パラメータをフォーム値へ変換する。
func (u IssueUpdate) values() url.Values {
	v := url.Values{}
	if u.Summary != nil {
		v.Set("summary", *u.Summary)
	}
	if u.Description != nil {
		v.Set("description", *u.Description) // "" でクリア
	}
	if u.IssueTypeID != nil {
		v.Set("issueTypeId", strconv.FormatInt(*u.IssueTypeID, 10))
	}
	if u.PriorityID != nil {
		v.Set("priorityId", strconv.FormatInt(*u.PriorityID, 10))
	}
	if u.StatusID != nil {
		v.Set("statusId", strconv.FormatInt(*u.StatusID, 10))
	}
	if u.AssigneeID != nil {
		if *u.AssigneeID <= 0 {
			// 空文字パラメータによるクリアは公式仕様に明記が無いため要実機確認(中 5)。
			// 検証されるまで #CLEAR#(担当者)の結果は保証されない。
			v.Set("assigneeId", "") // クリア(未割当)
		} else {
			v.Set("assigneeId", strconv.FormatInt(*u.AssigneeID, 10))
		}
	}
	if u.DueDate != nil {
		// assigneeId と同じく、空文字による期限クリアは公式仕様に明記が無いため
		// 要実機確認(中 5)。検証されるまで #CLEAR#(期限)の結果は保証されない。
		v.Set("dueDate", *u.DueDate) // "" でクリア
	}
	return v
}

// CreateIssue は課題を 1 件追加する(POST /api/v2/issues、form encoding)。
// バルク API は存在しないため、一括追加は呼び出し側が 1 件ずつ実行する(設計書 5 節)。
func (c *Client) CreateIssue(ctx context.Context, in IssueCreate) (*Issue, error) {
	// 必須項目は送信前に検証する(無駄な update 区分の消費を避ける)
	switch {
	case in.ProjectID <= 0:
		return nil, errors.New("プロジェクトが指定されていません")
	case strings.TrimSpace(in.Summary) == "":
		return nil, errors.New("件名が入力されていません")
	case in.IssueTypeID <= 0:
		return nil, errors.New("種別が指定されていません")
	case in.PriorityID <= 0:
		return nil, errors.New("優先度が指定されていません")
	}
	v := url.Values{}
	v.Set("projectId", strconv.FormatInt(in.ProjectID, 10))
	v.Set("summary", in.Summary)
	v.Set("issueTypeId", strconv.FormatInt(in.IssueTypeID, 10))
	v.Set("priorityId", strconv.FormatInt(in.PriorityID, 10))
	if in.Description != nil {
		v.Set("description", *in.Description)
	}
	if in.AssigneeID != nil {
		v.Set("assigneeId", strconv.FormatInt(*in.AssigneeID, 10))
	}
	if in.DueDate != nil {
		v.Set("dueDate", *in.DueDate)
	}
	body, err := c.rawForm(ctx, http.MethodPost, "/api/v2/issues", v)
	if err != nil {
		return nil, err
	}
	// 2xx を受け取った後に結果(課題 ID)を確認できない場合は、課題が
	// 作成済みの可能性が高いため成否不明として返す(高 4 / 2 回目 中 1)
	return parseWriteResult("課題の追加", body)
}

// UpdateIssue は課題を 1 件更新する(PATCH /api/v2/issues/:issueIdOrKey)。
// 変更対象が 1 つも無い場合は送信せずエラーを返す。
func (c *Client) UpdateIssue(ctx context.Context, issueIDOrKey string, in IssueUpdate) (*Issue, error) {
	if strings.TrimSpace(issueIDOrKey) == "" {
		return nil, errors.New("課題キーが指定されていません")
	}
	v := in.values()
	if len(v) == 0 {
		return nil, errors.New("更新する項目がありません")
	}
	body, err := c.rawForm(ctx, http.MethodPatch, "/api/v2/issues/"+url.PathEscape(issueIDOrKey), v)
	if err != nil {
		return nil, err
	}
	// 2xx 受信後に結果を確認できない場合は成否不明
	// (更新は反映済みの可能性がある。高 4 / 2 回目 中 1)
	return parseWriteResult("課題の更新", body)
}

// rawForm は API キー付きの POST / PATCH(application/x-www-form-urlencoded)を
// 実行し、レスポンスボディをそのまま返す。
//
// API キーはクエリパラメータで送る(取得系 rawGet と同じ。ボディには入れない)。
// ボディは strings.Reader で渡すため net/http が GetBody を設定し、
// 429 リトライ時に interceptor が再送できる。
func (c *Client) rawForm(ctx context.Context, method, path string, form url.Values) ([]byte, error) {
	q := url.Values{}
	q.Set("apiKey", c.apiKey)
	req, err := http.NewRequestWithContext(ctx, method, c.spaceURL+path+"?"+q.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("リクエストを作成できません: %s", MaskAPIKey(err.Error()))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	maskedPath := MaskAPIKey(path)
	resp, err := c.httpDo.Do(req)
	if err != nil {
		// transport 側でマスク・正規化済み。ここで確定的拒否か成否不明かを分ける(高 4)
		return nil, classifyTransportError(method, maskedPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		// ステータスは受信できているが本文を読めていない。
		// 2xx だったのかも判別できないため成否不明として返す(高 4)
		return nil, &UncertainError{
			Op: fmt.Sprintf("%s %s", method, maskedPath),
			Err: fmt.Errorf("レスポンスの読み取りに失敗しました: %s",
				MaskAPIKey(err.Error())),
		}
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, &RejectedError{
			StatusCode: resp.StatusCode, Method: method, Path: maskedPath,
			err: fmt.Errorf("%w: %s %s", ErrNotFound, method, maskedPath),
		}
	}
	if resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode >= 500 {
		// サーバ側の障害・タイムアウト。書き込みを終えた後に失敗した可能性が
		// あるため、反映されていないとは断定できない(2 回目 高 2)。
		return nil, &UncertainError{
			Op:  fmt.Sprintf("%s %s", method, maskedPath),
			Err: statusError(resp.StatusCode, apiErrorMessage(body)),
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 失敗理由は行ごとの結果としてユーザへ提示する必要があるため、
		// 応答の errors[].message のみを取り出して載せる(本文全体は載せない)
		return nil, &RejectedError{
			StatusCode: resp.StatusCode, Method: method, Path: maskedPath,
			Message: apiErrorMessage(body),
		}
	}
	return body, nil
}

// classifyTransportError は httpDo.Do のエラーを 2 分類する(高 4)。
//
//   - 401 / 403 / 429 は transport がステータスを受信したうえで正規化した
//     エラーであり、サーバはリクエストを反映していない → 確定的拒否。
//   - それ以外(接続失敗・タイムアウト・中断)は応答を受け取れていないだけで、
//     リクエストがサーバへ届いて反映された可能性を否定できない → 成否不明。
func classifyTransportError(method, maskedPath string, err error) error {
	switch {
	case errors.Is(err, ErrUnauthorized),
		errors.Is(err, ErrPermissionDenied),
		errors.Is(err, ErrRateLimitExceeded):
		return &RejectedError{Method: method, Path: maskedPath, err: err}
	default:
		return &UncertainError{Op: fmt.Sprintf("%s %s", method, maskedPath), Err: err}
	}
}

// statusError は HTTP ステータス(と取得できた API メッセージ)から
// 表示用のエラーを作る。
func statusError(status int, message string) error {
	if message == "" {
		return fmt.Errorf("Backlog API がエラーを返しました(HTTP %d)", status)
	}
	return fmt.Errorf("Backlog API がエラーを返しました(HTTP %d): %s", status, message)
}

// parseWriteResult は書き込み応答(2xx)の本文を課題へ変換する。
//
// 解析できない場合に加え、課題 ID が取得できない場合(本文が {} / null、
// id 欠落・0)も成否不明として返す(2 回目 中 1)。ID の無い応答を成功として
// 扱うと、作成された課題を追跡できないまま「完了」と記録してしまい、
// 再送前突合(bulk)でも作成済みかを判定できなくなる。
func parseWriteResult(op string, body []byte) (*Issue, error) {
	issue, err := parseIssue(body)
	if err != nil {
		return nil, &UncertainError{Op: op, Err: err}
	}
	if issue.ID <= 0 {
		return nil, &UncertainError{
			Op:  op,
			Err: errors.New("応答に課題 ID が含まれていません"),
		}
	}
	return &issue, nil
}

// apiErrorMessage はエラー応答の errors[].message を連結して返す
// (解析できない場合は空文字)。apiKey はマスクし、長さも制限する。
func apiErrorMessage(body []byte) string {
	var res struct {
		Errors []struct {
			Message *string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return ""
	}
	msgs := make([]string, 0, len(res.Errors))
	for _, e := range res.Errors {
		if m := strings.TrimSpace(derefString(e.Message)); m != "" {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) == 0 {
		return ""
	}
	joined := MaskAPIKey(strings.Join(msgs, " / "))
	return truncateRunes(joined, maxAPIErrorMessageLen)
}

// truncateRunes は文字数(ルーン単位)で切り詰める。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
