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
it('keeps the right panel and the same body root at height 600 before any frame', async () => {
 vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 1)
 await open()
 const layout = mounted!.host.querySelector<HTMLElement>('.wiki-layout')!
 mounted!.host.querySelectorAll<HTMLElement>('.wiki-node-name')[1]!.click(); await flush()
 expect(panel().previousElementSibling?.classList.contains('wiki-content')).toBe(true)
 // 共通MarkdownBodyのroot参照を検査し、低高時の外側rootへの退避を検知する。
 function find(node: VNode | undefined): unknown {
  if (node?.component?.type === MarkdownBody) return node.component.props.scrollContainer
  if (node?.component) { const result = find(node.component.subTree); if (result) return result }
  if (Array.isArray(node?.children)) for (const child of node.children) if (typeof child === 'object' && child && 'type' in child) { const result = find(child as VNode); if (result) return result }
 }
 expect(find(mounted!.app._instance?.subTree)).toBe(mounted!.host.querySelector('.wiki-content'))
 expect(layout.classList.contains('panel-scroll')).toBe(true)
 expect(layout.style.height).toBe('584px')
})
it.each([759, 760])('uses actual layout width %i and separates drawer state from saved preference', async value => {
 width = value; localStorage.setItem('ba.wikiPanelCollapsed', '0'); await open()
 expect(panel().hidden).toBe(value < 760)
 expect(toggle().getAttribute('aria-expanded')).toBe(String(value >= 760))
 toggle().click(); await flush()
 expect(panel().getAttribute('role')).toBe(value < 760 ? 'dialog' : null)
 expect(localStorage.getItem('ba.wikiPanelCollapsed')).toBe(value < 760 ? '0' : '1')
})
