/**
 * 画面をまたいだプロジェクト一覧の自動突合の共有(二重実行の防止)の統合テスト。
 *
 * 課題抽出で自動突合が走っている最中に同期状態へ移動すると、移動先も同じ
 * プロファイルの突合を始めてしまう(Go 側は syncMu で直列化するため、
 * 待たされたうえで同じ API 突合をやり直す)ことがあった。
 * projectRefresh の実行中共有(runSharedProjectRefresh)で合流するようにした
 * 配線を、**実際に 2 つの画面を順にマウントして** 確認する。
 *
 * 個々の画面の判定・記録は IssuesView.projectRefresh.test.ts /
 * SyncStatusView.projectRefresh.test.ts が見るため、ここは画面間の合流だけを見る。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App, type Component } from 'vue'
import type { Backend, IssueSearchResult, Project } from '../lib/backend'
import { projectsRefreshedAt, resetProjectRefreshState } from '../lib/projectRefresh'
import { selectedProjectId } from '../lib/projectSelection'
import IssuesView from './IssuesView.vue'
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

/** syncProjects の完了を外から制御できる、両画面ぶんのバックエンド */
function createFakeBackend() {
  const calls = { listProjects: 0, syncProjects: 0 }
  const pending: (() => void)[] = []
  const backend = {
    getActiveProfile: async () => 'p1',
    getLogInfo: async () => ({ path: '', enabled: false }),
    getSyncState: async () => [],
    getRateLimitStatus: async () => ({ categories: [] }),
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
    syncProjects: () => {
      calls.syncProjects++
      return new Promise<void>((resolve) => pending.push(resolve))
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
  /** 実行中の突合をすべて完了させる */
  const finishAll = () => {
    for (const resolve of pending.splice(0)) resolve()
  }
  return { backend: backend as unknown as Backend, calls, finishAll }
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

interface Screen {
  app: App
  host: HTMLElement
}

/** 画面を 1 つマウントする(サイドバーの切替と同じく、同時に 1 画面だけ表示する想定) */
async function mountView(component: Component, backend: Backend): Promise<Screen> {
  holder.backend = backend
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(component)
  app.mount(host)
  await flush()
  return { app, host }
}

function unmount(screen: Screen | null): void {
  screen?.app.unmount()
  screen?.host.remove()
}

let current: Screen | null = null

beforeEach(() => {
  localStorage.clear()
  resetProjectRefreshState()
})

afterEach(() => {
  unmount(current)
  current = null
  selectedProjectId.value = 0
  resetProjectRefreshState()
  localStorage.clear()
})

describe('画面をまたいだ自動突合の共有', () => {
  it('課題抽出の突合中に同期状態へ移動しても、突合をやり直さない', async () => {
    const fake = createFakeBackend()
    const issues = await mountView(IssuesView, fake.backend)
    expect(fake.calls.syncProjects).toBe(1)

    // 突合の完了を待たずに画面を移動する(App.vue は前の画面を破棄する)
    unmount(issues)
    current = await mountView(SyncStatusView, fake.backend)

    // 移動先は実行中の突合へ合流するだけで、API 突合は増えない
    expect(fake.calls.syncProjects).toBe(1)

    fake.finishAll()
    await flush()

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
    // 合流先の完了後、移動先の画面もローカル一覧を読み込めている
    expect(fake.calls.listProjects).toBeGreaterThanOrEqual(1)
  })

  it('同期状態の突合中に課題抽出へ移動しても、突合をやり直さない', async () => {
    const fake = createFakeBackend()
    const status = await mountView(SyncStatusView, fake.backend)
    expect(fake.calls.syncProjects).toBe(1)

    unmount(status)
    current = await mountView(IssuesView, fake.backend)

    expect(fake.calls.syncProjects).toBe(1)

    fake.finishAll()
    await flush()

    expect(fake.calls.syncProjects).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeDefined()
    expect(fake.calls.listProjects).toBeGreaterThanOrEqual(1)
  })
})
