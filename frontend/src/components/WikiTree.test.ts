import { afterEach, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { buildWikiTree } from '../lib/wikiTree'
import WikiTree from './WikiTree.vue'

let mounted: MountedApp | undefined

afterEach(() => mounted?.unmount())

it('renders and selects descendants when their ancestors are also pages', async () => {
  const nodes = buildWikiTree([
    { id: 1, name: 'a' },
    { id: 2, name: 'a/b' },
    { id: 3, name: 'a/b/c' },
  ])
  const selectedId = ref(1)
  const select = vi.fn((_path: string, id: number) => {
    selectedId.value = id
  })
  mounted = mountWithI18n(defineComponent({
    setup: () => () => h(WikiTree, {
      nodes,
      selectedId: selectedId.value,
      disabled: false,
      onSelect: select,
    }),
  }))

  const buttons = Array.from(mounted.host.querySelectorAll('button'))
  expect(buttons.map(button => button.textContent?.trim())).toEqual(['a', 'b', 'c'])
  buttons[1]!.click()
  await nextTick()
  expect(select).toHaveBeenCalledExactlyOnceWith('a/b', 2)
  expect(buttons[1]!.getAttribute('aria-pressed')).toBe('true')
  expect(buttons[0]!.getAttribute('aria-pressed')).toBe('false')
})
