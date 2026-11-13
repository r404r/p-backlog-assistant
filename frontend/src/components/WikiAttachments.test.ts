import { afterEach, expect, it, vi } from 'vitest'
import { h, nextTick } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { createMockBackend } from '../lib/backend/mock'
import WikiAttachments from './WikiAttachments.vue'
const holder=vi.hoisted(()=>({backend:null as unknown}))
vi.mock('../lib/backend',async original=>({...await original<typeof import('../lib/backend')>(),getBackend:()=>holder.backend}))
let mounted:MountedApp|undefined
afterEach(()=>mounted?.unmount())
const flush=async()=>{for(let n=0;n<20;n++)await nextTick()}
it('hides an empty section and displays saved results without exposing a path',async()=>{
 const b=createMockBackend();holder.backend=b
 b.beginWikiAttachmentDownload=vi.fn(async()=>({runId:'run',code:''}))
 b.downloadWikiAttachment=vi.fn(async()=>({runId:'run',state:'saved',code:'',bytes:4,sizeChanged:false}))
 mounted=mountWithI18n({render:()=>h(WikiAttachments,{projectId:1,wikiId:2,attachments:[{id:3,wikiId:2,name:'架空.txt',size:4}],disabled:false})})
 await flush();(mounted.host.querySelector('button') as HTMLButtonElement).click();await flush()
 expect(mounted.host.textContent).toContain('保存しました')
 expect(b.downloadWikiAttachment).toHaveBeenCalledWith(1,2,3)
 mounted.unmount();mounted=mountWithI18n({render:()=>h(WikiAttachments,{projectId:1,wikiId:2,attachments:[],disabled:false})})
 expect(mounted.host.querySelector('section')).toBeNull()
})
