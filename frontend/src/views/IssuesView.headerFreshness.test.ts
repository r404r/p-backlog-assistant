/**
 * IssuesView の「見出し(h1)右側の最終同期表示」の統合テスト。
 *
 * 同期セクションの鮮度表示とは別に、画面上部でも最終同期時刻が一目で分かるように
 * している。整形(日時・経過)は同期セクションと共有しているため、ここで固定するのは
 * **見出し行に選択中プロジェクトの最終同期が出ること・その表示が言語 / プロジェクト切替 /
 * 同期完了に追従すること**の 3 点。
 *
 * 方式は IssuesView.stale.test.ts と同じ(@vue/test-utils を入れず createApp で
 * マウントし、i18n の登録だけ mountWithI18n に集約する)。
 *
 * TDD 例外(GUI): 「h1 と同じ行の右側に出る」というレイアウトそのものは
 * happy-dom では検証できないため手動確認の対象とし、ここでは見出し行の要素
 * (.header-freshness)に正しい文言が入ることだけを検証する。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { Backend, IssueSearchResult, Project, SyncResult } from '../lib/backend'
import type { Language } from '../lib/i18n'
import { selectedProjectId } from '../lib/projectSelection'
import { activeIssueSync, endIssueSync } from '../lib/syncState'
import IssuesView from './IssuesView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

// Wails ランタイムに触れる入口だけを差し替える(整形ヘルパ等は実物のまま)
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onSyncProgress: () => () => {},
  }
})

/** 「n 分前」の表示を確かめるための時刻(ISO) */
function isoMinutesAgo(min: number): string {
  return new Date(Date.now() - min * 60_000).toISOString()
}

/**
 * 期待する日時表記(YYYY-MM-DD HH:mm・ローカル時刻)。
 * lib/format の実装を呼ぶとトートロジーになるため、テスト側で独立に組み立てる。
 */
function expectedDateTime(iso: string): string {
  const d = new Date(iso)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** プロジェクト名は利用者データのため翻訳されない。英語表示の判定を濁さないよう英字にする */
function project(id: number, key: string, lastSyncedAt: string): Project {
  return { id, projectKey: key, name: `Project ${key}`, lastSyncedAt, syncStateUnknown: false }
}

interface FakeOptions {
  /** 画面へ返すプロジェクト一覧(listProjects が呼ばれるたびに現在値を返す) */
  projects: Project[]
  /** 課題同期の完了後に listProjects が返すようになる一覧(同期による鮮度更新の検証用) */
  projectsAfterSync?: Project[]
}

function createFakeBackend(options: FakeOptions) {
  let projects = options.projects
  const backend = {
    getActiveProfile: async () => 'p1',
    listProfiles: async () => [
      {
        id: 'p1',
        name: 'Test',
        spaceUrl: 'https://example.backlog.jp',
        lastUserName: '',
        lastUserId: 1,
      },
    ],
    getIssueExportColumns: async () => [{ key: 'issueKey', label: 'キー', byDefault: true }],
    listProjects: async () => projects.map((p) => ({ ...p })),
    syncProjects: async () => {},
    listFilterOptions: async () => ({ statuses: [], assignees: [] }),
    getMasterData: async () => ({
      issueTypes: [],
      priorities: [],
      statuses: [],
      customFields: [],
    }),
    searchIssues: async (): Promise<IssueSearchResult> => ({ rows: [], total: 0, unverifiable: 0 }),
    syncIssues: async (): Promise<SyncResult> => {
      if (options.projectsAfterSync) projects = options.projectsAfterSync
      return { mode: 'full', fetched: 1, upserted: 1, deleted: 0, warnings: [], durationMs: 10 }
    },
  }
  return backend as unknown as Backend
}

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null

async function mountView(options: FakeOptions, locale: Language = 'ja'): Promise<MountedApp> {
  holder.backend = createFakeBackend(options)
  const app = mountWithI18n(IssuesView, { locale })
  // onMounted の非同期連鎖(プロファイル → プロジェクト → 候補・カスタム属性)を待つ
  await flush()
  return app
}

/** 見出し行の最終同期表示(無ければ null) */
function headerFreshness(host: HTMLElement): string | null {
  const el = host.querySelector('.header-freshness')
  return el ? (el.textContent ?? '').trim() : null
}

/** ラベル(前後の空白を除いた文字列)でボタンを探す */
function button(host: HTMLElement, label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
  return found
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  mounted?.unmount()
  mounted = null
  // プロジェクト選択・同期状態はモジュールレベルの共有状態のため、次のテストへ持ち越さない
  selectedProjectId.value = 0
  const active = activeIssueSync.value
  if (active) endIssueSync(active.runId)
  localStorage.clear()
})

describe('IssuesView の見出し右側の最終同期表示', () => {
  it('選択中プロジェクトの最終同期を日時と経過で表示する(ja)', async () => {
    const syncedAt = isoMinutesAgo(5)
    const { host } = (mounted = await mountView({ projects: [project(101, 'SAMPLE', syncedAt)] }))

    expect(headerFreshness(host)).toBe(`最終同期: ${expectedDateTime(syncedAt)} (5 分前)`)
  })

  it('表示言語に追従する(en)', async () => {
    const syncedAt = isoMinutesAgo(5)
    const { host } = (mounted = await mountView(
      { projects: [project(101, 'SAMPLE', syncedAt)] },
      'en',
    ))

    expect(headerFreshness(host)).toBe(`Last synced: ${expectedDateTime(syncedAt)} (5 min ago)`)
  })

  it('未同期のプロジェクトでは「未同期」と表示する', async () => {
    const { host } = (mounted = await mountView({ projects: [project(101, 'SAMPLE', '')] }))

    expect(headerFreshness(host)).toBe('最終同期: 未同期')
  })

  it('プロジェクトを切り替えると切替先の最終同期に変わる', async () => {
    const syncedAt = isoMinutesAgo(90)
    const { host } = (mounted = await mountView({
      projects: [project(101, 'SAMPLE', ''), project(102, 'OTHER', syncedAt)],
    }))
    expect(headerFreshness(host)).toBe('最終同期: 未同期')

    selectedProjectId.value = 102
    await flush()

    expect(headerFreshness(host)).toBe(`最終同期: ${expectedDateTime(syncedAt)} (1 時間前)`)
  })

  it('課題同期の完了後に最終同期の表示が更新される', async () => {
    const syncedAt = isoMinutesAgo(0)
    const { host } = (mounted = await mountView({
      projects: [project(101, 'SAMPLE', '')],
      projectsAfterSync: [project(101, 'SAMPLE', syncedAt)],
    }))
    expect(headerFreshness(host)).toBe('最終同期: 未同期')

    button(host, '同期').click()
    await flush()
    await flush()

    expect(headerFreshness(host)).toBe(`最終同期: ${expectedDateTime(syncedAt)} (たった今)`)
  })

  it('鮮度を取得できなかったときは見出しに出さない(同期セクションの説明に任せる)', async () => {
    const unknown: Project = { ...project(101, 'SAMPLE', ''), syncStateUnknown: true }
    const { host } = (mounted = await mountView({ projects: [unknown] }))

    expect(headerFreshness(host)).toBeNull()
    // 説明そのものは同期セクション側に出ている
    expect(host.textContent).toContain('鮮度を取得できませんでした')
  })

  it('プロジェクトが 1 つも無ければ表示しない', async () => {
    const { host } = (mounted = await mountView({ projects: [] }))

    expect(headerFreshness(host)).toBeNull()
  })
})
