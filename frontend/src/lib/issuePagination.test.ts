/**
 * issuePagination.ts のテスト(検索結果ページネーション)。
 *
 * ページ計算の純粋関数と、ページング状態遷移(要求中 / 確定済みの分離・
 * 世代ガード・範囲外ページのクランプ・stale 化)を検証する。
 * ページャの見た目・クリックは GUI のため対象外(手動確認)。
 */
import { describe, expect, it, vi } from 'vitest'
import type { IssueQuery, IssueRow, IssueSearchResult } from './backend'
import {
  buildPagedQuery,
  clampPage,
  pageOffset,
  pageRange,
  totalPageCount,
  useIssuePagination,
} from './issuePagination'

/** テストで使うページサイズ(実際の画面は 200 件だが、件数を書きやすい値にする) */
const PAGE_SIZE = 10

/** 一覧の 1 行(検証に使うのは課題キーだけなので、他は空で埋める) */
function row(issueKey: string): IssueRow {
  return {
    issueKey,
    summary: '',
    statusName: '',
    assigneeName: '',
    issueTypeName: '',
    priorityName: '',
    created: '',
    updated: '',
    dueDate: '',
    customFields: {},
  }
}

/** 連番の課題キーを持つ n 行を作る */
function rowsOf(n: number, prefix = 'SAMPLE'): IssueRow[] {
  return Array.from({ length: n }, (_, i) => row(`${prefix}-${i + 1}`))
}

/** 検索結果を組み立てる */
function result(rows: IssueRow[], total: number, unverifiable = 0): IssueSearchResult {
  return { rows, total, unverifiable }
}

/** 外から解決・拒否できる Promise(応答の到着順を testable にするため) */
interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** マイクロタスクを吐き出す(応答の continuation を進めてから次の検証を行う) */
function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

/** テストで使う表示列(実際の画面は ExportColumn だが、composable は列の型に依存しない) */
interface Column {
  key: string
  label: string
}

/** バックエンド呼び出し 1 回分の記録 */
interface FetchCall {
  query: IssueQuery
  columns: Column[]
  deferred: Deferred<IssueSearchResult>
}

/** 呼び出しを記録し、応答の到着を外から制御できる fetch を作る */
function createFetcher() {
  const calls: FetchCall[] = []
  const fetch = (query: IssueQuery, columns: Column[]) => {
    const d = deferred<IssueSearchResult>()
    calls.push({ query, columns, deferred: d })
    return d.promise
  }
  return { calls, fetch }
}

/** 検索スナップショット(offset を含まない条件 + 表示列) */
function snapshot(projectId = 7, columns: Column[] = []): { query: IssueQuery; columns: Column[] } {
  return { query: { projectId }, columns }
}

// ---------------------------------------------------------------------------
// ページ計算(純粋関数)
// ---------------------------------------------------------------------------

describe('totalPageCount', () => {
  it('総件数をページサイズで切り上げる', () => {
    expect(totalPageCount(0, 200)).toBe(0)
    expect(totalPageCount(1, 200)).toBe(1)
    expect(totalPageCount(200, 200)).toBe(1)
    expect(totalPageCount(201, 200)).toBe(2)
    expect(totalPageCount(1000, 200)).toBe(5)
    expect(totalPageCount(1001, 200)).toBe(6)
  })

  it('不正な値は 0 ページ(ページャを出さない)', () => {
    expect(totalPageCount(-1, 200)).toBe(0)
    expect(totalPageCount(Number.NaN, 200)).toBe(0)
    expect(totalPageCount(100, 0)).toBe(0)
  })
})

describe('clampPage', () => {
  it('1 〜 最終ページの範囲へ丸める', () => {
    expect(clampPage(0, 1000, 200)).toBe(1)
    expect(clampPage(-5, 1000, 200)).toBe(1)
    expect(clampPage(3, 1000, 200)).toBe(3)
    expect(clampPage(5, 1000, 200)).toBe(5)
    expect(clampPage(6, 1000, 200)).toBe(5)
    expect(clampPage(999, 1000, 200)).toBe(5)
  })

  it('総件数が 0 でも 1 ページ目にする(0 ページは存在しない)', () => {
    expect(clampPage(3, 0, 200)).toBe(1)
  })

  it('小数・不正な値は整数のページ番号へ丸める', () => {
    expect(clampPage(2.7, 1000, 200)).toBe(2)
    expect(clampPage(Number.NaN, 1000, 200)).toBe(1)
  })
})

describe('pageOffset', () => {
  it('ページ番号を 0 始まりの offset へ変換する', () => {
    expect(pageOffset(1, 200)).toBe(0)
    expect(pageOffset(2, 200)).toBe(200)
    expect(pageOffset(5, 200)).toBe(800)
  })

  it('1 未満・不正な値は 0(先頭)へ丸める', () => {
    expect(pageOffset(0, 200)).toBe(0)
    expect(pageOffset(-3, 200)).toBe(0)
    expect(pageOffset(Number.NaN, 200)).toBe(0)
  })
})

describe('pageRange', () => {
  it('「x〜y 件目」の範囲を返す', () => {
    expect(pageRange(1, 200, 200)).toEqual({ start: 1, end: 200 })
    expect(pageRange(2, 200, 200)).toEqual({ start: 201, end: 400 })
    // 最終ページの半端な件数
    expect(pageRange(3, 200, 45)).toEqual({ start: 401, end: 445 })
  })

  it('0 行なら範囲を持たない', () => {
    expect(pageRange(1, 200, 0)).toEqual({ start: 0, end: 0 })
  })
})

describe('buildPagedQuery', () => {
  it('検索条件に limit と offset を載せる', () => {
    expect(buildPagedQuery({ projectId: 7, keyword: 'ログイン' }, 3, 200)).toEqual({
      projectId: 7,
      keyword: 'ログイン',
      limit: 200,
      offset: 400,
    })
  })

  it('1 ページ目の offset は 0', () => {
    expect(buildPagedQuery({ projectId: 7 }, 1, 200)).toEqual({
      projectId: 7,
      limit: 200,
      offset: 0,
    })
  })

  it('元の条件は書き換えない(出力経路の条件に offset を混ぜないため)', () => {
    const query: IssueQuery = { projectId: 7 }
    buildPagedQuery(query, 3, 200)
    expect(query).toEqual({ projectId: 7 })
  })
})

// ---------------------------------------------------------------------------
// 状態遷移(composable)
// ---------------------------------------------------------------------------

describe('useIssuePagination の検索', () => {
  it('成功したら結果・ページ・スナップショットをまとめて確定する', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const snap = snapshot(7, [{ key: 'cf_1', label: '契約種別' }])

    const done = p.search(snap)
    expect(p.searching.value).toBe(true)
    f.calls[0].deferred.resolve(result(rowsOf(10), 25, 3))
    await done

    expect(f.calls[0].query).toEqual({ projectId: 7, limit: PAGE_SIZE, offset: 0 })
    expect(f.calls[0].columns).toEqual([{ key: 'cf_1', label: '契約種別' }])
    expect(p.searching.value).toBe(false)
    expect(p.searched.value).toBe(true)
    expect(p.page.value).toBe(1)
    expect(p.rows.value).toHaveLength(10)
    expect(p.total.value).toBe(25)
    expect(p.unverifiable.value).toBe(3)
    expect(p.totalPages.value).toBe(3)
    expect(p.rangeStart.value).toBe(1)
    expect(p.rangeEnd.value).toBe(10)
    expect(p.snapshot.value).toEqual(snap)
    expect(p.error.value).toBe('')
  })

  it('検索は常に 1 ページ目から取得する', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    let done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done

    done = p.goToPage(3)
    f.calls[1].deferred.resolve(result(rowsOf(10), 50))
    await done
    expect(p.page.value).toBe(3)

    // 再検索は 1 ページ目へ戻る
    done = p.search(snapshot())
    f.calls[2].deferred.resolve(result(rowsOf(10), 50))
    await done

    expect(f.calls[2].query.offset).toBe(0)
    expect(p.page.value).toBe(1)
  })

  it('失敗したら旧状態(結果・ページ・スナップショット)を維持する', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const first = snapshot(7, [{ key: 'cf_1', label: '契約種別' }])

    let done = p.search(first)
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done
    done = p.goToPage(2)
    f.calls[1].deferred.resolve(result(rowsOf(10, 'PAGE2'), 50))
    await done

    // 条件を変えた検索が失敗する
    done = p.search(snapshot(7, [{ key: 'cf_2', label: '担当部署' }]))
    f.calls[2].deferred.reject(new Error('DB が壊れています'))
    await done

    expect(p.error.value).toBe('検索に失敗しました: DB が壊れています')
    expect(p.page.value).toBe(2)
    expect(p.rows.value[0].issueKey).toBe('PAGE2-1')
    expect(p.total.value).toBe(50)
    // 失敗した検索のスナップショットは確定しない(旧結果 + 新条件の混在を防ぐ)
    expect(p.snapshot.value).toEqual(first)
  })

  it('検索に失敗した後のページ移動は確定済みスナップショットを使う', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const first = snapshot(7, [{ key: 'cf_1', label: '契約種別' }])

    let done = p.search(first)
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done

    done = p.search({ query: { projectId: 7, keyword: '新しい条件' }, columns: [] })
    f.calls[1].deferred.reject(new Error('失敗'))
    await done

    done = p.goToPage(2)
    f.calls[2].deferred.resolve(result(rowsOf(10), 50))
    await done

    expect(f.calls[2].query).toEqual({ projectId: 7, limit: PAGE_SIZE, offset: PAGE_SIZE })
    expect(f.calls[2].columns).toEqual(first.columns)
  })

  it('実行中は多重に検索しない', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    const done = p.search(snapshot())
    await p.search(snapshot())
    expect(f.calls).toHaveLength(1)

    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done
  })
})

describe('useIssuePagination のページ移動', () => {
  /** 1 ページ目を確定させた状態を作る(total 件・PAGE_SIZE 行) */
  async function searched(total: number, f: ReturnType<typeof createFetcher>) {
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(Math.min(PAGE_SIZE, total)), total))
    await done
    return p
  }

  it('成功時のみページと行をまとめて更新する', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(3)
    // 応答が届くまでは確定済みページを動かさない
    expect(p.page.value).toBe(1)
    f.calls[1].deferred.resolve(result(rowsOf(10, 'PAGE3'), 50))
    await done

    expect(f.calls[1].query.offset).toBe(20)
    expect(p.page.value).toBe(3)
    expect(p.rows.value[0].issueKey).toBe('PAGE3-1')
    expect(p.rangeStart.value).toBe(21)
    expect(p.rangeEnd.value).toBe(30)
  })

  it('失敗したらページと行を維持してエラーを出す', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(3)
    f.calls[1].deferred.reject(new Error('読み取り失敗'))
    await done

    expect(p.error.value).toBe('ページの取得に失敗しました: 読み取り失敗')
    expect(p.page.value).toBe(1)
    expect(p.rows.value[0].issueKey).toBe('SAMPLE-1')
  })

  it('範囲外のページ番号は最終ページへクランプする(ページ番号の直接入力)', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(999)
    f.calls[1].deferred.resolve(result(rowsOf(10), 50))
    await done

    expect(f.calls[1].query.offset).toBe(40)
    expect(p.page.value).toBe(5)
  })

  it('確定済みページと同じページなら取得し直さない', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    await p.goToPage(1)
    await p.goToPage(0)

    expect(f.calls).toHaveLength(1)
  })

  it('検索前(スナップショット未確定)はページ移動しない', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    await p.goToPage(2)

    expect(f.calls).toHaveLength(0)
  })

  it('範囲外ページ(データ減少)は最終ページへ 1 回だけ再取得する', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(5)
    // ページ移動の直前にデータが減り、5 ページ目は空になった(総件数 25 = 3 ページ)
    f.calls[1].deferred.resolve(result([], 25))
    await flush()

    // 最終ページ(3 ページ目)へクランプして再取得する
    expect(f.calls).toHaveLength(3)
    expect(f.calls[1].query.offset).toBe(40)
    expect(f.calls[2].query.offset).toBe(20)

    // 再取得も 0 行だった場合でも、もう一度クランプし直さない(無限ループ防止)
    f.calls[2].deferred.resolve(result([], 25))
    await done

    expect(f.calls).toHaveLength(3)
    expect(p.page.value).toBe(3)
  })

  it('クランプ再取得の結果を確定する', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(5)
    f.calls[1].deferred.resolve(result([], 25))
    await flush()
    f.calls[2].deferred.resolve(result(rowsOf(5, 'LAST'), 25))
    await done

    expect(f.calls).toHaveLength(3)
    expect(f.calls[2].query.offset).toBe(20)
    expect(p.page.value).toBe(3)
    expect(p.rows.value[0].issueKey).toBe('LAST-1')
    expect(p.total.value).toBe(25)
  })

  it('総件数が 0 まで減ったらクランプせず 1 ページ目の空結果を確定する', async () => {
    const f = createFetcher()
    const p = await searched(50, f)

    const done = p.goToPage(3)
    f.calls[1].deferred.resolve(result([], 0))
    await done

    expect(f.calls).toHaveLength(2)
    expect(p.page.value).toBe(1)
    expect(p.rows.value).toEqual([])
    expect(p.total.value).toBe(0)
    expect(p.totalPages.value).toBe(0)
  })
})

describe('useIssuePagination の世代ガード', () => {
  it('失効させた要求の応答は反映しない', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    const done = p.search(snapshot())
    p.invalidate()
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done

    expect(p.searched.value).toBe(false)
    expect(p.rows.value).toEqual([])
    expect(p.snapshot.value).toBeNull()
    // 実行中フラグは応答の到着で必ず下ろす(次の検索を始められるようにする)
    expect(p.searching.value).toBe(false)
  })

  it('失効させた要求の失敗もエラー表示しない', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    const done = p.search(snapshot())
    p.invalidate()
    f.calls[0].deferred.reject(new Error('前のプロジェクトの失敗'))
    await done

    expect(p.error.value).toBe('')
  })

  it('reset は結果・ページ・スナップショット・stale をすべて片付ける', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    let done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done
    done = p.goToPage(2)
    f.calls[1].deferred.resolve(result(rowsOf(10), 50))
    await done
    p.markStale()

    p.reset()

    expect(p.rows.value).toEqual([])
    expect(p.total.value).toBe(0)
    expect(p.unverifiable.value).toBe(0)
    expect(p.page.value).toBe(1)
    expect(p.searched.value).toBe(false)
    expect(p.stale.value).toBe(false)
    expect(p.snapshot.value).toBeNull()
    expect(p.error.value).toBe('')
  })
})

describe('useIssuePagination の stale 化', () => {
  /** 1 ページ目を確定させた composable を返す */
  async function searchedPagination(f: ReturnType<typeof createFetcher>, projectId = 7) {
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const done = p.search(snapshot(projectId))
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done
    return p
  }

  it('確定済みの結果があるときだけ stale にする', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    p.markStale()
    expect(p.stale.value).toBe(false)

    const done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done
    p.markStale()

    expect(p.stale.value).toBe(true)
    // 結果自体は残す(再検索を促すだけ)
    expect(p.rows.value).toHaveLength(10)
  })

  it('stale 中はページ移動しない', async () => {
    const f = createFetcher()
    const p = await searchedPagination(f)
    p.markStale()

    await p.goToPage(2)

    expect(f.calls).toHaveLength(1)
  })

  it('ページ取得中に stale 化したら進行中の応答を破棄する', async () => {
    const f = createFetcher()
    const p = await searchedPagination(f)

    const done = p.goToPage(3)
    p.markStale()
    f.calls[1].deferred.resolve(result(rowsOf(10, 'PAGE3'), 50))
    await done

    expect(p.page.value).toBe(1)
    expect(p.rows.value[0].issueKey).toBe('SAMPLE-1')
    expect(p.stale.value).toBe(true)
  })

  it('再検索の成功でのみ解除する(失敗では解除しない)', async () => {
    const f = createFetcher()
    const p = await searchedPagination(f)
    p.markStale()

    let done = p.search(snapshot())
    f.calls[1].deferred.reject(new Error('失敗'))
    await done
    expect(p.stale.value).toBe(true)

    done = p.search(snapshot())
    f.calls[2].deferred.resolve(result(rowsOf(10), 50))
    await done
    expect(p.stale.value).toBe(false)
  })

  it('起点プロジェクトが一致する場合だけ stale にする(詳細更新の完了)', async () => {
    const f = createFetcher()
    const p = await searchedPagination(f, 7)

    // プロジェクト 8 で始めた更新の完了(切替後の新しい結果は stale にしない)
    p.markStaleForProject(8)
    expect(p.stale.value).toBe(false)

    // 表示中の結果と同じプロジェクトなら、モーダルを閉じた後の完了でも stale
    p.markStaleForProject(7)
    expect(p.stale.value).toBe(true)
  })

  it('検索結果が無ければ起点プロジェクトが一致しても何もしない', () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    p.markStaleForProject(7)

    expect(p.stale.value).toBe(false)
  })
})

describe('useIssuePagination のページャ状態', () => {
  it('前後のページの有無を返す', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    let done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(10), 25))
    await done
    expect(p.hasPrev.value).toBe(false)
    expect(p.hasNext.value).toBe(true)

    done = p.goToPage(3)
    f.calls[1].deferred.resolve(result(rowsOf(5), 25))
    await done
    expect(p.hasPrev.value).toBe(true)
    expect(p.hasNext.value).toBe(false)
  })

  it('1 ページに収まるなら前後どちらも無い', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    const done = p.search(snapshot())
    f.calls[0].deferred.resolve(result(rowsOf(4), 4))
    await done

    expect(p.totalPages.value).toBe(1)
    expect(p.hasPrev.value).toBe(false)
    expect(p.hasNext.value).toBe(false)
  })
})

describe('useIssuePagination のスナップショット固定', () => {
  it('検索後にフォーム・列選択を変えてもページ移動には影響しない', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const columns: Column[] = [{ key: 'cf_1', label: '契約種別' }]
    const snap = { query: { projectId: 7, keyword: '検索時の条件' } as IssueQuery, columns }

    const done = p.search(snap)
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done

    // 画面側のフォーム・列選択が後から変わっても、確定済みスナップショットは変わらない
    const next = p.goToPage(2)
    f.calls[1].deferred.resolve(result(rowsOf(10), 50))
    await next

    expect(f.calls[1].query.keyword).toBe('検索時の条件')
    expect(f.calls[1].columns).toEqual(columns)
  })

  it('fetch には毎回新しい query オブジェクトを渡す(スナップショットを汚さない)', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })
    const snap = snapshot(7)

    const done = p.search(snap)
    f.calls[0].deferred.resolve(result(rowsOf(10), 50))
    await done

    expect(snap.query).toEqual({ projectId: 7 })
    expect(p.snapshot.value?.query).toEqual({ projectId: 7 })
  })
})

describe('useIssuePagination のエラー整形', () => {
  it('Error 以外の例外も文字列にして表示する', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    const done = p.search(snapshot())
    f.calls[0].deferred.reject('文字列の例外')
    await done

    expect(p.error.value).toBe('検索に失敗しました: 文字列の例外')
  })

  it('新しい検索を始めたら前のエラーを消す', async () => {
    const f = createFetcher()
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch: f.fetch })

    let done = p.search(snapshot())
    f.calls[0].deferred.reject(new Error('失敗'))
    await done
    expect(p.error.value).not.toBe('')

    done = p.search(snapshot())
    expect(p.error.value).toBe('')
    f.calls[1].deferred.resolve(result(rowsOf(10), 50))
    await done
    expect(p.error.value).toBe('')
  })
})

describe('useIssuePagination の fetch 呼び出し回数', () => {
  it('1 ページ目の検索は 1 回だけ呼ぶ', async () => {
    const fetch = vi.fn().mockResolvedValue(result(rowsOf(10), 50))
    const p = useIssuePagination<Column>({ pageSize: PAGE_SIZE, fetch })

    await p.search(snapshot())

    expect(fetch).toHaveBeenCalledTimes(1)
  })
})
