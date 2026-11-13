<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { flattenWikiTree, revealWikiRow, type WikiTreeNode, type WikiTreeRow } from '../lib/wikiTree'
const props = withDefaults(defineProps<{
  nodes: WikiTreeNode[]
  selectedId: number
  selectedRowKey?: string
  focusedRowKey?: string
  collapsed?: ReadonlySet<string>
  outsideId?: number
  filtered?: boolean
  disabled: boolean
  scrollPort?: HTMLElement | null
}>(), { selectedRowKey: '', focusedRowKey: '', collapsed: () => new Set<string>(), outsideId: 0, scrollPort: null })
const emit = defineEmits<{
  select: [path: string, id: number, rowKey: string]
  toggle: [nodeKey: string]
  focusRow: [rowKey: string]
}>()
const { t, locale } = useI18n()
const port = ref<HTMLElement | null>(null), focusKey = ref('')
const rows = computed(() => flattenWikiTree(props.nodes, props.collapsed))
const rowElements = new Map<string, HTMLElement>()
let prefix = '', typedAt = 0
const foldAscii = (value: string) => value.replace(/[A-Z]/g, c => c.toLowerCase())
const focused = computed(() => rows.value.find(r => r.rowKey === focusKey.value))
watch(() => props.focusedRowKey, value => { if (value) focusKey.value = value })
watch(rows, (current, old) => {
  if (current.some(r => r.rowKey === focusKey.value)) return
  const previous = old?.find(r => r.rowKey === focusKey.value)
  const path = previous?.node.segments ?? []
  focusKey.value = current.find(r => r.node.key === previous?.node.key)?.rowKey
    ?? [...current].reverse().find(r => r.node.segments.length < path.length && r.node.segments.every((p, i) => p === path[i]))?.rowKey
    ?? current[0]?.rowKey ?? ''
  if (port.value?.contains(document.activeElement)) void nextTick(() => rowElements.get(focusKey.value)?.focus({ preventScroll: true }))
}, { immediate: true })
function reveal(key: string) {
  const el = rowElements.get(key)
  const owner = props.scrollPort ?? port.value
  if (owner && el) revealWikiRow(owner, el)
}
async function focus(row: WikiTreeRow) {
  focusKey.value = row.rowKey
  emit('focusRow', row.rowKey)
  await nextTick()
  rowElements.get(row.rowKey)?.focus({ preventScroll: true })
  reveal(row.rowKey)
}
function activate(row: WikiTreeRow) {
  if (props.disabled) return
  void focus(row)
  emit('select', row.node.path, row.pageId, row.rowKey)
}
function toggle(row: WikiTreeRow) {
  if (props.disabled) return
  void focus(row)
  emit('toggle', row.node.key)
}
function keydown(event: KeyboardEvent, row: WikiTreeRow) {
  if (event.isComposing || event.keyCode === 229 || event.altKey || event.metaKey || event.ctrlKey) return
  const index = rows.value.findIndex(r => r.rowKey === row.rowKey)
  const keys = ['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', 'Home', 'End', 'Enter', ' ']
  if (keys.includes(event.key)) event.preventDefault()
  if (props.disabled) return
  let target: WikiTreeRow | undefined
  switch (event.key) {
    case 'ArrowUp': target = rows.value[Math.max(0, index - 1)]; break
    case 'ArrowDown': target = rows.value[Math.min(rows.value.length - 1, index + 1)]; break
    case 'Home': target = rows.value[0]; break
    case 'End': target = rows.value.at(-1); break
    case 'ArrowRight':
      if (row.hasChildren && props.collapsed.has(row.node.key)) toggle(row)
      else if (row.hasChildren) target = rows.value[index + 1]
      break
    case 'ArrowLeft':
      if (row.hasChildren && !props.collapsed.has(row.node.key)) toggle(row)
      else target = rows.value.find(r => r.rowKey === row.parentRowKey)
      break
    case 'Enter': case ' ': activate(row); break
    default: {
      if (event.key.length !== 1) return
      if (Date.now() - typedAt > 700) prefix = ''
      typedAt = Date.now()
      prefix += foldAscii(event.key)
      const search = [...rows.value.slice(index + 1), ...rows.value.slice(0, index + 1)]
      const query = [...prefix].every(c => c === prefix[0]) ? prefix[0]! : prefix
      target = search.find(r => foldAscii(r.node.label).startsWith(query))
    }
  }
  if (target) void focus(target)
}
defineExpose({ reveal, port })
</script>
<template>
  <div ref="port" class="wiki-tree" role="tree" :aria-label="t('wiki.tree')" :aria-description="`${t('wiki.selectSubtree')}. ${t('wiki.treeKeyboardHelp')}`">
    <div
      v-for="row in rows" :key="row.rowKey"
      :ref="el => { if (el) rowElements.set(row.rowKey, el as HTMLElement); else rowElements.delete(row.rowKey) }"
      v-memo="[row, focusKey === row.rowKey, selectedId === row.pageId, selectedRowKey === row.rowKey, collapsed.has(row.node.key), disabled, outsideId === row.pageId, filtered, locale]"
      class="wiki-tree-row" :class="{ current: selectedId === row.pageId && row.pageId !== 0, selected: selectedRowKey === row.rowKey, folder: !row.pageId }"
      role="treeitem" :data-row-key="row.rowKey" :tabindex="focusKey === row.rowKey ? 0 : -1"
      :aria-level="row.level" :aria-posinset="row.posinset" :aria-setsize="row.setsize"
      :aria-expanded="row.hasChildren ? !collapsed.has(row.node.key) : undefined"
      :aria-selected="selectedRowKey === row.rowKey" :aria-current="selectedId === row.pageId && row.pageId !== 0 ? 'page' : undefined"
      :aria-disabled="disabled" :title="row.node.path || t('wiki.unnamed')"
      :style="{ marginLeft: `${Math.min(row.level - 1, 6)}rem` }"
      @focus="focusKey = row.rowKey; emit('focusRow', row.rowKey)" @keydown="keydown($event, row)"
    >
      <span class="wiki-node-arrow" :title="collapsed.has(row.node.key) ? t('wiki.expandNode', { name: row.node.label }) : t('wiki.collapseNode', { name: row.node.label })" @click.stop="row.hasChildren && toggle(row)">
        <svg v-if="row.hasChildren" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path :d="collapsed.has(row.node.key) ? 'm6 4 4 4-4 4' : 'm4 6 4 4 4-4'" /></svg>
      </span>
      <svg viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path :d="row.pageId ? 'M3 1.5h6l4 4v9H3zM9 1.5v4h4M5 8h6M5 11h6' : 'M1.5 4V2.5h5l2 2h6v9h-13zM1.5 5h13'" /></svg>
      <span class="wiki-node-name" @click="activate(row)">{{ row.node.label || t('wiki.unnamed') }}</span>
      <span v-if="selectedId === row.pageId && row.pageId" class="wiki-sr-only">{{ t('wiki.currentPage') }}</span>
      <span class="wiki-sr-only">{{ !row.pageId ? t('wiki.folderNode') : row.hasChildren ? t('wiki.pageWithChildrenNode') : t('wiki.pageNode') }}</span>
      <span v-if="row.node.pageIds.length > 1" class="wiki-node-id" :aria-label="t('wiki.duplicatePage', { id: row.pageId })">#{{ row.pageId }}</span>
      <span v-if="row.hasChildren" class="wiki-node-count" :aria-label="filtered ? t('wiki.filteredDescendantPages', { count: row.node.descendantPageCount }) : t('wiki.descendantPages', { count: row.node.descendantPageCount })">{{ row.node.descendantPageCount }}</span>
      <svg v-if="selectedRowKey === row.rowKey" viewBox="0 0 16 16" :aria-label="t('wiki.selectedHierarchy')" role="img"><path d="m3 8 3 3 7-7" /></svg>
      <span v-if="outsideId && outsideId === row.pageId" class="wiki-outside" :title="t('wiki.outsideSearch')">{{ t('wiki.outsideSearch') }}</span>
    </div>
  </div>
  <p v-if="focused" class="wiki-focus-path">{{ focused.node.path || t('wiki.unnamed') }}</p>
</template>
<style scoped>
.wiki-tree { flex: 1; min-height: 8rem; overflow-y: auto; overscroll-behavior: contain; overflow-anchor: none; padding: 7px; }
.wiki-tree-row { display: flex; align-items: center; gap: 4px; min-height: 2rem; padding: 2px 4px; border-left: var(--quote-border-width) solid transparent; position: relative; border-radius: var(--radius-none); box-shadow: var(--shadow-none); }
.wiki-tree-row::before { content: ''; position: absolute; left: 0; height: 100%; border-left: var(--border-width) solid var(--border); width: 4px; pointer-events: none; }
.wiki-tree-row[aria-level='1']::before { display: none; }
.wiki-tree-row:hover { background: var(--bg-hover); }
.wiki-tree-row.current { background: var(--selection-bg); border-left-color: var(--border); }
.wiki-tree-row.selected::after { content: ''; position: absolute; inset: 1px; border: var(--border-width) dashed var(--border); pointer-events: none; }
.wiki-tree-row:focus-visible { outline: var(--focus-width) solid var(--focus-ring); outline-offset: var(--focus-offset); }
.wiki-tree-row[aria-disabled='true'] { color: var(--text-faint); background: var(--bg-muted); }
.wiki-tree-row svg { width: 16px; height: 16px; flex: none; fill: none; stroke: currentColor; stroke-width: 1.6; }
.wiki-node-arrow { width: 24px; height: 24px; display: grid; place-items: center; flex: none; cursor: pointer; }
.wiki-node-name { flex: 1; min-width: 0; min-height: 24px; display: block; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer; line-height: 24px; }
.folder .wiki-node-name { font-weight: var(--strong-weight); }
.wiki-node-count, .wiki-node-id { color: var(--text-muted); flex: none; }
.wiki-node-id { font-size: 0.75rem; padding: 0 3px; background: var(--bg-muted); border: var(--border-width) solid var(--border); border-radius: var(--radius-ui); }
.wiki-focus-path { position: absolute; bottom: 0; left: 0; right: 0; z-index: 1; background: var(--surface); color: var(--text); display: none; overflow-wrap: anywhere; margin: 4px 0; }
.wiki-tree:focus-within + .wiki-focus-path { display: block; }
.wiki-outside { max-width: 4rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.75rem; }
.wiki-sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
</style>
