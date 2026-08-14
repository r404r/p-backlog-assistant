/**
 * テーマ制御(lib/theme.ts)と起動時 prepaint スクリプト(public/prepaint.js)の検証。
 *
 * theme.ts はモジュール単位のシングルトン(モード ref・MediaQueryList 購読)を持つため、
 * テストごとに vi.resetModules() + 動的 import で新しいインスタンスを読み込む。
 *
 * prepaint は「手で複製したコピー」ではなく **public/prepaint.js の実ファイルを読み込んで
 * 実行**して検証する(設計 §3.3-1)。実ファイルとテストの乖離を防ぐため。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type ThemeModule = typeof import('./theme')

/** シングルトン状態を持ち越さないよう、テストごとに新しいモジュールを読み込む */
async function loadTheme(): Promise<ThemeModule> {
  vi.resetModules()
  return await import('./theme')
}

// ---------------------------------------------------------------------------
// テスト用の偽 matchMedia / 偽 Wails ランタイム
// ---------------------------------------------------------------------------

interface FakeMedia {
  /** window.matchMedia が返すオブジェクト(MediaQueryList のつもり) */
  mql: Record<string, unknown>
  /** 登録されているリスナー(冪等性の確認に使う) */
  listeners: ((e: { matches: boolean }) => void)[]
  /** matchMedia が呼ばれた回数 */
  queries: string[]
  /** OS の設定変更を再現する */
  emit(matches: boolean): void
}

/** 偽の MediaQueryList の作り分け(環境差・古い WebView の再現) */
interface FakeMediaOptions {
  /**
   * 備える購読 API。
   * modern = addEventListener のみ / legacy = addListener のみ / both = 両方
   * (both は「新しい API はあるが使えない」実装の再現に使う)。
   */
  api?: 'modern' | 'legacy' | 'both'
  /** addEventListener の呼び出しが例外になる(登録できない実装) */
  modernThrows?: boolean
  /** 解除 API(removeEventListener / removeListener)を持たない */
  noRemove?: boolean
}

/**
 * 偽の MediaQueryList を作る。
 * 設計 §3.2 の「addEventListener → addListener フォールバックと対の解除」を、
 * 実装が取り得る環境ごとに再現するためのもの。
 */
function createFakeMedia(matches: boolean, opts: FakeMediaOptions = {}): FakeMedia {
  const listeners: ((e: { matches: boolean }) => void)[] = []
  const queries: string[] = []
  const api = opts.api ?? 'modern'
  const mql: Record<string, unknown> = {
    matches,
    media: '(prefers-color-scheme: dark)',
  }
  const add = (cb: (e: { matches: boolean }) => void) => {
    listeners.push(cb)
  }
  const remove = (cb: (e: { matches: boolean }) => void) => {
    const i = listeners.indexOf(cb)
    if (i >= 0) listeners.splice(i, 1)
  }

  if (api === 'modern' || api === 'both') {
    mql.addEventListener = opts.modernThrows
      ? () => {
          throw new Error('addEventListener は利用できません')
        }
      : (type: string, cb: (e: { matches: boolean }) => void) => {
          if (type === 'change') add(cb)
        }
    if (!opts.noRemove) {
      mql.removeEventListener = (type: string, cb: (e: { matches: boolean }) => void) => {
        if (type === 'change') remove(cb)
      }
    }
  }
  if (api === 'legacy' || api === 'both') {
    mql.addListener = add
    if (!opts.noRemove) mql.removeListener = remove
  }

  return {
    mql,
    listeners,
    queries,
    emit(next: boolean) {
      mql.matches = next
      for (const cb of [...listeners]) cb({ matches: next })
    },
  }
}

/** window.matchMedia を偽物に差し替える */
function installMatchMedia(fake: FakeMedia): void {
  ;(window as unknown as Record<string, unknown>).matchMedia = (query: string) => {
    fake.queries.push(query)
    return fake.mql
  }
}

/** window.matchMedia を無くす(古い WebView・非対応環境の再現) */
function removeMatchMedia(): void {
  ;(window as unknown as Record<string, unknown>).matchMedia = undefined
}

/** window.matchMedia の呼び出し自体が例外になる環境の再現 */
function installThrowingMatchMedia(): void {
  ;(window as unknown as Record<string, unknown>).matchMedia = () => {
    throw new Error('matchMedia は利用できません')
  }
}

interface FakeRuntime {
  /** 呼ばれたタイトルバー API の順序 */
  titleBar: string[]
  /** WindowSetBackgroundColour の引数 */
  background: number[][]
}

/** window.runtime(Wails ランタイム)を偽物に差し替える */
function installRuntime(overrides: Record<string, unknown> = {}): FakeRuntime {
  const titleBar: string[] = []
  const background: number[][] = []
  const rt: Record<string, unknown> = {
    WindowSetSystemDefaultTheme: () => titleBar.push('system'),
    WindowSetLightTheme: () => titleBar.push('light'),
    WindowSetDarkTheme: () => titleBar.push('dark'),
    WindowSetBackgroundColour: (r: number, g: number, b: number, a: number) =>
      background.push([r, g, b, a]),
    ...overrides,
  }
  ;(window as unknown as Record<string, unknown>).runtime = rt
  return { titleBar, background }
}

/** documentElement に付いたテーマ指定 */
function currentAttribute(): { theme: string | null; colorScheme: string } {
  return {
    theme: document.documentElement.getAttribute('data-theme'),
    colorScheme: document.documentElement.style.colorScheme,
  }
}

const originalMatchMedia = (window as unknown as Record<string, unknown>).matchMedia
const originalLocalStorage = window.localStorage

/** window.localStorage を差し替える(happy-dom の実体は Proxy なので spyOn では戻せない) */
function replaceLocalStorage(value: Storage): void {
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    writable: true,
    value,
  })
}

/**
 * 指定したメソッドだけが例外を投げる localStorage を差し込む。
 * WebView の設定によっては参照・保存が例外になり得るため、その環境の再現に使う。
 */
function breakLocalStorage(method: 'getItem' | 'setItem'): void {
  const real = originalLocalStorage
  const stub: Record<string, unknown> = {
    getItem: (k: string) => real.getItem(k),
    setItem: (k: string, v: string) => real.setItem(k, v),
    removeItem: (k: string) => real.removeItem(k),
    clear: () => real.clear(),
  }
  stub[method] = () => {
    throw new Error('localStorage は利用できません')
  }
  replaceLocalStorage(stub as unknown as Storage)
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.style.colorScheme = ''
})

afterEach(() => {
  vi.restoreAllMocks()
  replaceLocalStorage(originalLocalStorage)
  ;(window as unknown as Record<string, unknown>).matchMedia = originalMatchMedia
  delete (window as unknown as Record<string, unknown>).runtime
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.style.colorScheme = ''
})

// ---------------------------------------------------------------------------
// 純関数
// ---------------------------------------------------------------------------

describe('parseStoredThemeMode', () => {
  it('保存済みの 3 値はそのまま返す', async () => {
    const { parseStoredThemeMode } = await loadTheme()
    expect(parseStoredThemeMode('system')).toBe('system')
    expect(parseStoredThemeMode('light')).toBe('light')
    expect(parseStoredThemeMode('dark')).toBe('dark')
  })

  it('未保存・不正値は system に落とす', async () => {
    const { parseStoredThemeMode } = await loadTheme()
    expect(parseStoredThemeMode(null)).toBe('system')
    expect(parseStoredThemeMode('')).toBe('system')
    expect(parseStoredThemeMode('Dark')).toBe('system')
    expect(parseStoredThemeMode('purple')).toBe('system')
  })
})

describe('resolveTheme', () => {
  it('モードと OS 設定の 6 組合せを解決する', async () => {
    const { resolveTheme } = await loadTheme()
    expect(resolveTheme('system', false)).toBe('light')
    expect(resolveTheme('system', true)).toBe('dark')
    expect(resolveTheme('light', false)).toBe('light')
    expect(resolveTheme('light', true)).toBe('light')
    expect(resolveTheme('dark', false)).toBe('dark')
    expect(resolveTheme('dark', true)).toBe('dark')
  })
})

describe('applyTheme', () => {
  it('data-theme と style.colorScheme を設定する', async () => {
    const { applyTheme } = await loadTheme()

    applyTheme('dark')
    expect(currentAttribute()).toEqual({ theme: 'dark', colorScheme: 'dark' })

    applyTheme('light')
    expect(currentAttribute()).toEqual({ theme: 'light', colorScheme: 'light' })
  })

  it('ウィンドウ背景色をテーマの背景色へ同期する', async () => {
    const runtime = installRuntime()
    const { applyTheme, THEME_BACKGROUND_RGB } = await loadTheme()

    applyTheme('dark')

    expect(runtime.background).toEqual([[...THEME_BACKGROUND_RGB.dark, 255]])
  })

  it('Wails ランタイムが無くてもテーマ適用は成功する', async () => {
    const { applyTheme } = await loadTheme()

    expect(() => applyTheme('dark')).not.toThrow()
    expect(currentAttribute().theme).toBe('dark')
  })

  it('ランタイムの呼び出しが例外を投げてもテーマ適用は成功する', async () => {
    installRuntime({
      WindowSetBackgroundColour: () => {
        throw new Error('ランタイムの呼び出しに失敗しました')
      },
    })
    const { applyTheme } = await loadTheme()

    expect(() => applyTheme('dark')).not.toThrow()
    expect(currentAttribute().theme).toBe('dark')
  })
})

// ---------------------------------------------------------------------------
// initTheme / setMode
// ---------------------------------------------------------------------------

describe('initTheme', () => {
  it('保存済みモードを復元して適用する', async () => {
    localStorage.setItem('ba.themeMode', 'dark')
    installMatchMedia(createFakeMedia(false))
    const { initTheme, useTheme } = await loadTheme()

    initTheme()

    expect(useTheme().mode.value).toBe('dark')
    expect(currentAttribute()).toEqual({ theme: 'dark', colorScheme: 'dark' })
  })

  it('未保存なら system として OS 設定から解決する', async () => {
    installMatchMedia(createFakeMedia(true))
    const { initTheme, useTheme } = await loadTheme()

    initTheme()

    expect(useTheme().mode.value).toBe('system')
    expect(useTheme().theme.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('prepaint が付けた属性に依存せず、起動時に再解決して上書きする', async () => {
    // インライン実行〜アプリ起動の間に OS 設定が変わったケース(設計 §3.2)
    document.documentElement.setAttribute('data-theme', 'dark')
    document.documentElement.style.colorScheme = 'dark'
    localStorage.setItem('ba.themeMode', 'light')
    installMatchMedia(createFakeMedia(true))
    const { initTheme } = await loadTheme()

    initTheme()

    expect(currentAttribute()).toEqual({ theme: 'light', colorScheme: 'light' })
  })

  it('localStorage の読み取りが例外でも system として適用に成功する', async () => {
    breakLocalStorage('getItem')
    installMatchMedia(createFakeMedia(false))
    const { initTheme, useTheme } = await loadTheme()

    expect(() => initTheme()).not.toThrow()
    expect(useTheme().mode.value).toBe('system')
    expect(currentAttribute().theme).toBe('light')
  })

  it('matchMedia が無い環境ではライト固定で適用に成功する', async () => {
    removeMatchMedia()
    const { initTheme, useTheme } = await loadTheme()

    expect(() => initTheme()).not.toThrow()
    expect(useTheme().theme.value).toBe('light')
    expect(currentAttribute().theme).toBe('light')
  })

  it('matchMedia の呼び出しが例外でもライト固定で適用に成功する', async () => {
    installThrowingMatchMedia()
    const { initTheme, useTheme } = await loadTheme()

    expect(() => initTheme()).not.toThrow()
    expect(useTheme().theme.value).toBe('light')
    expect(currentAttribute().theme).toBe('light')
  })

  it('2 回呼んでもリスナーが重複しない(冪等)', async () => {
    const media = createFakeMedia(false)
    installMatchMedia(media)
    const { initTheme } = await loadTheme()

    initTheme()
    initTheme()

    expect(media.listeners).toHaveLength(1)
  })

  it('system モードでは OS 設定の変更に追従する', async () => {
    const media = createFakeMedia(false)
    installMatchMedia(media)
    const { initTheme, useTheme } = await loadTheme()
    initTheme()
    expect(currentAttribute().theme).toBe('light')

    media.emit(true)

    expect(useTheme().theme.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('明示モードで保存されていれば OS 設定の変更を購読しない', async () => {
    localStorage.setItem('ba.themeMode', 'light')
    const media = createFakeMedia(false)
    installMatchMedia(media)
    const { initTheme } = await loadTheme()

    initTheme()

    expect(media.listeners).toHaveLength(0)
  })

  it('addEventListener が無い環境では addListener へフォールバックする', async () => {
    const media = createFakeMedia(false, { api: 'legacy' })
    installMatchMedia(media)
    const { initTheme, useTheme } = await loadTheme()
    initTheme()
    expect(media.listeners).toHaveLength(1)

    media.emit(true)

    expect(useTheme().theme.value).toBe('dark')
  })

  it('addEventListener が例外を投げる環境では addListener へフォールバックする', async () => {
    // 新しい API が「存在はするが使えない」実装。存在確認だけでは購読に失敗する。
    const media = createFakeMedia(false, { api: 'both', modernThrows: true })
    installMatchMedia(media)
    const { initTheme, useTheme } = await loadTheme()

    initTheme()
    expect(media.listeners).toHaveLength(1)

    media.emit(true)
    expect(useTheme().theme.value).toBe('dark')
  })

  it('購読 API がどれも使えなくてもライト固定で適用に成功する', async () => {
    const media = createFakeMedia(true, { api: 'modern', modernThrows: true })
    installMatchMedia(media)
    const { initTheme, useTheme } = await loadTheme()

    expect(() => initTheme()).not.toThrow()
    expect(media.listeners).toHaveLength(0)
    // 購読はできないが、その時点の OS 設定からの解決自体は行える
    expect(useTheme().theme.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('起動時にモードに応じたタイトルバー同期を行う', async () => {
    localStorage.setItem('ba.themeMode', 'dark')
    installMatchMedia(createFakeMedia(false))
    const runtime = installRuntime()
    const { initTheme } = await loadTheme()

    initTheme()

    expect(runtime.titleBar).toEqual(['dark'])
  })
})

describe('setMode', () => {
  it('適用・保存・タイトルバー同期を行う', async () => {
    installMatchMedia(createFakeMedia(false))
    const runtime = installRuntime()
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    runtime.titleBar.length = 0

    setMode('dark')

    expect(useTheme().mode.value).toBe('dark')
    expect(currentAttribute()).toEqual({ theme: 'dark', colorScheme: 'dark' })
    expect(localStorage.getItem('ba.themeMode')).toBe('dark')
    expect(runtime.titleBar).toEqual(['dark'])
  })

  it('system を選ぶと OS 追従へ戻り、システム既定のタイトルバーへ戻す', async () => {
    localStorage.setItem('ba.themeMode', 'light')
    const media = createFakeMedia(true)
    installMatchMedia(media)
    const runtime = installRuntime()
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    expect(media.listeners).toHaveLength(0)
    // 起動時の同期(light)は数えず、切り替えによる同期だけを見る
    runtime.titleBar.length = 0

    setMode('system')

    expect(useTheme().theme.value).toBe('dark')
    expect(media.listeners).toHaveLength(1)
    expect(runtime.titleBar).toEqual(['system'])
    expect(localStorage.getItem('ba.themeMode')).toBe('system')
  })

  it('明示モードへ切り替えると OS 設定の購読を解除する', async () => {
    const media = createFakeMedia(false)
    installMatchMedia(media)
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    expect(media.listeners).toHaveLength(1)

    setMode('light')
    expect(media.listeners).toHaveLength(0)

    // 解除後に OS 設定が変わってもテーマは動かない
    media.emit(true)
    expect(useTheme().theme.value).toBe('light')
    expect(currentAttribute().theme).toBe('light')
  })

  it('同じモードを繰り返し選んでも購読は増えない', async () => {
    const media = createFakeMedia(false)
    installMatchMedia(media)
    const { initTheme, setMode } = await loadTheme()
    initTheme()

    setMode('system')
    setMode('system')

    expect(media.listeners).toHaveLength(1)
  })

  it('addListener で購読した場合も、明示モードへの切替で解除する', async () => {
    const media = createFakeMedia(false, { api: 'legacy' })
    installMatchMedia(media)
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    expect(media.listeners).toHaveLength(1)

    setMode('light')
    expect(media.listeners).toHaveLength(0)

    media.emit(true)
    expect(useTheme().theme.value).toBe('light')
  })

  it('解除 API を持たない環境でも、モード切替は例外にならない', async () => {
    // removeEventListener が無い実装。解除できないぶんリスナーは残るが、
    // 購読済みとして扱い続けるので二重登録で増え続けることはない。
    const media = createFakeMedia(false, { api: 'modern', noRemove: true })
    installMatchMedia(media)
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    expect(media.listeners).toHaveLength(1)

    expect(() => setMode('light')).not.toThrow()
    expect(() => setMode('system')).not.toThrow()
    expect(() => setMode('dark')).not.toThrow()

    expect(media.listeners).toHaveLength(1)
    expect(useTheme().theme.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('解除 API を持たない環境でも、残ったリスナーが明示モードを上書きしない', async () => {
    const media = createFakeMedia(false, { api: 'modern', noRemove: true })
    installMatchMedia(media)
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    setMode('light')

    // 解除できずに残ったリスナーが呼ばれても、明示モードの解決結果は変わらない
    media.emit(true)

    expect(useTheme().theme.value).toBe('light')
    expect(currentAttribute().theme).toBe('light')
  })

  it('localStorage への保存が例外でも適用は成功する', async () => {
    installMatchMedia(createFakeMedia(false))
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()
    breakLocalStorage('setItem')

    expect(() => setMode('dark')).not.toThrow()
    expect(useTheme().mode.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('Wails ランタイムが無くてもテーマ適用は成功する', async () => {
    installMatchMedia(createFakeMedia(false))
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()

    expect(() => setMode('dark')).not.toThrow()
    expect(useTheme().theme.value).toBe('dark')
  })

  it('タイトルバー同期が例外を投げてもテーマ適用は成功する', async () => {
    installMatchMedia(createFakeMedia(false))
    installRuntime({
      WindowSetDarkTheme: () => {
        throw new Error('この OS では利用できません')
      },
    })
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()

    expect(() => setMode('dark')).not.toThrow()
    expect(useTheme().theme.value).toBe('dark')
    expect(currentAttribute().theme).toBe('dark')
  })

  it('古いランタイム(タイトルバー API 未実装)でもテーマ適用は成功する', async () => {
    installMatchMedia(createFakeMedia(false))
    const runtime = installRuntime({
      WindowSetDarkTheme: undefined,
      WindowSetLightTheme: undefined,
      WindowSetSystemDefaultTheme: undefined,
    })
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()

    expect(() => setMode('dark')).not.toThrow()
    expect(useTheme().theme.value).toBe('dark')
    // 背景色の同期だけは行われる
    expect(runtime.background.length).toBeGreaterThan(0)
  })

  it('Windows 以外と判定できた場合はタイトルバー API を呼ばない', async () => {
    installMatchMedia(createFakeMedia(false))
    const runtime = installRuntime({
      Environment: () => Promise.resolve({ platform: 'darwin', buildType: 'production', arch: 'arm64' }),
    })
    const { initTheme, setMode } = await loadTheme()
    initTheme()
    // Environment() の解決を待つ
    await Promise.resolve()
    await Promise.resolve()
    runtime.titleBar.length = 0

    setMode('dark')

    expect(runtime.titleBar).toEqual([])
    expect(currentAttribute().theme).toBe('dark')
  })

  it('Environment() が例外・失敗でもテーマ適用は成功する', async () => {
    installMatchMedia(createFakeMedia(false))
    installRuntime({
      Environment: () => {
        throw new Error('Environment は利用できません')
      },
    })
    const { initTheme, setMode, useTheme } = await loadTheme()

    expect(() => initTheme()).not.toThrow()
    expect(() => setMode('dark')).not.toThrow()
    expect(useTheme().theme.value).toBe('dark')
  })
})

describe('useTheme', () => {
  it('モードとテーマの共有状態を返し、setMode で更新される', async () => {
    installMatchMedia(createFakeMedia(false))
    const { initTheme, setMode, useTheme } = await loadTheme()
    initTheme()

    const first = useTheme()
    const second = useTheme()
    expect(first.mode.value).toBe('system')

    setMode('dark')

    // 同じシングルトンを参照するため、どの呼び出し元からも同じ値が見える
    expect(first.mode.value).toBe('dark')
    expect(second.theme.value).toBe('dark')
  })
})

// ---------------------------------------------------------------------------
// prepaint(public/prepaint.js の実ファイルを実行して検証する)
// ---------------------------------------------------------------------------

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const PREPAINT_PATH = resolve(process.cwd(), 'public/prepaint.js')
const INDEX_HTML_PATH = resolve(process.cwd(), 'index.html')

interface PrepaintEnv {
  /** localStorage.getItem が返す値(null = 未保存) */
  stored?: string | null
  /** localStorage の参照で例外を投げるか */
  storageThrows?: boolean
  /** matchMedia を用意するか */
  matchMedia?: 'absent' | 'throws' | 'dark' | 'light'
}

/**
 * prepaint.js を偽の window / document で実行し、設定された属性を返す。
 * クラシックスクリプト(グローバルの window / document を参照する)なので、
 * new Function の引数で同名の変数を与えて差し替える。
 */
function runPrepaint(env: PrepaintEnv): { theme: string | null; colorScheme: string } {
  const code = readFileSync(PREPAINT_PATH, 'utf8')
  const root = document.createElement('html')
  const fakeDocument = { documentElement: root }
  const fakeWindow: Record<string, unknown> = {}

  if (env.storageThrows) {
    Object.defineProperty(fakeWindow, 'localStorage', {
      get() {
        throw new Error('localStorage は利用できません')
      },
    })
  } else {
    fakeWindow.localStorage = {
      getItem: (key: string) => (key === 'ba.themeMode' ? (env.stored ?? null) : null),
    }
  }

  if (env.matchMedia === 'throws') {
    fakeWindow.matchMedia = () => {
      throw new Error('matchMedia は利用できません')
    }
  } else if (env.matchMedia === 'dark' || env.matchMedia === 'light') {
    fakeWindow.matchMedia = () => ({ matches: env.matchMedia === 'dark' })
  }

  const fn = new Function('window', 'document', 'localStorage', code)
  fn(fakeWindow, fakeDocument, env.storageThrows ? undefined : fakeWindow.localStorage)

  return {
    theme: root.getAttribute('data-theme'),
    colorScheme: root.style.colorScheme,
  }
}

describe('prepaint.js(起動時のちらつき対策)', () => {
  it('保存済み dark をそのまま適用する', () => {
    expect(runPrepaint({ stored: 'dark', matchMedia: 'light' })).toEqual({
      theme: 'dark',
      colorScheme: 'dark',
    })
  })

  it('保存済み light は OS がダークでもライトを適用する', () => {
    expect(runPrepaint({ stored: 'light', matchMedia: 'dark' })).toEqual({
      theme: 'light',
      colorScheme: 'light',
    })
  })

  it('system は OS 設定から解決する', () => {
    expect(runPrepaint({ stored: 'system', matchMedia: 'dark' }).theme).toBe('dark')
    expect(runPrepaint({ stored: 'system', matchMedia: 'light' }).theme).toBe('light')
  })

  it('未保存は system として扱う', () => {
    expect(runPrepaint({ stored: null, matchMedia: 'dark' }).theme).toBe('dark')
  })

  it('不正値は system として扱う', () => {
    expect(runPrepaint({ stored: 'purple', matchMedia: 'dark' }).theme).toBe('dark')
  })

  it('localStorage の参照が例外でもライトとして属性を設定する', () => {
    expect(runPrepaint({ storageThrows: true, matchMedia: 'dark' })).toEqual({
      theme: 'light',
      colorScheme: 'light',
    })
  })

  it('matchMedia が無い環境でもライトとして属性を設定する', () => {
    expect(runPrepaint({ stored: 'system', matchMedia: 'absent' })).toEqual({
      theme: 'light',
      colorScheme: 'light',
    })
  })

  it('matchMedia の呼び出しが例外でもライトとして属性を設定する', () => {
    expect(runPrepaint({ stored: 'system', matchMedia: 'throws' })).toEqual({
      theme: 'light',
      colorScheme: 'light',
    })
  })

  it('theme.ts と同じ localStorage キーを参照する', () => {
    const code = readFileSync(PREPAINT_PATH, 'utf8')
    expect(code).toContain('ba.themeMode')
  })
})

describe('index.html の prepaint 読込', () => {
  const html = readFileSync(INDEX_HTML_PATH, 'utf8')

  it('/prepaint.js をブロッキング読込する(defer / async / module でない)', () => {
    const tag = html.match(/<script[^>]*prepaint\.js[^>]*>/)
    expect(tag, '/prepaint.js の script タグが見つかりません').not.toBeNull()
    const attrs = tag![0]
    expect(attrs).toContain('src="/prepaint.js"')
    expect(attrs).not.toMatch(/\bdefer\b/)
    expect(attrs).not.toMatch(/\basync\b/)
    expect(attrs).not.toMatch(/type\s*=\s*"module"/)
  })

  it('アプリ本体(main.ts)より前に読み込む', () => {
    expect(html.indexOf('prepaint.js')).toBeLessThan(html.indexOf('main.ts'))
  })

  it('color-scheme のメタタグを持つ', () => {
    const tag = html.match(/<meta[^>]*color-scheme[^>]*>/)
    expect(tag, 'color-scheme のメタタグが見つかりません').not.toBeNull()
    expect(tag![0]).toContain('name="color-scheme"')
    expect(tag![0]).toContain('content="light dark"')
  })
})
