/**
 * SyncStatusView の「画面表示時のプロジェクト一覧同期のスロットリング」配線の統合テスト。
 *
 * 判定そのものは projectRefresh.test.ts で確認済みなので、ここでは
 * IssuesView.projectRefresh.test.ts と同じ流儀で、実際の SyncStatusView を
 * マウントし **バックエンド呼び出しの有無** だけを見る(2 画面で配線が
 * 食い違わないよう、同じ観点を両方に用意する)。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import type { Backend, Project } from '../lib/backend'
import {
  PROJECT_REFRESH_INTERVAL_MS,
  markProjectsRefreshed,
  projectsRefreshedAt,
  resetProjectRefreshState,
  runSharedProjectRefresh,
} from '../lib/projectRefresh'
import { selectedProjectId } from '../lib/projectSelection'
import SyncStatusView from './SyncStatusView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onSyncProgress: () => () => {},
  }
})

const PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル', lastSyncedAt: '', syncStateUnknown: false },
]

function createFakeBackend(options: { syncFails?: boolean } = {}) {
  const calls = { listProjects: 0, syncProjects: 0 }
  const backend = {
    getActiveProfile: async () => 'p1',
    getLogInfo: async () => ({ path: '', enabled: false }),
    getSyncState: async () => [],
    getRateLimitStatus: async () => ({ categories: [] }),
    listProjects: async () => {
      calls.listProjects++
      return PROJECTS
    },
    syncProjects: async () => {
      calls.syncProjects++
      if (options.syncFails) throw new Error('オフラインです')
    },
  }
  return { backend: backend as unknown as Backend, calls }
}

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

interface Screen {
  app: App
  host: HTMLElement
  text(): string
  button(label: string): HTMLButtonElement
}

async function mountSyncStatusView(backend: Backend): Promise<Screen> {
  holder.backend = backend
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(SyncStatusView)
  app.mount(host)
  // onMounted の非同期連鎖(プロファイル → 動作ログ → プロジェクト一覧)を待つ
  await flush()
  return {
    app,
    host,
    text: () => host.textContent ?? '',
    button(label) {
      const found = Array.from(host.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === label,
      )
      if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
      return found
    },
  }
}

const SKIP_WARNING_PREFIX = 'プロジェクト一覧を最新化できませんでした'

let mounted: Screen | null = null

beforeEach(() => {
  localStorage.clear()
  resetProjectRefreshState()
})

afterEach(() => {
  // 残量の自動更新タイマーを止めるため、必ずアンマウントする
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  selectedProjectId.value = 0
  resetProjectRefreshState()
  localStorage.clear()
})

describe('SyncStatusView の画面表示時のプロジェクト一覧同期', () => {
  it('未記録(起動後の初回表示)では突合し、成功を記録する', async () => {
    const fake = createFakeBackend()
    mounted = await mountSyncStatusView(fake.backend)

    expect(fake.calls.syncProjects).toBe(1)
    expect(fake.calls.listProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
  })

  it('前回成功から 10 分以内なら突合を省略し、一覧の読み込みだけを行う', async () => {
    markProjectsRefreshed('p1', Date.now())
    const fake = createFakeBackend()
    const screen = (mounted = await mountSyncStatusView(fake.backend))

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)
    expect(screen.text()).not.toContain(SKIP_WARNING_PREFIX)
    expect(screen.text()).not.toContain('省略しました')
  })

  it('前回成功から 10 分を超えていたら突合する', async () => {
    markProjectsRefreshed('p1', Date.now() - PROJECT_REFRESH_INTERVAL_MS - 1)
    const fake = createFakeBackend()
    mounted = await mountSyncStatusView(fake.backend)

    expect(fake.calls.syncProjects).toBe(1)
  })

  it('突合に失敗したら記録しない(次の表示で再試行する)', async () => {
    const fake = createFakeBackend({ syncFails: true })
    const screen = (mounted = await mountSyncStatusView(fake.backend))

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
    expect(screen.text()).toContain(SKIP_WARNING_PREFIX)
  })

  it('手動の「プロジェクト一覧を同期」は省略できる状態でも実行し、記録を更新する', async () => {
    const marked = Date.now()
    markProjectsRefreshed('p1', marked)
    const fake = createFakeBackend()
    const screen = (mounted = await mountSyncStatusView(fake.backend))
    expect(fake.calls.syncProjects).toBe(0)

    screen.button('プロジェクト一覧を同期').click()
    await flush()

    expect(fake.calls.syncProjects).toBe(1)
    expect(fake.calls.listProjects).toBe(2)
    const after = projectsRefreshedAt('p1')
    expect(after).toBeDefined()
    expect(after).toBeGreaterThanOrEqual(marked)
  })

  it('他画面が開始した突合が実行中なら、突合を再実行せず合流する', async () => {
    let finishOther!: () => void
    const other = runSharedProjectRefresh(
      'p1',
      () => new Promise<void>((resolve) => (finishOther = resolve)),
    )
    const fake = createFakeBackend()
    mounted = await mountSyncStatusView(fake.backend)

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(0)

    finishOther()
    await other
    await flush()

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
  })

  it('手動同期も実行中の突合があれば合流する(重複した API 突合を増やさない)', async () => {
    markProjectsRefreshed('p1', Date.now())
    let finishOther!: () => void
    const other = runSharedProjectRefresh(
      'p1',
      () => new Promise<void>((resolve) => (finishOther = resolve)),
    )
    const fake = createFakeBackend()
    const screen = (mounted = await mountSyncStatusView(fake.backend))
    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)

    screen.button('プロジェクト一覧を同期').click()
    await flush()
    expect(fake.calls.syncProjects).toBe(0)

    finishOther()
    await other
    await flush()

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(2)
  })

  it('手動同期が失敗したら記録を更新しない', async () => {
    const fake = createFakeBackend({ syncFails: true })
    markProjectsRefreshed('p1', Date.now())
    const screen = (mounted = await mountSyncStatusView(fake.backend))
    resetProjectRefreshState()

    screen.button('プロジェクト一覧を同期').click()
    await flush()

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
    expect(screen.text()).toContain('プロジェクトの同期に失敗しました')
  })
})
