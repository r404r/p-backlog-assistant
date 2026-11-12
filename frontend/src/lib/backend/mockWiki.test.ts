import { describe, expect, it } from 'vitest'
import { createMockBackend } from './mock'

// 実 API を呼ばず、閲覧画面で Markdown と選択状態を確認するための契約。
describe('合成 Wiki の閲覧', () => {
  it('階層・Markdown 本文・取得時刻を返し、返却値の変更を隔離する', async () => {
    const backend = createMockBackend()
    const query = { projectId: 101, keyword: '', keywordMode: 'and' as const }
    const list = await backend.listWikis('preview', query)
    expect(list.pages.length).toBeGreaterThan(1)
    expect(list.textFormattingRule).toBe('markdown')
    expect(list.pages.some((page) => page.name.includes('/'))).toBe(true)
    const page = await backend.refreshWiki('preview', 101, list.pages[0].id)
    expect(page.content).toContain('# ')
    expect(page.content).toContain('> ')
    expect(page.content).toContain('```')
    expect(page.content).toContain('|')
    expect(page.contentFetchedAt).not.toBe('')
    page.tags.push('変更')
    page.content = '変更'
    expect((await backend.listWikis('preview', query)).pages[0].content).not.toBe('変更')
    expect((await backend.refreshWiki('preview', 101, list.pages[0].id)).tags).not.toContain('変更')
  })
  it('検索語を AND / OR で絞り込み、存在しないページを拒否する', async () => {
    const backend = createMockBackend()
    const base = { projectId: 101, keyword: 'ガイド 存在しない語', keywordMode: 'and' as const }
    expect((await backend.listWikis('preview', base)).pages).toHaveLength(0)
    expect((await backend.listWikis('preview', { ...base, keywordMode: 'or' })).pages.length).toBeGreaterThan(0)
    await expect(backend.refreshWiki('preview', 101, -1)).rejects.toThrow()
    await expect(backend.refreshWiki('preview', 102, 1)).rejects.toThrow()
    expect((await backend.listWikis('preview', { ...base, projectId: 999 })).pages).toHaveLength(0)
  })
})
