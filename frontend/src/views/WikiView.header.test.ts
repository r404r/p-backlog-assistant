import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import WikiView from './WikiView.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { activeIssueSync, endIssueSync } from '../lib/syncState'
const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async original => ({ ...await original<typeof import('../lib/backend')>(), getBackend: () => holder.backend }))
let mounted: MountedApp | undefined, width = 1000
let measure: FrameRequestCallback | undefined
const flush = async () => { for (let n = 0; n < 25; n++) await nextTick() }
beforeEach(() => {
  const b = createMockBackend(); holder.backend = b
  b.getActiveProfile = async () => 'p'; b.listProfiles = async () => []; b.syncProjects = async () => {}
  b.listProjects = async () => [{ id: 1, name: 'Sample', projectKey: 'SAMPLE', lastSyncedAt: '', syncStateUnknown: false }]
  b.listWikis = async () => ({ pages: [], disabled: false, lastSyncedAt: '' })
  width = 1000; measure = undefined
  vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(1800)
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(cb => { measure = cb; return 1 })
  vi.spyOn(window, 'cancelAnimationFrame').mockImplementation(() => {})
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(function(this: HTMLElement) { return this.classList.contains('wiki-header') ? width : 1000 })
})
afterEach(() => { mounted?.unmount(); mounted = undefined; if (activeIssueSync.value) endIssueSync(activeIssueSync.value.runId); vi.restoreAllMocks() })
it('ページ上部の実幅で900/520pxの境界を切り替える', async () => {
  mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush()
  const header = mounted.host.querySelector('.wiki-header')!
  for (const value of [1000, 900, 899, 520, 519, 900]) {
    width = value; window.dispatchEvent(new Event('resize')); measure?.(0); await flush()
    expect(header.classList.contains('compact'), String(value)).toBe(value < 900)
    expect(header.classList.contains('stacked'), String(value)).toBe(value < 520)
  }
})
it('二つの操作行の順序を保ち、処理表示は両行の後にだけ置く', async () => {
  const b = holder.backend as ReturnType<typeof createMockBackend>
  let finish!: () => void
  b.prepareWikiExport = async () => {
    await new Promise<void>(done => { finish = done })
    return { pages: 0, attachments: 0, bytes: 0, estimatedSeconds: 0, missingContent: 0, lastSyncedAt: '', warnings: [] }
  }
  mounted = mountWithI18n(WikiView, { locale: 'en' }); await flush()
  const host = mounted.host, project = host.querySelector('.wiki-header-row--project')!, exports = host.querySelector('.wiki-header-row--export')!
  expect(project.querySelector('select')?.title).toBe('SAMPLE — Sample')
  expect([...project.querySelectorAll('button')].map(n => n.textContent?.trim())).toEqual(['Sync Wiki', 'Hide search / tree'])
  expect([...exports.querySelectorAll('button')].map(n => n.textContent?.trim())).toEqual(['New page', 'Export project', 'Export selected hierarchy'])
  expect(host.querySelector('.wiki-operation-status')).toBeNull()
  exports.querySelectorAll<HTMLButtonElement>('button')[1]!.click(); await flush()
  const status = host.querySelector('.wiki-operation-status')!
  expect(status).not.toBeNull()
  expect(status.parentElement).toBe(project.parentElement?.parentElement)
  expect(project.compareDocumentPosition(exports) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(exports.compareDocumentPosition(status) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  expect(status.querySelector('button')?.textContent?.trim()).toBe('Cancel')
  finish(); await flush()
})
