/**
 * IssuesView の「画面表示時のプロジェクト一覧同期のスロットリング」配線の統合テスト。
 *
 * projectRefresh.test.ts は判定の純関数と記録だけを見ているため、画面側の接続
 * (refreshProjects の省略判定・成功時の記録・手動ボタンは省略しないこと)が
 * 外れても気づけない。ここでは IssuesView.stale.test.ts と同じ流儀で
 * **実際の IssuesView をマウントし、バックエンド呼び出しの有無で** 検証する。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import type { Backend, IssueSearchResult, Project } from '../lib/backend'
import {
  PROJECT_REFRESH_INTERVAL_MS,
  markProjectsRefreshed,
  projectsRefreshedAt,
  resetProjectRefreshState,
  runSharedProjectRefresh,
} from '../lib/projectRefresh'
import { selectedProjectId } from '../lib/projectSelection'
import IssuesView from './IssuesView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

// Wails ランタイムに触れる 3 つの入口だけを差し替える(整形ヘルパ等は実物のまま)
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

/** 呼び出し回数を数えるだけのバックエンド(syncProjects の成否を切り替えられる) */
function createFakeBackend(options: { syncFails?: boolean } = {}) {
  const calls = { listProjects: 0, syncProjects: 0 }
  const backend = {
    getActiveProfile: async () => 'p1',
    getIssueExportColumns: async () => [{ key: 'issueKey', label: '課題キー', byDefault: true }],
    listProfiles: async () => [
      {
        id: 'p1',
        name: 'テスト',
        spaceUrl: 'https://example.backlog.jp',
        lastUserName: '',
        lastUserId: 1,
      },
    ],
    listProjects: async () => {
      calls.listProjects++
      return PROJECTS
    },
    syncProjects: async () => {
      calls.syncProjects++
      if (options.syncFails) throw new Error('オフラインです')
    },
    listFilterOptions: async () => ({ statuses: [], assignees: [] }),
    getMasterData: async () => ({
      issueTypes: [],
      priorities: [],
      statuses: [],
      customFields: [],
    }),
    searchIssues: async (): Promise<IssueSearchResult> => ({ rows: [], total: 0, unverifiable: 0 }),
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

async function mountIssuesView(backend: Backend): Promise<Screen> {
  holder.backend = backend
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(IssuesView)
  app.mount(host)
  // onMounted の非同期連鎖(プロファイル → プロジェクト → 候補・カスタム属性)を待つ
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

/** 最新化を省略できない状態の警告(スキップ時に出てはいけない文言) */
const SKIP_WARNING_PREFIX = 'プロジェクト一覧を最新化できませんでした'

let mounted: Screen | null = null

beforeEach(() => {
  localStorage.clear()
  resetProjectRefreshState()
})

afterEach(() => {
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  // プロジェクト選択・最終同期時刻はモジュールレベルの共有状態のため持ち越さない
  selectedProjectId.value = 0
  resetProjectRefreshState()
  localStorage.clear()
})

describe('IssuesView の画面表示時のプロジェクト一覧同期', () => {
  it('未記録(起動後の初回表示)では突合し、成功を記録する', async () => {
    const fake = createFakeBackend()
    mounted = await mountIssuesView(fake.backend)

    expect(fake.calls.syncProjects).toBe(1)
    expect(fake.calls.listProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
  })

  it('前回成功から 10 分以内なら突合を省略し、一覧の読み込みだけを行う', async () => {
    markProjectsRefreshed('p1', Date.now())
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)
    // 省略は正常動作のため、警告・案内は出さない
    expect(screen.text()).not.toContain(SKIP_WARNING_PREFIX)
    expect(screen.text()).not.toContain('省略しました')
  })

  it('前回成功から 10 分を超えていたら突合する', async () => {
    markProjectsRefreshed('p1', Date.now() - PROJECT_REFRESH_INTERVAL_MS - 1)
    const fake = createFakeBackend()
    mounted = await mountIssuesView(fake.backend)

    expect(fake.calls.syncProjects).toBe(1)
    expect(fake.calls.listProjects).toBe(1)
  })

  it('突合に失敗したら記録しない(次の表示で再試行する)', async () => {
    const fake = createFakeBackend({ syncFails: true })
    const screen = (mounted = await mountIssuesView(fake.backend))

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
    // 失敗時は従来どおり警告を出し、キャッシュ表示を継続する
    expect(screen.text()).toContain(SKIP_WARNING_PREFIX)
  })

  it('別プロファイルの記録では省略しない', async () => {
    markProjectsRefreshed('p2', Date.now())
    const fake = createFakeBackend()
    mounted = await mountIssuesView(fake.backend)

    expect(fake.calls.syncProjects).toBe(1)
  })

  it('手動の「プロジェクト一覧を同期」は省略できる状態でも実行し、記録を更新する', async () => {
    const marked = Date.now()
    markProjectsRefreshed('p1', marked)
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
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
    // 別画面(破棄済み)が開始した自動突合を再現する
    let finishOther!: () => void
    const other = runSharedProjectRefresh(
      'p1',
      () => new Promise<void>((resolve) => (finishOther = resolve)),
    )
    const fake = createFakeBackend()
    mounted = await mountIssuesView(fake.backend)

    // 実行中のものへ合流するだけなので、API 突合は増えない
    expect(fake.calls.syncProjects).toBe(0)
    // 合流先の完了を待ってから一覧を読む(古いキャッシュを表示して終わらない)
    expect(fake.calls.listProjects).toBe(0)

    finishOther()
    await other
    await flush()

    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
  })

  it('手動同期も実行中の突合があれば合流する(重複した API 突合を増やさない)', async () => {
    // 自動突合を省略させたうえで、別画面が開始した突合を実行中にしておく
    markProjectsRefreshed('p1', Date.now())
    let finishOther!: () => void
    const other = runSharedProjectRefresh(
      'p1',
      () => new Promise<void>((resolve) => (finishOther = resolve)),
    )
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(1)

    screen.button('プロジェクト一覧を同期').click()
    await flush()
    expect(fake.calls.syncProjects).toBe(0)

    finishOther()
    await other
    await flush()

    // 合流先の完了後に一覧を読み直す
    expect(fake.calls.syncProjects).toBe(0)
    expect(fake.calls.listProjects).toBe(2)
  })

  it('手動同期が失敗したら記録を更新しない', async () => {
    const fake = createFakeBackend({ syncFails: true })
    // 自動突合を省略させ、手動同期だけを失敗させる
    markProjectsRefreshed('p1', Date.now())
    const screen = (mounted = await mountIssuesView(fake.backend))
    resetProjectRefreshState()

    screen.button('プロジェクト一覧を同期').click()
    await flush()

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
    expect(screen.text()).toContain('プロジェクトの同期に失敗しました')
  })
})
