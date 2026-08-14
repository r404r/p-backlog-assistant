/**
 * SettingsView の英語表示の検証(設計 §3.4)。
 *
 * ja(既定)の表示は SettingsView.projectRefresh.test.ts が実表示の文言
 * (「変更」「接続テスト」「保存」)でボタンを引いているため、そちらで担保される。
 * ここは **en で描画したときに実際に英語が出る**ことを、実表示文字列で確認する
 * (実装と同じキーを参照するアサートはトートロジーになるため避ける)。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import type { Backend, Profile } from '../lib/backend'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import SettingsView from './SettingsView.vue'

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

const PROFILE: Profile = {
  id: 'p1',
  name: 'テスト',
  spaceUrl: 'https://example.backlog.jp',
  lastUserName: 'テスト 太郎',
  lastUserId: 1,
}

function createFakeBackend(profiles: Profile[] = [PROFILE]) {
  return {
    listProfiles: async () => profiles,
    getActiveProfile: async () => (profiles.length > 0 ? profiles[0].id : ''),
    setActiveProfile: async () => {},
    testConnection: async () => ({
      ok: true,
      userId: 1,
      userName: 'テスト 太郎',
      roleType: 1,
      adminAvailable: true,
      message: '',
    }),
    getPermissionStatus: async () => ({ adminAvailable: true, degraded: false, message: 'OK' }),
  } as unknown as Backend
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null

async function mountEnglish(backend: Backend): Promise<MountedApp> {
  holder.backend = backend
  const app = mountWithI18n(SettingsView, { locale: 'en' })
  await flush()
  return app
}

/** ラベルでボタンを引く(見つからなければ失敗させる) */
function button(host: HTMLElement, label: string): HTMLButtonElement {
  const found = Array.from(host.querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === label,
  )
  if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
  return found
}

afterEach(() => {
  mounted?.unmount()
  mounted = null
})

describe('SettingsView の英語表示', () => {
  it('見出し・一覧・操作が英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend()))
    const text = host.textContent ?? ''

    expect(text).toContain('Connection Settings')
    expect(text).toContain('Profiles')
    expect(text).toContain('Space URL')
    expect(text).toContain('Connected user')
    expect(text).toContain('In use')
    expect(button(host, 'Edit')).toBeTruthy()
    expect(button(host, 'Delete')).toBeTruthy()
    expect(button(host, 'Add new')).toBeTruthy()
    // 日本語が混ざらないこと(ユーザ定義データのプロファイル名は除く)
    expect(text).not.toContain('プロファイル一覧')
    expect(text).not.toContain('接続中')
  })

  it('初回起動のウィザードも英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend([])))
    const text = host.textContent ?? ''

    expect(text).toContain('Welcome')
    expect(text).toContain('There is no setting to connect to Backlog yet.')
    expect(button(host, 'Register a profile')).toBeTruthy()
    expect(text).not.toContain('ようこそ')
  })

  it('登録フォームのラベル・入力例が英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend([])))
    button(host, 'Register a profile').click()
    await flush()
    const text = host.textContent ?? ''

    expect(text).toContain('Register a new profile')
    expect(text).toContain('Profile name')
    expect(text).toContain('API key')
    expect(host.querySelector<HTMLInputElement>('#f-name')?.placeholder).toBe(
      'e.g. For the dev team',
    )
    expect(host.querySelector<HTMLInputElement>('#f-key')?.placeholder).toBe('Enter the API key')
    expect(button(host, 'Test connection')).toBeTruthy()
    expect(button(host, 'Cancel')).toBeTruthy()
  })

  it('接続テストの結果も英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend([])))
    button(host, 'Register a profile').click()
    await flush()
    const url = host.querySelector<HTMLInputElement>('#f-url')!
    url.value = 'https://example.backlog.jp'
    url.dispatchEvent(new Event('input'))
    const key = host.querySelector<HTMLInputElement>('#f-key')!
    key.value = 'api-key'
    key.dispatchEvent(new Event('input'))
    await flush()

    button(host, 'Test connection').click()
    await flush()
    const text = host.textContent ?? ''

    expect(text).toContain('Connection test succeeded')
    expect(text).toContain('User name: テスト 太郎') // ユーザ定義データは訳さない
    expect(text).toContain('Administrator features are expected to be available')
    expect(text).not.toContain('接続テスト成功')
  })

  it('削除の確認ダイアログも英語で表示される', async () => {
    const { host } = (mounted = await mountEnglish(createFakeBackend()))
    button(host, 'Delete').click()
    await flush()
    const text = host.textContent ?? ''

    expect(text).toContain('Delete the profile')
    expect(text).toContain('The profile "テスト" (https://example.backlog.jp) will be deleted.')
    expect(text).toContain('Also delete the local data (DB) (recommended)')
    expect(text).not.toContain('プロファイルの削除')
  })
})
