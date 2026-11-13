import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick, type VNode } from 'vue'
import WikiView from './WikiView.vue'
import MarkdownBody from '../components/MarkdownBody.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async original => ({ ...(await original<typeof import('../lib/backend')>()), getBackend: () => holder.backend }))
let mounted: MountedApp | undefined, width = 1000, height = 600
async function flush() { for (let i = 0; i < 30; i++) await nextTick() }
const panel = () => document.querySelector<HTMLElement>('#wiki-search-panel')!
const toggle = () => mounted!.host.querySelector<HTMLButtonElement>('[aria-controls="wiki-search-panel"]')!
// 背景オーバーレイのpointer遮断と本文下へのCSS配置はhappy-domでは検証できない。
// 主庫のdrawer-interactions.mjs（hit-test）とpanel-layout.mjs（128条件の矩形比較）が担当する。
function markdownNode(node: VNode | undefined): VNode | undefined {
 if (node?.component?.type === MarkdownBody) return node
 if (node?.component) { const found = markdownNode(node.component.subTree); if (found) return found }
 if (Array.isArray(node?.children)) for (const child of node.children) if (child && typeof child === 'object' && 'type' in child) {
  const found = markdownNode(child as VNode); if (found) return found
 }
}
async function selectPage() {
 panel().querySelector<HTMLElement>('[data-row-key="p:1"] .wiki-node-name')!.click(); await flush()
 if (panel().hidden) { toggle().click(); await flush() }
 await new Promise<void>(resolve => requestAnimationFrame(() => resolve())); await flush()
}
async function open() { mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush() }
beforeEach(() => {
 localStorage.clear()
 const backend = createMockBackend()
 backend.getActiveProfile = async () => 'p'; backend.listProfiles = async () => []; backend.syncProjects = async () => {}
 backend.listProjects = async () => [{ id: 101, name: 'Sample', projectKey: 'SAMPLE', lastSyncedAt: '', syncStateUnknown: false }]
 backend.listWikis = async () => ({ pages: [{ id: 1, projectId: 101, name: 'Guide/Start', content: '# Start\n\n## End\n\nSample', created: '', updated: 'T', contentUpdated: 'T', contentFetchedAt: 'T', tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'markdown', attachments: [] }], disabled: false, lastSyncedAt: '' })
 holder.backend = backend; width = 1000; height = 600
 vi.spyOn(window, 'innerHeight', 'get').mockImplementation(() => height)
 vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function(this: HTMLElement) { return this.classList.contains('wiki-layout') ? width : 320 })
 vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) {
  return { x: 0, y: 0, top: 0, left: 0, bottom: height, right: width, width, height: this.tagName === 'DETAILS' ? 450 : 20, toJSON() {} }
 })
 vi.spyOn(HTMLElement.prototype, 'getClientRects').mockImplementation(function(this: HTMLElement) { return [this.getBoundingClientRect()] as unknown as DOMRectList })
})
afterEach(() => { mounted?.unmount(); mounted = undefined; vi.restoreAllMocks(); localStorage.clear() })
it('Wiki の外枠を ResizeObserver の監視対象に含める', async () => {
 const observe = vi.fn(), disconnect = vi.fn()
 vi.stubGlobal('ResizeObserver', class {
  observe = observe
  disconnect = disconnect
 })
 try {
  await open()
  const frame = mounted!.host.querySelector('.wiki-content-frame')!
  expect(frame).not.toBeNull()
  expect(observe).toHaveBeenCalledWith(frame)
 } finally {
  mounted?.unmount(); mounted = undefined
  vi.unstubAllGlobals()
 }
})
it('keeps the right panel and the same body root at height 600 before any frame', async () => {
 vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 1)
 await open()
 const layout = mounted!.host.querySelector<HTMLElement>('.wiki-layout')!
 mounted!.host.querySelectorAll<HTMLElement>('.wiki-node-name')[1]!.click(); await flush()
 expect(panel().previousElementSibling?.querySelector('.wiki-content')).toBe(mounted!.host.querySelector('.wiki-content'))
 // 共通MarkdownBodyのroot参照を検査し、低高時の外側rootへの退避を検知する。
 function find(node: VNode | undefined): unknown {
  if (node?.component?.type === MarkdownBody) return node.component.props.scrollContainer
  if (node?.component) { const result = find(node.component.subTree); if (result) return result }
  if (Array.isArray(node?.children)) for (const child of node.children) if (typeof child === 'object' && child && 'type' in child) { const result = find(child as VNode); if (result) return result }
 }
 expect(find(mounted!.app._instance?.subTree)).toBe(mounted!.host.querySelector('.wiki-content'))
 expect(layout.classList.contains('panel-scroll')).toBe(true)
 expect(layout.style.height).toBe('600px')
})
it.each([759, 760])('uses actual layout width %i and separates drawer state from saved preference', async value => {
 width = value; localStorage.setItem('ba.wikiPanelCollapsed', '0'); await open()
 expect(panel().hidden).toBe(value < 760)
 expect(toggle().getAttribute('aria-expanded')).toBe(String(value >= 760))
 toggle().click(); await flush()
 expect(panel().getAttribute('role')).toBe(value < 760 ? 'dialog' : null)
 expect(localStorage.getItem('ba.wikiPanelCollapsed')).toBe(value < 760 ? '0' : '1')
})
it('traps focus without inert support, ignores IME Escape, restores focus and cleans up', async () => {
 width = 759
 const inert = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'inert')
 delete (HTMLElement.prototype as { inert?: boolean }).inert
 const setAttribute = Element.prototype.setAttribute
 vi.spyOn(Element.prototype, 'setAttribute').mockImplementation(function(this: Element, name, value) { if (name !== 'inert') setAttribute.call(this, name, value) })
 try {
  await open(); toggle().focus(); toggle().click(); await flush()
  const drawer = panel(), close = drawer.querySelector<HTMLButtonElement>('.wiki-drawer-close')!
  expect(close).not.toBeNull(); expect(document.activeElement).toBe(close)
  expect(mounted!.host.getAttribute('aria-hidden')).toBe('true')
  toggle().focus(); expect(document.activeElement).toBe(close)
  const last = [...drawer.querySelectorAll<HTMLElement>('button, [tabindex="0"]')].filter(el => !el.hasAttribute('disabled')).at(-1)!
  last.focus(); last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }))
  expect(document.activeElement).toBe(close)
  close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true }))
  expect(drawer.hidden).toBe(false)
  close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await flush()
  expect(drawer.hidden).toBe(true); expect(document.activeElement).toBe(toggle())
  expect(mounted!.host.hasAttribute('aria-hidden')).toBe(false)
  expect(localStorage.getItem('ba.wikiPanelCollapsed')).toBeNull()
  toggle().click(); await flush(); mounted!.unmount(); mounted = undefined
  expect(document.querySelector('.wiki-drawer-host')).toBeNull()
 } finally { if (inert) Object.defineProperty(HTMLElement.prototype, 'inert', inert) }
})
it('keeps the drawer for folder and fold actions, closes only after page selection', async () => {
 width = 759; await open(); toggle().click(); await flush()
 panel().querySelector<HTMLElement>('.folder .wiki-node-name')!.click(); await flush()
 expect(panel().hidden).toBe(false)
 panel().querySelector<HTMLElement>('.folder .wiki-node-name')!.click(); await flush()
 panel().querySelector<HTMLElement>('[data-row-key="p:1"] .wiki-node-name')!.click(); await flush()
 expect(panel().hidden).toBe(true)
 expect(document.activeElement).toBe(mounted!.host.querySelector('.wiki-detail h2'))
})
it('closes through the mask and restores the opener without writing preference', async () => {
 width = 759; await open(); toggle().click(); await flush()
 document.querySelector<HTMLElement>('.wiki-drawer-mask')!.click(); await flush()
 expect(panel().hidden).toBe(true); expect(document.activeElement).toBe(toggle())
 expect(localStorage.getItem('ba.wikiPanelCollapsed')).toBeNull()
})

it('keeps both text actions together on a dedicated row after the tree title', async () => {
 await open()
 const heading = panel().querySelector('.wiki-tree-heading')!
 const actions = heading.querySelector('.wiki-tree-actions')!
 expect(actions).not.toBeNull()
 expect(actions.querySelectorAll('button')).toHaveLength(2)
 expect(actions.previousElementSibling?.tagName).toBe('H3')
 expect(actions.textContent).toContain('Expand all')
 expect(actions.textContent).toContain('Collapse all')
})

it.each([1000, 759])('caps contents in a scrolling panel at layout width %i', async value => {
 width = value
 vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(480)
 await open(); if (panel().hidden) { toggle().click(); await flush() }
 await selectPage()
 const nav = panel().querySelector<HTMLElement>('.wiki-toc')!
 expect(panel().classList.contains('wiki-panel-scroll')).toBe(true)
 expect(nav.style.maxHeight).toBe(value < 760 ? '120px' : '150px')
 expect(nav.parentElement!.nextElementSibling?.classList.contains('wiki-tree-heading')).toBe(true)
})

it('reveals the active contents entry within contents without moving the panel', async () => {
 await open(); await selectPage()
 panel().querySelector<HTMLDetailsElement>('.wiki-toc-section')!.open = true
 await flush()
 const nav = panel().querySelector<HTMLElement>('.wiki-toc')!, buttons = nav.querySelectorAll('button')
 Object.defineProperty(nav, 'getBoundingClientRect', { value: () => ({ top: 100, bottom: 200 }) })
 Object.defineProperty(buttons[1]!, 'getBoundingClientRect', { value: () => ({ top: 300, bottom: 330 }) })
 panel().scrollTop = 75; nav.scrollTop = 0
 markdownNode(mounted!.app._instance?.subTree)!.component!.emit('active', '')
 await flush()
 markdownNode(mounted!.app._instance?.subTree)!.component!.emit('active', mounted!.host.querySelector('.wiki-content h2[id]')!.id)
 await flush()
 expect(buttons[1]!.getAttribute('aria-current')).toBe('location')
 expect(nav.scrollTop).toBe(130)
 expect(panel().scrollTop).toBe(75)
})

it.each(['drawer', 'contents'] as const)('reveals the current entry when reopening %s without moving the panel', async mode => {
 width = mode === 'drawer' ? 759 : 1000
 await open()
 if (panel().hidden) { toggle().click(); await flush() }
 await selectPage()
 const details = panel().querySelector<HTMLDetailsElement>('.wiki-toc-section')!
 details.open = true; await flush()
 const nav = panel().querySelector<HTMLElement>('.wiki-toc')!, current = nav.querySelectorAll('button')[1]!
 Object.defineProperty(nav, 'getBoundingClientRect', { value: () => ({ top: 100, bottom: 200 }) })
 Object.defineProperty(current, 'getBoundingClientRect', { value: () => ({ top: 300, bottom: 330 }) })
 markdownNode(mounted!.app._instance?.subTree)!.component!.emit('active', '')
 await flush()
 if (mode === 'drawer') toggle().click()
 else { details.open = false; details.dispatchEvent(new Event('toggle')) }
 await flush()
 panel().scrollTop = 75; nav.scrollTop = 0
 markdownNode(mounted!.app._instance?.subTree)!.component!.emit('active', mounted!.host.querySelector('.wiki-content h2[id]')!.id)
 await flush()
 expect(current.getAttribute('aria-current')).toBe('location')
 expect(nav.scrollTop).toBe(0)
 if (mode === 'drawer') toggle().click()
 else { details.open = true; details.dispatchEvent(new Event('toggle')) }
 await flush()
 expect(nav.scrollTop).toBe(130)
 expect(panel().scrollTop).toBe(75)
})

it('Wiki 同期中の状態行に合わせて本文領域の高さを再計算する', async () => {
 const backend = holder.backend as ReturnType<typeof createMockBackend>
 let finishSync!: (result: Awaited<ReturnType<typeof backend.syncWikis>>) => void
 backend.syncWikis = vi.fn(() => new Promise<Awaited<ReturnType<typeof backend.syncWikis>>>(resolve => { finishSync = resolve }))
 height = 800
 const originalRect = vi.mocked(HTMLElement.prototype.getBoundingClientRect).getMockImplementation()!
 vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) {
  const rect = originalRect.call(this)
  if (!this.classList.contains('wiki-layout')) return rect
  const top = mounted?.host.querySelector('.wiki-operation-status p[role="status"]') ? 239 : 200
  return { ...rect, top, y: top, height: height - top }
 })
 const settleLayout = async () => {
  await flush()
  await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  await flush()
 }
 try {
  await open(); await selectPage(); await settleLayout()
  const layout = mounted!.host.querySelector<HTMLElement>('.wiki-layout')!
  const before = Number.parseFloat(layout.style.height)
  const sync = [...mounted!.host.querySelectorAll('button')].find(button => button.textContent?.trim() === 'Sync Wiki')!
  sync.click(); await settleLayout()
  expect(backend.syncWikis).toHaveBeenCalledOnce()
  expect(mounted!.host.querySelector('.wiki-operation-status p[role="status"]')).not.toBeNull()
  expect(layout.style.height).toBe(`${before - 39}px`)
 } finally {
  finishSync?.({ mode: 'full', fetched: 1, upserted: 0, deleted: 0, durationMs: 0, disabled: false, warnings: [] })
  await settleLayout()
 }
})

it.each([
 { name: '標準の余白', viewport: 800, outerHeight: 800, clientTop: 0, scroll: 0, padding: 24, section: 20, top: 200, expected: 556 },
 { name: '外側スクロール後', viewport: 800, outerHeight: 800, clientTop: 0, scroll: 80, padding: 24, section: 20, top: 120, expected: 556 },
 { name: 'client 領域の下端', viewport: 800, outerHeight: 740, clientTop: 2, scroll: 0, padding: 24, section: 20, top: 200, expected: 498 },
 { name: 'ルート文字サイズの変化', viewport: 800, outerHeight: 800, clientTop: 0, scroll: 0, padding: 30, section: 25, top: 200, expected: 545 },
 { name: '通知で上端が下がる', viewport: 800, outerHeight: 800, clientTop: 0, scroll: 0, padding: 24, section: 20, top: 350, expected: 406 },
 { name: '内側 160px の下限', viewport: 240, outerHeight: 240, clientTop: 0, scroll: 0, padding: 24, section: 20, top: 200, expected: 170 },
])('$name を実寸から計算する', async sample => {
 height = sample.viewport
 const outer = document.createElement('main'); outer.className = 'content'; document.body.append(outer)
 outer.scrollTop = sample.scroll
 Object.defineProperties(outer, { clientHeight: { value: sample.outerHeight }, clientTop: { value: sample.clientTop } })
 const originalRect = vi.mocked(HTMLElement.prototype.getBoundingClientRect).getMockImplementation()!
 vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function(this: HTMLElement) {
  if (this === outer) return { top: 0, bottom: height, height, left: 0, right: width, width, x: 0, y: 0, toJSON() {} }
  if (this.classList.contains('wiki-layout') || this.classList.contains('wiki-content')) return { top: sample.top, bottom: height, height: height - sample.top, left: 0, right: width, width, x: 0, y: sample.top, toJSON() {} }
  return originalRect.call(this)
 })
 const originalStyle = window.getComputedStyle.bind(window)
 vi.spyOn(window, 'getComputedStyle').mockImplementation((el, pseudo) => {
  const css = originalStyle(el, pseudo)
  const values: Record<string, string> = el === outer ? { paddingBottom: `${sample.padding}px` }
   : el.classList.contains('wiki-view') ? { paddingBottom: `${sample.section}px` }
   : el.classList.contains('wiki-content-frame') ? { paddingTop: '4px', paddingBottom: '4px', borderTopWidth: '1px', borderBottomWidth: '1px' } : {}
  return new Proxy(css, { get: (target, key) => typeof key === 'string' && key in values ? values[key] : Reflect.get(target, key) })
 })
 // マウント時から外側コンテナを持たせ、初回同期測定も検査する。
 const append = document.body.appendChild.bind(document.body)
 vi.spyOn(document.body, 'appendChild').mockImplementation(<T extends Node>(node: T): T => node.nodeName === 'DIV' ? outer.appendChild(node) : append(node))
 try {
  await open()
  expect(mounted!.host.querySelector<HTMLElement>('.wiki-layout')!.style.height).toBe(`${sample.expected}px`)
  const before = outer.scrollTop
  mounted!.host.querySelector<HTMLElement>('[data-row-key="p:1"] .wiki-node-name')!.click(); await flush()
  expect(outer.scrollTop).toBe(sample.expected === 170 ? before + sample.top : before)
 } finally { mounted?.unmount(); mounted = undefined; outer.remove() }
})
