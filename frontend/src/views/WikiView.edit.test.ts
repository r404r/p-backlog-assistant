import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import WikiView from './WikiView.vue'
import { createMockBackend } from '../lib/backend/mock'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import type { WikiPage } from '../lib/backend'

const holder = vi.hoisted(() => ({ backend: null as unknown }))
vi.mock('../lib/backend', async (original) => ({
  ...(await original<typeof import('../lib/backend')>()), getBackend: () => holder.backend,
}))
let mounted: MountedApp | undefined
let backend: ReturnType<typeof createMockBackend>
const page: WikiPage = { id: 10, projectId: 1, name: 'Guide/Start', content: 'old', created: '', updated: 'T1', contentUpdated: 'T1', contentFetchedAt: 'now', tags: [], createdUser: '', updatedUser: '', textFormattingRule: 'markdown', attachments: [] }
async function flush() { for (let i=0;i<20;i++) await nextTick() }
function button(text: string) { const b=Array.from(mounted!.host.querySelectorAll('button')).find(b=>b.textContent?.trim()===text); expect(b, text).toBeDefined(); return b! }
function input(selector: string, value: string) { const el=mounted!.host.querySelector<HTMLInputElement>(selector)!; expect(el).not.toBeNull(); el.value=value; el.dispatchEvent(new Event('input',{bubbles:true})) }
beforeEach(async () => {
  backend=createMockBackend()
  backend.getActiveProfile=async()=>'p'; backend.listProfiles=async()=>[]; backend.syncProjects=async()=>{}
  backend.listProjects=async()=>[{id:1,name:'Sample',projectKey:'SAMPLE',lastSyncedAt:'',syncStateUnknown:false}]
  backend.listWikis=async()=>({pages:[page],disabled:false,lastSyncedAt:''})
  backend.beginWikiEdit=vi.fn(async()=>({token:'edit',page:{...page,content:'fresh'},baselineUpdated:'T1'}))
  backend.saveWikiEdit=vi.fn(async()=>({status:'saved',page:{...page,content:'new'}}))
  backend.createWiki=vi.fn(async()=>({status:'uncertain',page:null}))
  holder.backend=backend
  mounted=mountWithI18n(WikiView,{locale:'en'});await flush();button('Start').click();await flush()
})
afterEach(()=>{mounted?.unmount();mounted=undefined;vi.restoreAllMocks()})
it('starts from a fresh detail, previews safely, and saves with notifications off',async()=>{
  button('Edit').click();await flush()
  expect(backend.beginWikiEdit).toHaveBeenCalledWith('p',1,10)
  expect(mounted!.host.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('fresh')
  input('textarea','new');button('Preview').click();await flush()
  expect(mounted!.host.querySelector('.wiki-edit-preview')?.textContent).toBe('new')
  button('Save').click();await flush()
  expect(backend.saveWikiEdit).toHaveBeenCalledWith('p',{token:'edit',name:'Guide/Start',content:'new',mailNotify:false,overwrite:false})
})
it('refuses editing if detail GET fails',async()=>{
  backend.beginWikiEdit=vi.fn(async()=>{throw new Error('offline')})
  button('Edit').click();await flush()
  expect(mounted!.host.querySelector('textarea')).toBeNull()
  expect(mounted!.host.textContent).toContain('offline')
})
it('keeps local edits and shows the remote version on conflict; overwrite needs confirmation',async()=>{
  backend.saveWikiEdit=vi.fn(async()=>({status:'conflict',page:{...page,content:'remote',updated:'T2'}}))
  button('Edit').click();await flush();input('textarea','mine');button('Save').click();await flush()
  expect(mounted!.host.querySelector('[role="dialog"]')?.textContent).toContain('remote')
  const confirm=vi.spyOn(window,'confirm').mockReturnValue(false)
  button('Overwrite after confirmation').click();await flush()
  expect(backend.saveWikiEdit).toHaveBeenCalledTimes(1)
  confirm.mockReturnValue(true);button('Overwrite after confirmation').click();await flush()
  expect(backend.saveWikiEdit).toHaveBeenLastCalledWith('p',expect.objectContaining({content:'mine',overwrite:true}))
})
it('confirms leaving a dirty edit and keeps the draft if declined',async()=>{
  button('Edit').click();await flush();input('textarea','mine');await flush()
  vi.spyOn(window,'confirm').mockReturnValue(false)
  button('Cancel editing').click();await flush()
  expect(mounted!.host.querySelector<HTMLTextAreaElement>('textarea')?.value).toBe('mine')
})
it('locks an uncertain creation and checks the same attempt without a new draft',async()=>{
  button('New page').click();await flush();input('[data-testid="wiki-page-name"]','New');input('textarea','new')
  button('Create page').click();await flush()
  expect(backend.createWiki).toHaveBeenCalledTimes(1)
  expect(mounted!.host.querySelector<HTMLTextAreaElement>('textarea')?.disabled).toBe(true)
  expect(mounted!.host.textContent).toContain('Check result')
  button('Check result').click();await flush()
  expect(backend.createWiki).toHaveBeenCalledTimes(2)
})
