package sync

// refresh.go は課題 1 件だけをその場で最新化する経路(課題詳細ポップアップの
// 「最新の状態を取得」)。プロジェクト全体を同期せずに、開いている 1 件だけを
// Backlog から取り直してローカル DB へ反映する。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

// ErrIssueGone は「Backlog 上に課題が見つからない(404)」ことを表す。
// backlogclient.ErrNotFound を包むため、呼び出し側は従来どおり
// errors.Is(err, backlogclient.ErrNotFound) でも判定できる。
//
// 元の 404 エラー(GET のパスを含む = 課題キーを含む)は包まない。
// 動作ログのマスク方針に合わせ、課題キーをメッセージへ載せないため。
var ErrIssueGone = fmt.Errorf(
	"課題を Backlog 上で確認できませんでした。削除された可能性があります"+
		"(ローカルの内容はそのままです。削除はフル同期で反映されます): %w",
	backlogclient.ErrNotFound)

// maskedRefPlaceholder は伏せた URL・課題キーの置換先。
const maskedRefPlaceholder = "<伏字>"

// urlPattern はエラーメッセージ中の URL・API パスにマッチする。
// GET /issues/:issueIdOrKey のエラーはこの中に課題キーを含む。
// 引用符で囲まれた URL(net/http のエラー)は開き引用符ごと伏せる
// (閉じ引用符だけが URL の末尾に含まれて片方だけ残るのを避けるため)。
var urlPattern = regexp.MustCompile(`"?https?://\S+|/api/v2/\S*`)

// maskIssueRef はエラーメッセージから URL・API パス・課題キーを取り除く。
//
// このエラーはバインディング(appOp)の失敗ログへそのまま記録されるため、
// 課題キーを残せば動作ログのマスク方針(課題キー・課題本文を記録しない)を破る。
// 一方でメッセージ全体を捨てると 5xx・通信エラーの原因を追えなくなるため、
// 「どこを叩いたか」だけを伏せ、「何が起きたか」は残す。
func maskIssueRef(msg, issueKey string) string {
	msg = urlPattern.ReplaceAllString(msg, maskedRefPlaceholder)
	if issueKey == "" {
		return msg
	}
	// URL の外(独自のメッセージ等)に課題キーが現れる場合にも備える
	msg = strings.ReplaceAll(msg, issueKey, maskedRefPlaceholder)
	if escaped := url.PathEscape(issueKey); escaped != issueKey {
		msg = strings.ReplaceAll(msg, escaped, maskedRefPlaceholder)
	}
	return msg
}

// maskedError は課題キー・URL を伏せたメッセージを表示しつつ、
// 元のエラーを Unwrap で返すエラー。
//
// 呼び出し側はメッセージを安全に記録・表示でき、同時に
// errors.Is(err, backlogclient.ErrUnauthorized) 等の分類判定も従来どおり行える。
type maskedError struct {
	msg string
	err error
}

func (e *maskedError) Error() string { return e.msg }
func (e *maskedError) Unwrap() error { return e.err }

const (
	// commentPageSize はコメント取得の 1 ページ件数(API 上限)。
	commentPageSize = backlogclient.MaxPageSize
	// commentFetchLimit は 1 課題あたりのコメント取得上限。
	//
	// 長寿命の課題では数千件になりうる。詳細ポップアップは「最近のやり取りを
	// 確認する」ための場所であり、全件を取ると待ち時間・API 消費・DB 容量が
	// 見合わないため、新しい方から この件数までで打ち切る(打ち切ったことは
	// RefreshResult.Truncated で画面へ伝え、Backlog 側で見るよう案内する)。
	commentFetchLimit = 500
)

// RefreshResult は課題 1 件の最新化の結果(コメント取得の顛末を含む)。
type RefreshResult struct {
	// Comments は保存した(本文のある)コメント件数。
	Comments int
	// HistoryOnly は本文が無い(状態変更等の変更履歴のみの)項目の件数。
	HistoryOnly int
	// Truncated は取得上限に達し、古いコメントを取得しきれていないこと。
	Truncated bool
	// Warnings は部分的な失敗(課題本体は反映できたがコメントは取得できない等)。
	// 課題キー・URL は含めない(動作ログへ記録されるため)。
	Warnings []string
}

func (r *RefreshResult) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// RefreshIssue は課題 1 件を Backlog から取得し直してローカル DB へ反映する。
//
// 反映は同期(フル・差分)とまったく同じ変換(toStoreIssues)と同じ UPSERT を通す。
// 検索索引(FTS)は UPSERT のトリガーが自動的に更新するため、ここでは触らない。
//
// 課題本体に続けてコメントも取得する(通常の同期はコメントに触れない。
// コメントはこの経路だけで更新される)。コメント取得は課題本体の反映に成功した
// 場合にのみ行う。取得できなかった課題のコメントを消したり、別の課題の
// コメントで上書きしたりしないためである。
//
// 同期状態(sync_state)は更新しない。1 件の最新化は「プロジェクト同期の完了」
// ではないため、最終同期時刻を進めるとプロジェクト一覧・同期状態画面の鮮度表示が
// 実態より新しく見え、差分同期の起点も誤って前進してしまう。
//
// 404(ErrNotFound)はローカルを変更せず ErrIssueGone を返す。1 件の 404 で
// 論理削除まで行うと、一時的な権限変更や課題の移動でローカルの課題を失いかねない
// ため、削除の確定は削除検知を伴う同期に委ねる(設計書 3 節と同じ判断)。
func (e *Engine) RefreshIssue(ctx context.Context, projectID int64, issueKey string) (*RefreshResult, error) {
	if projectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	if issueKey == "" {
		return nil, errors.New("課題が指定されていません")
	}

	issue, err := e.api.GetIssue(ctx, issueKey)
	if err != nil {
		if errors.Is(err, backlogclient.ErrNotFound) {
			return nil, ErrIssueGone
		}
		// 401・403・429・5xx・通信エラー。元のメッセージには GET した URL
		//(= 課題キー)が含まれるため、伏せたうえで返す(分類は Unwrap で保つ)
		return nil, &maskedError{
			msg: maskIssueRef(fmt.Sprintf("課題の取得に失敗しました: %v", err), issueKey),
			err: err,
		}
	}
	// 応答が期待どおりでない(空・ID 不明・別プロジェクト)場合は誤った行を作らない
	// (フル同期の存在確認と同じ防御。中 2)。課題キーはメッセージに載せない。
	if issue == nil || issue.ID <= 0 {
		return nil, errors.New("課題を取得できませんでした(応答の内容が想定と異なります)")
	}
	if issue.ProjectID != projectID {
		return nil, errors.New("取得した課題が選択中のプロジェクトのものではないため、更新しませんでした")
	}

	rows := toStoreIssues([]backlogclient.Issue{*issue}, e.nowString())
	if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
		return store.UpsertIssues(ctx, tx, rows)
	}); err != nil {
		return nil, err
	}

	res := &RefreshResult{Warnings: []string{}}
	if err := e.refreshComments(ctx, issue.ID, projectID, issueKey, res); err != nil {
		return nil, err
	}
	return res, nil
}

// refreshComments は課題のコメントを取得してローカルへ全入れ替えする。
//
// 部分失敗の扱い: API 取得の失敗は警告にとどめ、エラーを返さない。
// コメントは付随情報であり、その取得失敗で課題本体の最新化まで「失敗」に
// 見せると利用者の損失が大きいため(既存コメントと取得時刻は据え置き、
// 画面には警告を出して再試行を促す)。
// 一方 DB 書き込みの失敗はエラーとして返す。ローカルの一貫性に関わるうえ、
// 黙って続けると「取得できたのに保存されていない」状態を隠してしまう。
func (e *Engine) refreshComments(ctx context.Context, issueID, projectID int64, issueKey string, res *RefreshResult) error {
	comments, truncated, err := e.fetchIssueComments(ctx, issueKey)
	if err != nil {
		res.warn("コメントを取得できなかったため、コメントは前回取得時のままです: %s",
			maskIssueRef(err.Error(), issueKey))
		return nil
	}

	// 本文が無い項目(状態変更等の変更履歴)は保存しない。表示では必ず除外する
	// ため保存する意味が無く、行数だけが増えるためである。画面が「ほか変更履歴
	// N 件」と添えられるよう、件数だけを取得結果として記録する。
	rows := make([]*store.IssueComment, 0, len(comments))
	historyOnly := 0
	for _, c := range comments {
		if c.Content == "" {
			historyOnly++
			continue
		}
		rows = append(rows, &store.IssueComment{
			ID: c.ID, IssueID: issueID, ProjectID: projectID,
			AuthorName: c.AuthorName, Content: c.Content,
			Created: c.Created, Updated: c.Updated,
		})
	}
	status := store.CommentFetchStatus{
		FetchedAt:   e.nowString(),
		Truncated:   truncated,
		HistoryOnly: historyOnly,
	}
	if err := e.st.WithTx(ctx, func(tx *sql.Tx) error {
		return store.ReplaceIssueComments(ctx, tx, issueID, projectID, rows, status)
	}); err != nil {
		return err
	}
	res.Comments = len(rows)
	res.HistoryOnly = historyOnly
	res.Truncated = truncated
	return nil
}

// fetchIssueComments は新しい順にページングして最大 commentFetchLimit 件を取得する。
//
// コメント一覧 API に offset は無いため、order=desc で取得しながら
// 「取得済みの最小 ID - 1」を次ページの maxId にして遡る(同じ境界を
// 二重に取らないため)。
//
// ページ終端は「戻り件数 < count」で判定する。これは backlogclient.GetIssueComments が
// 応答要素を握り潰さない(id が不正ならエラーにする)契約に依存している。
// 要素を黙って落とす実装にすると、満杯のページを端数ページと誤認して
// 古いページを取り逃したまま「全件取得できた」と判断し、
// ReplaceIssueComments が既存コメントを消してしまう。
func (e *Engine) fetchIssueComments(ctx context.Context, issueKey string) ([]backlogclient.Comment, bool, error) {
	var out []backlogclient.Comment
	maxID := int64(0)
	for len(out) < commentFetchLimit {
		page, err := e.api.GetIssueComments(ctx, issueKey, backlogclient.CommentQuery{
			MaxID: maxID, Order: "desc", Count: commentPageSize,
		})
		if err != nil {
			return nil, false, err
		}
		if len(page) == 0 {
			return out, false, nil
		}
		out = append(out, page...)
		if len(page) < commentPageSize {
			return out, false, nil // 最終ページ
		}
		minID := minCommentID(page)
		if minID <= 1 {
			return out, false, nil // これ以上遡れない(無限ループ防止)
		}
		maxID = minID - 1
	}

	out = out[:commentFetchLimit]
	// 上限ちょうどの課題を「打ち切った」と誤って案内しないよう、
	// さらに古いコメントが実在するかを 1 件だけ問い合わせて確定する。
	// 追加リクエストは上限に達した場合の 1 回だけなので、
	// 誤案内を避ける価値に見合う(通常の課題では発生しない)。
	oldest := minCommentID(out)
	if oldest <= 1 {
		return out, false, nil
	}
	probe, err := e.api.GetIssueComments(ctx, issueKey, backlogclient.CommentQuery{
		MaxID: oldest - 1, Order: "desc", Count: 1,
	})
	if err != nil {
		// 確認できない場合は「打ち切ったかもしれない」側へ倒す。取得済みの
		// コメントは有効なので失敗にはせず、Backlog 側で確認するよう案内する
		// (過小警告で続きの存在に気づけないより害が小さい)。
		return out, true, nil
	}
	return out, len(probe) > 0, nil
}

// minCommentID はコメント群の最小 ID を返す(空なら 0)。
// 応答が ID 降順に並んでいる保証は無いため、末尾ではなく最小値を採る。
func minCommentID(comments []backlogclient.Comment) int64 {
	if len(comments) == 0 {
		return 0
	}
	min := comments[0].ID
	for _, c := range comments {
		if c.ID < min {
			min = c.ID
		}
	}
	return min
}
