import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { WikiPage } from '../lib/backend'

const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async (original) => ({
  ...(await original<typeof import('../lib/backend')>()),
  getBackend: () => holder.backend,
}))

const storageKey = 'ba.wikiPanelCollapsed'
let mounted: MountedApp | undefined
let View: typeof import('./WikiView.vue')['default']
const page: WikiPage = {
  id: 1, projectId: 101, name: 'Guide/Start', content: 'Sample body',
  created: '', updated: 'T1', contentUpdated: 'T1', contentFetchedAt: 'now',
  tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'backlog',
  attachments: [],
}
async function flush() {
  for (let i = 0; i < 15; i++) await nextTick()
}
async function open(locale: 'en' | 'ja' = 'en') {
  mounted = mountWithI18n(View, { locale })
  await flush()
  return mounted.host
}
function toggle() {
  const button = mounted!.host.querySelector<HTMLButtonElement>('[aria-controls="wiki-search-panel"]')
  expect(button).not.toBeNull()
  return button!
}
beforeEach(async () => {
  localStorage.clear()
  vi.resetModules()
  View = (await import('./WikiView.vue')).default
  const backend = createMockBackend()
  backend.getActiveProfile = async () => 'p'
  backend.listProfiles = async () => []
  backend.syncProjects = async () => {}
  backend.listProjects = async () => [
    { id: 101, name: 'Sample', projectKey: 'SAMPLE', lastSyncedAt: '', syncStateUnknown: false },
  ]
  backend.listWikis = vi.fn(async () => ({ pages: [page], disabled: false, lastSyncedAt: '' }))
  holder.backend = backend
})
afterEach(() => {
  mounted?.unmount()
  mounted = undefined
  vi.restoreAllMocks()
  localStorage.clear()
})

it('places the search panel after the content with an accessible native toggle', async () => {
  const host = await open()
  const panel = host.querySelector<HTMLElement>('#wiki-search-panel')
  expect(panel).not.toBeNull()
  expect(panel?.previousElementSibling?.classList.contains('wiki-content')).toBe(true)
  expect(toggle().type).toBe('button')
  expect(toggle().getAttribute('aria-expanded')).toBe('true')
  expect(toggle().textContent).toContain('Hide search and tree')
  expect(panel?.getAttribute('aria-label')).toBe('Search and page tree')
})

it('collapses and expands while retaining the selected page and search controls', async () => {
  const host = await open()
  const start = Array.from(host.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Start')!
  start.click()
  const keyword = host.querySelector<HTMLInputElement>('[data-testid="wiki-keyword"]')!
  keyword.value = 'Sample'
  keyword.dispatchEvent(new Event('input', { bubbles: true }))
  host.querySelector<HTMLInputElement>('input[value="or"]')!.click()
  await flush()
  toggle().focus()
  toggle().click()
  await flush()
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
  expect(host.querySelector<HTMLElement>('#wiki-search-panel')?.hidden).toBe(true)
  expect(host.querySelector('.wiki-layout')?.classList.contains('panel-collapsed')).toBe(true)
  expect(host.querySelector('.wiki-preview')?.textContent).toBe('Sample body')
  expect(document.activeElement).toBe(toggle())
  toggle().click()
  await flush()
  expect(toggle().getAttribute('aria-expanded')).toBe('true')
  expect(host.querySelector<HTMLElement>('#wiki-search-panel')?.hidden).toBe(false)
  expect(host.querySelector<HTMLInputElement>('[data-testid="wiki-keyword"]')?.value).toBe('Sample')
  expect(host.querySelector<HTMLInputElement>('input[value="or"]')?.checked).toBe(true)
  expect(start.getAttribute('aria-pressed')).toBe('true')
})

it('persists the preference across view visits and a fresh module session', async () => {
  await open()
  toggle().click()
  await flush()
  expect(localStorage.getItem(storageKey)).toBe('1')
  mounted!.unmount()
  await open()
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
  mounted!.unmount()
  vi.resetModules()
  View = (await import('./WikiView.vue')).default
  await open()
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
  toggle().click()
  await flush()
  expect(localStorage.getItem(storageKey)).toBe('0')
})

it.each(['invalid', 'true', '0'])('opens the panel for non-collapsed stored value %s', async (value) => {
  localStorage.setItem(storageKey, value)
  await open()
  expect(toggle().getAttribute('aria-expanded')).toBe('true')
})

it.each(['read', 'write'])('retains session state when storage %s fails', async (failure) => {
  if (failure === 'read') {
    vi.spyOn(localStorage, 'getItem').mockImplementation(() => { throw new Error('unavailable') })
  } else {
    localStorage.setItem(storageKey, '0')
  }
  vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('unwritable') })
  await open()
  expect(toggle().getAttribute('aria-expanded')).toBe('true')
  toggle().click()
  await flush()
  mounted!.unmount()
  await open()
  expect(toggle().getAttribute('aria-expanded')).toBe('false')
})

it('updates both toggle states and the panel name when switching languages', async () => {
  const host = await open('ja')
  expect(toggle().textContent).toContain('検索・ツリーを隠す')
  expect(host.querySelector('aside')?.getAttribute('aria-label')).toBe('検索・ページツリー')
  toggle().click()
  await flush()
  expect(toggle().textContent).toContain('検索・ツリーを表示')
  mounted!.i18n.global.locale.value = 'en'
  await flush()
  expect(toggle().textContent).toContain('Show search and tree')
  toggle().click()
  await flush()
  expect(toggle().textContent).toContain('Hide search and tree')
  expect(host.querySelector('aside')?.getAttribute('aria-label')).toBe('Search and page tree')
})
