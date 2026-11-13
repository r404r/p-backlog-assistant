import { nextTick, onUnmounted, watch, type Ref } from 'vue'

// Wikiだけのmodal制御。inert非対応でもpointer・読上げ・焦点の背景抑止を保つ。
export function useWikiDrawer(open: Ref<boolean>, panel: Ref<HTMLElement | null>, host: Ref<HTMLElement | null>, opener: Ref<HTMLElement | null>) {
  let origin: HTMLElement | null = null, generation = 0
  const backgrounds = new Map<HTMLElement, { inert: string | null; hidden: string | null }>()
  let observer: MutationObserver | undefined
  const higherModal = () => [...document.querySelectorAll<HTMLElement>('[aria-modal="true"]')].some(el => el !== panel.value && !el.hidden && el.getClientRects().length > 0)
  function visible(el: HTMLElement) {
    if (!el.getClientRects().length || el.closest('[hidden], [inert]')) return false
    if (getComputedStyle(el).visibility === 'hidden') return false
    for (let parent = el.parentElement; parent && parent !== panel.value; parent = parent.parentElement) {
      if (parent instanceof HTMLDetailsElement && !parent.open && !parent.querySelector('summary')?.contains(el)) return false
    }
    return true
  }
  const candidates = () => [...(panel.value?.querySelectorAll<HTMLElement>('button, input, select, textarea, a[href], summary, [tabindex]') ?? [])]
    .filter(el => el.tabIndex >= 0 && !el.matches(':disabled') && visible(el))
  function focusFirst() { (candidates()[0] ?? panel.value)?.focus({ preventScroll: true }) }
  function focusin(event: FocusEvent) {
    if (open.value && !higherModal() && !panel.value?.contains(event.target as Node)) focusFirst()
  }
  function keydown(event: KeyboardEvent) {
    if (!open.value || higherModal()) return
    if (event.key === 'Escape' && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault(); event.stopPropagation(); close()
    }
    if (event.key !== 'Tab') return
    const list = candidates(), index = list.indexOf(document.activeElement as HTMLElement)
    if (!list.length) { event.preventDefault(); panel.value?.focus(); return }
    if (event.shiftKey && index <= 0) { event.preventDefault(); list.at(-1)?.focus() }
    else if (!event.shiftKey && (index < 0 || index === list.length - 1)) { event.preventDefault(); list[0]?.focus() }
  }
  function restoreBackgrounds() {
    for (const [el, before] of backgrounds) {
      if (before.inert === null) el.removeAttribute('inert'); else el.setAttribute('inert', before.inert)
      if (before.hidden === null) el.removeAttribute('aria-hidden'); else el.setAttribute('aria-hidden', before.hidden)
    }
    backgrounds.clear()
  }
  function shield() {
    // 編集確認などの上位modalはアプリ内にあるため、祖先の抑止を一時解除する。
    if (higherModal()) { restoreBackgrounds(); return }
    for (const el of document.body.children) {
      if (!(el instanceof HTMLElement) || el === host.value || el.contains(host.value) || el.matches('script, style')) continue
      if (!backgrounds.has(el)) backgrounds.set(el, { inert: el.getAttribute('inert'), hidden: el.getAttribute('aria-hidden') })
      el.setAttribute('inert', ''); el.setAttribute('aria-hidden', 'true')
    }
  }
  function release() {
    generation++
    observer?.disconnect(); observer = undefined
    document.removeEventListener('keydown', keydown, true); document.removeEventListener('focusin', focusin, true)
    restoreBackgrounds()
  }
  function close(restore = true) {
    open.value = false; release()
    if (restore) (origin?.isConnected ? origin : opener.value)?.focus({ preventScroll: true })
  }
  watch(open, async value => {
    release()
    if (!value) return
    origin = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : opener.value
    const current = generation
    await nextTick()
    if (!open.value || current !== generation) return
    // 背景をaria-hiddenにする前に焦点をドロワーへ移す。
    focusFirst(); shield()
    document.addEventListener('keydown', keydown, true); document.addEventListener('focusin', focusin, true)
    observer = new MutationObserver(shield); observer.observe(document.body, { childList: true, subtree: true })
  }, { flush: 'post' })
  onUnmounted(release)
  return { close }
}
