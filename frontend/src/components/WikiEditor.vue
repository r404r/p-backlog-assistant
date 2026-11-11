<script setup lang="ts">
// OS 終了確認・クリップボードと WebView の結合は実機確認対象。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getBackend, copyToClipboard, openExternalURL, type WikiPage, type WikiWriteResult } from '../lib/backend'
import { errorMessage } from '../lib/format'
import { renderMarkdown, isMarkdownRule, markdownLinkHref } from '../lib/markdown'
import { useModalFocus } from '../lib/modalFocus'
import { registerNavigationGuard } from '../lib/navigationGuard'

const props = defineProps<{
  profile: string
  projectId: number
  pageId: number
  parentPath: string
  parents: string[]
  textFormattingRule: string
}>()
const emit = defineEmits<{
  saved: [page: WikiPage]
  cancel: []
  error: [message: string]
  busy: [value: boolean]
}>()
const { t, locale } = useI18n()
const backend = getBackend()
const creating = props.pageId === 0
const loading = ref(!creating), saving = ref(false)
const token = ref(''), name = ref(''), content = ref(''), parent = ref(props.parentPath)
const initialName = ref(''), initialContent = ref('')
const mailNotify = ref(false), preview = ref(false), uncertain = ref(false)
const error = ref(''), notice = ref(''), remote = ref<WikiPage | null>(null)
const confirmKind = ref<'discard' | 'overwrite' | ''>('')
let confirmResolve: ((answer: boolean) => void) | undefined
const confirmDialog = ref<HTMLElement | null>(null)
const confirming = computed(() => confirmKind.value !== '')
const formattingRule = ref(props.textFormattingRule)
const formDialog = ref<HTMLElement | null>(null), conflictDialog = ref<HTMLElement | null>(null)
const newDialogOpen = computed(() => creating && !confirming.value)
const conflictOpen = computed(() => remote.value !== null && !confirming.value)
const fullName = computed(() => creating && parent.value ? `${parent.value}/${name.value}` : name.value)
const dirty = computed(() => uncertain.value || name.value !== initialName.value || content.value !== initialContent.value || (creating && !!parent.value && !!name.value))
const working = computed(() => loading.value || saving.value)
const immutable = computed(() => working.value || uncertain.value)
const html = computed(() => preview.value && isMarkdownRule(formattingRule.value) ? renderMarkdown(content.value) : '')

function confirmAction(kind: 'discard' | 'overwrite'): Promise<boolean> {
  if (confirmResolve) return Promise.resolve(false)
  confirmKind.value = kind
  return new Promise(resolve => { confirmResolve = resolve })
}
function answerConfirm(answer: boolean) {
  const resolve = confirmResolve
  confirmResolve = undefined
  confirmKind.value = ''
  resolve?.(answer)
}
async function canLeave(): Promise<boolean> {
  return !working.value && (!dirty.value || await confirmAction('discard'))
}
defineExpose({ canLeave })
async function cancel() { if (await canLeave()) emit('cancel') }
useModalFocus(formDialog, newDialogOpen, { onEscape: () => { void cancel() } })
useModalFocus(conflictDialog, conflictOpen, { onEscape: () => {} })
useModalFocus(confirmDialog, confirming, {
  initialFocus: () => confirmDialog.value?.querySelector('[data-confirm-cancel]'),
  onEscape: () => answerConfirm(false),
})
const unregister = registerNavigationGuard(canLeave)
function beforeUnload(event: BeforeUnloadEvent) {
  if (dirty.value || working.value) { event.preventDefault(); event.returnValue = '' }
}
window.addEventListener('beforeunload', beforeUnload)
watch([dirty, working, locale], () => {
  emit('busy', working.value)
  void backend.setWikiWindowState(dirty.value, working.value, locale.value).catch(() => {
    error.value = t('wiki.closeGuardFailed')
  })
}, { immediate: true })
onMounted(async () => {
  if (creating) return
  try {
    const edit = await backend.beginWikiEdit(props.profile, props.projectId, props.pageId)
    if (!edit.token || !edit.baselineUpdated) throw new Error(t('wiki.editUnavailable'))
    token.value = edit.token
    name.value = initialName.value = edit.page.name
    content.value = initialContent.value = edit.page.content
    formattingRule.value = edit.page.textFormattingRule
  } catch (e) {
    emit('error', errorMessage(e))
    emit('cancel')
  } finally { loading.value = false }
})
onUnmounted(() => {
  answerConfirm(false)
  unregister()
  window.removeEventListener('beforeunload', beforeUnload)
  void backend.setWikiWindowState(false, false, locale.value).catch(() => {})
})
async function save(overwrite = false) {
  if (working.value || !name.value.trim() || (!creating && !token.value)) return
  if (overwrite && !(await confirmAction('overwrite'))) return
  saving.value = true
  error.value = notice.value = ''
  try {
    let result: WikiWriteResult
    await backend.setWikiWindowState(true, true, locale.value)
    if (creating) {
      result = await backend.createWiki(props.profile, { projectId: props.projectId, name: fullName.value, content: content.value, mailNotify: mailNotify.value })
    } else {
      result = await backend.saveWikiEdit(props.profile, { token: token.value, name: name.value, content: content.value, mailNotify: mailNotify.value, overwrite })
    }
    if (result.status === 'saved' && result.page) {
      initialName.value = name.value
      initialContent.value = content.value
      uncertain.value = false
      emit('saved', result.page)
    } else if (result.status === 'conflict' && result.page) {
      remote.value = result.page
    } else if (result.status === 'not_saved') {
      uncertain.value = false
      remote.value = null
      notice.value = t('wiki.notSaved')
    } else if (result.status === 'duplicate') {
      error.value = t('wiki.duplicate')
    } else {
      // バインディングの応答欠落も不確定として扱い、フォームを固定する。
      uncertain.value = true
      remote.value = null
      notice.value = t('wiki.uncertain')
    }
  } catch (e) {
    // 呼び出し自体の失敗では送信結果が不明な場合がある。再操作は照合から行う。
    error.value = errorMessage(e)
  } finally { saving.value = false }
}
async function copyDraft() {
  try {
    await copyToClipboard(`${fullName.value}\n\n${content.value}`)
    notice.value = t('wiki.draftCopied')
  } catch (e) { error.value = errorMessage(e) }
}
async function discardAndReload() {
  if (working.value) return
  loading.value = true
  error.value = ''
  try {
    const edit = await backend.beginWikiEdit(props.profile, props.projectId, props.pageId)
    token.value = edit.token
    name.value = initialName.value = edit.page.name
    content.value = initialContent.value = edit.page.content
    remote.value = null
    uncertain.value = false
    formattingRule.value = edit.page.textFormattingRule
  } catch (e) { error.value = errorMessage(e) }
  finally { loading.value = false }
}
function openLink(event: MouseEvent | KeyboardEvent) {
  if (event instanceof KeyboardEvent && event.key !== 'Enter') return
  const href = markdownLinkHref(event.target)
  if (href) { event.preventDefault(); openExternalURL(href) }
}
</script>

<template>
  <div :class="{ 'wiki-editor-backdrop': creating }">
    <section
      ref="formDialog"
      class="wiki-editor"
      :class="{ 'wiki-editor-dialog': creating }"
      :role="creating ? 'dialog' : undefined"
      :aria-modal="creating ? true : undefined"
      :aria-label="creating ? t('wiki.newPage') : t('wiki.edit')"
      tabindex="-1"
    >
      <h2>{{ creating ? t('wiki.newPage') : t('wiki.edit') }}</h2>
      <p v-if="loading" role="status">{{ t('wiki.working') }}</p>
      <p v-if="error" role="alert" class="error">{{ error }}</p>
      <p v-if="notice" role="status">{{ notice }}</p>
      <template v-if="!loading">
        <label v-if="creating">{{ t('wiki.parentPath') }}
          <select v-model="parent" :disabled="immutable">
            <option value="">{{ t('wiki.rootPath') }}</option>
            <option v-for="path in parents" :key="path" :value="path">{{ path }}</option>
          </select>
        </label>
        <label>{{ t('wiki.pageName') }}
          <input v-model="name" data-testid="wiki-page-name" :disabled="immutable" />
        </label>
        <p v-if="creating">{{ t('wiki.fullName', { name: fullName }) }}</p>
        <p>{{ t('wiki.hierarchyNote') }}</p>
        <div class="wiki-editor-actions">
          <button type="button" :aria-pressed="!preview" @click="preview = false">{{ t('wiki.text') }}</button>
          <button type="button" :aria-pressed="preview" @click="preview = true">{{ t('wiki.preview') }}</button>
        </div>
        <label v-show="!preview">{{ t('wiki.body') }}
          <textarea v-model="content" :disabled="immutable" rows="16" />
        </label>
        <!-- eslint-disable vue/no-v-html -->
        <div v-if="preview && html" class="wiki-edit-preview markdown-body" @click="openLink" @keydown="openLink" v-html="html" />
        <!-- eslint-enable vue/no-v-html -->
        <pre v-else-if="preview" class="wiki-edit-preview">{{ content }}</pre>
        <p v-if="preview && !isMarkdownRule(formattingRule)">{{ t('wiki.rawPreview') }}</p>
        <label><input v-model="mailNotify" type="checkbox" :disabled="immutable" />{{ t('wiki.mailNotify') }}</label>
        <div class="wiki-editor-actions">
          <button :disabled="working || !name.trim() || !!remote" @click="save(false)">
            {{ uncertain ? t('wiki.checkResult') : creating ? t('wiki.createPage') : t('wiki.save') }}
          </button>
          <button :disabled="working" @click="cancel">{{ creating ? t('wiki.cancel') : t('wiki.cancelEdit') }}</button>
        </div>
      </template>
    </section>
  </div>
  <div v-if="remote" class="wiki-editor-backdrop">
    <section ref="conflictDialog" role="dialog" aria-modal="true" :aria-label="t('wiki.conflict')" class="wiki-editor-dialog" tabindex="-1">
      <h2>{{ t('wiki.conflict') }}</h2>
      <p>{{ t('wiki.conflictNote') }}</p>
      <h3>{{ remote.name }}</h3>
      <p>{{ remote.updatedUser }} {{ remote.updated }}</p>
      <pre class="wiki-remote">{{ remote.content }}</pre>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="notice" role="status">{{ notice }}</p>
      <div class="wiki-editor-actions">
        <button :disabled="working" @click="discardAndReload">{{ t('wiki.discardReload') }}</button>
        <button :disabled="working" @click="copyDraft">{{ t('wiki.copyDraft') }}</button>
        <button :disabled="working" @click="save(true)">{{ t('wiki.overwrite') }}</button>
      </div>
    </section>
  </div>
  <div v-if="confirming" class="wiki-editor-backdrop wiki-confirm-backdrop">
    <section ref="confirmDialog" role="dialog" aria-modal="true" :aria-label="t('wiki.confirmChange')" class="wiki-editor-dialog" tabindex="-1">
      <h2>{{ t('wiki.confirmChange') }}</h2>
      <p>{{ confirmKind === 'discard' ? t('wiki.discardConfirm') : t('wiki.overwriteConfirm') }}</p>
      <div class="wiki-editor-actions">
        <button data-confirm-cancel @click="answerConfirm(false)">{{ t('wiki.keepEditing') }}</button>
        <button @click="answerConfirm(true)">{{ confirmKind === 'discard' ? t('wiki.discardChanges') : t('wiki.confirmOverwrite') }}</button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.wiki-editor { min-width: 0; }
.wiki-editor > label { display: block; margin: 0.75rem 0; }
.wiki-editor input:not([type='checkbox']), .wiki-editor textarea, .wiki-editor select {
  display: block; width: 100%; max-width: 100%; box-sizing: border-box;
}
.wiki-editor textarea { resize: vertical; font-family: monospace; }
.wiki-editor-actions { display: flex; flex-wrap: wrap; gap: 0.6rem; margin: 0.75rem 0; }
.wiki-edit-preview, .wiki-remote { overflow-wrap: anywhere; white-space: pre-wrap; }
.wiki-edit-preview.markdown-body { white-space: normal; }
.wiki-editor-backdrop {
  position: fixed; inset: 0; background: var(--overlay); display: flex;
  align-items: center; justify-content: center; z-index: 110; padding: 1rem;
}
.wiki-editor-dialog {
  background: var(--surface); color: var(--text); padding: 1.25rem;
  width: min(56rem, 100%); max-height: 85vh; overflow: auto;
  border: 1px solid var(--border); border-radius: 8px;
}
.wiki-confirm-backdrop { z-index: 120; }
</style>
