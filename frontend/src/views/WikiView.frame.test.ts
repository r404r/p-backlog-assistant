import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import WikiView from './WikiView.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'

const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async original => ({ ...(await original<typeof import('../lib/backend')>()), getBackend: () => holder.backend }))
let mounted: MountedApp | undefined
async function flush() { for (let i = 0; i < 30; i++) await nextTick() }
beforeEach(() => {
  localStorage.clear()
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
  const backend = createMockBackend()
  backend.getActiveProfile = async () => 'p'
  backend.listProfiles = async () => []
  backend.syncProjects = async () => {}
  backend.listProjects = async () => [{ id: 101, name: 'Sample', projectKey: 'SAMPLE', lastSyncedAt: '', syncStateUnknown: false }]
  backend.listWikis = async () => ({ pages: [{ id: 1, projectId: 101, name: 'Guide/Start', content: '# Sample\n\nBody', created: '', updated: 'T', contentUpdated: 'T', contentFetchedAt: 'T', tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'markdown', attachments: [] }], disabled: false, lastSyncedAt: '' })
  backend.beginWikiEdit = async () => ({ token: 'edit', baselineUpdated: 'T', page: (await backend.listWikis('p', { projectId: 101, keyword: '', keywordMode: 'and' })).pages[0]! })
  holder.backend = backend
})
afterEach(() => { mounted?.unmount(); mounted = undefined; vi.restoreAllMocks(); localStorage.clear() })
it('未選択・整形・原文・編集で同じ枠と本文スクロール要素を使う', async () => {
  mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush()
  const host = mounted.host, frame = host.querySelector('.wiki-content-frame')!
  expect(frame).not.toBeNull()
  const root = frame.querySelector('.wiki-content')!
  expect(root.textContent).toContain('Select a page')
  expect(frame.hasAttribute('tabindex')).toBe(false)
  host.querySelector<HTMLElement>('[data-row-key="p:1"] .wiki-node-name')!.click(); await flush()
  expect(frame.querySelector('.wiki-detail h2')?.textContent).toBe('Guide/Start')
  expect(frame.querySelector('.markdown-body')).not.toBeNull()
  root.querySelector<HTMLInputElement>('.toolbar input[type="checkbox"]')!.click(); await flush()
  expect(root.querySelector('pre')?.textContent).toContain('# Sample')
  const edit = [...root.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Edit')!
  edit.click(); await flush()
  expect(root.querySelector('textarea')).not.toBeNull()
  expect(host.querySelector('.wiki-content-frame')).toBe(frame)
  expect(frame.querySelector('.wiki-content')).toBe(root)
})
