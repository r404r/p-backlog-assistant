/**
 * 表示言語(lib/language.ts)の検証。
 *
 * language.ts は theme.ts と同じ流儀のモジュールシングルトン(モード ref・
 * `languagechange` の購読)を持つため、テストごとに vi.resetModules() +
 * 動的 import で新しいインスタンスを読み込む。i18n インスタンス(lib/i18n.ts)も
 * 同時に読み直されるので、locale の持ち越しも起きない。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type LanguageModule = typeof import('./language')
type I18nModule = typeof import('./i18n')

/** シングルトン状態を持ち越さないよう、テストごとに新しいモジュールを読み込む */
async function loadLanguage(): Promise<{ lang: LanguageModule; i18n: I18nModule }> {
  vi.resetModules()
  const lang = await import('./language')
  const i18n = await import('./i18n')
  return { lang, i18n }
}

// ---------------------------------------------------------------------------
// テスト用の偽 navigator / 偽 window イベント
// ---------------------------------------------------------------------------

const originalLocalStorage = window.localStorage

/** window.localStorage を差し替える(happy-dom の実体は Proxy なので spyOn では戻せない) */
function replaceLocalStorage(value: Storage): void {
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    writable: true,
    value,
  })
}

/** 指定したメソッドだけが例外を投げる localStorage を差し込む(theme.test.ts と同じ流儀) */
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

/** navigator.language を差し替える(happy-dom の navigator は読み取り専用のため定義し直す) */
function setNavigatorLanguage(value: string | undefined): void {
  Object.defineProperty(window.navigator, 'language', {
    configurable: true,
    get: () => value,
  })
}

interface FakeLanguageEvents {
  /** 登録されている languagechange リスナー(重複購読の確認に使う) */
  listeners: EventListener[]
  /** OS / ブラウザの言語変更を再現する */
  emit(): void
  /** 差し替えを戻す */
  restore(): void
}

/** window.addEventListener('languagechange') を数えられるように差し替える */
function installLanguageEvents(opts: { addThrows?: boolean; noRemove?: boolean } = {}): FakeLanguageEvents {
  const listeners: EventListener[] = []
  const w = window as unknown as Record<string, unknown>
  const originalAdd = window.addEventListener
  const originalRemove = window.removeEventListener

  w.addEventListener = function (type: string, cb: EventListener, ...rest: unknown[]) {
    if (type === 'languagechange') {
      if (opts.addThrows) throw new Error('addEventListener は利用できません')
      listeners.push(cb)
      return
    }
    return (originalAdd as (...a: unknown[]) => unknown).call(window, type, cb, ...rest)
  }
  w.removeEventListener = function (type: string, cb: EventListener, ...rest: unknown[]) {
    if (type === 'languagechange') {
      if (opts.noRemove) return
      const i = listeners.indexOf(cb)
      if (i >= 0) listeners.splice(i, 1)
      return
    }
    return (originalRemove as (...a: unknown[]) => unknown).call(window, type, cb, ...rest)
  }

  return {
    listeners,
    emit() {
      for (const cb of [...listeners]) cb(new Event('languagechange'))
    },
    restore() {
      w.addEventListener = originalAdd
      w.removeEventListener = originalRemove
    },
  }
}

let events: FakeLanguageEvents | null = null

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('lang')
  setNavigatorLanguage('ja-JP')
})

afterEach(() => {
  events?.restore()
  events = null
  vi.restoreAllMocks()
  replaceLocalStorage(originalLocalStorage)
  localStorage.clear()
  document.documentElement.removeAttribute('lang')
})

// ---------------------------------------------------------------------------
// 純関数
// ---------------------------------------------------------------------------

describe('parseStoredLanguageMode', () => {
  it('保存済みの 3 値はそのまま返す', async () => {
    const { lang } = await loadLanguage()
    expect(lang.parseStoredLanguageMode('system')).toBe('system')
    expect(lang.parseStoredLanguageMode('ja')).toBe('ja')
    expect(lang.parseStoredLanguageMode('en')).toBe('en')
  })

  it('未保存・不正値は system に落とす', async () => {
    const { lang } = await loadLanguage()
    expect(lang.parseStoredLanguageMode(null)).toBe('system')
    expect(lang.parseStoredLanguageMode('')).toBe('system')
    expect(lang.parseStoredLanguageMode('JA')).toBe('system')
    expect(lang.parseStoredLanguageMode('fr')).toBe('system')
  })
})

describe('resolveLanguage', () => {
  it('明示モードは navigator.language に関わらずそのまま返す', async () => {
    const { lang } = await loadLanguage()
    expect(lang.resolveLanguage('ja', 'en-US')).toBe('ja')
    expect(lang.resolveLanguage('en', 'ja-JP')).toBe('en')
  })

  it('system は ja 系なら ja、それ以外は en に解決する', async () => {
    const { lang } = await loadLanguage()
    expect(lang.resolveLanguage('system', 'ja')).toBe('ja')
    expect(lang.resolveLanguage('system', 'ja-JP')).toBe('ja')
    expect(lang.resolveLanguage('system', 'JA-jp')).toBe('ja')
    expect(lang.resolveLanguage('system', 'en-US')).toBe('en')
    expect(lang.resolveLanguage('system', 'fr')).toBe('en')
  })

  it('navigator.language が取得できない場合は en に解決する', async () => {
    const { lang } = await loadLanguage()
    expect(lang.resolveLanguage('system', undefined)).toBe('en')
    expect(lang.resolveLanguage('system', '')).toBe('en')
    expect(lang.resolveLanguage('system', null)).toBe('en')
  })
})

// ---------------------------------------------------------------------------
// initLanguage / setLanguageMode
// ---------------------------------------------------------------------------

describe('initLanguage', () => {
  it('保存済みモードを復元して適用する', async () => {
    localStorage.setItem('ba.language', 'en')
    const { lang, i18n } = await loadLanguage()

    lang.initLanguage()

    expect(lang.useLanguage().mode.value).toBe('en')
    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
    expect(document.documentElement.lang).toBe('en')
  })

  it('未保存なら system として navigator.language から解決する', async () => {
    setNavigatorLanguage('en-GB')
    const { lang, i18n } = await loadLanguage()

    lang.initLanguage()

    expect(lang.useLanguage().mode.value).toBe('system')
    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
    expect(document.documentElement.lang).toBe('en')
  })

  it('localStorage の読み取りが例外でも system として適用に成功する', async () => {
    breakLocalStorage('getItem')
    setNavigatorLanguage('ja-JP')
    const { lang } = await loadLanguage()

    expect(() => lang.initLanguage()).not.toThrow()
    expect(lang.useLanguage().mode.value).toBe('system')
    expect(document.documentElement.lang).toBe('ja')
  })

  it('2 回呼んでもリスナーが重複しない(冪等)', async () => {
    events = installLanguageEvents()
    const { lang } = await loadLanguage()

    lang.initLanguage()
    lang.initLanguage()

    expect(events.listeners).toHaveLength(1)
  })

  it('system モードでは languagechange に追従する', async () => {
    events = installLanguageEvents()
    setNavigatorLanguage('ja-JP')
    const { lang, i18n } = await loadLanguage()
    lang.initLanguage()
    expect(i18n.i18n.global.locale.value).toBe('ja')

    setNavigatorLanguage('en-US')
    events.emit()

    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
    expect(document.documentElement.lang).toBe('en')
  })

  it('明示モードで保存されていれば languagechange を購読しない', async () => {
    localStorage.setItem('ba.language', 'ja')
    events = installLanguageEvents()
    const { lang } = await loadLanguage()

    lang.initLanguage()

    expect(events.listeners).toHaveLength(0)
  })

  it('addEventListener が例外でも適用に成功する', async () => {
    events = installLanguageEvents({ addThrows: true })
    const { lang } = await loadLanguage()

    expect(() => lang.initLanguage()).not.toThrow()
    expect(lang.useLanguage().language.value).toBe('ja')
  })
})

describe('setLanguageMode', () => {
  it('適用・保存・html lang の更新・i18n の locale 更新を行う', async () => {
    const { lang, i18n } = await loadLanguage()
    lang.initLanguage()

    lang.setLanguageMode('en')

    expect(lang.useLanguage().mode.value).toBe('en')
    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(localStorage.getItem('ba.language')).toBe('en')
  })

  it('明示モードへ切り替えると languagechange の購読を解除する', async () => {
    events = installLanguageEvents()
    const { lang, i18n } = await loadLanguage()
    lang.initLanguage()
    expect(events.listeners).toHaveLength(1)

    lang.setLanguageMode('ja')
    expect(events.listeners).toHaveLength(0)

    // 解除後は navigator の変更があっても表示言語は動かない
    setNavigatorLanguage('en-US')
    events.emit()
    expect(i18n.i18n.global.locale.value).toBe('ja')
  })

  it('system へ戻すと再解決して再購読する', async () => {
    localStorage.setItem('ba.language', 'ja')
    events = installLanguageEvents()
    setNavigatorLanguage('en-US')
    const { lang, i18n } = await loadLanguage()
    lang.initLanguage()
    expect(events.listeners).toHaveLength(0)

    lang.setLanguageMode('system')

    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
    expect(events.listeners).toHaveLength(1)
    expect(localStorage.getItem('ba.language')).toBe('system')
  })

  it('同じモードを繰り返し選んでも購読は増えない', async () => {
    events = installLanguageEvents()
    const { lang } = await loadLanguage()
    lang.initLanguage()

    lang.setLanguageMode('system')
    lang.setLanguageMode('system')

    expect(events.listeners).toHaveLength(1)
  })

  it('解除 API が効かない環境でも、残ったリスナーが明示モードを上書きしない', async () => {
    events = installLanguageEvents({ noRemove: true })
    setNavigatorLanguage('ja-JP')
    const { lang, i18n } = await loadLanguage()
    lang.initLanguage()

    lang.setLanguageMode('en')
    setNavigatorLanguage('ja-JP')
    events.emit()

    expect(lang.useLanguage().language.value).toBe('en')
    expect(i18n.i18n.global.locale.value).toBe('en')
  })

  it('localStorage への保存が例外でも適用は成功する', async () => {
    const { lang } = await loadLanguage()
    lang.initLanguage()
    breakLocalStorage('setItem')

    expect(() => lang.setLanguageMode('en')).not.toThrow()
    expect(lang.useLanguage().language.value).toBe('en')
  })
})

describe('useLanguage', () => {
  it('共有状態を返し、setLanguageMode で更新される', async () => {
    const { lang } = await loadLanguage()
    lang.initLanguage()

    const first = lang.useLanguage()
    const second = lang.useLanguage()
    expect(first.mode.value).toBe('system')

    lang.setLanguageMode('en')

    // 同じシングルトンを参照するため、どの呼び出し元からも同じ値が見える
    expect(first.mode.value).toBe('en')
    expect(second.language.value).toBe('en')
  })
})

describe('LANGUAGE_MODES', () => {
  it('アプリ情報画面のラジオと同じ並びを公開する', async () => {
    const { lang } = await loadLanguage()
    expect([...lang.LANGUAGE_MODES]).toEqual(['system', 'ja', 'en'])
  })
})
