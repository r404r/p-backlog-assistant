/**
 * IssuesView の英語表示テスト(設計 §3.4)。
 *
 * 既定(ja)の表示は IssuesView.stale.test.ts / IssuesView.projectRefresh.test.ts が
 * 実文言で押さえているため、ここでは **locale: 'en' でマウントしたときに
 * 主要な文言が英語になる**ことを検証する。実装と同じキーを参照するアサートは
 * トートロジーになるため避け、実際に描画された文字列で確認する(設計 §3.4)。
 *
 * あわせて設計 §3.1 の「表示は生の機械値を正とし、Go の日本語ラベルは表示に
 * 使わない」も検証する: バックエンドが日本語ラベル(列ラベル・同期モード)を
 * 返しても、画面は英語で表示する。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type {
  Backend,
  IssueDetail,
  IssueRow,
  IssueSearchResult,
  Project,
  SyncProgress,
  SyncResult,
} from '../lib/backend'
import type { Language } from '../lib/i18n'
import { selectedProjectId } from '../lib/projectSelection'
import { activeIssueSync, endIssueSync, issueSyncRunning } from '../lib/syncState'
import IssuesView from './IssuesView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({
  backend: null as unknown,
  /**
   * 進捗の購読者(テストから進捗イベントを流し込むために保持する)。
   * 購読するのは画面だけではない(lib/syncState も同期の完了検知に使う)ため、
   * 1 つだけ覚えるのではなく配列で全員に配る。
   */
  progressListeners: [] as ((p: SyncProgress) => void)[],
}))

/** 進捗イベントを購読者全員へ配る */
function emitSyncProgress(p: SyncProgress): void {
  for (const listener of [...holder.progressListeners]) listener(p)
}

// Wails ランタイムに触れる入口だけを差し替える(整形ヘルパ等は実物のまま)
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onSyncProgress: (listener: (p: SyncProgress) => void) => {
      holder.progressListeners.push(listener)
      return () => {
        holder.progressListeners = holder.progressListeners.filter((l) => l !== listener)
      }
    },
  }
})

/**
 * 日本語(ひらがな・カタカナ・漢字・全角記号)を含むか。
 * noHardcodedText.test.ts と同じ判定を、描画結果に対して使う。
 */
function hasJapanese(text: string): boolean {
  return /[぀-ヿ㐀-䶿一-鿿＀-ﾟ]/.test(text)
}

/** プロジェクト名は利用者データのため翻訳されない。判定を濁さないよう英字にする */
const PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'Sample', lastSyncedAt: '', syncStateUnknown: false },
]

/** 1 ページ 200 件のため、ページャを出すには総件数を 200 超にする */
const TOTAL = 450

function row(issueKey: string): IssueRow {
  return {
    issueKey,
    summary: `${issueKey} summary`,
    statusName: 'Open',
    assigneeName: '',
    issueTypeName: 'Task',
    priorityName: 'Normal',
    created: '',
    updated: '',
    dueDate: '',
    customFields: {},
  }
}

function issueDetail(issueKey: string): IssueDetail {
  return {
    issueKey,
    summary: `${issueKey} summary`,
    description: '',
    statusName: 'Open',
    assigneeName: '',
    issueTypeName: 'Task',
    priorityName: 'Normal',
    created: '',
    updated: '',
    dueDate: '',
    parentIssueKey: '',
    customFields: [],
    fetchedAt: '',
    comments: [],
    commentsFetchedAt: '',
    commentsHistoryOnly: 0,
    commentsTruncated: false,
    warnings: [],
    textFormattingRule: '',
  }
}

/** 同期結果は Go が解決したモード(機械値)を返す */
function syncResult(mode: string): SyncResult {
  return { mode, fetched: 12, upserted: 10, deleted: 2, warnings: [], durationMs: 1500 }
}

interface FakeOptions {
  /** プロジェクトの最終同期時刻(鮮度の経過時間表示の検証に使う) */
  lastSyncedAt?: string
  /** プロジェクト一覧の取得を失敗させる(エラーメッセージの言語追従の検証に使う) */
  listProjectsError?: string
  /** syncIssues を保留にする(実行中の進捗表示を検証するため) */
  deferSync?: boolean
  /** 検索を失敗させる(ページネーション composable のエラー表示の検証に使う) */
  searchError?: string
}

/**
 * バックエンド。**列ラベルは Go が返す日本語のまま**にして、
 * 画面が Go ラベルではなくカタログで表示していることを確かめる。
 */
function createFakeBackend(options: FakeOptions = {}) {
  /** 画面が採番した同期の実行 ID(進捗イベントの突き合わせに使う) */
  const runIds: string[] = []
  let finishSync: (() => void) | null = null
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
    getIssueExportColumns: async () => [
      { key: 'issueKey', label: 'キー', byDefault: true },
      { key: 'summary', label: '件名', byDefault: true },
      { key: 'dueDate', label: '期限', byDefault: false },
    ],
    listProjects: async () => {
      if (options.listProjectsError) throw new Error(options.listProjectsError)
      return PROJECTS.map((p) => ({ ...p, lastSyncedAt: options.lastSyncedAt ?? p.lastSyncedAt }))
    },
    syncProjects: async () => {},
    listFilterOptions: async () => ({ statuses: [], assignees: [] }),
    getMasterData: async () => ({
      issueTypes: [],
      priorities: [],
      statuses: [],
      customFields: [],
    }),
    searchIssues: async (): Promise<IssueSearchResult> => {
      if (options.searchError) throw new Error(options.searchError)
      return { rows: [row('SAMPLE-1'), row('SAMPLE-2')], total: TOTAL, unverifiable: 0 }
    },
    getIssueDetail: async (_profileId: string, _projectId: number, issueKey: string) =>
      issueDetail(issueKey),
    syncIssues: (_profileId: string, _projectId: number, _mode: string, runId: string) => {
      runIds.push(runId)
      if (!options.deferSync) return Promise.resolve(syncResult('full'))
      return new Promise<SyncResult>((resolve) => {
        finishSync = () => resolve(syncResult('full'))
      })
    },
  }
  return {
    backend: backend as unknown as Backend,
    runIds,
    /** 保留中の syncIssues を完了させる */
    finishSync: () => finishSync?.(),
  }
}

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null
/** 直近にマウントした画面のバックエンド(進捗イベント・同期完了の操作に使う) */
let fake: ReturnType<typeof createFakeBackend>

/** 指定言語でマウントする(既定は英語) */
async function mountView(options: FakeOptions = {}, locale: Language = 'en'): Promise<MountedApp> {
  fake = createFakeBackend(options)
  holder.backend = fake.backend
  const app = mountWithI18n(IssuesView, { locale })
  // onMounted の非同期連鎖(プロファイル → プロジェクト → 候補・カスタム属性)を待つ
  await flush()
  return app
}

async function mountEnglish(options: FakeOptions = {}): Promise<MountedApp> {
  return mountView(options, 'en')
}

/** ラベル(前後の空白を除いた文字列)でボタンを探す */
function button(host: HTMLElement, label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
  return found
}

/** 検索を実行して結果を確定させる */
async function search(host: HTMLElement): Promise<void> {
  button(host, 'Search').click()
  await flush()
}

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  mounted?.unmount()
  mounted = null
  holder.progressListeners = []
  // プロジェクト選択・同期状態はモジュールレベルの共有状態のため、次のテストへ持ち越さない
  // (アサート失敗で後片付けに届かなくても漏らさないよう、ここで確実に解除する)
  selectedProjectId.value = 0
  const active = activeIssueSync.value
  if (active) endIssueSync(active.runId)
  localStorage.clear()
})

describe('IssuesView の英語表示', () => {
  it('見出し・ボタン・案内が英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish())
    const text = host.textContent ?? ''

    expect(text).toContain('Issues')
    expect(text).toContain('Project')
    expect(text).toContain('Sync')
    expect(text).toContain('Search conditions')
    expect(text).toContain('Excel export')
    expect(button(host, 'Search')).toBeTruthy()
    expect(button(host, 'Clear conditions')).toBeTruthy()
    expect(button(host, 'Sync project list')).toBeTruthy()
  })

  it('初期表示に日本語が残っていない(利用者データを除く)', async () => {
    const { host } = (mounted = await mountEnglish())

    const japanese = (host.textContent ?? '')
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => hasJapanese(line))
    expect(japanese, '英語表示に日本語が残っています').toEqual([])
  })

  it('Go が返す日本語の列ラベルではなく、カタログの英語で列を表示する', async () => {
    const { host } = (mounted = await mountEnglish())
    const text = host.textContent ?? ''

    // Excel 出力の列選択(Go は「キー」「件名」「期限」を返している)
    expect(text).toContain('Key')
    expect(text).toContain('Summary')
    expect(text).toContain('Due Date')
    expect(text).not.toContain('件名')
  })

  it('検索結果の件数・ページャ・見出しが英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish())
    await search(host)
    const text = host.textContent ?? ''

    expect(text).toContain('Search results')
    expect(text).toContain(`Matches: ${TOTAL}`)
    expect(text).toContain('showing 1–2')
    expect(text).toContain('Issue key')
    expect(text).toContain('Assignee')
    expect(button(host, 'Next ›')).toBeTruthy()
    expect(button(host, '« First')).toBeTruthy()
    expect(text).toContain('Page')
    expect(hasJapanese(text)).toBe(false)
  })

  it('同期の結果は Go の日本語ラベルではなく英語のモード名で表示される', async () => {
    const { host } = (mounted = await mountEnglish())

    button(host, 'Sync').click()
    await flush()
    const text = host.textContent ?? ''

    expect(text).toContain('Full sync completed')
    expect(text).toContain('Fetched: 12')
    expect(hasJapanese(text)).toBe(false)
  })

  it('同期の進捗は画面の i18n インスタンスに従って英語で表示される', async () => {
    // formatSyncProgress をグローバル Composer(既定 ja)で解決していると、
    // 英語で開いた画面に日本語の進捗が出る(Codex レビュー指摘)。
    const { host } = (mounted = await mountEnglish({ deferSync: true }))

    button(host, 'Sync').click()
    await nextTick()
    emitSyncProgress({
      runId: fake.runIds[0],
      profileId: 'p1',
      projectId: 101,
      phase: 'fetch',
      fetched: 30,
      total: 120,
    })
    await nextTick()

    expect(host.textContent).toContain('Fetching 30 / 120')
    expect(hasJapanese(host.textContent ?? '')).toBe(false)

    // 実行中の同期を残すと、共有状態(syncState)が次のテストへ漏れる
    fake.finishSync()
    await flush()
    await flush()
    expect(issueSyncRunning.value).toBe(false)
  })

  it('データ鮮度の経過時間も画面の i18n インスタンスに従って英語で表示される', async () => {
    const lastSyncedAt = new Date(Date.now() - 5 * 60_000).toISOString()

    const { host } = (mounted = await mountEnglish({ lastSyncedAt }))

    expect(host.textContent).toContain('5 min ago')
    expect(hasJapanese(host.textContent ?? '')).toBe(false)
  })

  it('切替前に生成されたエラーメッセージも、切替後の言語で表示される', async () => {
    // t() の結果を ref に保存していると、言語を切り替えても旧言語のまま残る
    // (Codex レビュー指摘)。キー + 補間値で保持していることをここで固定する。
    const { host, i18n } = (mounted = await mountView({ listProjectsError: 'offline' }, 'ja'))
    expect(host.textContent).toContain('プロジェクト一覧の取得に失敗しました: offline')

    i18n.global.locale.value = 'en'
    await nextTick()

    // Go 由来の自由文(offline)は翻訳されないが、定型文は新しい言語になる
    expect(host.textContent).toContain('Failed to get the project list: offline')
    expect(host.textContent).not.toContain('プロジェクト一覧の取得に失敗しました')
  })

  it('検索の失敗メッセージ(ページネーション composable)も言語切替に追従する', async () => {
    const { host, i18n } = (mounted = await mountView({ searchError: 'offline' }, 'ja'))
    button(host, '検索').click()
    await flush()
    expect(host.textContent).toContain('検索に失敗しました: offline')

    i18n.global.locale.value = 'en'
    await nextTick()

    expect(host.textContent).toContain('Failed to search: offline')
    expect(host.textContent).not.toContain('検索に失敗しました')
  })

  it('課題詳細のポップアップが英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish())
    await search(host)

    host.querySelector<HTMLButtonElement>('button.issue-key')?.click()
    await flush()
    const text = host.textContent ?? ''

    expect(text).toContain('Get the latest state')
    expect(text).toContain('Comments')
    expect(text).toContain('Not set')
    expect(button(host, 'Close')).toBeTruthy()
    expect(hasJapanese(text)).toBe(false)
  })
})
