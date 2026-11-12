import { afterEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { createMockBackend } from '../lib/backend/mock'
import type { WikiPage } from '../lib/backend'
import { activeIssueSync, endIssueSync } from '../lib/syncState'
import WikiView from './WikiView.vue'
import { wikiMarkdownFixture } from '../../test/fixtures/wikiMarkdownFixture'
const holder = vi.hoisted(() => ({ backend: null as unknown, openExternal: vi.fn() }))
vi.mock('../lib/backend', async (original) => ({
  ...(await original<typeof import('../lib/backend')>()),
  getBackend: () => holder.backend,
  openExternalURL: holder.openExternal,
}))
let mounted: MountedApp | undefined
async function flush() {
  for (let i = 0; i < 15; i++) await nextTick()
}
afterEach(() => {
  mounted?.unmount()
  holder.openExternal.mockClear()
  if (activeIssueSync.value) endIssueSync(activeIssueSync.value.runId)
})
it('browses cached pages, searches locally and confirms export with sync enabled', async () => {
  const b = createMockBackend()
  const p: WikiPage = {
    id: 1,
    projectId: 101,
    name: 'Guide/Start',
    content: `# Hello\n<script>bad()</script>\n\n${wikiMarkdownFixture}`,
    created: '',
    updated: 'T1',
    contentUpdated: 'T1',
    contentFetchedAt: 'now',
    tags: ['tag'],
    createdUser: 'Writer',
    updatedUser: 'Editor',
    textFormattingRule: 'markdown',
    attachments: [{ id: 2, wikiId: 1, name: 'sample.txt', size: 4 }],
  }
  b.getActiveProfile = async () => 'p'
  b.listProfiles = async () => [
    {
      id: 'p',
      name: 'Sample',
      spaceUrl: 'https://example.backlog.jp',
      lastUserId: 1,
      lastUserName: '',
    },
  ]
  b.syncProjects = vi.fn(async () => {})
  b.listProjects = async () => [
    {
      id: 101,
      name: 'Sample',
      projectKey: 'SAMPLE',
      lastSyncedAt: '',
      syncStateUnknown: false,
    },
  ]
  b.listWikis = vi.fn(async () => ({
    pages: [p],
    disabled: false,
    lastSyncedAt: '2026-11-11T00:00:00Z',
  }))
  b.prepareWikiExport = vi.fn(async () => ({
    pages: 1,
    attachments: 1,
    bytes: 4,
    estimatedSeconds: 1,
    missingContent: 0,
    lastSyncedAt: 'now',
    warnings: [],
  }))
  b.exportWiki = vi.fn(async () => ({
    path: 'export',
    rows: 1,
    unverifiable: 0,
  }))
  holder.backend = b
  mounted = mountWithI18n(WikiView, { locale: 'en' })
  await flush()
  const button = (text: string) =>
    Array.from(mounted!.host.querySelectorAll<HTMLElement>('button, .wiki-node-name')).find(
      (el) => el.textContent?.trim() === text,
    )!
  button('Start').click()
  await flush()
  expect(mounted.host.querySelector('.wiki-preview h1')?.textContent).toBe(
    'Hello',
  )
  expect(mounted.host.querySelector('script')).toBeNull()
  const preview = mounted.host.querySelector('.wiki-preview.markdown-body')!
  expect(preview.querySelectorAll('th')).toHaveLength(3)
  expect(preview.querySelectorAll('td')).toHaveLength(3)
  expect(preview.querySelector('img, script, input, [href], [src]')).toBeNull()
  const link = preview.querySelector('a')!
  link.click()
  link.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  link.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
  expect(holder.openExternal.mock.calls).toEqual([
    ['https://example.com/guide'], ['https://example.com/guide'],
  ])
  const toggle = mounted.host.querySelector<HTMLInputElement>('.wiki-detail input[type="checkbox"]')!
  toggle.click()
  await flush()
  expect(mounted.host.querySelector('pre.wiki-preview')?.textContent).toBe(p.content)
  toggle.click()
  await flush()
  expect(mounted.host.querySelectorAll('.wiki-preview th')).toHaveLength(3)
  const input = mounted.host.querySelector<HTMLInputElement>(
    '[data-testid="wiki-keyword"]',
  )!
  input.value = 'Hello'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  input.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
  )
  await flush()
  expect(b.listWikis).toHaveBeenLastCalledWith('p', {
    projectId: 101,
    keyword: 'Hello',
    keywordMode: 'and',
  })
  button('Export project').click()
  await flush()
  expect(b.prepareWikiExport).toHaveBeenCalledWith(
    'p',
    101,
    '',
    true,
    expect.any(String),
  )
  expect(mounted.host.querySelector('[role="dialog"]')?.textContent).toContain(
    '1 attachments',
  )
  expect(b.exportWiki).not.toHaveBeenCalled()
  button('Start export').click()
  await flush()
  expect(b.exportWiki).toHaveBeenCalled()
})
it('shows disabled Wiki without triggering a Wiki sync on mount', async () => {
  const b = createMockBackend()
  b.getActiveProfile = async () => 'p'
  b.listProfiles = async () => []
  b.listProjects = async () => [
    {
      id: 101,
      name: 'Sample',
      projectKey: 'SAMPLE',
      lastSyncedAt: '',
      syncStateUnknown: false,
    },
  ]
  b.syncProjects = async () => {}
  b.listWikis = async () => ({ pages: [], disabled: true, lastSyncedAt: '' })
  b.syncWikis = vi.fn(b.syncWikis)
  holder.backend = b
  mounted = mountWithI18n(WikiView, { locale: 'en' })
  await flush()
  expect(mounted.host.textContent).toContain('Wiki disabled')
  expect(b.syncWikis).not.toHaveBeenCalled()
})

it('cancels preparation while a project refresh is still pending', async () => {
  const b = createMockBackend()
  b.getActiveProfile = async () => 'p'
  b.listProfiles = async () => []
  b.listProjects = async () => [
    {
      id: 101,
      name: 'Sample',
      projectKey: 'SAMPLE',
      lastSyncedAt: '',
      syncStateUnknown: false,
    },
  ]
  b.syncProjects = async () => {}
  b.listWikis = async () => ({ pages: [], disabled: false, lastSyncedAt: '' })
  b.prepareWikiExport = vi.fn(b.prepareWikiExport)
  holder.backend = b
  mounted = mountWithI18n(WikiView, { locale: 'en' })
  await flush()
  let release!: () => void
  b.syncProjects = () =>
    new Promise<void>((resolve) => {
      release = resolve
    })
  const button = (text: string) =>
    Array.from(mounted!.host.querySelectorAll<HTMLElement>('button, .wiki-node-name')).find(
      (el) => el.textContent?.trim() === text,
    )!
  button('Export project').click()
  await flush()
  button('Cancel').click()
  await flush()
  release()
  await flush()
  expect(b.prepareWikiExport).not.toHaveBeenCalled()
  expect(activeIssueSync.value).toBeNull()
})
