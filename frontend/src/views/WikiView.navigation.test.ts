import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import WikiView from './WikiView.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { WikiAttachmentDownloadResult, WikiPage } from '../lib/backend'
import { canLeaveCurrentView } from '../lib/navigationGuard'
import { activeIssueSync, beginIssueSync, endIssueSync } from '../lib/syncState'
import { selectedProjectId } from '../lib/projectSelection'

const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async original => ({
  ...await original<typeof import('../lib/backend')>(), getBackend: () => holder.backend,
}))
let mounted: MountedApp | undefined
let backend: ReturnType<typeof createMockBackend>
const page: WikiPage = { id: 10, projectId: 1, name: 'Guide/Start', content: '[[Next]]', created: '', updated: 'T1', contentUpdated: 'T1', contentFetchedAt: 'now', tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'markdown', attachments: [{ id: 30, wikiId: 10, name: 'sample.txt', size: 4 }] }
const flush = async () => { for (let n = 0; n < 30; n++) await nextTick() }
const button = (text: string) => {
  const node = [...mounted!.host.querySelectorAll<HTMLButtonElement>('button, .wiki-node-name')].find(n => n.textContent?.trim() === text)
  expect(node, text).toBeDefined()
  return node!
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
async function open() {
  mounted = mountWithI18n(WikiView, { locale: 'en' })
  await flush()
}
beforeEach(() => {
  localStorage.clear(); selectedProjectId.value = 1
  backend = createMockBackend(); holder.backend = backend
  backend.getActiveProfile = async () => 'p'
  backend.listProfiles = async () => []
  backend.syncProjects = async () => {}
  backend.listProjects = async () => [1, 2].map(id => ({ id, name: `Sample ${id}`, projectKey: `SAMPLE${id}`, lastSyncedAt: '', syncStateUnknown: false }))
  backend.listWikis = vi.fn(async () => ({ pages: [page, { ...page, id: 11, name: 'Next', attachments: [] }], disabled: false, lastSyncedAt: '' }))
  backend.beginWikiAttachmentDownload = vi.fn(async () => ({ runId: 'download', code: '' }))
  backend.cancelWikiAttachmentDownload = vi.fn(async () => {})
  backend.beginWikiEdit = vi.fn(async () => ({ token: 'edit', page, baselineUpdated: 'T1' }))
})
afterEach(async () => {
  mounted?.unmount(); mounted = undefined
  if (activeIssueSync.value) endIssueSync(activeIssueSync.value.runId)
  await flush(); vi.restoreAllMocks(); localStorage.clear()
})

it('初期読込中も別画面への移動を許可する', async () => {
  const active = deferred<string>()
  backend.getActiveProfile = () => active.promise
  await open()
  expect(await canLeaveCurrentView()).toBe(true)
  active.resolve('p'); await flush()
})

it('課題同期中に入った Wiki から別画面へ戻れる', async () => {
  beginIssueSync('p', 1, 'issues')
  await open()
  expect(button('Sync Wiki').disabled).toBe(true)
  expect(await canLeaveCurrentView()).toBe(true)
})

it.each(['sync', 'refresh', 'prepare', 'export'] as const)('%s 中も画面を離れられ、出力はアンマウントで取り消す', async operation => {
  await open(); button('Start').click(); await flush()
  const done = deferred<void>()
  backend.syncWikis = vi.fn(async () => { await done.promise; return { disabled: false, warnings: [], mode: 'full' as const, fetched: 0, upserted: 0, deleted: 0, durationMs: 0 } })
  backend.refreshWiki = vi.fn(async () => { await done.promise; return page })
  backend.prepareWikiExport = vi.fn(async () => {
    if (operation === 'prepare') await done.promise
    return { pages: 1, attachments: 1, bytes: 4, estimatedSeconds: 1, missingContent: 0, lastSyncedAt: '', warnings: [] }
  })
  backend.exportWiki = vi.fn(async () => { await done.promise; return { path: '', rows: 0, unverifiable: 0 } })
  backend.cancelWikiExport = vi.fn(async () => { done.resolve() })
  button(operation === 'sync' ? 'Sync Wiki' : operation === 'refresh' ? 'Fetch latest' : 'Export project').click(); await flush()
  if (operation === 'export') { button('Start export').click(); await flush() }
  expect(button('Edit').disabled).toBe(true)
  expect(await canLeaveCurrentView()).toBe(true)
  mounted!.unmount(); mounted = undefined; await flush()
  if (operation === 'prepare' || operation === 'export') expect(backend.cancelWikiExport).toHaveBeenCalled()
  done.resolve(); await flush()
})

async function startDownload() {
  await open(); button('Start').click(); await flush()
  const download = deferred<WikiAttachmentDownloadResult>(), cleanup = deferred<void>()
  backend.downloadWikiAttachment = vi.fn(() => download.promise)
  backend.cancelWikiAttachmentDownload = vi.fn(async () => {
    await cleanup.promise
    download.resolve({ runId: 'download', state: 'cancelled', code: 'cancelled', bytes: 0, sizeChanged: false })
  })
  mounted!.host.querySelector<HTMLButtonElement>('.wiki-attachments button')!.click(); await flush()
  return { cleanup, download }
}

it.each(['screen', 'page', 'project', 'link'] as const)('%s への移動はダウンロードの後片付け完了を待つ', async target => {
  const { cleanup, download } = await startDownload()
  let left = false
  let leaving: Promise<void> | undefined
  if (target === 'screen') leaving = canLeaveCurrentView().then(ok => { left = ok })
  if (target === 'page') button('Next').click()
  if (target === 'project') {
    const select = mounted!.host.querySelector<HTMLSelectElement>('.wiki-project-sync select')!
    select.value = '2'; select.dispatchEvent(new Event('change', { bubbles: true }))
  }
  if (target === 'link') mounted!.host.querySelector<HTMLAnchorElement>('.wiki-preview a')!.click()
  await flush()
  expect(backend.cancelWikiAttachmentDownload).toHaveBeenCalledWith(1, 10, 30)
  // Download だけが先に返っても、Cancel の応答前には移動しない。
  download.resolve({ runId: 'download', state: 'cancelled', code: 'cancelled', bytes: 0, sizeChanged: false })
  await flush()
  expect(left).toBe(false)
  expect(selectedProjectId.value).toBe(1)
  expect(mounted!.host.querySelector('.wiki-detail h2')?.textContent).toBe('Guide/Start')
  cleanup.resolve(); await flush(); await leaving
  if (target === 'screen') expect(left).toBe(true)
  else if (target === 'project') expect(selectedProjectId.value).toBe(2)
  else expect(mounted!.host.querySelector('.wiki-detail h2')?.textContent).toBe('Next')
})

it('取得中の同期・出力・編集開始を無効にし、画面を破棄すると取り消す', async () => {
  const { cleanup } = await startDownload()
  for (const text of ['Sync Wiki', 'Fetch latest', 'Edit', 'New page', 'Export project']) expect(button(text).disabled, text).toBe(true)
  expect(activeIssueSync.value).toBeNull()
  mounted!.unmount(); mounted = undefined; await flush()
  expect(backend.cancelWikiAttachmentDownload).toHaveBeenCalledWith(1, 10, 30)
  cleanup.resolve(); await flush()
})

it('未保存確認の拒否は草稿を維持し、ダウンロードを取り消さない', async () => {
  await open(); button('Start').click(); await flush(); button('Edit').click(); await flush()
  const input = mounted!.host.querySelector<HTMLTextAreaElement>('textarea')!
  input.value = 'draft'; input.dispatchEvent(new Event('input', { bubbles: true })); await flush()
  const leaving = canLeaveCurrentView(); await flush(); button('Keep editing').click(); await flush()
  expect(await leaving).toBe(false)
  expect(input.value).toBe('draft')
  expect(backend.cancelWikiAttachmentDownload).not.toHaveBeenCalled()
  expect(mounted!.host.querySelector('.wiki-attachments')).toBeNull()
})
