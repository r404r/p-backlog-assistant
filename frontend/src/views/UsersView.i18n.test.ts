/**
 * UsersView の表示文言の検証(設計 §3.4)。
 *
 * この画面には既存の統合テストが無かったため、ja / en の**実表示文字列**を
 * ここで確認する。特に重要なのは次の 2 点(設計 §3.1):
 *  - ロール名は Go が返す `roleName`(日本語)ではなく、生の `roleType` から
 *    フロントで翻訳する。**日本語ラベルを含む応答でも英語表示になる**こと。
 *  - Excel 出力の列ラベルも Go の日本語 label ではなくフロント翻訳を通ること。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import type { Backend, ExportColumn, UserRow } from '../lib/backend'
import type { Language } from '../lib/i18n'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import UsersView from './UsersView.vue'

/** 画面が getBackend() で受け取るバックエンド(テストごとに差し替える) */
const holder = vi.hoisted(() => ({ backend: null as unknown }))

vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => holder.backend,
    isMockBackend: () => false,
  }
})

/** Go は解決済みの日本語ロール名も返す(表示には使わないことを確かめるため入れておく) */
const USERS: UserRow[] = [
  {
    id: 1,
    userCode: 'taro',
    name: 'テスト 太郎',
    mailAddress: 'taro@example.com',
    roleType: 1,
    roleName: '管理者',
    teamNames: ['開発'],
    projectKeys: ['SAMPLE'],
    adminProjectKeys: [],
  },
  {
    id: 2,
    userCode: 'hanako',
    name: 'テスト 花子',
    mailAddress: 'hanako@example.com',
    roleType: 9, // 未知のロール種別
    roleName: '不明(9)',
    teamNames: [],
    projectKeys: [],
    adminProjectKeys: [],
  },
]

/** Go の列定義は日本語ラベルのまま返る(契約不変。設計 §3.3) */
const COLUMNS: ExportColumn[] = [
  { key: 'name', label: '名前', byDefault: true },
  { key: 'mailAddress', label: 'メールアドレス', byDefault: true },
  { key: 'cf_12', label: '独自の属性', byDefault: false },
]

function createFakeBackend() {
  return {
    getActiveProfile: async () => 'p1',
    getPermissionStatus: async () => ({
      adminAvailable: true,
      degraded: false,
      message: 'OK',
    }),
    getSyncState: async () => [{ dataKind: 'users', projectId: 0, lastSyncedAt: '' }],
    listUsers: async () => ({ rows: USERS, total: USERS.length }),
    getUserExportColumns: async () => COLUMNS,
  } as unknown as Backend
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null

async function mountUsersView(locale: Language): Promise<MountedApp> {
  holder.backend = createFakeBackend()
  const app = mountWithI18n(UsersView, { locale })
  await flush()
  return app
}

afterEach(() => {
  mounted?.unmount()
  mounted = null
})

describe('UsersView の日本語表示', () => {
  it('ロール名は roleType から解決する(未知の値は数値を添える)', async () => {
    const { host } = (mounted = await mountUsersView('ja'))
    const text = host.textContent ?? ''

    expect(text).toContain('ユーザ抽出')
    expect(text).toContain('管理者')
    expect(text).toContain('不明(9)')
  })
})

describe('UsersView の英語表示', () => {
  it('見出し・条件・結果が英語で表示される', async () => {
    const { host } = (mounted = await mountUsersView('en'))
    const text = host.textContent ?? ''

    expect(text).toContain('User Export')
    expect(text).toContain('Permission status')
    expect(text).toContain('Search conditions')
    expect(text).toContain('Keyword')
    expect(text).toContain('Results')
    expect(text).toContain('2 matched')
    expect(text).toContain('Not synced')
    expect(text).toContain('テスト 太郎') // ユーザ定義データは訳さない
    expect(text).not.toContain('抽出条件')
    expect(text).not.toContain('未同期')
  })

  it('ロール名は Go の日本語ラベルではなく roleType のフロント翻訳を出す(設計 §3.1)', async () => {
    const { host } = (mounted = await mountUsersView('en'))
    const text = host.textContent ?? ''

    expect(text).toContain('Administrator')
    expect(text).toContain('Unknown (9)')
    expect(text).not.toContain('管理者')
    expect(text).not.toContain('不明(9)')
  })

  it('ロールの絞り込みの選択肢も英語になる', async () => {
    const { host } = (mounted = await mountUsersView('en'))
    const options = Array.from(
      host.querySelectorAll<HTMLOptionElement>('#u-role option'),
    ).map((o) => (o.textContent ?? '').trim())

    expect(options).toEqual([
      'All',
      'Administrator',
      'Normal User',
      'Reporter',
      'Viewer',
      'Guest Reporter',
      'Guest Viewer',
    ])
  })

  it('列見出しと出力列のラベルもフロント翻訳を通る(カスタム属性は Go label のまま)', async () => {
    const { host } = (mounted = await mountUsersView('en'))
    const headers = Array.from(host.querySelectorAll('th')).map((th) =>
      (th.textContent ?? '').trim(),
    )
    expect(headers).toEqual([
      'Name',
      'User ID',
      'Email Address',
      'Role',
      'Teams',
      'Projects',
      'Admin Projects',
    ])

    const columnLabels = Array.from(host.querySelectorAll('.columns .checkbox')).map((el) =>
      (el.textContent ?? '').trim(),
    )
    // 固定列は翻訳し、ユーザ定義のカスタム属性(cf_12)は定義名をそのまま出す
    expect(columnLabels).toEqual(['Name', 'Email Address', '独自の属性'])
  })
})
