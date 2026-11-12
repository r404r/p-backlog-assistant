import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { buildWikiTree } from '../lib/wikiTree'
import WikiTree from './WikiTree.vue'
let mounted: MountedApp | undefined
afterEach(() => mounted?.unmount())
function open(disabled = false) {
  const nodes = buildWikiTree([{ id: 1, name: 'a' }, { id: 2, name: 'a/b' }, { id: 3, name: 'folder/c' }])
  const collapsed = ref(new Set<string>())
  const selected = ref('p:1')
  const select = vi.fn((_path: string, id: number) => { selected.value = `p:${id}` })
  mounted = mountWithI18n(defineComponent({ setup: () => () => h(WikiTree, {
    nodes, selectedId: 1, disabled, collapsed: collapsed.value, selectedRowKey: selected.value,
    onSelect: select,
    onToggle: (key: string) => {
      const next = new Set(collapsed.value)
      if (next.has(key)) next.delete(key); else next.add(key)
      collapsed.value = next
    },
  }) }))
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
