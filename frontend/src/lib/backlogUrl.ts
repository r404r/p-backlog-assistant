/**
 * Backlog の Web 画面の URL を組み立てるヘルパ。
 *
 * 置き場所の判断: format.ts は「画面に出す値の整形」を集めたモジュールで、
 * ここで扱うのは整形ではなく Backlog の URL 規約(スペース URL + /view/ + 課題キー)
 * というドメイン知識のため、別ファイルに分けた。URL 規約が増えた場合
 * (プロジェクトのボード画面等)も、この 1 ファイルに集約できる。
 *
 * すべて副作用のない純粋関数。テストは backlogUrl.test.ts。
 */

/**
 * 課題ページの URL を返す(例: https://example.backlog.jp/view/SAMPLE-1)。
 *
 * スペース URL は利用者が入力した値(プロファイルの spaceUrl)なので、
 * 末尾のスラッシュ・前後の空白を正規化してから連結する。
 * どちらかが空(空白のみを含む)の場合は、壊れた URL を作らずに空文字を返す
 * — 呼び出し側はこれを「URL を組み立てられない」= 機能を出さない合図として扱う。
 *
 * 課題キーは英数字・アンダースコア・ハイフンのみ(Backlog の仕様。
 * いずれも URL の非予約文字)のため URL エスケープはしない。
 */
export function issueUrl(spaceUrl: string, issueKey: string): string {
  const base = (spaceUrl ?? '').trim().replace(/\/+$/, '')
  const key = (issueKey ?? '').trim()
  if (!base || !key) return ''
  return `${base}/view/${key}`
}
