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
