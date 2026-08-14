/**
 * SyncStatusView の英語表示の検証(設計 §3.4)。
 *
 * ja(既定)の表示は SyncStatusView.projectRefresh.test.ts が実表示の文言で
 * ボタン・警告を引いているため、そちらで担保される。ここは **en で描画したときに
 * 実際に英語が出る**ことを確認する。特に:
 *  - 同期モード(機械値)は Go のラベルではなくフロント翻訳を通る(設計 §3.1)
 *  - 同期進捗(formatSyncProgress)も英語になる
 *
 * 進捗文言はグローバル Composer から言語を取るため、マウントは `shared: true`
 * (= アプリ本体と同じ i18n インスタンス)で行い、後始末で ja へ戻す。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import type { Backend, Project, SyncProgress, SyncResult, SyncStateRow } from '../lib/backend'
import { i18n } from '../lib/i18n'
import { selectedProjectId } from '../lib/projectSelection'
import { resetProjectRefreshState } from '../lib/projectRefresh'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import SyncStatusView from './SyncStatusView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))
/** 画面が onSyncProgress で登録した購読(進捗イベントの発火に使う) */
const progressHandlers = vi.hoisted(() => [] as ((p: SyncProgress) => void)[])

vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onSyncProgress: (handler: (p: SyncProgress) => void) => {
      progressHandlers.push(handler)
      return () => {
        const i = progressHandlers.indexOf(handler)
        if (i >= 0) progressHandlers.splice(i, 1)
      }
    },
  }
})

const PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル', lastSyncedAt: '', syncStateUnknown: false },
]

const STATES: SyncStateRow[] = [
  { dataKind: 'projects', projectId: 0, lastSyncedAt: '' },
  { dataKind: 'issues', projectId: 101, lastSyncedAt: '' },
  { dataKind: 'project_users', projectId: 101, lastSyncedAt: '' },
]

const FULL_SYNC_RESULT: SyncResult = {
  mode: 'full',
  fetched: 12,
  upserted: 10,
  deleted: 2,
  warnings: [],
  durationMs: 1500,
}

/** syncIssues の完了を外から制御できるバックエンド(進捗表示の検証に使う) */
function createFakeBackend() {
  const captured = { runId: '' }
  let finishSync: (() => void) | null = null
  const backend = {
    getActiveProfile: async () => 'p1',
    getLogInfo: async () => ({ path: '/tmp/app.log', enabled: true }),
    getSyncState: async () => STATES,
    getRateLimitStatus: async () => ({
      categories: [
        { name: 'read', limit: 600, remaining: 590, resetUnix: 0, observed: true },
        { name: 'search', limit: 150, remaining: 0, resetUnix: 0, observed: false },
      ],
    }),
    listProjects: async () => PROJECTS,
    syncProjects: async () => {},
    syncIssues: async (_p: string, _id: number, _mode: string, runId: string) => {
      captured.runId = runId
      await new Promise<void>((resolve) => (finishSync = resolve))
      return FULL_SYNC_RESULT
    },
  }
  return {
    backend: backend as unknown as Backend,
    captured,
    finish: () => finishSync?.(),
  }
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null

async function mountEnglish(backend: Backend): Promise<MountedApp> {
  holder.backend = backend
  // 進捗文言はグローバル Composer から言語を取るため、本体と同じインスタンスを使う
  const app = mountWithI18n(SyncStatusView, { locale: 'en', shared: true })
  await flush()
  return app
}

function button(host: HTMLElement, label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
  return found
}

beforeEach(() => {
  localStorage.clear()
  resetProjectRefreshState()
  progressHandlers.length = 0
})

afterEach(() => {
  mounted?.unmount()
  mounted = null
  selectedProjectId.value = 0
  resetProjectRefreshState()
  localStorage.clear()
  // 共有インスタンスを使ったため、既定の言語へ必ず戻す
  i18n.global.locale.value = 'ja'
})

describe('SyncStatusView の英語表示', () => {
  it('見出し・表・データ種別が英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend().backend))
    const text = host.textContent ?? ''

    expect(text).toContain('Sync Status')
    expect(text).toContain('Last sync time per data type')
    expect(text).toContain('Data type')
    expect(text).toContain('Elapsed')
    expect(text).toContain('Projects')
    expect(text).toContain('Issues')
    expect(text).toContain('Project members')
    expect(text).toContain('Not synced')
    expect(text).toContain('(Entire space)')
    expect(text).toContain('サンプル (SAMPLE)') // プロジェクト名は訳さない
    expect(text).not.toContain('データ種別')
    expect(text).not.toContain('未同期')
  })

  it('同期モードのラジオは機械値のフロント翻訳で英語になる(設計 §3.1)', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend().backend))
    const text = host.textContent ?? ''

    expect(text).toContain('Sync mode')
    expect(text).toContain('Auto (full sync on the first run)')
    expect(text).toContain('Full sync')
    expect(text).toContain('Incremental sync')
    expect(text).not.toContain('フル同期')
    expect(text).not.toContain('差分同期')
  })

  it('レート制限・動作ログの節も英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend().backend))
    const text = host.textContent ?? ''

    expect(text).toContain('Rate limit remaining')
    expect(text).toContain('Remaining / limit (per minute)')
    expect(text).toContain('Read')
    expect(text).toContain('Not observed yet (shown after the API is used)')
    expect(text).toContain('Log file:')
    expect(text).not.toContain('読み込み')
    expect(text).not.toContain('リセット時刻')
  })

  it('同期の進捗と結果が英語で表示される', async () => {
    const fake = createFakeBackend()
    const { host } = (mounted = await mountEnglish(fake.backend))

    button(host, 'Sync issues').click()
    await flush()
    expect(fake.captured.runId).not.toBe('')

    for (const handler of progressHandlers) {
      handler({
        runId: fake.captured.runId,
        profileId: 'p1',
        projectId: 101,
        phase: 'fetch',
        fetched: 30,
        total: 120,
      })
    }
    await nextTick()
    expect(host.textContent).toContain('Fetching 30 / 120')
    expect(host.textContent).toContain('Syncing issues...')

    fake.finish()
    await flush()
    const text = host.textContent ?? ''

    // 実行モード(機械値 full)はフロント翻訳を通る
    expect(text).toContain('Full sync of サンプル (SAMPLE) completed')
    expect(text).toContain('Fetched: 12')
    expect(text).toContain('Added / updated: 10')
    expect(text).toContain('Deleted: 2')
    expect(text).toContain('Duration: 1.5 s')
    expect(text).not.toContain('が完了しました')
  })
})
