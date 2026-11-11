import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { renderMarkdown } from './markdown'
import { wikiMarkdownFixture } from '../../test/fixtures/wikiMarkdownFixture'

function render(source: string): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = renderMarkdown(source)
  return host
}

describe('Wiki の Markdown 互換', () => {
  it('旧テンプレートの区切り列不足を表示時だけ補う', () => {
    const host = render(wikiMarkdownFixture)
    expect(Array.from(host.querySelectorAll('th'), (el) => el.textContent)).toEqual([
      '指標', '現在値', '目標値',
    ])
    expect(Array.from(host.querySelectorAll('td'), (el) => el.textContent)).toEqual([
      'サンプル数', '2', '4',
    ])
  })

  it('単一改行を br にし、CRLF・CR と LF の結果を一致させる', () => {
    const host = render('最初の行\n次の行')
    expect(host.querySelectorAll('p > br')).toHaveLength(1)
    const lf = renderMarkdown(wikiMarkdownFixture)
    expect(renderMarkdown(wikiMarkdownFixture.replaceAll('\n', '\r\n'))).toBe(lf)
    expect(renderMarkdown(wikiMarkdownFixture.replaceAll('\n', '\r'))).toBe(lf)
  })

  it('loose list と連続空行を空段落・余分な br に変えない', () => {
    const host = render(wikiMarkdownFixture)
    expect(host.querySelectorAll('li > p')).toHaveLength(2)
    expect(host.querySelectorAll('ul ul li')).toHaveLength(1)
    expect(Array.from(host.querySelectorAll('p')).filter((p) => !p.textContent?.trim())).toHaveLength(0)
    expect(host.querySelectorAll('br')).toHaveLength(3)
    expect(host.querySelectorAll('h1, h2, h3')).toHaveLength(4)
    expect(host.querySelector('blockquote')?.textContent).toContain('架空の引用')
    expect(host.querySelector('hr')).not.toBeNull()
    expect(host.querySelector('s')?.textContent).toBe('旧案')
  })

  it.each([
    '| A | B | C |\n| --- | --- |\n| 1 | 2 | 3 |',
    'A \\| B | C | D\n--- | ---\n1 | 2 | 3',
    '> A | B | C\n> --- | ---\n> 1 | 2 | 3',
    '- A | B | C\n  --- | ---\n  1 | 2 | 3',
  ])('区切り不足の表を囲み・エスケープ・入れ子でも扱う: %s', (source) => {
    const host = render(source)
    expect(host.querySelectorAll('th')).toHaveLength(3)
    expect(host.querySelectorAll('td')).toHaveLength(3)
  })

  it.each([
    'A | B | C\n--- | ---\n1 | 2',
    'A | B | C\n--- | text\n1 | 2 | 3',
    'A | B\n--- | --- | ---\n1 | 2',
    'A | B\n---\n1 | 2',
    '`A | B | C\n--- | ---\n1 | 2 | 3`',
    '`例\nA | B | C\n--- | ---\n1 | 2 | 3\n終わり`',
    '```text\nA | B | C\n--- | ---\n1 | 2 | 3\n```',
    '    A | B | C\n    --- | ---\n    1 | 2 | 3',
  ])('曖昧な文章やコードを互換表に変えない: %s', (source) => {
    expect(render(source).querySelector('table')).toBeNull()
  })

  it('標準表・コード・後続本文を壊さず、呼び出し間で補正状態を残さない', () => {
    const code = render(wikiMarkdownFixture).querySelector('pre code')!
    expect(code.textContent).toBe('const sample = "<example>"\n\nconsole.log(sample)\n')
    expect(code.querySelector('*')).toBeNull()
    const host = render('A | B\n:--- | ---:\n1 | 2\n\n後続\n次行')
    expect(host.querySelectorAll('th')).toHaveLength(2)
    expect(host.querySelector('p')?.textContent).toBe('後続\n次行')
    expect(host.querySelectorAll('p > br')).toHaveLength(1)
  })

  it('Backlog 添付記法とタスクリストは原文を保持し、画像や入力要素を生成しない', () => {
    const host = render(wikiMarkdownFixture)
    for (const source of ['![image][sample.png]', '#image(sample.png)', '#attach(sample.zip:11)', '[ ] 確認予定', '[x] 確認済み']) {
      expect(host.textContent).toContain(source)
    }
    expect(host.querySelector('img, input, [src], [href]')).toBeNull()
    const ref = render('![image][sample]\n\n[sample]: https://example.com/sample.png')
    expect(ref.textContent).toContain('![image](https://example.com/sample.png)')
    expect(ref.querySelector('img')).toBeNull()
  })

  it('互換表のセルでも XSS・危険な属性を排除する', () => {
    const host = render('A | B | C\n--- | ---\n<script>alert(1)</script> | ![x](https://example.com/a.png) | [x](javascript:alert(1))\n\n<span style="color:red">x</span>')
    expect(host.querySelectorAll('td')).toHaveLength(3)
    expect(host.querySelector('script, img, input, svg, math, iframe, [style], [href], [src], [onerror], [data-href]')).toBeNull()
  })
})

it('Wiki の pre-wrap は原文に限定し、整形表示は共通スタイルを使う', () => {
  const style = document.createElement('style')
  style.textContent = readFileSync(resolve('src/markdown.css'), 'utf8')
  const host = document.createElement('div')
  host.className = 'markdown-body'
  host.innerHTML = renderMarkdown(wikiMarkdownFixture)
  document.head.append(style)
  document.body.append(host)
  try {
    expect(getComputedStyle(host).whiteSpace).toBe('normal')
    expect(getComputedStyle(host.querySelector('pre')!).whiteSpace).toBe('pre')
    expect(getComputedStyle(host.querySelector('li > p')!).marginTop).toBe('0px')
    expect(getComputedStyle(host.querySelector('table')!).overflowX).toBe('auto')
    expect(getComputedStyle(host.querySelector('h1')!).fontSize).not.toBe(getComputedStyle(host.querySelector('h2')!).fontSize)
  } finally {
    host.remove()
    style.remove()
  }
})
