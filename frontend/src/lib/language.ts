/**
 * 表示言語(日本語 / 英語)の制御。
 *
 * 方式(設計 §3.2): 表示テーマ(lib/theme.ts)と同じ流儀で、
 * 「モード(system / ja / en)を解決して表示言語を確定させる」責務だけを持つ。
 * 実際の文言の差し替えは vue-i18n(lib/i18n.ts のシングルトン)が行う。
 *
 * 状態の所有者:
 *  - 初期化は **main.ts が initLanguage() を 1 回呼ぶ**(マウント前)。
 *  - モードと OS / ブラウザの言語設定(`languagechange`)の購読は、theme.ts と同じく
 *    **モジュールレベルのシングルトン**として持つ。App.vue は KeepAlive 無しの
 *    動的コンポーネントで、画面を切り替えるたびに AboutView が破棄されるため、
 *    画面側に購読を持たせると「アプリ情報を開いている間だけ追従する」ことになる。
 *  - AboutView は useLanguage() の参照と setLanguageMode() の呼び出しだけを行う。
 *
 * 防御的に書いてある箇所(いずれも言語の適用自体は必ず成功させる):
 *  - localStorage: WebView の設定によっては参照・保存が例外になり得る。
 *  - addEventListener / removeEventListener: 呼び出しが例外になる実装もあり得る。
 *    theme.ts の matchMedia と同じく、解除できたかどうかと購読状態を分けて扱う。
 */
import { readonly, ref } from 'vue'

import { FALLBACK_LANGUAGE, setI18nLanguage, type Language } from './i18n'

export type { Language }

/** 利用者が選べる表示言語のモード(既定は system = OS / ブラウザの言語設定に追従) */
export type LanguageMode = 'system' | 'ja' | 'en'

/** 表示言語モードの保存先(既存の `ba.*` 規約に合わせる) */
export const LANGUAGE_MODE_KEY = 'ba.language'

/** 選択肢の並び(アプリ情報画面のラジオと同じ順) */
export const LANGUAGE_MODES: readonly LanguageMode[] = ['system', 'ja', 'en'] as const

/** `languagechange` は HTML 標準のイベント(OS / ブラウザの言語設定の変更で発火する) */
const LANGUAGE_CHANGE_EVENT = 'languagechange'

// ---------------------------------------------------------------------------
// 純関数
// ---------------------------------------------------------------------------

/** localStorage の生値をモードへ変換する(未保存・不正値は system) */
export function parseStoredLanguageMode(raw: string | null): LanguageMode {
  return raw === 'ja' || raw === 'en' || raw === 'system' ? raw : 'system'
}

/**
 * モードと環境の言語設定から、実際に表示する言語を決める。
 *
 * system の解決は `ja*`(ja / ja-JP 等)なら日本語、それ以外・取得できない場合は英語。
 * 英語カタログが無い部分は vue-i18n の fallbackLocale で日本語になる。
 */
export function resolveLanguage(
  mode: LanguageMode,
  navigatorLanguage: string | null | undefined,
): Language {
  if (mode === 'ja' || mode === 'en') return mode
  const tag = (navigatorLanguage ?? '').toLowerCase()
  return tag === 'ja' || tag.startsWith('ja-') ? 'ja' : 'en'
}

// ---------------------------------------------------------------------------
// シングルトンの状態
// ---------------------------------------------------------------------------

const mode = ref<LanguageMode>('system')
const language = ref<Language>(FALLBACK_LANGUAGE)

/** initLanguage() が済んでいるか(2 回目以降は購読を作り直さない) */
let initialized = false

/**
 * 現在張っている `languagechange` の購読(未購読なら null)。
 *
 * detach は「実際に解除できたか」を返す。解除できなかった場合は購読済みのまま扱い、
 * 二重登録でリスナーが増え続けるのを防ぐ(theme.ts の systemWatch と同じ契約)。
 */
let systemWatch: { detach: () => boolean } | null = null

/** 環境の言語設定を取得する(取得できない環境では空文字) */
function navigatorLanguage(): string {
  try {
    return window.navigator?.language ?? ''
  } catch {
    return ''
  }
}

/** 表示言語を DOM と i18n へ適用する */
function applyLanguage(next: Language): void {
  language.value = next
  try {
    document.documentElement.lang = next
  } catch {
    // 属性が設定できなくても文言の切り替えは成立する
  }
  setI18nLanguage(next)
}

/** 現在のモードから表示言語を解決して適用する */
function applyCurrentMode(): void {
  applyLanguage(resolveLanguage(mode.value, navigatorLanguage()))
}

/** `languagechange` の購読を解除する(未購読なら何もしない) */
function stopWatchingSystem(): void {
  if (!systemWatch) return
  let detached: boolean
  try {
    detached = systemWatch.detach()
  } catch {
    detached = false
  }
  if (detached) systemWatch = null
}

/**
 * `languagechange` を購読する(system モードのときだけ)。
 * 既に購読していれば何もしない(initLanguage の冪等性・setLanguageMode の連打に耐える)。
 */
function startWatchingSystem(): void {
  if (systemWatch) return

  const handler = (): void => {
    // 購読中は基本的に system モード。解除できなかった場合に備えて、
    // 環境の言語設定ではなく「現在のモード」から解決し直す。
    applyCurrentMode()
  }

  try {
    if (typeof window.addEventListener !== 'function') return
    window.addEventListener(LANGUAGE_CHANGE_EVENT, handler)
  } catch {
    // 購読できない環境では追従をあきらめる(モード変更時に解決し直すのみ)
    return
  }

  systemWatch = {
    detach: () => {
      if (typeof window.removeEventListener !== 'function') return false
      window.removeEventListener(LANGUAGE_CHANGE_EVENT, handler)
      return true
    },
  }
}

/** モードに応じて購読を張る / 外す */
function updateSystemWatch(): void {
  if (mode.value === 'system') {
    startWatchingSystem()
  } else {
    stopWatchingSystem()
  }
}

/** 保存済みのモードを読み出す(参照に失敗しても既定値で起動を継続する) */
function loadMode(): LanguageMode {
  try {
    return parseStoredLanguageMode(localStorage.getItem(LANGUAGE_MODE_KEY))
  } catch {
    return 'system'
  }
}

/** モードを保存する(保存できなくても切り替え自体は成立するため失敗は無視する) */
function saveMode(next: LanguageMode): void {
  try {
    localStorage.setItem(LANGUAGE_MODE_KEY, next)
  } catch {
    // 次回起動時に既定へ戻るだけ
  }
}

// ---------------------------------------------------------------------------
// 公開 API
// ---------------------------------------------------------------------------

/**
 * 表示言語を初期化する。main.ts がマウント前に 1 回だけ呼ぶ。
 * 2 回呼んでも購読は重複しない。
 */
export function initLanguage(): void {
  mode.value = loadMode()
  applyCurrentMode()
  initialized = true
  updateSystemWatch()
}

/** 初期化済みか(main.ts 以外からの二重初期化を避けたい場合の判定用) */
export function isLanguageInitialized(): boolean {
  return initialized
}

/** 表示言語を切り替える(即時適用 + 保存 + `<html lang>` 更新 + i18n の locale 更新) */
export function setLanguageMode(next: LanguageMode): void {
  mode.value = next
  applyCurrentMode()
  updateSystemWatch()
  saveMode(next)
}

/**
 * 表示言語の共有状態(参照専用)と切替関数を返す。
 * 状態はモジュールシングルトンのため、どの画面から呼んでも同じ値を見る。
 */
export function useLanguage() {
  return { mode: readonly(mode), language: readonly(language), setLanguageMode }
}
