/**
 * 課題の検索条件フォームの入力値と、それを検索条件(IssueQuery)へ変換する処理。
 *
 * 課題抽出(IssuesView)の検索・Excel 出力と、一括更新のテンプレート出力
 * (BulkUpdateView)が同じ条件を扱うため、定義と変換規則をここに 1 つだけ置く。
 * 画面ごとに写しを持つと、片方だけ条件が増えた・空文字の扱いが違うといった
 * 食い違いが生まれるため(Go 側 store.IssueFilter が受け取る形はどちらも同じ)。
 *
 * カスタム属性の絞り込み(customFieldFilters)と取得上限(limit)は
 * 画面ごとに扱いが異なるため、ここでは詰めない(呼び出し側で足す)。
 */
import type { IssueQuery } from './backend'

/** 検索条件フォームの入力値(すべて文字列。未入力は空文字) */
export interface IssueConditions {
  /** キーワード(課題キー + 件名 + 詳細の部分一致。空白区切りで複数語) */
  keyword: string
  /** 複数キーワードの連結方法(既定は AND = すべて含む) */
  keywordMode: 'and' | 'or'
  /** 更新日の下限・上限(YYYY-MM-DD) */
  updatedFrom: string
  updatedTo: string
  /** 作成日の下限・上限(YYYY-MM-DD) */
  createdFrom: string
  createdTo: string
  /** 状態名(完全一致。空ならすべて) */
  statusName: string
  /** 担当者名(完全一致。空ならすべて) */
  assigneeName: string
}

/** 未入力の条件を返す(画面の初期値・条件クリアの基準) */
export function newIssueConditions(): IssueConditions {
  return {
    keyword: '',
    keywordMode: 'and',
    updatedFrom: '',
    updatedTo: '',
    createdFrom: '',
    createdTo: '',
    statusName: '',
    assigneeName: '',
  }
}

/**
 * 条件を初期値へ戻す(reactive で包んだ同一オブジェクトを保つため in-place で書く)。
 * 新しいオブジェクトを代入すると、reactive の参照を持つ入力欄との結び付きが切れる。
 */
export function resetIssueConditions(cond: IssueConditions): void {
  Object.assign(cond, newIssueConditions())
}

/**
 * 現在の条件を IssueQuery に変換する(空文字の条件は送らない)。
 *
 * 空の条件を送っても Go 側(store.IssueFilter)では無視されるが、
 * 「指定した条件だけが載る」形にしておくと、ログ・デバッグで実際の絞り込みが分かる。
 */
export function buildIssueQuery(projectId: number, cond: IssueConditions): IssueQuery {
  const q: IssueQuery = { projectId }
  const keyword = cond.keyword.trim()
  if (keyword) {
    q.keyword = keyword
    // キーワードが空なら連結方法は意味を持たないため送らない
    q.keywordMode = cond.keywordMode
  }
  if (cond.updatedFrom) q.updatedFrom = cond.updatedFrom
  if (cond.updatedTo) q.updatedTo = cond.updatedTo
  if (cond.createdFrom) q.createdFrom = cond.createdFrom
  if (cond.createdTo) q.createdTo = cond.createdTo
  if (cond.statusName) q.statusName = cond.statusName
  if (cond.assigneeName) q.assigneeName = cond.assigneeName
  return q
}
