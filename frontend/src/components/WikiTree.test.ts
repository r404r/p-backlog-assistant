import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { buildWikiTree } from '../lib/wikiTree'
import WikiTree from './WikiTree.vue'
let mounted: MountedApp | undefined
afterEach(() => mounted?.unmount())
function renderTree(props: () => InstanceType<typeof WikiTree>['$props']) {
  return mountWithI18n(defineComponent({ setup: () => () => h(WikiTree, props()) }))
}
function open(disabled = false) {
  const nodes = buildWikiTree([{ id: 1, name: 'a' }, { id: 2, name: 'a/b' }, { id: 3, name: 'folder/c' }])
  const collapsed = ref(new Set<string>())
  const selected = ref('p:1')
  const select = vi.fn((_path: string, id: number) => { selected.value = `p:${id}` })
  mounted = renderTree(() => ({
    nodes, selectedId: 1, disabled, collapsed: collapsed.value, selectedRowKey: selected.value,
    onSelect: select,
    onToggle: (key: string) => {
      const next = new Set(collapsed.value)
      if (next.has(key)) next.delete(key); else next.add(key)
      collapsed.value = next
    },
  }))
  return { host: mounted.host, select, collapsed }
}
function key(el: Element, value: string) {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: value, bubbles: true, cancelable: true }))
}
it('distinguishes the current page from selected hierarchy and labels folders and descendants', async () => {
  const { host, select } = open()
  const rows = host.querySelectorAll<HTMLElement>('[role=treeitem]')
  expect(rows.length).toBe(4)
  expect(rows[0]!.getAttribute('aria-current')).toBe('page')
  expect(rows[0]!.getAttribute('aria-expanded')).toBe('true')
  expect(rows[1]!.hasAttribute('aria-expanded')).toBe(false)
  rows[1]!.querySelector<HTMLElement>('.wiki-node-name')!.click()
  await nextTick()
  expect(select).toHaveBeenCalledExactlyOnceWith('a/b', 2, 'p:2')
  expect(rows[1]!.getAttribute('aria-selected')).toBe('true')
  expect(rows[0]!.getAttribute('aria-current')).toBe('page')
})
it('collapses by arrow without selecting, moves focus with arrows and type-ahead, activates once', async () => {
  const { host, select } = open()
  const row = host.querySelector<HTMLElement>('[role=treeitem]')!
  row.focus()
  key(row, 'ArrowLeft')
  await nextTick()
  expect(host.querySelectorAll('[role=treeitem]')).toHaveLength(3)
  expect(select).not.toHaveBeenCalled()
  key(row, 'ArrowRight')
  await nextTick()
  key(row, 'ArrowRight')
  await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:2')
  key(document.activeElement!, 'f')
  await nextTick()
  expect(document.activeElement?.textContent).toContain('folder')
  key(document.activeElement!, ' ')
  await nextTick()
  expect(select).toHaveBeenCalledTimes(1)
  expect(host.querySelectorAll('[tabindex="0"]')).toHaveLength(1)
})
it('rejects pointer and keyboard changes while disabled', async () => {
  const { host, select, collapsed } = open(true)
  const row = host.querySelector<HTMLElement>('[role=treeitem]')!
  key(row, 'Enter'); key(row, 'ArrowLeft')
  row.querySelector<HTMLElement>('.wiki-node-name')!.click()
  await nextTick()
  expect(select).not.toHaveBeenCalled()
  expect(collapsed.value.size).toBe(0)
  expect(row.getAttribute('aria-disabled')).toBe('true')
})
it('keeps duplicate pages distinct, restores ancestor focus, and supports Home/End', async () => {
  const nodes = ref(buildWikiTree([{ id: 1, name: 'a' }, { id: 2, name: 'a/child' }, { id: 3, name: 'a/child' }]))
  const collapsed = ref(new Set<string>())
  mounted = renderTree(() => ({ nodes: nodes.value, selectedId: 3, selectedRowKey: 'p:2', collapsed: collapsed.value, disabled: false }))
  let rows = mounted.host.querySelectorAll<HTMLElement>('[role=treeitem]')
  expect(rows[1]!.getAttribute('aria-setsize')).toBe('2')
  expect(rows[2]!.getAttribute('aria-current')).toBe('page')
  expect(rows[1]!.getAttribute('aria-selected')).toBe('true')
  expect(rows[2]!.getAttribute('aria-selected')).toBe('false')
  rows[0]!.focus(); key(rows[0]!, 'End'); await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:3')
  nodes.value = buildWikiTree([{ id: 3, name: 'a/renamed' }, { id: 1, name: 'a' }])
  await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:3')
  collapsed.value = new Set(['["a"]']); await nextTick(); await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:1')
  rows = mounted.host.querySelectorAll<HTMLElement>('[role=treeitem]')
  key(rows[0]!, 'Home'); await nextTick()
  expect(rows[0]!.tabIndex).toBe(0)
})

it('shows duplicate IDs with a number sign while preserving the accessible label', () => {
  const nodes = buildWikiTree([{ id: 1, name: 'a' }, { id: 2, name: 'a' }, { id: 3, name: 'a/child' }])
  mounted = renderTree(() => ({ nodes, selectedId: 1, disabled: false }))
  const ids = mounted.host.querySelectorAll('.wiki-node-id')
  expect([...ids].map(el => el.textContent)).toEqual(['#1', '#2'])
  expect(ids[0]!.getAttribute('aria-label')).toBe('同名ページ ID: 1')
  expect(mounted.host.querySelector('.wiki-node-count')?.textContent).toBe('1')
})
it.each([['É', 'é'], ['Ａ', 'ａ']])('folds ASCII only during type-ahead (%s / %s)', async (upper, lower) => {
  const nodes = buildWikiTree([{ id: 1, name: upper }, { id: 2, name: lower }, { id: 3, name: 'A' }, { id: 4, name: 'a' }])
  mounted = renderTree(() => ({ nodes, selectedId: 0, disabled: false }))
  const row = mounted.host.querySelector<HTMLElement>('[data-row-key="p:1"]')!
  row.focus()
  key(row, lower); await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:2')
  key(document.activeElement!, lower); await nextTick()
  expect((document.activeElement as HTMLElement).dataset.rowKey).toBe('p:2')
})
it('ignores keyCode 229 even when isComposing is false', async () => {
  const { host } = open()
  const row = host.querySelector<HTMLElement>('[role=treeitem]')!
  row.focus()
  row.dispatchEvent(new KeyboardEvent('keydown', { key: 'f', keyCode: 229, isComposing: false, bubbles: true }))
  await nextTick()
  expect(document.activeElement).toBe(row)
})
it('reveals through the owning whole-panel port without scrolling the tree itself', async () => {
 const owner = document.createElement('aside')
 const nodes = buildWikiTree([{ id: 1, name: 'Sample' }])
 const component = ref<InstanceType<typeof WikiTree> | null>(null)
 mounted = renderTree(() => ({ ref: component, nodes, selectedId: 1, disabled: false, scrollPort: owner }))
 const row = mounted.host.querySelector<HTMLElement>('[role=treeitem]')!, tree = mounted.host.querySelector<HTMLElement>('.wiki-tree')!
 vi.spyOn(owner, 'getBoundingClientRect').mockReturnValue({ top: 100, bottom: 300 } as DOMRect)
 vi.spyOn(row, 'getBoundingClientRect').mockReturnValue({ top: 320, bottom: 350 } as DOMRect)
 owner.scrollTop = 10; tree.scrollTop = 20
 component.value!.reveal('p:1')
 expect(owner.scrollTop).toBe(60); expect(tree.scrollTop).toBe(20)
 vi.mocked(row.getBoundingClientRect).mockReturnValue({ top: 110, bottom: 140 } as DOMRect)
 component.value!.reveal('p:1')
 expect(owner.scrollTop).toBe(60)
})

it('updates row state and translations after focus changes without rebuilding nodes', async () => {
 const nodes = buildWikiTree([{ id: 1, name: 'a' }, { id: 2, name: 'a/b' }])
 const current = ref(1), selection = ref('p:1'), outside = ref(0), disabled = ref(false), filtered = ref(false)
 mounted = renderTree(() => ({ nodes, selectedId: current.value, selectedRowKey: selection.value,
  outsideId: outside.value, disabled: disabled.value, filtered: filtered.value }))
 const row = (id: number) => mounted!.host.querySelector<HTMLElement>(`[data-row-key="p:${id}"]`)!
 row(1).focus(); key(row(1), 'ArrowDown'); await nextTick()
 expect(row(2).tabIndex).toBe(0); expect(row(1).tabIndex).toBe(-1)
 current.value = 2; selection.value = 'p:2'; outside.value = 2; disabled.value = true
 await nextTick()
 expect(row(1).hasAttribute('aria-current')).toBe(false)
 expect(row(2).getAttribute('aria-current')).toBe('page')
 expect(row(1).getAttribute('aria-selected')).toBe('false')
 expect(row(2).getAttribute('aria-selected')).toBe('true')
 expect(row(1).getAttribute('aria-disabled')).toBe('true')
 expect(row(2).getAttribute('aria-disabled')).toBe('true')
 const previousLabel = row(2).querySelector('.wiki-outside')!.textContent
 mounted.i18n.global.locale.value = 'en'; await nextTick()
 expect(row(2).querySelector('.wiki-outside')?.textContent).not.toBe(previousLabel)
 expect(row(2).querySelector('.wiki-outside')?.textContent).toBe(mounted.i18n.global.t('wiki.outsideSearch'))
 expect(row(2).querySelector('.wiki-sr-only')?.textContent).toBe(mounted.i18n.global.t('wiki.currentPage'))
 filtered.value = true; await nextTick()
 expect(row(1).querySelector('.wiki-node-count')?.getAttribute('aria-label')).toBe(mounted.i18n.global.t('wiki.filteredDescendantPages', { count: 1 }))
 outside.value = 0; disabled.value = false; await nextTick()
 expect(row(2).querySelector('.wiki-outside')).toBeNull()
 expect(row(2).getAttribute('aria-disabled')).toBe('false')
})
