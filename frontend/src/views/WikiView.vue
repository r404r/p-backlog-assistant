<script setup lang="ts">
// OS ダイアログと WebView 結合は実機確認対象。状態遷移はコンポーネントテストで検証する。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import WikiTree from '../components/WikiTree.vue'
import WikiEditor from '../components/WikiEditor.vue'
import {
  getBackend,
  newSyncRunId,
  copyToClipboard,
  openExternalURL,
  onSyncProgress,
  type Project,
  type WikiPage,
  type WikiExportEstimate,
  type SyncProgress,
} from '../lib/backend'
import { errorMessage, formatDateTime } from '../lib/format'
import {
  renderMarkdown,
  isMarkdownRule,
  markdownLinkHref,
} from '../lib/markdown'
import { buildWikiTree } from '../lib/wikiTree'
import {
  selectedProjectId,
  restoreProjectSelection,
  resolveProjectSelection,
} from '../lib/projectSelection'
import {
  issueSyncRunning,
  beginIssueSync,
  endIssueSync,
} from '../lib/syncState'
import {
  runSharedProjectRefresh,
  shouldSkipProjectRefreshFor,
} from '../lib/projectRefresh'
import { useModalFocus } from '../lib/modalFocus'
import { loadWikiPanelCollapsed, saveWikiPanelCollapsed } from '../lib/wikiPanel'

const { t } = useI18n()
const backend = getBackend()
const panelCollapsed = ref(loadWikiPanelCollapsed())
function togglePanel() {
  panelCollapsed.value = !panelCollapsed.value
  saveWikiPanelCollapsed(panelCollapsed.value)
}
const profile = ref(''),
  spaceURL = ref('')
const projects = ref<Project[]>([]),
  pages = ref<WikiPage[]>([])
const initializing = ref(true),
  busy = ref(false),
  disabled = ref(false)
const keyword = ref(''),
  keywordMode = ref<'and' | 'or'>('and')
const lastSynced = ref(''),
  error = ref(''),
  notice = ref(''),
  warnings = ref<string[]>([])
const selectedId = ref(0),
  selectedPath = ref(''),
  formatted = ref(true),
  syncFirst = ref(true)
const estimate = ref<WikiExportEstimate | null>(null),
  exporting = ref(false),
  preparing = ref(false)
const cancelRequested = ref(false)
const runId = ref(''),
  progress = ref<SyncProgress | null>(null)
const dialog = ref<HTMLElement | null>(null)
const editor = ref<InstanceType<typeof WikiEditor> | null>(null)
const projectFormatting = ref('')
const editing = ref(false), editingId = ref(0), writingBusy = ref(false)
const editorGeneration = ref(0)
const editorParents = computed(() => {
  const paths = new Set<string>()
  for (const p of pages.value) {
    const parts = p.name.split('/')
    for (let i = 1; i <= parts.length; i++) paths.add(parts.slice(0, i).join('/'))
  }
  if (selectedPath.value) paths.add(selectedPath.value)
  return [...paths].sort()
})
async function leaveEditor() {
  if (!editing.value) return true
  if (!editor.value || !(await editor.value.canLeave())) return false
  editing.value = false
  writingBusy.value = false
  return true
}
async function openEditor(create: boolean) {
  if (locked.value || disabled.value || !selectedProjectId.value || !(await leaveEditor())) return
  editingId.value = create ? 0 : selectedId.value
  editorGeneration.value++
  editing.value = true
  error.value = notice.value = ''
}
async function wikiSaved(page: WikiPage) {
  editing.value = false
  writingBusy.value = false
  // 検索条件を維持する。名前変更で検索から外れても保存済みページは表示する。
  pages.value = [...pages.value.filter(p => p.id !== page.id), page]
  selectedId.value = page.id
  selectedPath.value = page.name
  notice.value = t('wiki.saved')
}
async function changeProject(event: Event) {
  const select = event.target as HTMLSelectElement
  if (locked.value || !(await leaveEditor())) { select.value = String(selectedProjectId.value); return }
  selectedProjectId.value = Number(select.value)
}
const dialogOpen = computed(() => estimate.value !== null)
const locked = computed(
  () => initializing.value || busy.value || writingBusy.value || issueSyncRunning.value,
)
const tree = computed(() => buildWikiTree(pages.value))
const selected = computed(() =>
  pages.value.find((p) => p.id === selectedId.value),
)
const html = computed(() =>
  selected.value &&
  formatted.value &&
  isMarkdownRule(selected.value.textFormattingRule)
    ? renderMarkdown(selected.value.content)
    : '',
)
let alive = true,
  request = 0
const off = onSyncProgress((p) => {
  if (p.profileId === profile.value && p.runId === runId.value)
    progress.value = p
})
useModalFocus(dialog, dialogOpen, {
  onEscape: () => {
    if (!exporting.value) void cancelExport()
  },
})
function startRun() {
  const id = newSyncRunId()
  runId.value = id
  cancelRequested.value = false
  progress.value = null
  beginIssueSync(profile.value, selectedProjectId.value, id)
  busy.value = true
  error.value = ''
  notice.value = ''
  return id
}
function finishRun(id: string) {
  endIssueSync(id)
  if (runId.value === id) {
    runId.value = ''
    busy.value = false
    progress.value = null
  }
}
async function load() {
  const sequence = ++request,
    pid = selectedProjectId.value
  if (!profile.value || !pid) {
    pages.value = []
    lastSynced.value = ''
    return
  }
  const result = await backend.listWikis(profile.value, {
    projectId: pid,
    keyword: keyword.value,
    keywordMode: keywordMode.value,
  })
  if (!alive || sequence !== request || pid !== selectedProjectId.value) return
  pages.value = result.pages
  projectFormatting.value = result.textFormattingRule ?? result.pages[0]?.textFormattingRule ?? ''
  disabled.value = result.disabled
  lastSynced.value = result.lastSyncedAt
  if (!pages.value.some((p) => p.id === selectedId.value)) {
    selectedId.value = 0
    selectedPath.value = ''
  }
}
async function search() {
  if (locked.value || !(await leaveEditor())) return
  error.value = ''
  try {
    await load()
  } catch (e) {
    error.value = errorMessage(e)
  }
}
function keywordEnter(event: KeyboardEvent) {
  event.preventDefault()
  if (!event.isComposing) void search()
}
async function select(path: string, id: number) {
  if (locked.value || !(await leaveEditor())) return
  selectedPath.value = path
  selectedId.value = id
  formatted.value = true
}
watch(selectedProjectId, () => {
  selectedId.value = 0
  selectedPath.value = ''
  pages.value = []
  disabled.value = false
  lastSynced.value = ''
  if (!initializing.value)
    void load().catch((e) => {
      error.value = errorMessage(e)
    })
})
onMounted(async () => {
  try {
    profile.value = await backend.getActiveProfile()
    if (!profile.value) return
    spaceURL.value =
      (await backend.listProfiles()).find((p) => p.id === profile.value)
        ?.spaceUrl ?? ''
    if (!issueSyncRunning.value && !shouldSkipProjectRefreshFor(profile.value))
      await runSharedProjectRefresh(profile.value, () =>
        backend.syncProjects(profile.value),
      )
    projects.value = await backend.listProjects(profile.value)
    restoreProjectSelection(profile.value)
    selectedProjectId.value = resolveProjectSelection(
      projects.value,
      selectedProjectId.value,
    )
    await load()
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    initializing.value = false
  }
})
onUnmounted(() => {
  alive = false
  request++
  off()
  if (runId.value && (preparing.value || estimate.value || exporting.value))
    void backend.cancelWikiExport(profile.value, runId.value).catch(() => {})
  if (estimate.value && !exporting.value) finishRun(runId.value)
})
async function sync() {
  if (locked.value || !selectedProjectId.value || !(await leaveEditor())) return
  const id = startRun()
  try {
    await runSharedProjectRefresh(profile.value, () =>
      backend.syncProjects(profile.value),
    )
    projects.value = await backend.listProjects(profile.value)
    if (!projects.value.some((p) => p.id === selectedProjectId.value)) {
      selectedProjectId.value = resolveProjectSelection(projects.value, 0)
      await load()
      return
    }
    const result = await backend.syncWikis(
      profile.value,
      selectedProjectId.value,
      id,
    )
    warnings.value = result.warnings
    await load()
    disabled.value = result.disabled
    if (!result.disabled) notice.value = t('wiki.synced')
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    finishRun(id)
  }
}
async function refresh() {
  if (locked.value || !selected.value || !(await leaveEditor())) return
  const id = startRun()
  try {
    await backend.refreshWiki(
      profile.value,
      selectedProjectId.value,
      selected.value.id,
    )
    await load()
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    finishRun(id)
  }
}
async function copy() {
  if (!selected.value || !spaceURL.value) return
  try {
    await copyToClipboard(
      `${spaceURL.value.replace(/\/$/, '')}/alias/wiki/${selected.value.id}`,
    )
    notice.value = t('wiki.copied')
  } catch (e) {
    error.value = errorMessage(e)
  }
}
function openLink(event: MouseEvent | KeyboardEvent) {
  if (event instanceof KeyboardEvent && event.key !== 'Enter') return
  const href = markdownLinkHref(event.target)
  if (href) {
    event.preventDefault()
    openExternalURL(href)
  }
}
async function prepare(subtree: string) {
  if (locked.value || !selectedProjectId.value || !(await leaveEditor())) return
  const id = startRun()
  preparing.value = true
  try {
    if (syncFirst.value)
      await runSharedProjectRefresh(profile.value, () =>
        backend.syncProjects(profile.value),
      )
    if (!alive || cancelRequested.value) {
      finishRun(id)
      return
    }
    const result = await backend.prepareWikiExport(
      profile.value,
      selectedProjectId.value,
      subtree,
      syncFirst.value,
      id,
    )
    if (!alive || cancelRequested.value) {
      await backend.cancelWikiExport(profile.value, id)
      finishRun(id)
      return
    }
    estimate.value = result
    await load()
  } catch (e) {
    error.value = errorMessage(e)
    await backend.cancelWikiExport(profile.value, id).catch(() => {})
    estimate.value = null
    finishRun(id)
  } finally {
    preparing.value = false
  }
}
async function cancelExport() {
  cancelRequested.value = true
  const id = runId.value
  try {
    await backend.cancelWikiExport(profile.value, id)
    if (!exporting.value && !preparing.value) {
      estimate.value = null
      finishRun(id)
    }
  } catch (e) {
    error.value = errorMessage(e)
  }
}
async function exportNow() {
  if (!estimate.value || exporting.value || cancelRequested.value) return
  const id = runId.value
  exporting.value = true
  try {
    const r = await backend.exportWiki(profile.value, id)
    if (r.path) notice.value = t('wiki.exported', { path: r.path })
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    exporting.value = false
    estimate.value = null
    finishRun(id)
  }
}
</script>
<template>
  <section class="wiki-view">
    <h1>{{ t('wiki.title') }}</h1>
    <p v-if="!profile && !initializing">{{ t('wiki.noProfile') }}</p>
    <div class="toolbar">
      <label>{{ t('wiki.project') }}
        <select :value="selectedProjectId" :disabled="locked" @change="changeProject">
          <option v-for="p in projects" :key="p.id" :value="p.id">
            {{ p.projectKey }} — {{ p.name }}
          </option>
        </select></label>
      <button :disabled="locked || !selectedProjectId" @click="sync">
        {{ t('wiki.sync') }}
      </button>
      <span>{{
        lastSynced
          ? t('wiki.freshness', { at: formatDateTime(lastSynced) })
          : t('wiki.unsynced')
      }}</span>
    </div>
    <p v-if="disabled" role="status">{{ t('wiki.disabled') }}</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <ul v-if="warnings.length">
      <li v-for="w in warnings" :key="w">{{ w }}</li>
    </ul>
    <p v-if="busy" role="status">
      {{
        preparing
          ? t('wiki.preparing')
          : exporting
            ? t('wiki.exporting')
            : t('wiki.working')
      }}
      <span v-if="progress">{{ progress.fetched }} / {{ progress.total }}</span>
    </p>
    <button v-if="preparing || exporting" @click="cancelExport">
      {{ t('wiki.cancel') }}
    </button>
    <div class="toolbar">
      <button :disabled="locked || disabled || !selectedProjectId" @click="openEditor(true)">{{ t('wiki.newPage') }}</button>
      <label><input v-model="syncFirst" type="checkbox" :disabled="locked" />{{
        t('wiki.syncFirst')
      }}</label>
      <button
        :disabled="locked || disabled || !selectedProjectId"
        @click="prepare('')"
      >
        {{ t('wiki.exportProject') }}
      </button>
      <button
        :disabled="locked || disabled || !selectedPath"
        @click="prepare(selectedPath)"
      >
        {{ t('wiki.exportSubtree') }}
      </button>
    </div>
    <div class="wiki-panel-toolbar">
      <button
        type="button"
        :aria-expanded="!panelCollapsed"
        aria-controls="wiki-search-panel"
        @click="togglePanel"
      >
        {{ panelCollapsed ? t('wiki.showPanel') : t('wiki.hidePanel') }}
      </button>
    </div>
    <div class="wiki-layout" :class="{ 'panel-collapsed': panelCollapsed }">
      <div class="wiki-content">
        <WikiEditor
          v-if="editing"
          :key="editorGeneration"
          ref="editor"
          :profile="profile"
          :project-id="selectedProjectId"
          :page-id="editingId"
          :parent-path="selectedPath"
          :parents="editorParents"
          :text-formatting-rule="projectFormatting"
          @saved="wikiSaved"
          @cancel="editing = false; writingBusy = false"
          @error="error = $event"
          @busy="writingBusy = $event"
        />
        <article v-else-if="selected" class="wiki-detail">
          <h2>{{ selected.name }}</h2>
          <div class="toolbar">
            <button :disabled="locked || disabled" @click="openEditor(false)">{{ t('wiki.edit') }}</button>
            <button :disabled="locked" @click="refresh">
              {{ t('wiki.refresh') }}
            </button><button :disabled="!spaceURL" @click="copy">
              {{ t('wiki.copy') }}
            </button>
            <label v-if="isMarkdownRule(selected.textFormattingRule)"><input v-model="formatted" type="checkbox" />{{
              t('wiki.formatted')
            }}</label>
          </div>
          <dl>
            <dt>{{ t('wiki.tags') }}</dt>
            <dd>{{ selected.tags.join(', ') }}</dd>
            <dt>{{ t('wiki.created') }}</dt>
            <dd>
              {{ selected.createdUser }} {{ formatDateTime(selected.created) }}
            </dd>
            <dt>{{ t('wiki.updated') }}</dt>
            <dd>
              {{ selected.updatedUser }} {{ formatDateTime(selected.updated) }}
            </dd>
          </dl>
          <p v-if="!selected.contentFetchedAt">{{ t('wiki.missingBody') }}</p>
          <p v-else-if="selected.contentUpdated !== selected.updated">
            {{ t('wiki.staleBody') }}
          </p>
          <!-- 既存の markdown-it + DOMPurify の許可リストを再利用する。 -->
          <!-- eslint-disable vue/no-v-html -->
          <div
            v-if="html"
            class="wiki-preview markdown-body"
            @click="openLink"
            @keydown="openLink"
            v-html="html"
          />
          <!-- eslint-enable vue/no-v-html -->
          <pre v-else class="wiki-preview">{{ selected.content }}</pre>
          <h3>{{ t('wiki.attachments') }}</h3>
          <ul>
            <li v-for="a in selected.attachments" :key="a.id">
              {{ a.name }} ({{ a.size }} {{ t('wiki.bytes') }})
            </li>
          </ul>
        </article>
        <p v-else>{{ t('wiki.choosePage') }}</p>
      </div>
      <aside
        id="wiki-search-panel"
        :hidden="panelCollapsed"
        :aria-label="t('wiki.panel')"
      >
        <h2 class="wiki-panel-title">{{ t('wiki.panel') }}</h2>
        <form class="wiki-search" @submit.prevent="search">
          <label class="wiki-keyword">{{ t('wiki.keyword')
          }}<input
            v-model="keyword"
            data-testid="wiki-keyword"
            :disabled="locked"
            @keydown.enter="keywordEnter"
          /></label>
          <fieldset class="wiki-search-modes">
            <legend>{{ t('wiki.searchMode') }}</legend>
            <label><input
              v-model="keywordMode"
              type="radio"
              name="wiki-keyword-mode"
              value="and"
              :disabled="locked"
            />{{ t('wiki.and') }}</label>
            <label><input
              v-model="keywordMode"
              type="radio"
              name="wiki-keyword-mode"
              value="or"
              :disabled="locked"
            />{{ t('wiki.or') }}</label>
          </fieldset>
          <button type="submit" :disabled="locked">{{ t('wiki.search') }}</button>
        </form>
        <h3 class="wiki-tree-title">{{ t('wiki.tree') }}</h3>
        <WikiTree
          :nodes="tree"
          :selected-id="selectedId"
          :disabled="locked"
          @select="select"
        />
        <p v-if="!pages.length && !initializing">{{ t('wiki.empty') }}</p>
        <p v-if="selectedPath">
          {{ t('wiki.subtree', { path: selectedPath }) }}
        </p>
      </aside>
    </div>
    <div v-if="estimate" class="wiki-modal-backdrop">
      <div
        ref="dialog"
        role="dialog"
        aria-modal="true"
        :aria-label="t('wiki.confirm')"
        class="wiki-modal"
        tabindex="-1"
      >
        <h2>{{ t('wiki.confirm') }}</h2>
        <p>
          {{
            t('wiki.estimate', {
              pages: estimate.pages,
              attachments: estimate.attachments,
              bytes: estimate.bytes,
              seconds: estimate.estimatedSeconds,
            })
          }}
        </p>
        <p>{{ t('wiki.estimateNote') }}</p>
        <p v-if="estimate.missingContent">
          {{ t('wiki.missingWarning', { count: estimate.missingContent }) }}
        </p>
        <p v-if="!estimate.lastSyncedAt">{{ t('wiki.unsyncedWarning') }}</p>
        <ul>
          <li v-for="w in estimate.warnings" :key="w">{{ w }}</li>
        </ul>
        <p>{{ t('wiki.failureNote') }}</p>
        <button :disabled="exporting" @click="exportNow">
          {{ t('wiki.startExport') }}
        </button><button @click="cancelExport">{{ t('wiki.cancel') }}</button>
      </div>
    </div>
  </section>
</template>
<style scoped>
.wiki-view {
  padding: 1.25rem;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 0.8rem;
  flex-wrap: wrap;
  margin: 0.8rem 0;
}
.wiki-layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 20rem);
  gap: 1.5rem;
}
.wiki-layout.panel-collapsed {
  grid-template-columns: minmax(0, 1fr);
}
.wiki-panel-toolbar {
  display: flex;
  justify-content: flex-end;
  margin: 0.8rem 0;
}
.wiki-layout aside {
  min-width: 0;
  border-left: var(--border-width) solid var(--border);
  padding-left: 1rem;
  overflow-wrap: anywhere;
}
.wiki-panel-title {
  font-size: 1rem;
  margin: 0 0 1rem;
}
.wiki-tree-title {
  font-size: 1rem;
  margin: 1rem 0 0.5rem;
}
.wiki-search,
.wiki-keyword {
  display: grid;
  gap: 0.5rem;
  min-width: 0;
}
.wiki-search {
  margin-bottom: 1rem;
}
.wiki-keyword input {
  width: 100%;
  min-width: 0;
  box-sizing: border-box;
}
.wiki-search-modes {
  display: grid;
  gap: 0.4rem;
  margin: 0;
  padding: 0;
  border: 0;
  min-width: 0;
}
.wiki-search-modes legend {
  margin-bottom: 0.4rem;
}
.wiki-search-modes label {
  display: flex;
  align-items: baseline;
  gap: 0.4rem;
}
.wiki-search-modes input {
  flex: 0 0 auto;
  margin: 0;
}
.wiki-content {
  min-width: 0;
  overflow-wrap: anywhere;
  overflow-x: auto;
}
.wiki-detail {
  min-width: 0;
}
.wiki-detail > pre {
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.wiki-detail dl {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 0.4rem 1rem;
}
.wiki-detail dd {
  margin: 0;
}
.wiki-modal-backdrop {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.wiki-modal {
  box-shadow: var(--shadow-dialog);
  background: var(--surface);
  color: var(--text);
  padding: 1.5rem;
  max-width: 650px;
  max-height: 85vh;
  overflow: auto;
  border: var(--border-width) solid var(--border);
  border-radius: var(--radius-ui);
}
.wiki-modal button {
  margin-right: 0.6rem;
}
@media (max-width: 760px) {
  .wiki-layout {
    grid-template-columns: minmax(0, 1fr) minmax(0, 42%);
    gap: 0.75rem;
  }
  .wiki-layout aside {
    padding-left: 0.75rem;
  }
  .wiki-detail dl {
    grid-template-columns: minmax(0, 1fr);
  }
  .wiki-detail dd {
    margin-bottom: 0.5rem;
  }
}
</style>
