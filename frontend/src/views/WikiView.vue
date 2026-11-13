<script setup lang="ts">
// OS ダイアログと WebView 結合は実機確認対象。状態遷移はコンポーネントテストで検証する。
import { computed, nextTick, onMounted, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import MarkdownBody from '../components/MarkdownBody.vue'
import type { Heading } from '../lib/markdownDocument'
import WikiTree from '../components/WikiTree.vue'
import WikiEditor from '../components/WikiEditor.vue'
import WikiAttachments from '../components/WikiAttachments.vue'
import {registerNavigationGuard} from '../lib/navigationGuard'
import {
  getBackend,
  newSyncRunId,
  copyToClipboard,
  onSyncProgress,
  type Project,
  type WikiPage,
  type WikiExportEstimate,
  type SyncProgress,
} from '../lib/backend'
import { errorMessage, formatDateTime } from '../lib/format'
import { isMarkdownRule } from '../lib/markdown'
import { buildWikiTree, indexWikiTree } from '../lib/wikiTree'
import {
  selectedProjectId,
  restoreProjectSelection,
  resolveProjectSelection,
} from '../lib/projectSelection'
import { issueSyncRunning, beginIssueSync, endIssueSync } from '../lib/syncState'
import { runSharedProjectRefresh, shouldSkipProjectRefreshFor } from '../lib/projectRefresh'
import { useModalFocus } from '../lib/modalFocus'
import { useWikiDrawer } from '../lib/wikiDrawer'
import { loadWikiPanelCollapsed, saveWikiPanelCollapsed } from '../lib/wikiPanel'

const { t, locale } = useI18n()
const attachments=ref<InstanceType<typeof WikiAttachments>|null>(null),attachmentBusy=ref(false)
const apiLocked=computed(()=>locked.value||attachmentBusy.value)
const pageHeading = ref<HTMLElement | null>(null)
const markdown = ref<InstanceType<typeof MarkdownBody> | null>(null)
const headings = ref<Heading[]>([]),
  activeHeading = ref(''),
  tocLimit = ref(500),
  allowLarge = ref(false)
const wikiIndex = shallowRef<WikiPage[]>([])
const collapsedNodeKeys = shallowRef(new Set<string>())
const selectedRowKey = ref(''), focusedRowKey = ref('')
const treeComponent = ref<InstanceType<typeof WikiTree> | null>(null)
const layout = ref<HTMLElement | null>(null), bodyPort = ref<HTMLElement | null>(null)
const outerPort = ref<HTMLElement | null>(null), contentFrame = ref<HTMLElement | null>(null)
const atMinimum = ref(true)
const panelPort = ref<HTMLElement | null>(null), tocPort = ref<HTMLElement | null>(null)
const panelScroll = ref(false), layoutHeight = ref(160), tocHeight = ref(0)
const markdownScrollRoot = computed(() => bodyPort.value)
const narrow = ref(false), drawerOpen = ref(false)
const panelCollapsed = ref(loadWikiPanelCollapsed())
const drawerHost = ref<HTMLElement | null>(null), panelToggle = ref<HTMLElement | null>(null)
const panelHidden = computed(() => narrow.value ? !drawerOpen.value : panelCollapsed.value)
const wholePanel = computed(() => narrow.value || panelScroll.value)
const treeScrollPort = computed(() => wholePanel.value ? panelPort.value : null)
const { close: closeDrawer } = useWikiDrawer(drawerOpen, panelPort, drawerHost, panelToggle)
let pendingReveal = ''
const temporaryId = ref(0)
const fullTree = computed(() => indexWikiTree(buildWikiTree(wikiIndex.value)))
const hasCollapsed = computed(() => [...fullTree.value.branches].some(key => collapsedNodeKeys.value.has(key)))
const allCollapsed = computed(() => !fullTree.value.branches.size || [...fullTree.value.branches].every(key => collapsedNodeKeys.value.has(key)))
const displayedPages = computed(() => {
  const extra = wikiIndex.value.find(p => p.id === temporaryId.value)
  return extra && !pages.value.some(p => p.id === extra.id) ? [...pages.value, extra] : pages.value
})
const outsideId = computed(() => displayedPages.value.length > pages.value.length ? temporaryId.value : 0)
function toggleNode(key: string) {
  if (locked.value) return
  const next = new Set(collapsedNodeKeys.value)
  if (next.has(key)) next.delete(key); else next.add(key)
  collapsedNodeKeys.value = next
}
function expandAll() { if (!locked.value) collapsedNodeKeys.value = new Set() }
function collapseAll() { if (!locked.value) collapsedNodeKeys.value = new Set(fullTree.value.branches) }
watch(fullTree, index => {
  collapsedNodeKeys.value = new Set([...collapsedNodeKeys.value].filter(key => index.keys.has(key)))
})
let layoutObserver: ResizeObserver | undefined, layoutFrame = 0
function scheduleLayout() {
  if (!layoutFrame) layoutFrame = requestAnimationFrame(() => { layoutFrame = 0; measureLayout() })
}
function measureLayout() {
  const el = layout.value
  if (!el) return
  const outer = outerPort.value
  const clientBottom = outer ? outer.getBoundingClientRect().top + outer.clientTop + outer.clientHeight : window.innerHeight
  const bottom = Math.min(window.innerHeight, clientBottom)
  const top = el.getBoundingClientRect().top + (outer?.scrollTop ?? 0)
  const section = el.closest('.wiki-view')
  const tail = (outer ? parseFloat(getComputedStyle(outer).paddingBottom) || 0 : 0)
    + (section ? parseFloat(getComputedStyle(section).paddingBottom) || 0 : 0)
  const frameStyle = contentFrame.value ? getComputedStyle(contentFrame.value) : undefined
  const chrome = frameStyle ? ['borderTopWidth', 'borderBottomWidth', 'paddingTop', 'paddingBottom']
    .reduce((sum, key) => sum + (parseFloat(frameStyle[key as keyof CSSStyleDeclaration] as string) || 0), 0) : 0
  const minimum = 160 + chrome
  const rawHeight = bottom - top - tail
  const available = Math.max(minimum, rawHeight)
  atMinimum.value = rawHeight <= minimum
  layoutHeight.value = available
  // clientWidthが未計算の初回だけviewportを仮値にし、実寸確定後observerで再測定する。
  const nextNarrow = (el.clientWidth || window.innerWidth) < 760
  if (nextNarrow !== narrow.value) {
    const hadFocus = panelPort.value?.contains(document.activeElement)
    closeDrawer(false)
    narrow.value = nextNarrow
    void nextTick(scheduleLayout)
    if (hadFocus) void nextTick(() => panelToggle.value?.focus({ preventScroll: true }))
  }
  if (narrow.value) {
    // ドロワーは画面全高を使う。閉じていて実寸がない時はviewport高を用いる。
    tocHeight.value = Math.min(192, (panelPort.value?.clientHeight || window.innerHeight) * .25)
    return
  }
  if (panelHidden.value) return
  const panel = panelPort.value
  let fixed = 0, tocNatural = 0
  if (panel && !panelHidden.value && !narrow.value) {
    for (const child of panel.children) {
      if (child.classList.contains('wiki-tree')) continue
      const node = child as HTMLElement
      const css = getComputedStyle(node)
      if (css.display === 'none' || css.position === 'absolute') continue
      let height = node.getBoundingClientRect().height
      if (node.classList.contains('wiki-toc-section') && tocOpen.value) {
        height = node.querySelector('summary')?.getBoundingClientRect().height ?? 0
        tocNatural = node.querySelector('nav')?.scrollHeight ?? 0
        // navのmarginは固定予算に含め、全体scrollとの切替でも同じ予算を使う。
        const nav = node.querySelector('nav')
        if (nav) { const style = getComputedStyle(nav); fixed += (parseFloat(style.marginTop) || 0) + (parseFloat(style.marginBottom) || 0) }
      }
      fixed += height + (parseFloat(css.marginTop) || 0) + (parseFloat(css.marginBottom) || 0)
    }
  }
  const required = fixed + (tocNatural ? Math.min(tocNatural, 48) : 0) + 142
  const next = panelScroll.value ? available < required + 8 : available < required
  const cap = Math.min(192, available * .25)
  tocHeight.value = next ? cap : Math.max(0, Math.min(cap, available - fixed - 142))
  if (panelScroll.value !== next) {
    const previousPort = panelScroll.value ? panel : treeComponent.value?.port
    const active = document.activeElement as HTMLElement | null
    const anchor = active && panel?.contains(active) && active.matches('input') ? active
      : [...(panel?.querySelectorAll<HTMLElement>('summary, .wiki-tree-heading, [data-row-key], .wiki-selected-path') ?? [])]
        .find(node => node.getBoundingClientRect().bottom > (previousPort?.getBoundingClientRect().top ?? 0) && (panelScroll.value || node.hasAttribute('data-row-key')))
    const offset = anchor && previousPort ? anchor.getBoundingClientRect().top - previousPort.getBoundingClientRect().top : undefined
    panelScroll.value = next
    void nextTick(() => {
      if (!alive || !anchor || offset === undefined) return
      const destination = next ? panel : treeComponent.value?.port
      if (destination && destination.contains(anchor)) destination.scrollTop += anchor.getBoundingClientRect().top - destination.getBoundingClientRect().top - offset
      // 検索に焦点がある場合はツリーより入力の可視性を優先する。
      if (active && panel?.contains(active) && active.matches('input')) active.focus({ preventScroll: false })
    })
  }
}
const tocOpen = ref(true),
  searchOpen = ref(true)
function revealWithin(port: HTMLElement, target: HTMLElement) {
  const bounds = port.getBoundingClientRect(), rect = target.getBoundingClientRect()
  if (rect.top < bounds.top) port.scrollTop += rect.top - bounds.top
  else if (rect.bottom > bounds.bottom) port.scrollTop += rect.bottom - bounds.bottom
}
watch([activeHeading, panelHidden, tocOpen], () => {
  const nav = tocPort.value, current = nav?.querySelector<HTMLElement>('[aria-current="location"]')
  // 追従で祖先のパネルや本文を動かさない。
  if (nav && current && tocOpen.value && !panelHidden.value) revealWithin(nav, current)
}, { flush: 'post' })
function revealTocFocus(event: FocusEvent) {
  const target = event.target as HTMLElement
  if (tocPort.value) revealWithin(tocPort.value, target)
  // キーボード操作では目次自体がパネル外にある場合も可視化する。
  if (wholePanel.value && panelPort.value) revealWithin(panelPort.value, target)
}
function openToc() {
  tocOpen.value = true
  if (narrow.value) drawerOpen.value = true
  else { panelCollapsed.value = false; saveWikiPanelCollapsed(false) }
}
async function goToHeading(id: string) {
  if (narrow.value) closeDrawer(false)
  await nextTick()
  markdown.value?.goToHeading(id)
}
async function navigateWiki(id: number) {
  const page = wikiIndex.value.find((p) => p.id === id && p.projectId === selectedProjectId.value)
  if (!page) return
  if (!(await select(page.name, page.id))) return
  await nextTick()
  pageHeading.value?.focus({ preventScroll: true })
}
const backend = getBackend()
function togglePanel() {
  if (narrow.value) { if (drawerOpen.value) closeDrawer(); else drawerOpen.value = true; return }
  panelCollapsed.value = !panelCollapsed.value
  saveWikiPanelCollapsed(panelCollapsed.value)
}
const profile = ref(''),
  spaceURL = ref('')
const projects = ref<Project[]>([]),
  pages = shallowRef<WikiPage[]>([])
const initializing = ref(true),
  busy = ref(false),
  disabled = ref(false)
const appliedFilter = ref(false)
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
const editing = ref(false),
  editingId = ref(0),
  writingBusy = ref(false)
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
  if (apiLocked.value || disabled.value || !selectedProjectId.value || !(await leaveEditor())) return
  editingId.value = create ? 0 : selectedId.value
  editorGeneration.value++
  editing.value = true
  error.value = notice.value = ''
}
async function wikiSaved(page: WikiPage) {
  editing.value = false
  writingBusy.value = false
  // 検索条件を維持する。名前変更で検索から外れても保存済みページは表示する。
  pages.value = [...pages.value.filter((p) => p.id !== page.id), page]
  wikiIndex.value = [...wikiIndex.value.filter((p) => p.id !== page.id), page]
  await select(page.name, page.id)
  notice.value = t('wiki.saved')
}
function cancelEditor() {editing.value=false;writingBusy.value=false}
async function changeProject(event: Event) {
  const select = event.target as HTMLSelectElement
  if (locked.value || !(await leaveEditor())) {
    select.value = String(selectedProjectId.value)
    return
  }
  await attachments.value?.cancel()
  selectedProjectId.value = Number(select.value)
}
const dialogOpen = computed(() => estimate.value !== null)
watch(dialogOpen, open => { if (open) closeDrawer(false) }, { flush: 'sync' })
const locked = computed(
  () => initializing.value || busy.value || writingBusy.value || issueSyncRunning.value,
)
watch([headings, searchOpen, tocOpen, panelCollapsed, selectedPath, notice, error, editing, initializing, disabled, busy, warnings, preparing, exporting], () => void nextTick(scheduleLayout))
watch(panelHidden, hidden => {
  if (hidden || (narrow.value && !pendingReveal)) return
  const key = pendingReveal || `p:${selectedId.value}`
  pendingReveal = ''
  void nextTick(() => { if (!panelHidden.value) treeComponent.value?.reveal(key) })
})
const tree = computed(() => buildWikiTree(displayedPages.value))
const selected = computed(
  () =>
    wikiIndex.value.find((p) => p.id === selectedId.value) ??
    pages.value.find((p) => p.id === selectedId.value),
)
const wikiLarge = computed(() =>
  selected.value
    ? new TextEncoder().encode(selected.value.content).length > 2 * 1024 * 1024
    : false,
)
const wikiContext = computed(() =>
  selected.value ? { page: selected.value, pages: wikiIndex.value } : undefined,
)
let alive = true,
  request = 0
const off = onSyncProgress((p) => {
  if (p.profileId === profile.value && p.runId === runId.value) progress.value = p
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
    appliedFilter.value = false
    lastSynced.value = ''
    return
  }
  const submittedKeyword = keyword.value
  const [full, result] = await Promise.all([
    backend.listWikis(profile.value, { projectId: pid, keyword: '', keywordMode: 'and' }),
    backend.listWikis(profile.value, {
      projectId: pid,
      keyword: submittedKeyword,
      keywordMode: keywordMode.value,
    }),
  ])
  if (!alive || sequence !== request || pid !== selectedProjectId.value) return
  pages.value = result.pages
  appliedFilter.value = !!submittedKeyword.trim()
  wikiIndex.value = full.pages
  projectFormatting.value = result.textFormattingRule ?? result.pages[0]?.textFormattingRule ?? ''
  disabled.value = result.disabled
  lastSynced.value = result.lastSyncedAt
  if (selectedId.value && !wikiIndex.value.some((p) => p.id === selectedId.value)) {
    selectedId.value = 0
    headings.value = []
  }
  if (selectedRowKey.value && !fullTree.value.rows.has(selectedRowKey.value)) {
    selectedPath.value = ''
    selectedRowKey.value = ''
  } else if (selectedRowKey.value) {
    selectedPath.value = fullTree.value.rows.get(selectedRowKey.value)!.node.path
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
async function select(path: string, id: number, rowKey = '') {
  const pid = selectedProjectId.value, sequence = request
  if (locked.value || !(await leaveEditor()) || pid !== selectedProjectId.value || sequence !== request || !alive) return
  if (!id) {
    selectedPath.value = path
    selectedRowKey.value = rowKey || `d:${JSON.stringify(path.split('/').filter(Boolean))}`
    toggleNode(JSON.stringify(path.split('/').filter(Boolean)))
    return
  }
  if(id!==selectedId.value)await attachments.value?.cancel()
  const target = fullTree.value.pages.get(id)
  if (!target) return
  allowLarge.value = false
  tocLimit.value = 500
  temporaryId.value = id
  selectedPath.value = target.node.path
  selectedId.value = id
  selectedRowKey.value = target.rowKey
  focusedRowKey.value = target.rowKey
  const next = new Set(collapsedNodeKeys.value)
  for (let i = 1; i < target.node.segments.length; i++) next.delete(JSON.stringify(target.node.segments.slice(0, i)))
  collapsedNodeKeys.value = next
  formatted.value = true
  await nextTick()
  if (!alive || pid !== selectedProjectId.value || id !== selectedId.value) return
  if (bodyPort.value) { bodyPort.value.scrollTop = 0; bodyPort.value.scrollLeft = 0 }
  if (outerPort.value && bodyPort.value && atMinimum.value) {
    const delta = bodyPort.value.getBoundingClientRect().top - outerPort.value.getBoundingClientRect().top
    if (delta > 0) outerPort.value.scrollTop += delta
  }
  if (narrow.value) pendingReveal = target.rowKey
  if (!panelHidden.value) treeComponent.value?.reveal(target.rowKey)
  if (narrow.value && drawerOpen.value) { closeDrawer(false); await nextTick(); pageHeading.value?.focus({ preventScroll: true }) }
  return true
}
watch(selectedProjectId, () => {
  pendingReveal = ''
  closeDrawer(false)
  collapsedNodeKeys.value = new Set()
  selectedRowKey.value = focusedRowKey.value = ''
  temporaryId.value = 0
  if (treeComponent.value?.port) treeComponent.value.port.scrollTop = 0
  selectedId.value = 0
  selectedPath.value = ''
  pages.value = []
  wikiIndex.value = []
  headings.value = []
  activeHeading.value = ''
  disabled.value = false
  lastSynced.value = ''
  if (!initializing.value)
    void load().catch((e) => {
      error.value = errorMessage(e)
    })
})
let unregisterDownloadGuard: (()=>void)|undefined
watch(editing, value=>{
 if(!value){unregisterDownloadGuard?.();unregisterDownloadGuard=registerNavigationGuard(async()=>{if(locked.value)return false;await attachments.value?.cancel();return true})}
},{immediate:true,flush:'post'})
watch(locale,language=>{if(!editing.value)void backend.setWikiWindowState(false,false,language).catch(()=>{})},{immediate:true})
onMounted(async () => {
  outerPort.value = layout.value?.closest<HTMLElement>('.content') ?? null
  if (typeof ResizeObserver !== 'undefined') {
    layoutObserver = new ResizeObserver(scheduleLayout)
    if (outerPort.value) layoutObserver.observe(outerPort.value)
    if (layout.value) layoutObserver.observe(layout.value)
    if (contentFrame.value) layoutObserver.observe(contentFrame.value)
    const section = layout.value?.closest('.wiki-view')
    if (section) for (const child of section.children) if (child !== layout.value) layoutObserver.observe(child)
    const panel = layout.value?.querySelector('aside')
    if (panel) for (const child of panel.children) if (!child.classList.contains('wiki-tree')) layoutObserver.observe(child)
  }
  window.addEventListener('resize', scheduleLayout)
  // 初回はrAFへ遅延せず、最初の描画前にモードと高さを確定する。
  measureLayout()
  try {
    profile.value = await backend.getActiveProfile()
    if (!profile.value) return
    spaceURL.value =
      (await backend.listProfiles()).find((p) => p.id === profile.value)?.spaceUrl ?? ''
    if (!issueSyncRunning.value && !shouldSkipProjectRefreshFor(profile.value))
      await runSharedProjectRefresh(profile.value, () => backend.syncProjects(profile.value))
    projects.value = await backend.listProjects(profile.value)
    restoreProjectSelection(profile.value)
    selectedProjectId.value = resolveProjectSelection(projects.value, selectedProjectId.value)
    await load()
  } catch (e) {
    error.value = errorMessage(e)
  } finally {
    initializing.value = false
  }
})
onUnmounted(() => {
  unregisterDownloadGuard?.()
  layoutObserver?.disconnect()
  window.removeEventListener('resize', scheduleLayout)
  if (layoutFrame) cancelAnimationFrame(layoutFrame)
  alive = false
  request++
  off()
  if (runId.value && (preparing.value || estimate.value || exporting.value))
    void backend.cancelWikiExport(profile.value, runId.value).catch(() => {})
  if (estimate.value && !exporting.value) finishRun(runId.value)
})
async function sync() {
  if (apiLocked.value || !selectedProjectId.value || !(await leaveEditor())) return
  const id = startRun()
  try {
    await runSharedProjectRefresh(profile.value, () => backend.syncProjects(profile.value))
    projects.value = await backend.listProjects(profile.value)
    if (!projects.value.some((p) => p.id === selectedProjectId.value)) {
      selectedProjectId.value = resolveProjectSelection(projects.value, 0)
      await load()
      return
    }
    const result = await backend.syncWikis(profile.value, selectedProjectId.value, id)
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
  if (apiLocked.value || !selected.value || !(await leaveEditor())) return
  const id = startRun()
  try {
    await backend.refreshWiki(profile.value, selectedProjectId.value, selected.value.id)
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
    await copyToClipboard(`${spaceURL.value.replace(/\/$/, '')}/alias/wiki/${selected.value.id}`)
    notice.value = t('wiki.copied')
  } catch (e) {
    error.value = errorMessage(e)
  }
}
async function prepare(subtree: string) {
  if (apiLocked.value || !selectedProjectId.value || !(await leaveEditor())) return
  const id = startRun()
  preparing.value = true
  try {
    if (syncFirst.value)
      await runSharedProjectRefresh(profile.value, () => backend.syncProjects(profile.value))
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
      <button :disabled="apiLocked || !selectedProjectId" @click="sync">
        {{ t('wiki.sync') }}
      </button>
      <span>{{
        lastSynced ? t('wiki.freshness', { at: formatDateTime(lastSynced) }) : t('wiki.unsynced')
      }}</span>
    </div>
    <p v-if="disabled" role="status">{{ t('wiki.disabled') }}</p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <ul v-if="warnings.length">
      <li v-for="w in warnings" :key="w">{{ w }}</li>
    </ul>
    <p v-if="busy" role="status">
      {{ preparing ? t('wiki.preparing') : exporting ? t('wiki.exporting') : t('wiki.working') }}
      <span v-if="progress">{{ progress.fetched }} / {{ progress.total }}</span>
    </p>
    <button v-if="preparing || exporting" @click="cancelExport">
      {{ t('wiki.cancel') }}
    </button>
    <div class="toolbar">
      <button :disabled="apiLocked || disabled || !selectedProjectId" @click="openEditor(true)">
        {{ t('wiki.newPage') }}
      </button>
      <label><input v-model="syncFirst" type="checkbox" :disabled="apiLocked" />{{
        t('wiki.syncFirst')
      }}</label>
      <button :disabled="apiLocked || disabled || !selectedProjectId" @click="prepare('')">
        {{ t('wiki.exportProject') }}
      </button>
      <button :disabled="apiLocked || disabled || !selectedPath" @click="prepare(selectedPath)">
        {{ t('wiki.exportSubtree') }}
      </button>
    </div>
    <div class="wiki-panel-toolbar">
      <button
        ref="panelToggle"
        type="button"
        :aria-expanded="!panelHidden"
        aria-controls="wiki-search-panel"
        @click="togglePanel"
      >
        {{ panelHidden ? t('wiki.showPanel') : t('wiki.hidePanel') }}
      </button>
    </div>
    <div ref="layout" class="wiki-layout" :class="{ 'panel-collapsed': panelCollapsed || narrow, 'panel-scroll': panelScroll }" :style="{ height: `${layoutHeight}px` }">
      <div ref="contentFrame" class="wiki-content-frame">
        <div ref="bodyPort" class="wiki-content" :aria-busy="initializing">
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
            @cancel="cancelEditor"
            @error="error = $event"
            @busy="writingBusy = $event"
          />
          <article v-else-if="selected" class="wiki-detail">
            <h2 ref="pageHeading" tabindex="-1">{{ selected.name }}</h2>
            <div class="toolbar">
              <button :disabled="apiLocked || disabled" @click="openEditor(false)">
                {{ t('wiki.edit') }}
              </button>
              <button :disabled="apiLocked" @click="refresh">
                {{ t('wiki.refresh') }}
              </button><button :disabled="!spaceURL" @click="copy">
                {{ t('wiki.copy') }}
              </button>
              <label v-if="isMarkdownRule(selected.textFormattingRule)"><input v-model="formatted" type="checkbox" />{{ t('wiki.formatted') }}</label>
            </div>
            <dl>
              <dt>{{ t('wiki.tags') }}</dt>
              <dd>{{ selected.tags.join(', ') }}</dd>
              <dt>{{ t('wiki.created') }}</dt>
              <dd>{{ selected.createdUser }} {{ formatDateTime(selected.created) }}</dd>
              <dt>{{ t('wiki.updated') }}</dt>
              <dd>{{ selected.updatedUser }} {{ formatDateTime(selected.updated) }}</dd>
            </dl>
            <p v-if="!selected.contentFetchedAt">{{ t('wiki.missingBody') }}</p>
            <p v-else-if="selected.contentUpdated !== selected.updated">
              {{ t('wiki.staleBody') }}
            </p>
            <button
              v-if="formatted && isMarkdownRule(selected.textFormattingRule)"
              type="button"
              @click="openToc"
            >
              {{ t('wiki.render.toc') }}
            </button>
            <button v-if="wikiLarge && !allowLarge" type="button" @click="allowLarge = true">
              {{ t('wiki.render.renderLarge') }}
            </button>
            <MarkdownBody
              v-if="formatted && isMarkdownRule(selected.textFormattingRule)"
              :key="`${profile}:${selectedProjectId}:${selected.id}:${selected.contentUpdated}`"
              ref="markdown"
              class="wiki-preview markdown-body"
              variant="wiki"
              :source="selected.content"
              :wiki="wikiContext"
              :profile="profile"
              :disabled="locked"
              :allow-large="allowLarge"
              :scroll-container="markdownScrollRoot"
              @headings="headings = $event"
              @active="activeHeading = $event"
              @navigate="navigateWiki"
            />
            <pre v-else class="wiki-preview">{{ selected.content }}</pre>
            <WikiAttachments :key="`${profile}:${selectedProjectId}:${selected.id}`" ref="attachments" :project-id="selectedProjectId" :wiki-id="selected.id" :attachments="selected.attachments" :disabled="locked || disabled" @busy="attachmentBusy=$event" />
          </article>
          <p v-else-if="initializing" role="status">{{ t('wiki.working') }}</p>
          <p v-else>{{ t('wiki.choosePage') }}</p>
        </div>
      </div>
      <Teleport to="body"><div ref="drawerHost" class="wiki-drawer-host content"><div v-if="narrow && drawerOpen" class="wiki-drawer-mask" aria-hidden="true" @click.self="closeDrawer()" /></div></Teleport>
      <Teleport :to="drawerHost ?? 'body'" :disabled="!narrow">
        <aside id="wiki-search-panel" ref="panelPort" class="wiki-panel" :class="{ 'wiki-panel-scroll': wholePanel, 'wiki-drawer': narrow }" :hidden="panelHidden" :role="narrow ? 'dialog' : undefined" :aria-modal="narrow && drawerOpen ? true : undefined" :tabindex="narrow ? -1 : undefined" :aria-label="t('wiki.panel')">
          <button v-if="narrow" type="button" class="wiki-drawer-close" :title="t('wiki.closePanel')" :aria-label="t('wiki.closePanel')" @click="closeDrawer()">{{ t('wiki.closePanel') }}</button>
          <details
            :open="searchOpen"
            @toggle="searchOpen = ($event.target as HTMLDetailsElement).open"
          >
            <summary>{{ t('wiki.panel') }}</summary>
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
          </details>
          <details
            v-if="selected && formatted && !editing && isMarkdownRule(selected.textFormattingRule)"
            class="wiki-toc-section"
            :open="tocOpen"
            @toggle="tocOpen = ($event.target as HTMLDetailsElement).open"
          >
            <summary>{{ t('wiki.render.toc') }}</summary>
            <nav ref="tocPort" :style="{ maxHeight: `${tocHeight}px` }" class="wiki-toc" :aria-label="t('wiki.render.toc')" @focusin="revealTocFocus">
              <button
                v-for="h in headings.slice(0, tocLimit)"
                :key="h.id"
                type="button"
                :class="[`toc-level-${h.level}`, { current: activeHeading === h.id }]"
                :aria-current="activeHeading === h.id ? 'location' : undefined"
                @click="goToHeading(h.id)"
              >
                {{ h.text }}
              </button>
              <p v-if="!headings.length">{{ t('wiki.render.noHeadings') }}</p>
              <button v-if="headings.length > tocLimit" type="button" @click="tocLimit += 500">
                {{ t('wiki.render.more') }}
              </button>
            </nav>
          </details>
          <p v-if="!formatted || editing" role="status">{{ t('wiki.render.tocUnavailable') }}</p>
          <div class="wiki-tree-heading">
            <h3 class="wiki-tree-title">{{ t('wiki.tree') }}</h3>
            <div class="wiki-tree-actions">
              <button type="button" :disabled="locked || !hasCollapsed" :title="t('wiki.allTreeScope')" @click="expandAll">{{ t('wiki.expandAll') }}</button>
              <button type="button" :disabled="locked || allCollapsed" :title="t('wiki.allTreeScope')" @click="collapseAll">{{ t('wiki.collapseAll') }}</button>
            </div>
          </div>
          <WikiTree ref="treeComponent" :nodes="tree" :selected-id="selectedId" :selected-row-key="selectedRowKey" :focused-row-key="focusedRowKey" :collapsed="collapsedNodeKeys" :outside-id="outsideId" :filtered="appliedFilter" :disabled="locked" :scroll-port="treeScrollPort" @select="select" @toggle="toggleNode" @focus-row="focusedRowKey = $event" />
          <p v-if="!pages.length && !initializing">{{ t('wiki.empty') }}</p>
          <p v-if="selectedPath" class="wiki-selected-path">{{ t('wiki.subtree', { path: selectedPath }) }}</p>
        </aside>
      </Teleport>
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
  --wiki-frame-padding: 1rem;
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
  position: relative;
  min-height: 0;
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
.wiki-panel {
  position: relative;
  width: 100%;
  box-sizing: border-box;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
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
  margin: 0;
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
.wiki-view .wiki-content-frame {
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  border: var(--border-width) solid var(--border);
  border-radius: var(--radius-ui);
  background: var(--surface);
  color: var(--text);
  box-shadow: var(--shadow-none);
  padding: var(--radius-ui);
}
.wiki-view .wiki-content {
  flex: 1;
  box-sizing: border-box;
  /* --radius-ui が --wiki-frame-padding 以下であることを前提とする。 */
  padding: calc(var(--wiki-frame-padding) - var(--radius-ui));
  min-height: 0;
  overflow-y: auto;
  overflow-anchor: none;
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
@media (max-width: 900px) {
  .wiki-detail dl {
    grid-template-columns: minmax(0, 1fr);
  }
  .wiki-detail dd {
    margin-bottom: 0.5rem;
  }
}
.wiki-toc {
  max-height: 12rem;
  overflow-y: auto;
  display: grid;
  gap: 0.2rem;
  margin-block: 0.75rem;
}
.wiki-toc button {
  text-align: left;
  border: 0;
  background: var(--surface);
  box-shadow: var(--shadow-none);
  color: var(--text);
}
.wiki-toc button.current {
  background: var(--selection-bg);
  border-left: var(--quote-border-width) solid var(--border);
  font-weight: var(--strong-weight);
}
.wiki-toc .toc-level-2 {
  padding-left: 1rem;
}
.wiki-toc .toc-level-3 {
  padding-left: 1.5rem;
}
.wiki-toc :is(.toc-level-4, .toc-level-5, .toc-level-6) {
  padding-left: 2rem;
}

.wiki-panel[hidden] { display: none; }
.wiki-panel > :not(.wiki-tree) { flex: none; }
.wiki-tree-heading { display: flex; flex-direction: column; align-items: stretch; gap: .5rem; }
.wiki-tree-actions { display: flex; flex-wrap: wrap; gap: .5rem; }
.wiki-selected-path { overflow-wrap: anywhere; }
.wiki-panel-scroll { overflow-y: auto; overscroll-behavior: contain; overflow-anchor: none; }
.wiki-panel-scroll :deep(.wiki-tree) { overflow-y: visible; flex: none; }
.wiki-panel-scroll :deep(.wiki-focus-path) { position: static; }
.wiki-drawer-host { position: fixed; inset: 0; z-index: 90; pointer-events: none; }
.wiki-drawer-mask { position: absolute; inset: 0; background: var(--overlay); pointer-events: auto; touch-action: none; }
.wiki-panel.wiki-drawer { z-index: 1; position: fixed; top: 0; right: 0; width: min(20rem, 100vw); height: 100vh; height: 100dvh; padding: 1rem; background: var(--surface); box-shadow: var(--shadow-dialog); pointer-events: auto; animation: wiki-drawer-enter .15s ease-out; }
.wiki-drawer-close { align-self: flex-end; margin-bottom: .75rem; }
@keyframes wiki-drawer-enter { from { transform: translateX(100%); } to { transform: translateX(0); } }
@media (prefers-reduced-motion: reduce) { .wiki-panel.wiki-drawer { animation: none; } }
</style>
