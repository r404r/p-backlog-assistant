<script setup lang="ts">
// OS ダイアログと WebView 結合は実機確認対象。状態遷移はコンポーネントテストで検証する。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import WikiTree from '../components/WikiTree.vue'
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

const { t } = useI18n()
const backend = getBackend()
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
const runId = ref(''),
  progress = ref<SyncProgress | null>(null)
const dialog = ref<HTMLElement | null>(null)
const dialogOpen = computed(() => estimate.value !== null)
const locked = computed(
  () => initializing.value || busy.value || issueSyncRunning.value,
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
  disabled.value = result.disabled
  lastSynced.value = result.lastSyncedAt
  if (!pages.value.some((p) => p.id === selectedId.value)) {
    selectedId.value = 0
    selectedPath.value = ''
  }
}
async function search() {
  if (locked.value) return
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
function select(path: string, id: number) {
  if (locked.value) return
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
  if (locked.value || !selectedProjectId.value) return
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
  if (locked.value || !selected.value) return
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
  if (locked.value || !selectedProjectId.value) return
  const id = startRun()
  preparing.value = true
  try {
    if (syncFirst.value)
      await runSharedProjectRefresh(profile.value, () =>
        backend.syncProjects(profile.value),
      )
    const result = await backend.prepareWikiExport(
      profile.value,
      selectedProjectId.value,
      subtree,
      syncFirst.value,
      id,
    )
    if (!alive) {
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
  if (!estimate.value || exporting.value) return
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
        <select v-model="selectedProjectId" :disabled="locked">
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
    <div class="wiki-layout">
      <aside :aria-label="t('wiki.tree')">
        <form @submit.prevent="search">
          <label>{{ t('wiki.keyword')
          }}<input
            v-model="keyword"
            data-testid="wiki-keyword"
            :disabled="locked"
            @keydown.enter="keywordEnter"
          /></label>
          <label><input
            v-model="keywordMode"
            type="radio"
            value="and"
            :disabled="locked"
          />{{ t('wiki.and') }}</label>
          <label><input
            v-model="keywordMode"
            type="radio"
            value="or"
            :disabled="locked"
          />{{ t('wiki.or') }}</label>
          <button :disabled="locked">{{ t('wiki.search') }}</button>
        </form>
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
      <article v-if="selected" class="wiki-detail">
        <h2>{{ selected.name }}</h2>
        <div class="toolbar">
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
  grid-template-columns: minmax(220px, 28%) 1fr;
  gap: 1.5rem;
}
.wiki-layout aside {
  min-width: 0;
  border-right: 1px solid var(--border);
  padding-right: 1rem;
}
.wiki-detail {
  min-width: 0;
}
.wiki-preview {
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
  background: var(--surface);
  color: var(--text);
  padding: 1.5rem;
  max-width: 650px;
  max-height: 85vh;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 8px;
}
.wiki-modal button {
  margin-right: 0.6rem;
}
@media (max-width: 760px) {
  .wiki-layout {
    grid-template-columns: 1fr;
  }
  .wiki-layout aside {
    border-right: 0;
    border-bottom: 1px solid var(--border);
    padding-bottom: 1rem;
  }
}
</style>
