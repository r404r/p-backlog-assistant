import { expect, it } from 'vitest'
import { buildWikiTree } from './wikiTree'
it('keeps folder pages, virtual folders and duplicate names without merging case variants', () => {
  const tree = buildWikiTree([
    { id: 4, name: 'a/b' },
    { id: 1, name: 'a' },
    { id: 3, name: 'a/b' },
    { id: 5, name: 'A/c' },
    { id: 6, name: '../x' },
  ])
  const a = tree.find((n) => n.label === 'a')!
  expect(a.pageIds).toEqual([1])
  expect(a.children[0]?.pageIds).toEqual([3, 4])
  expect(a.children[0]?.path).toBe('a/b')
  expect(tree.find((n) => n.label === 'A')?.children[0]?.path).toBe('A/c')
  expect(tree.find((n) => n.label === '..')?.children[0]?.path).toBe('../x')
})
it('orders by Unicode code points, mixes folders and pages, and counts all descendants without self', () => {
  const names = ['ｶ', 'Ａ', '漢', 'カ', 'ア', 'あ', 'a', '_', 'B', 'A/子', 'A', '2', '10', '-', '!']
  const tree = buildWikiTree(names.map((name, i) => ({ id: i + 1, name })))
  expect(tree.map(n => n.label)).toEqual(['!', '-', '10', '2', 'A', 'B', '_', 'a', 'あ', 'ア', 'カ', '漢', 'Ａ', 'ｶ'])
  expect(tree.find(n => n.label === 'A')).toMatchObject({ key: '["A"]', descendantPageCount: 1 })
})
it('keeps empty-component compatibility, stable keys and duplicate IDs exactly once', () => {
  const pages = [{ id: 9, name: '/Guide//Start/' }, { id: 3, name: 'Guide' }, { id: 9, name: '/Guide//Start/' }, { id: 10, name: 'Guide/Start' }]
  const tree = buildWikiTree(pages)
  expect(tree[0]).toMatchObject({ path: 'Guide', key: '["Guide"]', descendantPageCount: 2 })
  expect(tree[0]!.children[0]!.pageIds).toEqual([9, 10])
  expect(buildWikiTree([...pages].reverse())).toEqual(tree)
})
it('compares supplementary code points without UTF-16 or normalization shortcuts', async () => {
  const { compareWikiNames } = await import('./wikiTree')
  expect(compareWikiNames('\ue000', '\u{10000}')).toBeLessThan(0)
  expect(compareWikiNames('e\u0301', 'é')).toBeLessThan(0)
  expect(compareWikiNames('a', 'a')).toBe(0)
  expect(compareWikiNames('a', 'aa')).toBeLessThan(0)
})
it('reveals only the owning scrollport by the clipped distance', async () => {
  const { revealWikiRow } = await import('./wikiTree')
  const port = document.createElement('div'), row = document.createElement('div')
  port.scrollTop = 100
  port.getBoundingClientRect = () => ({ top: 50, bottom: 150 }) as DOMRect
  row.getBoundingClientRect = () => ({ top: 140, bottom: 170 }) as DOMRect
  revealWikiRow(port, row)
  expect(port.scrollTop).toBe(120)
  row.getBoundingClientRect = () => ({ top: 60, bottom: 80 }) as DOMRect
  revealWikiRow(port, row)
  expect(port.scrollTop).toBe(120)
})
