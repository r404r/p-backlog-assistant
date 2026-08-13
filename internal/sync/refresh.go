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

// RefreshIssue は課題 1 件を Backlog から取得し直してローカル DB へ反映する。
//
// 反映は同期(フル・差分)とまったく同じ変換(toStoreIssues)と同じ UPSERT を通す。
// 検索索引(FTS)は UPSERT のトリガーが自動的に更新するため、ここでは触らない。
//
// 同期状態(sync_state)は更新しない。1 件の最新化は「プロジェクト同期の完了」
// ではないため、最終同期時刻を進めるとプロジェクト一覧・同期状態画面の鮮度表示が
// 実態より新しく見え、差分同期の起点も誤って前進してしまう。
//
// 404(ErrNotFound)はローカルを変更せず ErrIssueGone を返す。1 件の 404 で
// 論理削除まで行うと、一時的な権限変更や課題の移動でローカルの課題を失いかねない
// ため、削除の確定は削除検知を伴う同期に委ねる(設計書 3 節と同じ判断)。
func (e *Engine) RefreshIssue(ctx context.Context, projectID int64, issueKey string) error {
	if projectID <= 0 {
		return errors.New("プロジェクトが指定されていません")
	}
	if issueKey == "" {
		return errors.New("課題が指定されていません")
	}

	issue, err := e.api.GetIssue(ctx, issueKey)
	if err != nil {
		if errors.Is(err, backlogclient.ErrNotFound) {
			return ErrIssueGone
		}
		// 401・403・429・5xx・通信エラー。元のメッセージには GET した URL
		//(= 課題キー)が含まれるため、伏せたうえで返す(分類は Unwrap で保つ)
		return &maskedError{
			msg: maskIssueRef(fmt.Sprintf("課題の取得に失敗しました: %v", err), issueKey),
			err: err,
		}
	}
	// 応答が期待どおりでない(空・ID 不明・別プロジェクト)場合は誤った行を作らない
	// (フル同期の存在確認と同じ防御。中 2)。課題キーはメッセージに載せない。
	if issue == nil || issue.ID <= 0 {
		return errors.New("課題を取得できませんでした(応答の内容が想定と異なります)")
	}
	if issue.ProjectID != projectID {
		return errors.New("取得した課題が選択中のプロジェクトのものではないため、更新しませんでした")
	}

	rows := toStoreIssues([]backlogclient.Issue{*issue}, e.nowString())
	return e.st.WithTx(ctx, func(tx *sql.Tx) error {
		return store.UpsertIssues(ctx, tx, rows)
	})
}
