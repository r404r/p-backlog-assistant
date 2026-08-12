/**
 * モーダルダイアログのフォーカス制御(Codex Review 中 1)。
 *
 * `role="dialog" aria-modal="true"` を付けただけでは、Tab を押し続けると
 * フォーカスがダイアログの外(背景の表・ボタン)へ抜けてしまう。キーボード・
 * 支援技術の利用者が「閉じたつもりのない背景」を操作できてしまうため、
 * 開いている間はフォーカスをダイアログ内に閉じ込める。
 *
 * 課題詳細(IssuesView)とプロファイル削除確認(SettingsView)の 2 か所で
 * 同じ挙動が要るため、composable としてここへ集約する。
 * 抽出・循環の判定は純関数(focusableElementsIn / trapTabKey)に切り出して
 * テストし、Vue のライフサイクル結線だけを useModalFocus が担う。
 */
import { nextTick, onUnmounted, watch, type Ref } from 'vue'

/**
 * フォーカスを受け取れる要素のセレクタ。
 * tabindex="-1"(プログラムからのみフォーカスする要素)は Tab 順に現れないため除く。
 */
const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button',
  'input',
  'select',
  'textarea',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

/**
 * コンテナ内のフォーカス可能な要素を DOM 順(= Tab 順)で返す。
 *
 * 無効化された要素・非表示(hidden)の要素は Tab 順に現れないため除く。
 * 正の tabindex による並べ替えには対応しない(本アプリでは使っていない)。
 */
export function focusableElementsIn(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (el) =>
      !el.hasAttribute('disabled') && !el.hidden && el.getAttribute('aria-hidden') !== 'true',
  )
}

/**
 * Tab / Shift+Tab をコンテナ内で循環させる。
 *
 * 既定動作を止めてフォーカスを移した場合は true を返す(呼び出し側は
 * 戻り値を見なくてもよい。preventDefault はこの関数の中で行う)。
 * 途中の要素での Tab はブラウザの既定動作に任せる(false を返す)。
 */
export function trapTabKey(container: HTMLElement, e: KeyboardEvent): boolean {
  if (e.key !== 'Tab') return false
  const items = focusableElementsIn(container)
  if (items.length === 0) {
    // 押しても行き先が無い場合も、背景へ抜けさせない(閉じるまで留める)
    e.preventDefault()
    return true
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement as HTMLElement | null
  // 何らかの理由で外にフォーカスがある場合は、進行方向に応じた端から入れ直す
  if (!active || !container.contains(active)) {
    e.preventDefault()
    ;(e.shiftKey ? last : first).focus()
    return true
  }
  if (!e.shiftKey && active === last) {
    e.preventDefault()
    first.focus()
    return true
  }
  if (e.shiftKey && active === first) {
    e.preventDefault()
    last.focus()
    return true
  }
  return false
}

/** useModalFocus の任意設定 */
export interface ModalFocusOptions {
  /**
   * 開いた直後にフォーカスする要素を返す(省略時はダイアログ内の最初の
   * フォーカス可能要素)。読み込み中で目的の要素がまだ無い場合は null を返してよい。
   */
  initialFocus?: () => HTMLElement | null | undefined
  /**
   * 閉じた後にフォーカスを戻す先を返す(省略時は開く直前にフォーカスが
   * あった要素)。クリックでフォーカスが移らない WebView(WKWebView)でも
   * 確実に戻せるよう、呼び出し側が開いた要素を控えている場合はそれを返すこと。
   */
  returnFocus?: () => HTMLElement | null | undefined
  /** ESC が押されたときの処理(省略時は何もしない) */
  onEscape?: () => void
}

/**
 * モーダルが開いている間、フォーカスをダイアログ内に閉じ込める。
 *
 * - 開いたとき: 指定の要素(既定はダイアログ内の先頭)へフォーカスを移す
 * - 開いている間: Tab / Shift+Tab をダイアログ内で循環させ、ESC で onEscape を呼ぶ
 * - 閉じたとき: 開く前の要素(または returnFocus)へフォーカスを戻す
 *
 * キーイベントは document で受ける(何らかの理由でフォーカスが外にある
 * 場合でも ESC・Tab を取りこぼさないため)。
 *
 * @param container ダイアログ本体の要素(`ref` で受け取る)
 * @param isOpen    開いているか(ref / computed)
 */
export function useModalFocus(
  container: Ref<HTMLElement | null | undefined>,
  isOpen: Readonly<Ref<boolean>>,
  options: ModalFocusOptions = {},
): void {
  /** 開く直前にフォーカスがあった要素(閉じたときの戻り先の既定値) */
  let previouslyFocused: HTMLElement | null = null

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      options.onEscape?.()
      return
    }
    const el = container.value
    if (el) trapTabKey(el, e)
  }

  watch(isOpen, (open) => {
    if (open) {
      previouslyFocused = (document.activeElement as HTMLElement | null) ?? null
      document.addEventListener('keydown', onKeydown)
      // ダイアログの描画後にフォーカスを移す(描画前は要素がまだ無い)
      void nextTick(() => {
        const target = options.initialFocus?.() ?? null
        if (target) {
          target.focus()
          return
        }
        const el = container.value
        if (el) focusableElementsIn(el)[0]?.focus()
      })
      return
    }
    document.removeEventListener('keydown', onKeydown)
    const back = options.returnFocus?.() ?? previouslyFocused
    previouslyFocused = null
    // 一覧の再描画と競合しないよう、閉じた後の描画を待ってから戻す
    // (対象が既に取り除かれている場合は何も起きない)
    void nextTick(() => back?.focus())
  })

  // 開いたまま画面を離れた場合にリスナーを残さない
  onUnmounted(() => document.removeEventListener('keydown', onKeydown))
}
