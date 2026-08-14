/**
 * SettingsView の「プロファイル保存でプロジェクト一覧の突合記録を無効化する」配線の
 * 統合テスト。
 *
 * 接続先 URL・API キーを変更しても ID は変わらないため、記録を残したままだと
 * 10 分間は新しい接続先に対する初回突合が省略され、前の接続先のプロジェクト
 * 一覧を表示し続けてしまう。**保存の開始前に** 記録を捨てていることを、
 * 実際に SettingsView をマウントして確認する(保存の待機中に他画面へ
 * 移動されても、移動先が古い記録で突合を省略しないようにするため)。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, type App } from 'vue'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import type { Backend, Profile, ProfileInput } from '../lib/backend'
import {
  markProjectsRefreshed,
  projectsRefreshedAt,
  resetProjectRefreshState,
  shouldSkipProjectRefreshFor,
} from '../lib/projectRefresh'
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

function createFakeBackend(options: { saveFails?: boolean; deferSave?: boolean } = {}) {
  const calls = { saveProfile: 0 }
  /** deferSave 指定時、保存の完了を外から起こすための継続 */
  let finishSave: (() => void) | null = null
  const backend = {
    listProfiles: async () => [PROFILE],
    getActiveProfile: async () => 'p1',
    setActiveProfile: async () => {},
    testConnection: async () => ({
      ok: true,
      userId: 1,
      userName: 'テスト 太郎',
      roleType: 1,
      adminAvailable: true,
      message: '',
    }),
    saveProfile: async (input: ProfileInput): Promise<Profile> => {
      calls.saveProfile++
      if (options.deferSave) {
        await new Promise<void>((resolve) => (finishSave = resolve))
      }
      if (options.saveFails) throw new Error('キーチェーンへの保存に失敗しました')
      return { ...PROFILE, id: input.id || 'p-new', spaceUrl: input.spaceUrl }
    },
    getPermissionStatus: async () => ({ adminAvailable: true, degraded: false, message: 'OK' }),
  }
  return {
    backend: backend as unknown as Backend,
    calls,
    /** 保留中の保存を完了させる */
    finishSave: () => finishSave?.(),
  }
}

async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

interface Screen {
  app: App
  host: HTMLElement
  button(label: string): HTMLButtonElement
  /** v-model の入力欄へ値を入れる(input イベントで反映させる) */
  type(selector: string, value: string): void
}

async function mountSettingsView(backend: Backend): Promise<Screen> {
  holder.backend = backend
  const { app, host } = mountWithI18n(SettingsView)
  await flush()
  return {
    app,
    host,
    button(label) {
      const found = Array.from(host.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === label,
      )
      if (!found) throw new Error(`ボタンが見つかりません: ${label}`)
      return found
    },
    type(selector, value) {
      const el = host.querySelector<HTMLInputElement>(selector)
      if (!el) throw new Error(`入力欄が見つかりません: ${selector}`)
      el.value = value
      el.dispatchEvent(new Event('input'))
    },
  }
}

/** 既存プロファイルの変更フォームを開き、接続テストまで通す */
async function openEditAndTest(screen: Screen, spaceUrl: string): Promise<void> {
  screen.button('変更').click()
  await flush()
  screen.type('#f-url', spaceUrl)
  screen.type('#f-key', 'new-api-key')
  await flush()
  screen.button('接続テスト').click()
  await flush()
}

let mounted: Screen | null = null

beforeEach(() => {
  resetProjectRefreshState()
})

afterEach(() => {
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  resetProjectRefreshState()
})

describe('SettingsView のプロファイル保存と突合記録', () => {
  it('保存に成功したら、そのプロファイルの突合記録を無効化する', async () => {
    markProjectsRefreshed('p1', Date.now())
    const fake = createFakeBackend()
    const screen = (mounted = await mountSettingsView(fake.backend))

    await openEditAndTest(screen, 'https://changed.backlog.jp')
    screen.button('保存').click()
    await flush()

    expect(fake.calls.saveProfile).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })

  it('保存の完了を待たずに無効化する(待機中に他画面へ移動しても省略されない)', async () => {
    markProjectsRefreshed('p1', Date.now())
    const fake = createFakeBackend({ deferSave: true })
    const screen = (mounted = await mountSettingsView(fake.backend))

    await openEditAndTest(screen, 'https://changed.backlog.jp')
    screen.button('保存').click()
    await flush()

    // 保存はまだ完了していないが、この時点で他画面が突合を省略してはいけない
    expect(fake.calls.saveProfile).toBe(1)
    expect(shouldSkipProjectRefreshFor('p1')).toBe(false)
    expect(projectsRefreshedAt('p1')).toBeUndefined()

    fake.finishSave()
    await flush()
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })

  it('保存に失敗しても無効化したままにする(余分な突合 1 回で済む安全側)', async () => {
    markProjectsRefreshed('p1', Date.now())
    const fake = createFakeBackend({ saveFails: true })
    const screen = (mounted = await mountSettingsView(fake.backend))

    await openEditAndTest(screen, 'https://changed.backlog.jp')
    screen.button('保存').click()
    await flush()

    expect(fake.calls.saveProfile).toBe(1)
    expect(projectsRefreshedAt('p1')).toBeUndefined()
  })

  it('他のプロファイルの記録は消さない', async () => {
    markProjectsRefreshed('p1', Date.now())
    markProjectsRefreshed('p2', Date.now())
    const fake = createFakeBackend()
    const screen = (mounted = await mountSettingsView(fake.backend))

    await openEditAndTest(screen, 'https://changed.backlog.jp')
    screen.button('保存').click()
    await flush()

    expect(projectsRefreshedAt('p2')).toBeDefined()
  })
})
