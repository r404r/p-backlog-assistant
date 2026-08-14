/**
 * 課題抽出(IssuesView)の検索結果ページネーション。
 *
 * 画面は 1 ページぶん(PAGE_SIZE 件)だけを Go 側から取得し、ページ移動のたびに
 * offset を付け替えて取り直す(サーバ側 OFFSET ページング)。
 * ページ計算(総ページ数・クランプ・表示範囲)と状態遷移(要求中 / 確定済みの分離・
 * 世代ガード・範囲外ページのクランプ再取得・結果の stale 化)をここへ集約し、
 * issuePagination.test.ts で検証する。ページャの見た目・クリックは GUI のため
 * TDD 例外(手動確認)。
 *
 * 状態遷移の要点(設計: docs/research の「検索結果ページネーション」):
 * - 検索・ページ移動は**成功時のみ** page / rows / total / unverifiable /
 *   スナップショットをまとめて確定する。失敗時は表示中の結果をそのまま維持する
 *   (旧結果 + 新条件でのページ移動という混在を防ぐため)。
 * - 世代番号を検索・ページ移動・クランプ再取得で共用し、古い応答は破棄する。
 * - 検索集合を変え得る書き込み(課題同期の完了・課題詳細の再取得)が起きたら
 *   結果を stale 扱いにし、進行中の応答も失効させる。解除は再検索の成功時のみ。
 */
import { computed, shallowRef, type ComputedRef, type Ref, type ShallowRef } from 'vue'
import type { IssueQuery, IssueRow, IssueSearchResult } from './backend'
import { errorMessage } from './format'

// ---------------------------------------------------------------------------
// ページ計算(純粋関数)
// ---------------------------------------------------------------------------

/**
 * 総ページ数(= ceil(総件数 / ページサイズ))。
 * 0 件・不正な値は 0 ページ(画面はページャを出さない)。
 */
export function totalPageCount(total: number, pageSize: number): number {
  if (!Number.isFinite(total) || total <= 0) return 0
  if (!Number.isFinite(pageSize) || pageSize <= 0) return 0
  return Math.ceil(total / pageSize)
}

/**
 * ページ番号を 1 〜 最終ページの範囲へ丸める(ページ番号の直接入力・範囲外ページ対策)。
 * 0 件でも「1 ページ目」を返す(0 ページは存在しないため)。
 */
export function clampPage(page: number, total: number, pageSize: number): number {
  const last = Math.max(1, totalPageCount(total, pageSize))
  if (!Number.isFinite(page)) return 1
  const n = Math.trunc(page)
  if (n < 1) return 1
  if (n > last) return last
  return n
}

/** ページ番号(1 始まり)を取得開始位置(0 始まり)へ変換する */
export function pageOffset(page: number, pageSize: number): number {
  if (!Number.isFinite(page) || !Number.isFinite(pageSize)) return 0
  const n = Math.trunc(page)
  if (n <= 1) return 0
  return (n - 1) * Math.max(0, Math.trunc(pageSize))
}

/**
 * 「x〜y 件目」の表示範囲。
 * 終端は実際に取得できた行数から求める(最終ページの半端な件数に合わせるため)。
 * 0 行のときは範囲を持たない(0〜0)。
 */
export function pageRange(
  page: number,
  pageSize: number,
  rowCount: number,
): { start: number; end: number } {
  if (rowCount <= 0) return { start: 0, end: 0 }
  const start = pageOffset(page, pageSize) + 1
  return { start, end: start + rowCount - 1 }
}

/**
 * 検索条件へページング(limit / offset)を載せた新しい条件を返す。
 *
 * 元の条件は書き換えない。Excel 出力・テンプレート出力は同じ条件を limit / offset
 * 無しで使うため、スナップショットの条件をここで汚してはいけない。
 */
export function buildPagedQuery(query: IssueQuery, page: number, pageSize: number): IssueQuery {
  return { ...query, limit: pageSize, offset: pageOffset(page, pageSize) }
}

// ---------------------------------------------------------------------------
// ページング状態(composable)
// ---------------------------------------------------------------------------

/**
 * 検索スナップショット。
 *
 * 検索を実行した時点の条件(offset を含まない)と表示列を保持し、
 * ページ移動ではこれに offset だけを付け替えて取り直す。
 * 検索後にフォーム・列選択を変えてもスナップショットは変わらない
 * (表示中の行と見出しを次の検索まで揃えておくため)。
 */
export interface IssueSearchSnapshot<C> {
  /** offset を含まない検索条件 */
  query: IssueQuery
  /** 検索した時点で選ばれていた表示列(画面では ExportColumn) */
  columns: C[]
}

export interface IssuePaginationOptions<C> {
  /** 1 ページの件数 */
  pageSize: number
  /** 検索を実行する(limit / offset を載せた条件で呼ばれる) */
  fetch: (query: IssueQuery, columns: C[]) => Promise<IssueSearchResult>
}

export interface IssuePagination<C> {
  /** 確定済みページの行 */
  rows: ShallowRef<IssueRow[]>
  /** 条件に一致する総件数(表示中のページの件数ではない) */
  total: Ref<number>
  /** カスタム属性条件を判定できなかった課題の件数 */
  unverifiable: Ref<number>
  /** 確定済みページ(1 始まり) */
  page: Ref<number>
  /** 検索・ページ取得の実行中か */
  searching: Ref<boolean>
  /** 一度でも検索を確定したか(結果セクションの表示条件) */
  searched: Ref<boolean>
  /** 直近の失敗の説明(空 = 正常) */
  error: Ref<string>
  /**
   * 表示中の結果が古くなった可能性があるか。
   * 検索集合を変え得る書き込みの後に立ち、再検索の成功でのみ下りる。
   */
  stale: Ref<boolean>
  /** 確定済みの検索スナップショット(未検索は null) */
  snapshot: ShallowRef<IssueSearchSnapshot<C> | null>
  /** 総ページ数(0 = 結果なし) */
  totalPages: ComputedRef<number>
  /** 表示範囲の先頭(1 始まり。0 行なら 0) */
  rangeStart: ComputedRef<number>
  /** 表示範囲の末尾(0 行なら 0) */
  rangeEnd: ComputedRef<number>
  /** 前のページがあるか */
  hasPrev: ComputedRef<boolean>
  /** 次のページがあるか */
  hasNext: ComputedRef<boolean>
  /** 1 ページ目から検索し直す(成功時にスナップショットごと確定する) */
  search: (snapshot: IssueSearchSnapshot<C>) => Promise<void>
  /** 確定済みスナップショットで指定ページを取得する(範囲外はクランプする) */
  goToPage: (page: number) => Promise<void>
  /** 表示中の結果を stale にする(進行中の応答も失効させる) */
  markStale: () => void
  /** 表示中の結果が指定プロジェクトのものであれば stale にする */
  markStaleForProject: (projectId: number) => void
  /** 進行中の要求を失効させる(表示はそのまま) */
  invalidate: () => void
  /** 結果・ページ・スナップショット・stale をすべて片付ける(プロジェクト切替) */
  reset: () => void
}

export function useIssuePagination<C>(options: IssuePaginationOptions<C>): IssuePagination<C> {
  const pageSize = options.pageSize

  const rows = shallowRef<IssueRow[]>([])
  const total = shallowRef(0)
  const unverifiable = shallowRef(0)
  const page = shallowRef(1)
  const searching = shallowRef(false)
  const searched = shallowRef(false)
  const error = shallowRef('')
  const stale = shallowRef(false)
  const snapshot = shallowRef<IssueSearchSnapshot<C> | null>(null)

  /**
   * 要求の世代番号。検索・ページ移動・クランプ再取得で共用し、
   * プロジェクト切替(invalidate / reset)と stale 化で進める。
   * 応答の到着時にこの番号が変わっていたら、その応答は反映しない。
   */
  let requestSeq = 0

  const totalPages = computed(() => totalPageCount(total.value, pageSize))
  const range = computed(() => pageRange(page.value, pageSize, rows.value.length))
  const rangeStart = computed(() => range.value.start)
  const rangeEnd = computed(() => range.value.end)
  const hasPrev = computed(() => page.value > 1)
  const hasNext = computed(() => page.value < totalPages.value)

  /** 応答を確定する(成功時のみ呼ぶ。結果一式を原子的に入れ替える) */
  function confirm(
    res: IssueSearchResult,
    confirmedPage: number,
    snap: IssueSearchSnapshot<C>,
    clearStale: boolean,
  ): void {
    rows.value = res.rows
    total.value = res.total
    unverifiable.value = res.unverifiable
    page.value = confirmedPage
    snapshot.value = snap
    searched.value = true
    error.value = ''
    // stale の解除は再検索の成功時のみ(ページ移動では解除しない)
    if (clearStale) stale.value = false
  }

  /**
   * 1 回の取得(検索 / ページ移動)を実行する。
   *
   * 範囲外ページ(取得結果が 0 行・offset > 0・総件数 > 0)は、ページ間で
   * データが減った場合なので最終ページへクランプして **1 回だけ** 取り直す
   * (無限ループを避けるため再試行は 1 回。再取得も同じ世代に属させる)。
   */
  async function run(
    snap: IssueSearchSnapshot<C>,
    requestedPage: number,
    isSearch: boolean,
  ): Promise<void> {
    const seq = ++requestSeq
    searching.value = true
    error.value = ''
    try {
      let target = requestedPage
      let res = await options.fetch(buildPagedQuery(snap.query, target, pageSize), snap.columns)
      if (seq !== requestSeq) return
      if (res.rows.length === 0 && pageOffset(target, pageSize) > 0 && res.total > 0) {
        target = clampPage(totalPageCount(res.total, pageSize), res.total, pageSize)
        res = await options.fetch(buildPagedQuery(snap.query, target, pageSize), snap.columns)
        if (seq !== requestSeq) return
      }
      // 総件数が 0 まで減った場合はクランプの対象外。応答をそのまま確定し、
      // 表示は 1 ページ目(空の結果)へ戻す
      confirm(res, res.total > 0 ? target : 1, snap, isSearch)
    } catch (e) {
      if (seq !== requestSeq) return
      error.value = isSearch
        ? `検索に失敗しました: ${errorMessage(e)}`
        : `ページの取得に失敗しました: ${errorMessage(e)}`
    } finally {
      // 実行中フラグは失効済みの応答でも必ず下ろす(多重起動は下の guard で防いでいる
      // ため、ここで下ろさないと次の検索を始められなくなる)
      searching.value = false
    }
  }

  async function search(snap: IssueSearchSnapshot<C>): Promise<void> {
    // 多重起動しない(呼び出し側のボタンも無効化しているが、判定を UI に任せない)
    if (searching.value) return
    await run(snap, 1, true)
  }

  async function goToPage(n: number): Promise<void> {
    const snap = snapshot.value
    // 未検索(スナップショット未確定)・取得中・stale 中はページを動かさない
    if (!snap || searching.value || stale.value) return
    const target = clampPage(n, total.value, pageSize)
    // クランプの結果が表示中のページなら取り直さない(同じ内容の再取得を避ける)
    if (target === page.value) return
    await run(snap, target, false)
  }

  function markStale(): void {
    // 確定済みの結果が無ければ「古くなる」対象も無い
    if (!searched.value) return
    stale.value = true
    // stale 直前に開始した応答が page / rows / total を上書きしないよう失効させる
    requestSeq++
  }

  function markStaleForProject(projectId: number): void {
    if (!searched.value) return
    if (snapshot.value?.query.projectId !== projectId) return
    markStale()
  }

  function invalidate(): void {
    requestSeq++
  }

  function reset(): void {
    // 実行中フラグは下ろさない(進行中の応答が finally で下ろす。invalidate と同じ理由)
    requestSeq++
    rows.value = []
    total.value = 0
    unverifiable.value = 0
    page.value = 1
    searched.value = false
    error.value = ''
    stale.value = false
    snapshot.value = null
  }

  return {
    rows,
    total,
    unverifiable,
    page,
    searching,
    searched,
    error,
    stale,
    snapshot,
    totalPages,
    rangeStart,
    rangeEnd,
    hasPrev,
    hasNext,
    search,
    goToPage,
    markStale,
    markStaleForProject,
    invalidate,
    reset,
  }
}
