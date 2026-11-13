import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import WikiView from './WikiView.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { WikiPage } from '../lib/backend'
const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async original => ({ ...(await original<typeof import('../lib/backend')>()), getBackend: () => holder.backend }))
let mounted: MountedApp | undefined, backend: ReturnType<typeof createMockBackend>
const pages = ['Guide/Start', 'Guide/More/End', 'Box/Item', 'Other/Child'].map((name, i): WikiPage => ({ id: i + 1, projectId: 101, name, content: '# Start\n\n[[Other/Child]]', created: '', updated: 'T', contentUpdated: 'T', contentFetchedAt: 'T', tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'markdown', attachments: [] }))
async function flush() { for (let i = 0; i < 25; i++) await nextTick() }
function row(key: string) { return Array.from(mounted!.host.querySelectorAll<HTMLElement>('[role=treeitem]')).find(r => r.dataset.rowKey === key)! }
function name(text: string) { return Array.from(mounted!.host.querySelectorAll<HTMLElement>('.wiki-node-name')).find(r => r.textContent === text)! }
function button(text: string) { return Array.from(mounted!.host.querySelectorAll<HTMLButtonElement>('button')).find(r => r.textContent?.trim() === text)! }
beforeEach(async () => {
 localStorage.clear()
 backend = createMockBackend()
 backend.getActiveProfile = async () => 'p'; backend.listProfiles = async () => []; backend.syncProjects = async () => {}
 backend.listProjects = async () => [{ id: 101, name: 'Sample', projectKey: 'SAMPLE', lastSyncedAt: '', syncStateUnknown: false }]
 backend.listWikis = vi.fn(async (_p, query) => ({ pages: query.keyword ? pages.slice(0, 1) : pages, disabled: false, lastSyncedAt: '' }))
 backend.prepareWikiExport = vi.fn(async () => ({ pages: 1, attachments: 0, bytes: 0, estimatedSeconds: 0, missingContent: 0, warnings: [], lastSyncedAt: '', disabled: false }))
 holder.backend = backend
 mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush()
})
afterEach(() => { mounted?.unmount(); vi.restoreAllMocks(); localStorage.clear() })
it('selects a directory for export and new parent while keeping the current body', async () => {
 name('Start').click(); await flush()
 const before = mounted!.host.querySelector('.wiki-preview')?.textContent
 name('Box').click(); await flush()
 expect(mounted!.host.querySelector('.wiki-preview')?.textContent).toBe(before)
 expect(row('p:1').getAttribute('aria-current')).toBe('page')
 expect(row('d:["Box"]').getAttribute('aria-selected')).toBe('true')
 button('New page').click(); await flush()
 expect(mounted!.host.querySelector<HTMLSelectElement>('.wiki-editor select')?.value).toBe('Box')
 button('Cancel').click(); await flush()
 button('Export selected subtree').click(); await flush()
 expect(backend.prepareWikiExport).toHaveBeenCalledWith('p', 101, 'Box', true, expect.any(String))
})
it('retains collapsed branches across reload/search, applies all actions to the full tree, and reveals outside links', async () => {
 name('Start').click(); await flush()
 const box = row('d:["Box"]')
 box.querySelector<HTMLElement>('.wiki-node-arrow')!.click(); await flush()
 expect(name('Item')).toBeUndefined()
 const input = mounted!.host.querySelector<HTMLInputElement>('[data-testid=wiki-keyword]')!
 input.value = 'Start'; input.dispatchEvent(new Event('input', { bubbles: true }))
 button('Search').click(); await flush()
 button('Collapse all').click(); await flush()
 button('Expand all').click(); await flush()
 const link = mounted!.host.querySelector<HTMLElement>('a[data-md-wiki]')!
 link.click(); await flush()
 expect(input.value).toBe('Start')
 expect(row('p:4').getAttribute('aria-current')).toBe('page')
 expect(row('p:4').textContent).toContain('Current page outside search results')
 input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })); button('Search').click(); await flush()
 expect(name('Item')).toBeDefined()
})

it('keeps contents when reopening the same page and resets only the body', async () => {
 name('Start').click(); await flush()
 const toc = mounted!.host.querySelector('.wiki-toc')?.textContent
 const body = mounted!.host.querySelector<HTMLElement>('.wiki-content')!
 body.scrollTop = 200
 name('Start').click(); await flush()
 expect(mounted!.host.querySelector('.wiki-toc')?.textContent).toBe(toc)
 expect(body.scrollTop).toBe(0)
})
it('resets folding on project changes while searches retain collapsed branches', async () => {
 name('Box').click(); await flush()
 const input = mounted!.host.querySelector<HTMLInputElement>('[data-testid=wiki-keyword]')!
 input.value = 'Start'; input.dispatchEvent(new Event('input', { bubbles: true })); button('Search').click(); await flush()
 input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })); button('Search').click(); await flush()
 expect(row('d:["Box"]').getAttribute('aria-expanded')).toBe('false')
 const select = mounted!.host.querySelector('select')!
 select.value = ''; select.dispatchEvent(new Event('change', { bubbles: true })); await flush()
 select.value = '101'; select.dispatchEvent(new Event('change', { bubbles: true })); await flush()
 expect(row('d:["Box"]').getAttribute('aria-expanded')).toBe('true')
})

it('changes count descriptions only when the submitted search replaces the displayed rows', async () => {
 const input = mounted!.host.querySelector<HTMLInputElement>('[data-testid=wiki-keyword]')!
 const count = () => mounted!.host.querySelector('.wiki-node-count')!.getAttribute('aria-label')
 const initialRows = mounted!.host.querySelectorAll('[role=treeitem]').length
 expect(count()).toBe('Descendant pages: 1')
 input.value = 'Start'; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
 expect(count()).toBe('Descendant pages: 1')
 expect(mounted!.host.querySelectorAll('[role=treeitem]')).toHaveLength(initialRows)
 button('Search').click(); await flush()
 expect(count()).toBe('Visible descendant pages: 1')
 expect(mounted!.host.querySelectorAll('[role=treeitem]')).toHaveLength(2)
 input.value = ''; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
 expect(count()).toBe('Visible descendant pages: 1')
 expect(mounted!.host.querySelectorAll('[role=treeitem]')).toHaveLength(2)
 button('Search').click(); await flush()
 expect(count()).toBe('Descendant pages: 1')
 expect(mounted!.host.querySelectorAll('[role=treeitem]')).toHaveLength(initialRows)
})
it('opens only target ancestors while two unrelated branches remain collapsed', async () => {
 name('Start').click(); await flush()
 button('Collapse all').click(); await flush()
 expect(row('d:["Other"]').getAttribute('aria-expanded')).toBe('false')
 // 本文の内部リンクで第三の枝へ移動する。
 mounted!.host.querySelector<HTMLElement>('a[data-md-wiki]')!.click(); await flush()
 expect(row('d:["Box"]').getAttribute('aria-expanded')).toBe('false')
 expect(row('d:["Guide"]').getAttribute('aria-expanded')).toBe('false')
 expect(row('d:["Other"]').getAttribute('aria-expanded')).toBe('true')
 expect(row('p:4').getAttribute('aria-current')).toBe('page')
})
it('preserves the tree scrollTop when selecting an already visible page', async () => {
 const tree = mounted!.host.querySelector<HTMLElement>('.wiki-tree')!
 tree.scrollTop = 240
 // happy-domは実寸法を持たない。可視行の配置・clampは主庫のブラウザ検証で扱う。
 name('Start').click(); await flush()
 expect(tree.scrollTop).toBe(240)
 expect(row('p:1').getAttribute('aria-current')).toBe('page')
})
it('measures the initial layout before the first animation frame', async () => {
 mounted!.unmount()
 const frame = vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 1)
 const width = vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1280)
 const height = vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(800)
 mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush()
 expect(mounted.host.querySelector<HTMLElement>('.wiki-layout')!.style.height).toBe('784px')
 expect(mounted.host.querySelector<HTMLElement>('.wiki-content')).not.toBeNull()
 expect(mounted.host.querySelector<HTMLElement>('.wiki-panel')!.hidden).toBe(false)
 frame.mockRestore(); width.mockRestore(); height.mockRestore()
})
