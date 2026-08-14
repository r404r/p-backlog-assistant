/**
 * BulkUpdateView の表示言語の統合テスト(設計 §3.4)。
 *
 * カタログの静的検査(localeCatalog.test.ts)は「キーが揃っているか」しか見ず、
 * ハードコード検査(noHardcodedText.test.ts)は「生の文字列が残っていないか」しか
 * 見ない。どちらも **画面に実際に何が出るか**は保証しないため、ここでは
 * ja / en で実マウントして表示文字列を検証する
 * (実装と同じキーを参照するアサートはトートロジーになるため避ける。設計 §3.4)。
 *
 * 重点は 2 つ:
 *  1. 画面全体が選択言語で表示されること(日本語が残らないこと)
 *  2. **処理区分・行状態は生の機械値(action / status)から翻訳**されること。
 *     Go は互換のため日本語ラベル(actionLabel / statusLabel)も返し続けるが、
 *     英語表示ではそれを使わない(設計 §3.1)。応答に日本語ラベルを混ぜた
 *     フェイクバックエンドで、英語が出ることを確認する。
 *
 * マウント方式(@vue/test-utils を使わない理由)は IssuesView.stale.test.ts の
 * 冒頭コメントを参照。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, type App } from 'vue'

import { mountWithI18n } from '../lib/testing/mountWithI18n'
import type {
  Backend,
  BulkImportResult,
  BulkJobRow,
  BulkJobRowDetail,
  Project,
} from '../lib/backend'
import type { Language } from '../lib/i18n'
import { selectedProjectId } from '../lib/projectSelection'
import BulkUpdateView from './BulkUpdateView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

// Wails ランタイムに触れる入口だけを差し替える(整形ヘルパ等は実物のまま)。
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
    onBulkProgress: () => () => {},
    onSyncProgress: () => () => {},
  }
})

const PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル', lastSyncedAt: '', syncStateUnknown: false },
]

/**
 * 取り込み結果。**actionLabel には Go が返す日本語ラベルを入れておく**
 * (英語表示でこれが出ないこと = 生の action から翻訳していることの証明)。
 */
const IMPORT_RESULT: BulkImportResult = {
  jobId: 7,
  projectId: 101,
  totalRows: 3,
  creates: 1,
  updates: 1,
  skips: 1,
  valid: true,
  warnings: [],
  errors: [],
  previews: [
    {
      rowNo: 2,
      action: 'create',
      actionLabel: '新規追加',
      issueKey: '',
      summary: '新しい課題',
      changes: [],
      conflictWarning: false,
    },
    {
      rowNo: 3,
      action: 'update',
      actionLabel: '更新',
      issueKey: 'SAMPLE-1',
      summary: '既存の課題',
      changes: [],
      conflictWarning: true,
    },
    {
      rowNo: 4,
      action: 'skip',
      actionLabel: '変更なし',
      issueKey: 'SAMPLE-2',
      summary: '変わらない課題',
      changes: [],
      conflictWarning: false,
    },
  ],
}

const JOBS: BulkJobRow[] = [
  {
    jobId: 7,
    projectId: 101,
    kind: 'bulk_update',
    createdAt: '2026-08-14T00:00:00Z',
    status: 'done',
    total: 3,
    done: 2,
    failed: 0,
    pending: 0,
    sending: 0,
    conflict: 1,
    skipped: 1,
  },
]

/** 行明細。statusLabel も Go が返す日本語ラベルを入れておく(上記と同じ理由) */
const JOB_ROWS: BulkJobRowDetail[] = [
  { rowNo: 2, issueKey: 'SAMPLE-1', status: 'done', statusLabel: '完了', resultIssueId: 0, error: '' },
  { rowNo: 3, issueKey: '', status: 'conflict', statusLabel: '競合', resultIssueId: 0, error: '' },
]

function createFakeBackend(): Backend {
  const backend = {
    getActiveProfile: async () => 'p1',
    listProjects: async () => PROJECTS,
    getMasterData: async () => ({
      issueTypes: [],
      priorities: [{ id: 3, name: '中' }],
      statuses: [],
      customFields: [],
    }),
    listFilterOptions: async () => ({ statuses: [], assignees: [] }),
    listBulkJobs: async () => JOBS,
    getBulkJobRows: async () => JOB_ROWS,
    importBulkFile: async () => IMPORT_RESULT,
  }
  return backend as unknown as Backend
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
  /** 表示中のボタンをラベル(前後の空白を除いた文字列)で探して押す */
  click(label: string): Promise<void>
  /** セレクタに一致する要素の表示文字列(バッジの実表示を見るために使う) */
  texts(selector: string): string[]
}

async function mountBulkUpdateView(locale: Language): Promise<Screen> {
  holder.backend = createFakeBackend()
  const { app, host } = mountWithI18n(BulkUpdateView, { locale })
  // onMounted の非同期連鎖(プロファイル → プロジェクト → マスタ・候補・履歴)を待つ
  await flush()

  return {
    app,
    host,
    text: () => host.textContent ?? '',
    async click(label) {
      const found = Array.from(host.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === label,
      )
      if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
      found.click()
      await flush()
    },
    texts: (selector) =>
      Array.from(host.querySelectorAll(selector)).map((el) => (el.textContent ?? '').trim()),
  }
}

let mounted: Screen | null = null

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => {
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  // プロジェクト選択はモジュールレベルの共有状態のため、次のテストへ持ち越さない
  selectedProjectId.value = 0
  localStorage.clear()
})

describe('BulkUpdateView の日本語表示', () => {
  it('見出し・操作の文言が日本語で表示される', async () => {
    const screen = (mounted = await mountBulkUpdateView('ja'))

    expect(screen.text()).toContain('一括更新・追加')
    expect(screen.text()).toContain('① テンプレート出力')
    expect(screen.text()).toContain('② Excel を取り込む')
    expect(screen.text()).toContain('⑥ ジョブ履歴')
    expect(screen.text()).toContain('出力する課題の条件')
  })

  it('処理区分・行状態が日本語で表示される', async () => {
    const screen = (mounted = await mountBulkUpdateView('ja'))

    await screen.click('Excel を取り込む')
    expect(screen.texts('.badge.create')).toEqual(['新規追加'])
    expect(screen.texts('.badge.update')).toEqual(['更新'])
    expect(screen.texts('.badge.skip')).toEqual(['変更なし'])

    await screen.click('明細を表示')
    expect(screen.texts('.detail-table .badge')).toEqual(['完了', '競合'])
  })
})

describe('BulkUpdateView の英語表示', () => {
  it('見出し・操作の文言が英語で表示される', async () => {
    const screen = (mounted = await mountBulkUpdateView('en'))

    expect(screen.text()).toContain('Bulk Update & Add')
    expect(screen.text()).toContain('① Export template')
    expect(screen.text()).toContain('② Import Excel')
    expect(screen.text()).toContain('⑥ Job history')
    expect(screen.text()).not.toContain('一括更新・追加')
    expect(screen.text()).not.toContain('出力する課題の条件')
  })

  it('処理区分は日本語ラベルを含む応答でも英語で表示される', async () => {
    const screen = (mounted = await mountBulkUpdateView('en'))

    await screen.click('Import Excel')

    // 応答の actionLabel(= Go が解決した日本語)ではなく action から翻訳する
    expect(screen.texts('.badge.create')).toEqual(['Add new'])
    expect(screen.texts('.badge.update')).toEqual(['Update'])
    expect(screen.texts('.badge.skip')).toEqual(['No change'])
    expect(screen.text()).not.toContain('新規追加')
    expect(screen.text()).not.toContain('変更なし')
    // 集計行(行データを持たない見出し)も同じ対応表から組み立てる
    expect(screen.text()).toContain('Imported 3 rows / Add new 1 / Update 1 / No change 1')
  })

  it('行状態は日本語ラベルを含む応答でも英語で表示される', async () => {
    const screen = (mounted = await mountBulkUpdateView('en'))

    await screen.click('Show details')

    // 応答の statusLabel(= Go が解決した日本語)ではなく status から翻訳する
    expect(screen.texts('.detail-table .badge')).toEqual(['Done', 'Conflict'])
    expect(screen.text()).not.toContain('完了')
    expect(screen.text()).not.toContain('競合')
  })
})
