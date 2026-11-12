import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown'
import { prepareMarkdownDocument } from './markdownDocument'

const renderers = [
  ['プレビュー', renderMarkdown],
  ['文書', (source: string) => prepareMarkdownDocument(source).render()],
] as const

function renderToDom(render: (source: string) => string, source: string): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = render(source)
  return host
}

function expectPlainText(host: HTMLElement) {
  expect(
    host.querySelector(
      'a, img, script, [href], [src], [srcset], [style], [data-href], [data-md-link], [data-md-wiki]',
    ),
  ).toBeNull()
  for (const node of host.querySelectorAll('*')) {
    expect(node.getAttributeNames().filter((name) => name.startsWith('on'))).toEqual([])
  }
}

for (const [name, render] of renderers) {
  describe(`${name}の参照定義の境界`, () => {
    it.each([
      ['BMP', 'あ'],
      ['非 BMP', '😀'],
    ])('999 符号位置を超える %s ラベルを文字として保持する', (_kind, character) => {
      const label = character.repeat(1000)
      const host = renderToDom(render, `[${label}]: </asset>\n\n[${label}]`)
      expectPlainText(host)
      expect(host.textContent).toContain(`[${label}]: </asset>`)
      expect(host.textContent).toContain(`[${label}]`)
    })

    it('空白のない多行タイトルを文字として保持する', () => {
      const host = renderToDom(render, '[r]: </asset>"title\nend"\n\n[r]')
      expectPlainText(host)
      expect(host.textContent).toContain('[r]: </asset>"title')
      expect(host.textContent).toContain('end"')
      expect(host.textContent).toContain('[r]')
    })

    it.each([
      ['BMP', 'あ'],
      ['非 BMP', '😀'],
    ])('999 符号位置の %s ラベルを参照リンクにする', (_kind, character) => {
      const label = character.repeat(999)
      const host = renderToDom(render, `[${label}]: </asset>\n\n[${label}]`)
      expect(host.querySelectorAll('a')).toHaveLength(1)
      expect(host.querySelector('a')?.textContent).toBe(label)
      expect(host.querySelector('[href], img')).toBeNull()
      expect(host.textContent).not.toContain(']: </asset>')
    })

    it('空白のある多行タイトルを参照リンクにする', () => {
      const host = renderToDom(render, '[r]: </asset> "title\nend"\n\n[r]')
      expect(host.querySelectorAll('a')).toHaveLength(1)
      expect(host.querySelector('a')?.textContent).toBe('r')
      expect(host.querySelector('[href], img')).toBeNull()
      expect(host.textContent).not.toContain('[r]:')
    })
  })
}
