/**
 * projectRefresh.ts のテスト(R15)。
 *
 * 画面表示時のプロジェクト一覧の自動突合をスロットリングする判定と、
 * プロファイル別の最終成功時刻の記録を検証する。
 *
 * モジュールレベルの共有状態を持つため、各テストの前に
 * resetProjectRefreshState() でまっさらな状態へ戻す。
 */
import { beforeEach, describe, expect, it } from 'vitest'
import {
  PROJECT_REFRESH_INTERVAL_MS,
  invalidateProjectRefresh,
  markProjectsRefreshed,
  projectsRefreshedAt,
  resetProjectRefreshState,
  runSharedProjectRefresh,
  shouldSkipProjectRefresh,
  shouldSkipProjectRefreshFor,
} from './projectRefresh'

/** 解決タイミングを外から制御できる Promise(実行中の突合を再現する) */
function deferred(): { promise: Promise<void>; resolve: () => void; reject: (e: unknown) => void } {
  let resolve!: () => void
  let reject!: (e: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** 呼び出し回数を数える突合処理(応答は外から解決する) */
function countingSync() {
  const calls: ReturnType<typeof deferred>[] = []
  const sync = () => {
    const d = deferred()
    calls.push(d)
    return d.promise
  }
  return { sync, calls }
}

/** 判定の基準時刻(相対時刻の読みやすさのため、テスト内では now からの差で書く) */
const NOW = 1_700_000_000_000

beforeEach(() => {
  resetProjectRefreshState()
})

describe('PROJECT_REFRESH_INTERVAL_MS', () => {
  it('10 分', () => {
    expect(PROJECT_REFRESH_INTERVAL_MS).toBe(10 * 60 * 1000)
  })
})

describe('shouldSkipProjectRefresh', () => {
  it('未記録(undefined)なら省略しない', () => {
    expect(shouldSkipProjectRefresh(undefined, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(false)
  })

  it('経過が間隔未満なら省略する', () => {
    const last = NOW - (PROJECT_REFRESH_INTERVAL_MS - 1)
    expect(shouldSkipProjectRefresh(last, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(true)
  })

  it('直後(経過 0)なら省略する', () => {
    expect(shouldSkipProjectRefresh(NOW, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(true)
  })

  it('経過がちょうど間隔なら省略しない(境界は突合する側に倒す)', () => {
    const last = NOW - PROJECT_REFRESH_INTERVAL_MS
    expect(shouldSkipProjectRefresh(last, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(false)
  })

  it('経過が間隔を超えていたら省略しない', () => {
    const last = NOW - (PROJECT_REFRESH_INTERVAL_MS + 1)
    expect(shouldSkipProjectRefresh(last, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(false)
  })

  it('未来の記録(時計の巻き戻し等)でも省略しない', () => {
    // 経過が負になる状況では判断材料が壊れているため、安全側(突合する)に倒す
    expect(shouldSkipProjectRefresh(NOW + 1000, NOW, PROJECT_REFRESH_INTERVAL_MS)).toBe(false)
  })

  it('間隔 0 では常に省略しない', () => {
    expect(shouldSkipProjectRefresh(NOW, NOW, 0)).toBe(false)
  })
})

describe('markProjectsRefreshed / projectsRefreshedAt', () => {
  it('初期状態は未記録', () => {
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })

  it('記録した時刻を参照できる', () => {
    markProjectsRefreshed('p1', NOW)
    expect(projectsRefreshedAt('p1')).toBe(NOW)
  })

  it('同じプロファイルの再記録は最新の時刻で上書きする', () => {
    markProjectsRefreshed('p1', NOW)
    markProjectsRefreshed('p1', NOW + 5000)
    expect(projectsRefreshedAt('p1')).toBe(NOW + 5000)
  })

  it('プロファイルごとに独立して記録する', () => {
    markProjectsRefreshed('p1', NOW)
    expect(projectsRefreshedAt('p2')).toBeUndefined()
    markProjectsRefreshed('p2', NOW + 1000)
    expect(projectsRefreshedAt('p1')).toBe(NOW)
    expect(projectsRefreshedAt('p2')).toBe(NOW + 1000)
  })

  it('プロファイル ID が空なら記録しない(未確定のプロファイルを混同しないため)', () => {
    markProjectsRefreshed('', NOW)
    expect(projectsRefreshedAt('')).toBeUndefined()
  })

  it('resetProjectRefreshState で記録を消す', () => {
    markProjectsRefreshed('p1', NOW)
    resetProjectRefreshState()
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })
})

describe('shouldSkipProjectRefreshFor', () => {
  it('未記録のプロファイルでは省略しない(起動後の初回表示は必ず突合する)', () => {
    expect(shouldSkipProjectRefreshFor('p1', NOW)).toBe(false)
  })

  it('記録から 10 分未満なら省略する', () => {
    markProjectsRefreshed('p1', NOW)
    expect(shouldSkipProjectRefreshFor('p1', NOW + PROJECT_REFRESH_INTERVAL_MS - 1)).toBe(true)
  })

  it('記録から 10 分ちょうど・超過なら省略しない', () => {
    markProjectsRefreshed('p1', NOW)
    expect(shouldSkipProjectRefreshFor('p1', NOW + PROJECT_REFRESH_INTERVAL_MS)).toBe(false)
    expect(shouldSkipProjectRefreshFor('p1', NOW + PROJECT_REFRESH_INTERVAL_MS + 1)).toBe(false)
  })

  it('別のプロファイルの記録では省略しない', () => {
    markProjectsRefreshed('p1', NOW)
    expect(shouldSkipProjectRefreshFor('p2', NOW + 1000)).toBe(false)
  })

  it('プロファイル ID が空なら省略しない', () => {
    expect(shouldSkipProjectRefreshFor('', NOW)).toBe(false)
  })

  it('now を省略すると現在時刻で判定する', () => {
    markProjectsRefreshed('p1', Date.now())
    expect(shouldSkipProjectRefreshFor('p1')).toBe(true)
  })
})

describe('runSharedProjectRefresh', () => {
  it('実行中のものが無ければ突合を開始し、成功で記録する', async () => {
    const { sync, calls } = countingSync()
    const run = runSharedProjectRefresh('p1', sync)
    expect(calls).toHaveLength(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()

    calls[0].resolve()
    await run

    expect(projectsRefreshedAt('p1')).toBeDefined()
  })

  it('同じプロファイルの実行中があれば再実行せず合流する', async () => {
    const { sync, calls } = countingSync()
    const first = runSharedProjectRefresh('p1', sync)
    const second = runSharedProjectRefresh('p1', sync)
    expect(calls).toHaveLength(1)

    calls[0].resolve()
    await Promise.all([first, second])

    // 合流側も完了を待てて、記録は 1 回だけ行われる
    expect(projectsRefreshedAt('p1')).toBeDefined()
    expect(calls).toHaveLength(1)
  })

  it('完了後は次の呼び出しで新たに突合する', async () => {
    const { sync, calls } = countingSync()
    const first = runSharedProjectRefresh('p1', sync)
    calls[0].resolve()
    await first

    void runSharedProjectRefresh('p1', sync)
    expect(calls).toHaveLength(2)
    calls[1].resolve()
  })

  it('プロファイルごとに独立して実行する', async () => {
    const { sync, calls } = countingSync()
    void runSharedProjectRefresh('p1', sync)
    void runSharedProjectRefresh('p2', sync)
    expect(calls).toHaveLength(2)
    calls[0].resolve()
    calls[1].resolve()
    await Promise.resolve()
  })

  it('失敗は合流側にも伝わり、記録せず、次の呼び出しで再実行する', async () => {
    const { sync, calls } = countingSync()
    const first = runSharedProjectRefresh('p1', sync)
    const second = runSharedProjectRefresh('p1', sync)
    calls[0].reject(new Error('オフラインです'))

    await expect(first).rejects.toThrow('オフラインです')
    await expect(second).rejects.toThrow('オフラインです')
    expect(projectsRefreshedAt('p1')).toBeUndefined()

    void runSharedProjectRefresh('p1', sync).catch(() => {})
    expect(calls).toHaveLength(2)
    calls[1].resolve()
  })

  it('プロファイル ID が空なら共有も記録もしない', async () => {
    const { sync, calls } = countingSync()
    const first = runSharedProjectRefresh('', sync)
    const second = runSharedProjectRefresh('', sync)
    expect(calls).toHaveLength(2)
    calls[0].resolve()
    calls[1].resolve()
    await Promise.all([first, second])
    expect(projectsRefreshedAt('')).toBeUndefined()
  })

  it('resetProjectRefreshState 後の実行中の突合は記録しない', async () => {
    const { sync, calls } = countingSync()
    const run = runSharedProjectRefresh('p1', sync)
    resetProjectRefreshState()
    calls[0].resolve()
    await run
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })
})

describe('invalidateProjectRefresh', () => {
  it('記録を無効化し、次の表示で突合させる', () => {
    markProjectsRefreshed('p1', Date.now())
    expect(shouldSkipProjectRefreshFor('p1')).toBe(true)

    invalidateProjectRefresh('p1')

    expect(projectsRefreshedAt('p1')).toBeUndefined()
    expect(shouldSkipProjectRefreshFor('p1')).toBe(false)
  })

  it('他のプロファイルの記録は消さない', () => {
    markProjectsRefreshed('p1', Date.now())
    markProjectsRefreshed('p2', Date.now())
    invalidateProjectRefresh('p1')
    expect(shouldSkipProjectRefreshFor('p2')).toBe(true)
  })

  it('実行中の突合が後から成功しても記録し直さない(古い接続先の結果のため)', async () => {
    const { sync, calls } = countingSync()
    const run = runSharedProjectRefresh('p1', sync)
    invalidateProjectRefresh('p1')
    calls[0].resolve()
    await run

    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })

  it('無効化後の呼び出しは、古い突合に合流せず新たに突合する', () => {
    const { sync, calls } = countingSync()
    void runSharedProjectRefresh('p1', sync)
    invalidateProjectRefresh('p1')
    void runSharedProjectRefresh('p1', sync)
    expect(calls).toHaveLength(2)
    calls[0].resolve()
    calls[1].resolve()
  })

  it('プロファイル ID が空でも壊れない', () => {
    markProjectsRefreshed('p1', Date.now())
    invalidateProjectRefresh('')
    expect(shouldSkipProjectRefreshFor('p1')).toBe(true)
  })
})
