/**
 * テーマ(ライト / ダーク)の制御。
 *
 * 方式(設計 §3.2): 色は style.css のトークンに集約され、`<html data-theme="...">` の
 * 有無で一括して切り替わる。ここは「モード(system / light / dark)を解決して
 * data-theme を確定させる」責務だけを持つ。
 *
 * 状態の所有者:
 *  - 初期化は **main.ts が initTheme() を 1 回呼ぶ**(マウント前)。
 *  - モードと OS 設定(matchMedia)の購読は、syncState.ts と同じく
 *    **モジュールレベルのシングルトン**として持つ。App.vue は KeepAlive 無しの
 *    動的コンポーネントで、画面を切り替えるたびに AboutView が破棄されるため、
 *    画面側に購読を持たせると「アプリ情報を開いている間だけ OS 追従する」ことになる。
 *  - AboutView は useTheme() の参照と setMode() の呼び出しだけを行う。
 *
 * 防御的に書いてある箇所(いずれもテーマ適用自体は必ず成功させる):
 *  - localStorage: WebView の設定によっては参照・保存が例外になり得る。
 *  - matchMedia: 不存在だけでなく呼び出し例外もあり得る。古い実装向けに
 *    addEventListener → addListener のフォールバックを持ち、解除も対にする。
 *  - Wails ランタイム: 生成済みラッパー(wailsjs/runtime/runtime.js)は
 *    window.runtime を無条件に参照するため使わない。backend.ts の
 *    findWailsRuntimeObject と同じ流儀で存在確認 + try/catch で直接呼ぶ。
 *    vite dev・テスト・モックバックエンドでは自動的に no-op になる。
 */
import { readonly, ref } from 'vue'

/** 利用者が選べる表示テーマ(既定は system = OS の外観設定に追従) */
export type ThemeMode = 'system' | 'light' | 'dark'

/** 実際に適用される配色 */
export type Theme = 'light' | 'dark'

/** 表示テーマの保存先(既存の `ba.*` 規約に合わせる) */
export const THEME_MODE_KEY = 'ba.themeMode'

/** 選択肢の並び(アプリ情報画面のラジオと同じ順) */
export const THEME_MODES: readonly ThemeMode[] = ['system', 'light', 'dark'] as const

/**
 * ウィンドウ背景色(Wails の WindowSetBackgroundColour へ渡す RGB)。
 *
 * style.css の `--bg` と同じ値にしておく。リサイズ中などに WebView の外側が
 * 一瞬覗くことがあり、そこだけ白いとダークテーマで目立つため。
 * 値の一致は styleTokens.test.ts が検査する。
 */
export const THEME_BACKGROUND_RGB: Record<Theme, [number, number, number]> = {
  light: [255, 255, 255],
  dark: [13, 17, 23],
}

/** OS の外観設定を問い合わせるメディアクエリ */
const DARK_QUERY = '(prefers-color-scheme: dark)'

// ---------------------------------------------------------------------------
// 純関数
// ---------------------------------------------------------------------------

/** localStorage の生値をモードへ変換する(未保存・不正値は system) */
export function parseStoredThemeMode(raw: string | null): ThemeMode {
  return raw === 'light' || raw === 'dark' || raw === 'system' ? raw : 'system'
}

/** モードと OS 設定から、実際に適用する配色を決める */
export function resolveTheme(mode: ThemeMode, systemPrefersDark: boolean): Theme {
  if (mode === 'light' || mode === 'dark') return mode
  return systemPrefersDark ? 'dark' : 'light'
}

// ---------------------------------------------------------------------------
// Wails ランタイム(存在しない・古い・例外を投げる可能性がある)
// ---------------------------------------------------------------------------

/** Wails ランタイムのうち、このモジュールが使う API */
interface ThemeRuntime {
  WindowSetSystemDefaultTheme?: () => void
  WindowSetLightTheme?: () => void
  WindowSetDarkTheme?: () => void
  WindowSetBackgroundColour?: (r: number, g: number, b: number, a: number) => void
  Environment?: () => Promise<{ platform?: string }>
}

/** window.runtime をそのまま返す(Wails 外では null)。個々の API の有無は呼び出し側で見る */
function findRuntime(): ThemeRuntime | null {
  try {
    const w = window as unknown as Record<string, unknown>
    return (w['runtime'] as ThemeRuntime | undefined) ?? null
  } catch {
    return null
  }
}

/**
 * 実行中のプラットフォーム(Environment() で判明したもの)。
 * null = 未取得・取得できない。この場合はタイトルバー API の存在確認だけで呼ぶ
 * (非対応 OS では Wails 側が no-op になる)。
 */
let platform: string | null = null

/** プラットフォーム判定を一度だけ非同期に取得する(失敗しても機能は縮退しない) */
function detectPlatform(): void {
  const rt = findRuntime()
  if (!rt || typeof rt.Environment !== 'function') return
  try {
    void Promise.resolve(rt.Environment())
      .then((env) => {
        if (env && typeof env.platform === 'string') platform = env.platform
      })
      .catch(() => {
        // 取得できなくても、存在確認のみで呼ぶ経路で動く
      })
  } catch {
    // Environment 自体が例外を投げる実装(古いランタイム)もあり得る
  }
}

/**
 * タイトルバーの配色を同期する(Windows のみ有効な API)。
 *
 * macOS には実行時 API が無く OS の外観設定に従うため、明示モード時は
 * ウィンドウ枠だけ不一致になり得る(制限としてアプリ情報画面に明記している)。
 */
function syncNativeTitleBar(mode: ThemeMode): void {
  // Windows でないと分かっている場合は呼ばない。不明な場合は関数の存在確認のみで呼ぶ。
  if (platform !== null && platform !== 'windows') return
  const rt = findRuntime()
  if (!rt) return
  const fn =
    mode === 'dark'
      ? rt.WindowSetDarkTheme
      : mode === 'light'
        ? rt.WindowSetLightTheme
        : rt.WindowSetSystemDefaultTheme
  if (typeof fn !== 'function') return
  try {
    fn()
  } catch {
    // タイトルバーが追従しないだけで、画面の配色は成立する
  }
}

/** ウィンドウ(WebView の外側)の地の色をテーマの背景色へ同期する */
function syncNativeBackground(theme: Theme): void {
  const rt = findRuntime()
  if (!rt || typeof rt.WindowSetBackgroundColour !== 'function') return
  const [r, g, b] = THEME_BACKGROUND_RGB[theme]
  try {
    // A は 255 段階(0 はほぼ透明)。不透明で塗る。
    rt.WindowSetBackgroundColour(r, g, b, 255)
  } catch {
    // 地の色が変わらないだけで、画面の配色は成立する
  }
}

// ---------------------------------------------------------------------------
// 適用
// ---------------------------------------------------------------------------

/**
 * 配色を DOM とネイティブウィンドウへ適用する。
 *
 * data-theme が style.css のトークンを切り替え、colorScheme が
 * スクロールバー・フォームコントロールなどのネイティブ描画を切り替える。
 */
export function applyTheme(theme: Theme): void {
  const root = document.documentElement
  root.setAttribute('data-theme', theme)
  root.style.colorScheme = theme
  syncNativeBackground(theme)
}

// ---------------------------------------------------------------------------
// シングルトンの状態
// ---------------------------------------------------------------------------

const mode = ref<ThemeMode>('system')
const theme = ref<Theme>('light')

/** initTheme() が済んでいるか(2 回目以降は購読を作り直さない) */
let initialized = false

/**
 * 現在張っている OS 設定の購読(未購読なら null)。
 *
 * detach は「実際に解除できたか」を返す。解除 API を持たない実装があるため、
 * 解除の成否と購読状態を分けて扱う(stopWatchingSystem の説明を参照)。
 */
let systemWatch: { detach: () => boolean } | null = null

/**
 * OS の外観設定を問い合わせる MediaQueryList を返す。
 * 非対応(関数が無い)・呼び出し例外のどちらも null を返す。
 */
function queryDarkMedia(): MediaQueryList | null {
  try {
    if (typeof window.matchMedia !== 'function') return null
    return window.matchMedia(DARK_QUERY)
  } catch {
    return null
  }
}

/** OS の外観設定を取得する(取得できない環境ではライト扱い) */
function systemPrefersDark(): boolean {
  try {
    return !!queryDarkMedia()?.matches
  } catch {
    return false
  }
}

/** 現在のモードから配色を解決して適用する */
function applyCurrentMode(): void {
  theme.value = resolveTheme(mode.value, systemPrefersDark())
  applyTheme(theme.value)
}

/**
 * OS 設定の購読を解除する(未購読なら何もしない)。
 *
 * 解除できなかった場合(解除 API が無い実装)は購読済みのまま扱う。
 * ここで未購読へ戻すと、次に system へ切り替えたときに二重登録になり、
 * 切替のたびにリスナーが増えていくため。残ったリスナーが呼ばれても、
 * ハンドラは常に現在のモードから解決し直すので明示モードの表示は変わらない。
 */
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
 * OS 設定の変更を購読する(system モードのときだけ)。
 * 既に購読していれば何もしない(initTheme の冪等性・setMode の連打に耐える)。
 */
function startWatchingSystem(): void {
  if (systemWatch) return
  const mql = queryDarkMedia()
  if (!mql) return

  const handler = (): void => {
    // 購読中は基本的に system モード。解除できなかった場合に備えて、
    // OS 設定ではなく「現在のモード」から解決し直す。
    applyCurrentMode()
  }

  // 古い WebView 向けの API(新しい API が使えないときだけ使う)
  const legacy = mql as unknown as {
    addListener?: (cb: () => void) => void
    removeListener?: (cb: () => void) => void
  }

  // 新しい API は「存在するが呼ぶと例外」という実装もあり得るため、
  // 存在確認だけで済ませず、登録に失敗したら古い API へフォールバックする。
  if (typeof mql.addEventListener === 'function') {
    try {
      mql.addEventListener('change', handler)
      systemWatch = {
        detach: () => {
          if (typeof mql.removeEventListener !== 'function') return false
          mql.removeEventListener('change', handler)
          return true
        },
      }
      return
    } catch {
      // 登録できなかったので古い API を試す
    }
  }

  if (typeof legacy.addListener === 'function') {
    try {
      legacy.addListener(handler)
      systemWatch = {
        detach: () => {
          if (typeof legacy.removeListener !== 'function') return false
          legacy.removeListener(handler)
          return true
        },
      }
    } catch {
      // どちらでも購読できない場合は追従をあきらめる(モード変更時に解決し直すのみ)
    }
  }
}

/** モードに応じて OS 設定の購読を張る / 外す */
function updateSystemWatch(): void {
  if (mode.value === 'system') {
    startWatchingSystem()
  } else {
    stopWatchingSystem()
  }
}

/** 保存済みのモードを読み出す(参照に失敗しても既定値で起動を継続する) */
function loadMode(): ThemeMode {
  try {
    return parseStoredThemeMode(localStorage.getItem(THEME_MODE_KEY))
  } catch {
    return 'system'
  }
}

/** モードを保存する(保存できなくても切り替え自体は成立するため失敗は無視する) */
function saveMode(next: ThemeMode): void {
  try {
    localStorage.setItem(THEME_MODE_KEY, next)
  } catch {
    // 次回起動時に既定へ戻るだけ
  }
}

// ---------------------------------------------------------------------------
// 公開 API
// ---------------------------------------------------------------------------

/**
 * テーマを初期化する。main.ts がマウント前に 1 回だけ呼ぶ。
 *
 * prepaint.js が付けた data-theme には依存せず、必ず解決し直して上書きする
 * (prepaint の実行からアプリ起動までの間に OS 設定が変わることがあるため)。
 * 2 回呼んでも購読は重複しない。
 */
export function initTheme(): void {
  mode.value = loadMode()
  applyCurrentMode()
  if (!initialized) {
    initialized = true
    // プラットフォーム判定は非同期。判明するまでは存在確認のみで呼ぶ。
    detectPlatform()
  }
  updateSystemWatch()
  syncNativeTitleBar(mode.value)
}

/** 表示テーマを切り替える(即時適用 + 保存 + ネイティブ同期) */
export function setMode(next: ThemeMode): void {
  mode.value = next
  applyCurrentMode()
  updateSystemWatch()
  saveMode(next)
  syncNativeTitleBar(next)
}

/**
 * テーマの共有状態(参照専用)と切替関数を返す。
 * 状態はモジュールシングルトンのため、どの画面から呼んでも同じ値を見る。
 */
export function useTheme() {
  return { mode: readonly(mode), theme: readonly(theme), setMode }
}
