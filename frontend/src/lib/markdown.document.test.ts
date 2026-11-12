import { describe, expect, it } from 'vitest'
import { prepareMarkdownDocument } from './markdownDocument'

function dom(source: string) {
  const doc = prepareMarkdownDocument(source)
  const host = document.createElement('div')
  host.innerHTML = doc.render()
  return { doc, host }
}
describe('拡張 Markdown の安全な描画', () => {
  it('CJK 句読点と読み取り専用タスクを解釈し、コードは変更しない', () => {
    const { host } = dom('「**強調。**」\n\n- [ ] 予定\n- [X] 完了\n\n`- [x] 原文`')
    expect(host.querySelector('strong')?.textContent).toBe('強調。')
    expect([...host.querySelectorAll('[role=checkbox]')].map(n => n.getAttribute('aria-checked'))).toEqual(['false', 'true'])
    expect(host.querySelectorAll('[aria-disabled=true]')).toHaveLength(2)
    expect(host.querySelector('input')).toBeNull()
    expect(host.querySelector('code')?.textContent).toBe('- [x] 原文')
  })
  it('複数 root の同じ見出しにも独立 ID を生成する', () => {
    const a = dom('# __proto__\n\n# __proto__\n\n空\n===')
    const b = dom('# __proto__')
    expect(a.doc.headings.map(h => h.text)).toEqual(['__proto__', '__proto__', '空'])
    const ids = [...a.doc.headings, ...b.doc.headings].map(h => h.id)
    expect(new Set(ids).size).toBe(4)
    for (const n of a.host.querySelectorAll('h1')) {
      expect(n.id).toMatch(/^ba-md-\d+-h-\d+$/)
      expect(n.tabIndex).toBe(-1)
    }
  })
  it('言語を固定表で解決しコピー元をそのまま保持する', () => {
    const { doc, host } = dom('```ts\nconst a = "<img>"\n```\n\n```evil"><img\nx\n```')
    expect(doc.codeBlocks.map(c => c.language)).toEqual(['typescript', 'text'])
    expect(doc.codeBlocks[0].source).toBe('const a = "<img>"\n')
    expect(host.querySelectorAll('button[type=button][data-md-action=copy]')).toHaveLength(2)
    expect(host.querySelector('img')).toBeNull()
  })
  it.each(['https://example.invalid/pixel', '//example.invalid/pixel', '/pixel', 'data:image/png;base64,AA', 'blob:forged'])('外部画像は DOM の読込要素も属性も作らない: %s', url => {
    const { host } = dom(`![x](${url})\n\n![r][ref]\n\n[ref]: ${url}\n\n<img src="${url}" srcset="${url}">`)
    expect(host.querySelector('img, picture, source, iframe, object, embed, [src], [srcset], [poster], [style], [href]')).toBeNull()
    expect(host.textContent).toContain(url)
  })
})
