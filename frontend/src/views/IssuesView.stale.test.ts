/**
 * IssuesView の「検索結果の stale 化」配線の統合テスト。
 *
 * issuePagination.test.ts は composable 単体(markStale / markStaleForProject を
 * 直接呼ぶ)しか見ていないため、画面側の接続(同期完了の watcher・詳細更新の
 * finally)が外れても気づけない。ここでは **実際の IssuesView をマウントし、
 * DOM 操作と共有状態(syncState)経由で** stale になることを確認する。
 *
 * 方式(@vue/test-utils を入れずに createApp でマウントする理由):
 * このテストが必要とするのは「マウント・DOM のクリック・テキストの確認」だけで、
 * Vue の createApp と happy-dom の DOM API で足りる。@vue/test-utils は
 * js-beautify / glob 等 30 以上の依存を持ち込むため、意図的に軽く保っている
 * 現在の開発ツール構成(happy-dom を選んだ経緯は vite.config.ts のコメント参照)に
 * 対して割に合わない。
 *
 * GUI そのもの(見た目・レイアウト)は引き続き手動確認の対象で、
 * ここで固定するのは「配線されているか」という状態遷移の部分だけ。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import type {
  Backend,
  IssueDetail,
  IssueQuery,
  IssueRow,
  IssueSearchResult,
  Project,
} from '../lib/backend'
import { selectedProjectId } from '../lib/projectSelection'
import { beginIssueSync, endIssueSync } from '../lib/syncState'
import IssuesView from './IssuesView.vue'

/**
 * 画面が getBackend() で受け取るバックエンド(テストごとに差し替える)。
 * vi.mock のファクトリはホイストされるため、vi.hoisted で先に入れ物を作る。
 */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

// Wails ランタイムに触れる 3 つの入口だけを差し替える(整形ヘルパ等は実物のまま)。
// syncState.ts も onSyncProgress をこのモジュールから取るため、購読は no-op になる。
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onSyncProgress: () => () => {},
  }
})

/** テストで使うプロジェクト(既定の選択は先頭の 101) */
const PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル', lastSyncedAt: '', syncStateUnknown: false },
  { id: 102, projectKey: 'OTHER', name: 'その他', lastSyncedAt: '', syncStateUnknown: false },
]

/** 画面が 1 ページ 200 件で取得するため、ページャを出すには総件数を 200 超にする */
const TOTAL = 450

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

function row(issueKey: string): IssueRow {
  return {
    issueKey,
    summary: `${issueKey} の件名`,
    statusName: '未対応',
    assigneeName: '',
    issueTypeName: 'タスク',
    priorityName: '中',
    created: '',
    updated: '',
    dueDate: '',
    customFields: {},
  }
}

/** 検索結果の行(件数は総件数と揃える必要がないため、DOM を軽くする程度に少なくする) */
function rowsOf(n: number, prefix: string): IssueRow[] {
  return Array.from({ length: n }, (_, i) => row(`${prefix}-${i + 1}`))
}

function searchResult(rows: IssueRow[], total = TOTAL): IssueSearchResult {
  return { rows, total, unverifiable: 0 }
}

function issueDetail(issueKey: string): IssueDetail {
  return {
    issueKey,
    summary: `${issueKey} の件名`,
    description: '詳細',
    statusName: '未対応',
    assigneeName: '',
    issueTypeName: 'タスク',
    priorityName: '中',
    created: '',
    updated: '',
    dueDate: '',
    parentIssueKey: '',
    customFields: [],
    fetchedAt: '2026-08-14T00:00:00Z',
    comments: [],
    commentsFetchedAt: '',
    commentsHistoryOnly: 0,
    commentsTruncated: false,
    warnings: [],
  }
}

/** 検索・詳細再取得の応答を外から制御できるバックエンド */
function createFakeBackend() {
  const searchCalls: { query: IssueQuery; deferred: Deferred<IssueSearchResult> }[] = []
  const refreshCalls: { projectId: number; deferred: Deferred<IssueDetail> }[] = []
  const backend = {
    getActiveProfile: async () => 'p1',
    listProfiles: async () => [
      { id: 'p1', name: 'テスト', spaceUrl: 'https://example.backlog.jp', lastUserName: '', lastUserId: 1 },
    ],
    getIssueExportColumns: async () => [{ key: 'issueKey', label: '課題キー', byDefault: true }],
    listProjects: async () => PROJECTS,
    syncProjects: async () => {},
    listFilterOptions: async () => ({ statuses: [], assignees: [] }),
    getMasterData: async () => ({
      issueTypes: [],
      priorities: [],
      statuses: [],
      customFields: [],
    }),
    searchIssues: (_profileId: string, query: IssueQuery) => {
      const d = deferred<IssueSearchResult>()
      searchCalls.push({ query, deferred: d })
      return d.promise
    },
    getIssueDetail: async (_profileId: string, _projectId: number, issueKey: string) =>
      issueDetail(issueKey),
    refreshIssueDetail: (_profileId: string, projectId: number, _issueKey: string) => {
      const d = deferred<IssueDetail>()
      refreshCalls.push({ projectId, deferred: d })
      return d.promise
    },
  }
  return { backend: backend as unknown as Backend, searchCalls, refreshCalls }
}

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

/** マウントした画面に対する最小限の操作 */
interface Screen {
  app: App
  host: HTMLElement
  /** 画面全体のテキスト(表示されている案内・件数の確認に使う) */
  text(): string
  /** 表示中のボタンをラベル(前後の空白を除いた文字列)で探す */
  button(label: string): HTMLButtonElement
  /** ラベルのボタンが表示されているか */
  hasButton(label: string): boolean
  /** セレクタで要素を探す(ページ番号欄・課題キー等) */
  find<E extends HTMLElement>(selector: string): E
}

async function mountIssuesView(backend: Backend): Promise<Screen> {
  holder.backend = backend
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(IssuesView)
  app.mount(host)
  // onMounted の非同期連鎖(プロファイル → プロジェクト → 候補・カスタム属性)を待つ
  await flush()

  function buttons(): HTMLButtonElement[] {
    return Array.from(host.querySelectorAll('button'))
  }
  return {
    app,
    host,
    text: () => host.textContent ?? '',
    button(label) {
      const found = buttons().find((b) => (b.textContent ?? '').trim() === label)
      if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
      return found
    },
    hasButton: (label) => buttons().some((b) => (b.textContent ?? '').trim() === label),
    find<E extends HTMLElement>(selector: string): E {
      const el = host.querySelector<E>(selector)
      if (!el) throw new Error(`要素が見つかりません: ${selector}`)
      return el
    },
  }
}

/** 検索を実行し、1 ページ目の結果を確定させる */
async function searchAndConfirm(
  screen: Screen,
  fake: ReturnType<typeof createFakeBackend>,
  prefix = 'SAMPLE',
): Promise<void> {
  screen.button('検索').click()
  await nextTick()
  fake.searchCalls[fake.searchCalls.length - 1].deferred.resolve(
    searchResult(rowsOf(3, prefix)),
  )
  await flush()
}

/** 詳細ポップアップを開き、「最新の状態を取得」を実行中にする */
async function startDetailRefresh(screen: Screen): Promise<void> {
  screen.find<HTMLButtonElement>('button.issue-key').click()
  await flush()
  screen.button('最新の状態を取得').click()
  await nextTick()
}

/** 課題同期の開始 → 完了(共有状態 issueSyncing の true→false 遷移)を再現する */
async function runIssueSync(runId = 'run-1'): Promise<void> {
  beginIssueSync('p1', 101, runId)
  // 実際の同期と同じく、開始と完了の間に描画のタイミングを挟む
  // (同じティック内で開始・完了すると値が元に戻り、watcher が呼ばれない)
  await nextTick()
  endIssueSync(runId)
  await flush()
}

/** stale の案内文(表示中の結果が古くなったときだけ出る) */
const STALE_NOTICE = 'データが更新されました。最新の結果を見るには再検索してください。'

let mounted: Screen | null = null

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  // プロジェクト選択・同期状態はモジュールレベルの共有状態のため、次のテストへ持ち越さない
  selectedProjectId.value = 0
  localStorage.clear()
})

describe('IssuesView の課題同期による stale 化', () => {
  it('同期の完了で stale になり、ページャが無効化される(結果は残る)', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)

    // 前提: 結果が出ていて、ページャを操作できる
    expect(screen.text()).toContain(`該当 ${TOTAL} 件`)
    expect(screen.button('次へ ›').disabled).toBe(false)
    expect(screen.text()).not.toContain(STALE_NOTICE)

    await runIssueSync()

    expect(screen.text()).toContain(STALE_NOTICE)
    expect(screen.button('次へ ›').disabled).toBe(true)
    expect(screen.button('最後 »').disabled).toBe(true)
    expect(screen.find<HTMLInputElement>('#i-page').disabled).toBe(true)
    // 結果自体は消さない(再検索を促すだけ)
    expect(screen.text()).toContain(`該当 ${TOTAL} 件`)
    expect(screen.text()).toContain('SAMPLE-1')
  })

  it('ページ取得中に同期が完了したら、進行中の応答を破棄する', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)

    // 2 ページ目の取得を開始する(応答はまだ返さない)
    screen.button('次へ ›').click()
    await nextTick()
    expect(fake.searchCalls).toHaveLength(2)
    expect(fake.searchCalls[1].query.offset).toBe(200)

    await runIssueSync()
    // 失効した後に 2 ページ目の応答が届く
    fake.searchCalls[1].deferred.resolve(searchResult(rowsOf(3, 'PAGE2')))
    await flush()

    // 表示は 1 ページ目のまま(古い応答で上書きされない)
    expect(screen.text()).toContain('SAMPLE-1')
    expect(screen.text()).not.toContain('PAGE2-')
    expect(screen.text()).toContain('1〜3 件目を表示')
    expect(screen.find<HTMLInputElement>('#i-page').value).toBe('1')
    expect(screen.text()).toContain(STALE_NOTICE)
  })

  it('検索前の同期完了では案内を出さない(確定済みの結果が無いため)', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))

    await runIssueSync()

    expect(screen.text()).not.toContain(STALE_NOTICE)
  })
})

describe('IssuesView の詳細更新による stale 化', () => {
  it('「最新の状態を取得」が成功したら stale になる', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)
    await startDetailRefresh(screen)

    expect(screen.text()).not.toContain(STALE_NOTICE)
    fake.refreshCalls[0].deferred.resolve(issueDetail('SAMPLE-1'))
    await flush()

    expect(screen.text()).toContain(STALE_NOTICE)
  })

  it('「最新の状態を取得」が失敗しても stale になる(課題本体だけ更新され得るため)', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)
    await startDetailRefresh(screen)

    fake.refreshCalls[0].deferred.reject(new Error('通信に失敗しました'))
    await flush()

    expect(screen.text()).toContain('最新の状態を取得できませんでした')
    expect(screen.text()).toContain(STALE_NOTICE)
  })

  it('更新中に詳細ポップアップを閉じても stale になる(モーダルの世代に依存しない)', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)
    await startDetailRefresh(screen)

    // 取得の完了を待たずに閉じる(閉じても DB は変わり得る)
    screen.button('閉じる').click()
    await flush()
    expect(screen.hasButton('最新の状態を取得')).toBe(false)

    fake.refreshCalls[0].deferred.resolve(issueDetail('SAMPLE-1'))
    await flush()

    expect(screen.text()).toContain(STALE_NOTICE)
  })

  it('プロジェクト切替後の結果は、切替前に始めた更新では stale にしない', async () => {
    const fake = createFakeBackend()
    const screen = (mounted = await mountIssuesView(fake.backend))
    await searchAndConfirm(screen, fake)
    // プロジェクト 101 を起点に更新を開始する
    await startDetailRefresh(screen)
    expect(fake.refreshCalls[0].projectId).toBe(101)

    // プロジェクトを切り替え、切替先で検索し直す(結果はプロジェクト 102 のもの)
    const select = screen.find<HTMLSelectElement>('#i-project')
    select.value = '102'
    select.dispatchEvent(new Event('change'))
    await flush()
    expect(selectedProjectId.value).toBe(102)
    await searchAndConfirm(screen, fake, 'OTHER')
    expect(fake.searchCalls[fake.searchCalls.length - 1].query.projectId).toBe(102)

    // 切替前(プロジェクト 101 起点)の更新が完了しても、新しい結果は stale にしない
    fake.refreshCalls[0].deferred.resolve(issueDetail('SAMPLE-1'))
    await flush()
    expect(screen.text()).not.toContain(STALE_NOTICE)
    expect(screen.text()).toContain('OTHER-1')

    // 切替後のプロジェクトを起点にした更新なら stale にする(一致判定が働いている確認)
    await startDetailRefresh(screen)
    expect(fake.refreshCalls[1].projectId).toBe(102)
    fake.refreshCalls[1].deferred.resolve(issueDetail('OTHER-1'))
    await flush()
    expect(screen.text()).toContain(STALE_NOTICE)
  })
})
