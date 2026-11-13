<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getBackend, onWikiAttachmentProgress, type WikiAttachment, type WikiAttachmentDownloadResult, type WikiAttachmentDownloadProgress } from '../lib/backend'
import { formatAttachmentSize } from '../lib/wikiAttachment'

const props = defineProps<{ projectId: number; wikiId: number; attachments: WikiAttachment[]; disabled: boolean }>()
const emit=defineEmits<{busy:[value:boolean]}>()
const {t,locale}=useI18n(), backend=getBackend()
const active=ref(0), runId=ref(''), phase=ref(''), received=ref(0), total=ref(-1)
const results=ref<Record<number,WikiAttachmentDownloadResult>>({})
const announcement=ref('')
let alive=true, generation=0, pending:Promise<void>|undefined, cancelPending:Promise<void>|undefined
const percent=computed(()=>total.value>0?Math.min(100,Math.floor(received.value/total.value*100)):undefined)
const size=(n:number)=>formatAttachmentSize(n,locale.value)??t('wiki.attachmentDownload.unknownSize')
const errorKeys:Record<string,string>={not_found:'notFound',rate_limited:'rateLimited',cleanup_failed:'cleanupFailed',size_mismatch:'sizeMismatch',local_data:'localData'}
const errorText=(code:string)=>t(`wiki.attachmentDownload.error.${errorKeys[code]??(['unauthorized','forbidden','network','timeout','disk','truncated','disabled','busy','dialog'].includes(code)?code:'unexpected')}`)
const off=onWikiAttachmentProgress((p:WikiAttachmentDownloadProgress)=>{
 if(!alive||p.runId!==runId.value||p.projectId!==props.projectId||p.wikiId!==props.wikiId||p.attachmentId!==active.value)return
 phase.value=p.phase;received.value=p.receivedBytes;total.value=p.totalBytes
 if(p.phase==='downloading'&&percent.value!==undefined&&percent.value%10===0)announcement.value=t('wiki.attachmentDownload.progressKnown',{received:size(received.value),total:size(total.value),percent:percent.value})
})
async function cancel(){
 ++generation
 if(!active.value)return
 // Begin の遅延応答も pending 側で取り消し、両方の確定まで次を開始しない。
 cancelPending=backend.cancelWikiAttachmentDownload(props.projectId,props.wikiId,active.value).catch(()=>{})
 await Promise.all([pending,cancelPending])
}
defineExpose({cancel})
async function download(id:number){
 if(active.value||props.disabled)return
 const gen=++generation;active.value=id;phase.value='selecting';received.value=0;total.value=-1;runId.value='';emit('busy',true)
 announcement.value=t('wiki.attachmentDownload.selecting')
 const project=props.projectId,wiki=props.wikiId
 pending=(async()=>{
  try{
   const ticket=await backend.beginWikiAttachmentDownload(project,wiki,id)
   if(!alive||gen!==generation){if(ticket.runId)await backend.cancelWikiAttachmentDownload(project,wiki,id);return}
   if(ticket.code){results.value[id]={runId:ticket.runId,state:'failed',code:ticket.code,bytes:0,sizeChanged:false};return}
   runId.value=ticket.runId
   const result=await backend.downloadWikiAttachment(project,wiki,id)
   if(alive&&gen===generation){results.value[id]=result;announcement.value=result.state==='failed'?errorText(result.code):t(`wiki.attachmentDownload.${result.state}`)}
  }catch{
   if(alive&&gen===generation)results.value[id]={runId:runId.value,state:'failed',code:'unexpected',bytes:0,sizeChanged:false}
  }finally{
   await cancelPending
   active.value=0;runId.value='';emit('busy',false)
  }
 })()
 await pending
 await nextTick()
 if(alive)document.getElementById(`wiki-attachment-download-${id}`)?.focus({preventScroll:true})
}
onUnmounted(()=>{alive=false;off();void cancel()})
</script>
<template>
 <section v-if="attachments.length" class="wiki-attachments" aria-labelledby="wiki-attachments-title">
  <h3 id="wiki-attachments-title">{{t('wiki.attachmentDownload.title',{count:attachments.length})}}</h3>
  <p class="sr-only" aria-live="polite" aria-atomic="true">{{announcement}}</p>
  <ul>
   <li v-for="a in attachments" :key="a.id" class="wiki-attachment-row">
    <div class="wiki-attachment-name"><strong>{{a.name}}</strong><span :id="`wiki-attachment-size-${a.id}`">{{size(a.size)}}</span></div>
    <div class="wiki-attachment-actions">
     <template v-if="active===a.id">
      <span>{{t(`wiki.attachmentDownload.${['selecting','downloading','saving'].includes(phase)?phase:'saving'}`)}}</span>
      <template v-if="phase==='downloading'||phase==='saving'">
       <progress :value="percent" max="100" :aria-label="t('wiki.attachmentDownload.progressLabel',{name:a.name})" />
       <span>{{total>=0?t('wiki.attachmentDownload.progressKnown',{received:size(received),total:size(total),percent:percent??100}):t('wiki.attachmentDownload.progressUnknown',{received:size(received)})}}</span>
      </template>
      <button :aria-label="t('wiki.attachmentDownload.cancelLabel',{name:a.name})" @click="cancel">{{t('wiki.attachmentDownload.cancel')}}</button>
     </template>
     <template v-else>
      <span v-if="results[a.id]?.state==='saved'" class="attachment-saved">{{t('wiki.attachmentDownload.saved')}}</span>
      <span v-if="results[a.id]?.state==='failed'" class="attachment-failed">{{errorText(results[a.id]!.code)}}</span>
      <button :id="`wiki-attachment-download-${a.id}`" :disabled="disabled||active!==0" :aria-describedby="`wiki-attachment-size-${a.id}`" :aria-label="t('wiki.attachmentDownload.downloadLabel',{name:a.name})" @click="download(a.id)">{{t(`wiki.attachmentDownload.${results[a.id]?.state==='saved'?'downloadAgain':results[a.id]?.state==='failed'?'retry':'download'}`)}}</button>
     </template>
    </div>
   </li>
  </ul>
 </section>
</template>
<style scoped>
.wiki-attachments ul {list-style:none;padding:0}
.wiki-attachment-row {display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:1rem;padding:.8rem 0;border-top:1px solid var(--grid-border);min-width:0}
.wiki-attachment-name {min-width:0;overflow-wrap:anywhere;flex:1 1 12rem}
.wiki-attachment-name span {display:block;color:var(--text-muted);font-size:.85rem}
.wiki-attachment-actions {display:flex;align-items:center;justify-content:flex-end;gap:.6rem;flex-wrap:wrap;max-width:100%;min-width:0}
.attachment-saved {color:var(--success-text);background:var(--success-bg);padding:.4rem}
.attachment-failed {color:var(--danger-text);background:var(--danger-bg);padding:.4rem;overflow-wrap:anywhere}
progress {max-width:100%;accent-color:var(--border)}
.sr-only {position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
</style>
