/**
 * AboutView の「表示テーマ」切替の統合テスト。
 *
 * theme.ts 単体のテスト(lib/theme.test.ts)では、画面のラジオが実際に
 * setMode へ配線されているかまでは分からない。ここでは IssuesView.stale.test.ts と
 * 同じ流儀(@vue/test-utils を入れず createApp でマウントする)で、
 * ラジオ操作 → data-theme の変化・localStorage への保存を確認する。
 *
 * あわせて「AboutView は OS 設定の購読を所有しない」(設計 §3.2)ことを、
 * 再マウントでリスナーが増えないという形で固定する。App.vue は KeepAlive 無しの
 * 動的コンポーネントなので、画面を行き来するたびに AboutView は破棄・再生成される。
 */
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import { initTheme, setMode, THEME_MODE_KEY } from '../lib/theme'
import AboutView from './AboutView.vue'

// 画面が触れるバックエンドは最小限のスタブにする(表示テーマ以外は本テストの対象外)。
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => ({
      getAppVersion: async () => ({ version: 'test' }),
      getStorageInfo: async () => ({
        configDir: '/tmp/config',
        databases: [],
        logEnabled: false,
        logPath: '',
      }),
    }),
    openExternalURL: () => {},
  }
})

/** OS の外観設定(偽の MediaQueryList)。購読数を数えるためテストファイル全体で 1 つ使う */
const listeners: ((e: { matches: boolean }) => void)[] = []
const mql = {
  matches: false,
  media: '(prefers-color-scheme: dark)',
  addEventListener(type: string, cb: (e: { matches: boolean }) => void) {
    if (type === 'change') listeners.push(cb)
  },
  removeEventListener(type: string, cb: (e: { matches: boolean }) => void) {
    if (type !== 'change') return
    const i = listeners.indexOf(cb)
    if (i >= 0) listeners.splice(i, 1)
  },
}
;(window as unknown as Record<string, unknown>).matchMedia = () => mql

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

interface Screen {
  app: App
  host: HTMLElement
  text(): string
  /** 表示テーマのラジオをモード値で探す */
  radio(mode: string): HTMLInputElement
  /** ラジオを選択する(利用者のクリックと同じく change を発火させる) */
  choose(mode: string): Promise<void>
}

async function mountAboutView(): Promise<Screen> {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const app = createApp(AboutView)
  app.mount(host)
  await flush()

  const screen: Screen = {
    app,
    host,
    text: () => host.textContent ?? '',
    radio(mode) {
      const el = host.querySelector<HTMLInputElement>(`input[type="radio"][value="${mode}"]`)
      if (!el) throw new Error(`ラジオが見つかりません: ${mode}`)
      return el
    },
    async choose(mode) {
      const el = screen.radio(mode)
      el.checked = true
      el.dispatchEvent(new Event('change'))
      await nextTick()
    },
  }
  return screen
}

let mounted: Screen | null = null

beforeAll(() => {
  // 初期化の所有者は main.ts(= ここでは initTheme の 1 回呼び出し)。
  initTheme()
})

afterEach(() => {
  mounted?.app.unmount()
  mounted?.host.remove()
  mounted = null
  // テーマはモジュールレベルの共有状態のため、次のテストへ持ち越さない
  setMode('system')
  localStorage.clear()
})

describe('AboutView の表示テーマ', () => {
  it('3 択のラジオを表示する', async () => {
    const screen = (mounted = await mountAboutView())

    expect(screen.text()).toContain('表示テーマ')
    expect(screen.radio('system')).toBeTruthy()
    expect(screen.radio('light')).toBeTruthy()
    expect(screen.radio('dark')).toBeTruthy()
    expect(screen.text()).toContain('システムに合わせる')
    expect(screen.text()).toContain('OS の外観設定に追従します')
    expect(screen.text()).toContain('タイトルバー')
  })

  it('現在のモードが選択済みとして表示される', async () => {
    setMode('dark')
    const screen = (mounted = await mountAboutView())

    expect(screen.radio('dark').checked).toBe(true)
    expect(screen.radio('system').checked).toBe(false)
  })

  it('ダークを選ぶと即時に適用され、保存される', async () => {
    const screen = (mounted = await mountAboutView())

    await screen.choose('dark')

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.style.colorScheme).toBe('dark')
    expect(localStorage.getItem(THEME_MODE_KEY)).toBe('dark')
  })

  it('システムに合わせるへ戻すと OS 設定から解決し直す', async () => {
    const screen = (mounted = await mountAboutView())
    await screen.choose('dark')

    mql.matches = true
    await screen.choose('system')

    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem(THEME_MODE_KEY)).toBe('system')

    // system のままなら OS 設定の変更に追従する
    mql.matches = false
    for (const cb of [...listeners]) cb({ matches: false })
    await nextTick()
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('再マウントしても OS 設定のリスナーが増えない(購読を所有しない)', async () => {
    setMode('system')
    const before = listeners.length
    expect(before).toBe(1)

    for (let i = 0; i < 3; i++) {
      const screen = await mountAboutView()
      screen.app.unmount()
      screen.host.remove()
    }

    expect(listeners.length).toBe(before)
  })
})
