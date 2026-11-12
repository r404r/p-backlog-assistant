// @vitest-environment jsdom
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
    expect(
      [...host.querySelectorAll('[role=checkbox]')].map((n) => n.getAttribute('aria-checked')),
    ).toEqual(['false', 'true'])
    expect(host.querySelectorAll('[aria-disabled=true]')).toHaveLength(2)
    expect(host.querySelector('input')).toBeNull()
    expect(host.querySelector('code')?.textContent).toBe('- [x] 原文')
  })
  it('複数 root の同じ見出しにも独立 ID を生成する', () => {
    const a = dom('# __proto__\n\n# __proto__\n\n空\n===')
    const b = dom('# __proto__')
    expect(a.doc.headings.map((h) => h.text)).toEqual(['proto', 'proto', '空'])
    const ids = [...a.doc.headings, ...b.doc.headings].map((h) => h.id)
    expect(new Set(ids).size).toBe(4)
    for (const n of a.host.querySelectorAll('h1')) {
      expect(n.id).toMatch(/^ba-md-\d+-h-\d+$/)
      expect(n.tabIndex).toBe(-1)
    }
  })
  it('言語を固定表で解決しコピー元をそのまま保持する', () => {
    const { doc, host } = dom('```ts\nconst a = "<img>"\n```\n\n```evil"><img\nx\n```')
    expect(doc.codeBlocks.map((c) => c.language)).toEqual(['typescript', 'text'])
    expect(doc.codeBlocks[0].source).toBe('const a = "<img>"\n')
    expect(host.querySelectorAll('button[type=button][data-md-action=copy]')).toHaveLength(2)
    expect(host.querySelector('img')).toBeNull()
  })
  it.each([
    'https://example.invalid/pixel',
    '//example.invalid/pixel',
    '/pixel',
    'data:image/png;base64,AA',
    'blob:forged',
  ])('外部画像は DOM の読込要素も属性も作らない: %s', (url) => {
    const { host } = dom(
      `![x](${url})\n\n![r][ref]\n\n[ref]: ${url}\n\n<img src="${url}" srcset="${url}">`,
    )
    expect(
      host.querySelector(
        'img, picture, source, iframe, object, embed, [src], [srcset], [poster], [style], [href]',
      ),
    ).toBeNull()
    expect(host.textContent).toContain(url)
  })
})

import type { WikiPage } from './backend'
import { enforceDocumentAttributes, syntaxClasses } from './markdownDocument'
function page(overrides: Partial<WikiPage> = {}): WikiPage {
  return {
    id: 1,
    projectId: 101,
    name: 'ガイド',
    content: '',
    created: 'T',
    updated: 'T',
    contentUpdated: 'T',
    contentFetchedAt: 'T',
    tags: [],
    createdUser: '',
    updatedUser: '',
    textFormattingRule: 'markdown',
    attachments: [
      { id: 7, wikiId: 1, name: '日本語.png', size: 20 },
      { id: 8, wikiId: 1, name: 'same.png', size: 20 },
      { id: 9, wikiId: 1, name: 'same.png', size: 20 },
    ],
    ...overrides,
  }
}
const context = {
  page: page(),
  pages: [
    page(),
    page({ id: 2, name: 'ガイド/運用' }),
    page({ id: 3, name: '重複' }),
    page({ id: 4, name: '重複' }),
    page({ id: 5, projectId: 102, name: '他project' }),
  ],
}
function withWiki(source: string) {
  return prepareMarkdownDocument(source, context)
}
function parseHTML(html: string) {
  const host = document.createElement('div')
  host.innerHTML = html
  return host
}
describe('Wiki の参照解決と閉じた許可リスト', () => {
  it('内部リンクは同projectの完全一致で一意のものだけ', () => {
    const d = withWiki('[[ガイド/運用]] [[運用]] [[ガイド/運用 ]] [[重複]] [[他project]] [[guide]]')
    const host = parseHTML(d.render())
    expect(d.wikiLinks).toEqual([2])
    expect(host.querySelectorAll('a[data-md-wiki]')).toHaveLength(1)
    expect(host.querySelector('[href]')).toBeNull()
    expect(host.textContent).toContain('[[重複]]')
    expect(host.textContent).toContain('[[ガイド/運用 ]]')
  })
  it.each([
    '`[[ガイド]]`',
    '```\n[[ガイド]]\n```',
    '\\[[ガイド]]',
    '[リンク [[ガイド]]](https://example.com/)',
  ])('コード・escape・既存リンクの中は再解釈しない: %s', (source) => {
    const d = withWiki(source)
    expect(d.wikiLinks).toEqual([])
    expect(d.imageSlots).toEqual([])
  })
  it('添付ID/名前はこのWiki内だけで完全一致し、重名や先頭0を推測しない', () => {
    const d = withWiki(
      '![image][7] #image(日本語.png) #image(same.png) #image(07) #image(https://example.com/p.png) #image(999)',
    )
    expect(d.imageSlots.map((s) => s.attachmentId)).toEqual([7, 7, 0, 0, 0, 0])
    expect(parseHTML(d.render()).querySelector('img,[src],[srcset]')).toBeNull()
    const plain = prepareMarkdownDocument('#image(7) ![image][7]')
    expect(plain.imageSlots).toEqual([])
    expect(parseHTML(plain.render()).textContent).toContain('#image(7)')
  })
  it('標準参照定義を優先し、URLを添付IDへ変換しない', () => {
    const d = withWiki('![x][7]\n\n[7]: https://example.invalid/pixel')
    expect(d.imageSlots).toHaveLength(0)
    expect(parseHTML(d.render()).querySelector('img,[src]')).toBeNull()
  })
  it('自己生成blobと寸法の完全一致だけを許可し失効した画像を残さない', () => {
    const d = withWiki('#image(7)'),
      state = {
        images: new Map([[0, d.registerImage(0, { url: 'blob:owned', width: 4, height: 3 })]]),
      }
    const html = d.render(state),
      img = parseHTML(html).querySelector('img')!
    expect(img.src).toBe('blob:owned')
    expect(img.width).toBe(4)
    expect(img.height).toBe(3)
    for (const url of [
      'https://example.invalid/pixel',
      '/pixel',
      'data:image/png;base64,AA',
      'blob:forged',
    ]) {
      expect(
        parseHTML(d.sanitize(html.replace('blob:owned', url), state)).querySelector('img,[src]'),
      ).toBeNull()
    }
    expect(parseHTML(d.sanitize(html)).querySelector('img')).toBeNull()
    expect(parseHTML(withWiki('#image(8)').sanitize(html, state)).querySelector('img')).toBeNull()
    expect(
      parseHTML(
        d.sanitize(
          html.replace(
            '<img ',
            '<img onerror="alert(1)" srcset="https://example.invalid" style="color:red" ',
          ),
          state,
        ),
      ).querySelector('[onerror],[srcset],[style]'),
    ).toBeNull()
  })
  for (const cl of syntaxClasses) {
    it(`${cl} はcode配下の固定カテゴリだけ`, () => {
      const d = prepareMarkdownDocument('```js\nx\n```')
      const host = parseHTML(
        d.sanitize(
          `<pre><code><span class="${cl}" style="color:red" onclick="1">x</span><span class="${cl}-x">y</span></code></pre><p><span class="${cl}">z</span></p>`,
        ),
      )
      expect(host.querySelectorAll(`.${cl}`)).toHaveLength(1)
      expect(host.querySelector('[style],[onclick]')).toBeNull()
      expect(host.querySelector('p span')?.className).toBe('')
      expect(host.querySelectorAll('[class]')).toHaveLength(1)
    })
  }
  it.each(['true', 'false'])('読み取りタスクは list 内の状態 %s のみ', (value) => {
    const d = prepareMarkdownDocument(`- [${value === 'true' ? 'x' : ' '}] task`)
    const html = d.render()
    expect(parseHTML(html).querySelector('[role=checkbox]')?.getAttribute('aria-checked')).toBe(
      value,
    )
    for (const [from, to] of [
      ['aria-disabled="true"', 'aria-disabled="false"'],
      ['role="checkbox"', 'role="button"'],
      [`aria-checked="${value}"`, 'aria-checked="mixed"'],
    ]) {
      expect(
        parseHTML(d.sanitize(html.replace(from, to))).querySelector(
          '[role],[aria-checked],[aria-disabled]',
        ),
      ).toBeNull()
    }
    expect(
      parseHTML(d.sanitize(html.replace('<li>', '<p>').replace('</li>', '</p>'))).querySelector(
        '[role]',
      ),
    ).toBeNull()
  })
  it.each([1, 2, 3, 4, 5, 6])('h%s の生成ID以外と他rootIDを落とす', (level) => {
    const d = prepareMarkdownDocument(`${'#'.repeat(level)} header`),
      id = d.headings[0].id
    const html = `<h${level} id="${id}" tabindex="-1" name="location">header</h${level}>`
    expect(parseHTML(d.sanitize(html)).querySelector('[id]')?.id).toBe(id)
    expect(parseHTML(d.sanitize(html)).querySelector('[name]')).toBeNull()
    expect(
      parseHTML(d.sanitize(html.replace(id, 'constructor'))).querySelector('[id],[tabindex]'),
    ).toBeNull()
    expect(
      parseHTML(prepareMarkdownDocument('# other').sanitize(html)).querySelector('[id],[tabindex]'),
    ).toBeNull()
  })
  it('コピーとリンクのmanifest通番/固定属性/要素対応を検査する', () => {
    const d = withWiki('```ts\nx\n```\n\n[[ガイド]]')
    const html = d.render()
    expect(parseHTML(html).querySelector('button')?.getAttribute('type')).toBe('button')
    for (const n of ['-1', '01', '999999', 'javascript:1']) {
      expect(
        parseHTML(
          d.sanitize(html.replace('data-md-code="0"', `data-md-code="${n}"`)),
        ).querySelector('[data-md-code]'),
      ).toBeNull()
      expect(
        parseHTML(
          d.sanitize(html.replace('data-md-wiki="0"', `data-md-wiki="${n}"`)),
        ).querySelector('[data-md-wiki]'),
      ).toBeNull()
    }
    for (const tag of ['p', 'span', 'code', 'img', 'a']) {
      const el = document.createElement(tag)
      for (const [k, v] of Object.entries({
        'data-md-code': '0',
        'data-md-action': 'copy',
        type: 'button',
        src: 'blob:owned',
        alt: 'x',
        width: '4',
        height: '3',
      }))
        el.setAttribute(k, v)
      enforceDocumentAttributes(el, d)
      expect(el.getAttributeNames()).toEqual([])
    }
    expect(
      parseHTML(d.sanitize(html.replace('type="button"', 'type="submit"'))).querySelector(
        '[data-md-code]',
      ),
    ).toBeNull()
    expect(
      parseHTML(
        d.sanitize(
          html.replace('data-md-wiki="0"', 'data-md-wiki="0" data-href="https://example.com/"'),
        ),
      ).querySelector('[data-md-wiki],[data-href]'),
    ).toBeNull()
  })
  it('高亮結果も再sanitizeし、コード中のHTMLを実行しない', () => {
    const d = prepareMarkdownDocument('```js\n<script>alert(1)</script>\n```')
    const host = parseHTML(
      d.render({
        highlights: new Map([
          [
            0,
            '<span class="md-syntax-keyword">const</span><img src="https://example.invalid"><script>alert(1)</script><input><svg onload="1"></svg>',
          ],
        ]),
      }),
    )
    expect(host.querySelector('.md-syntax-keyword')?.textContent).toBe('const')
    expect(host.querySelector('img,script,input,svg,[src],[onload]')).toBeNull()
    const raw = parseHTML(d.render())
    expect(raw.textContent).toContain('<script>alert(1)</script>')
    expect(raw.querySelector('script')).toBeNull()
  })
})

it('表の横scrollだけをTabで操作可能にし、属性を他要素へ広げない',()=>{
 const doc=prepareMarkdownDocument('A | B\n--- | ---\n1 | 2')
 const host=parseHTML(doc.render())
 expect(host.querySelector('table')?.tabIndex).toBe(0)
 expect(host.querySelector('table')?.getAttribute('aria-label')).toBeTruthy()
 const wrong=parseHTML(doc.sanitize('<p tabindex="0" aria-label="fake">x</p><table tabindex="1" aria-label="fake"></table>'))
 expect(wrong.querySelector('[tabindex],[aria-label]')).toBeNull()
})

it('無害な生HTMLも文字として残す',()=>{
 const {host}=dom('<em>raw</em> <strong>raw</strong>')
 expect(host.querySelector('em,strong')).toBeNull()
 expect(host.textContent).toBe('<em>raw</em> <strong>raw</strong>')
})
it('外部imageのURLとaltを消さず文字として保持する',()=>{
 const {host}=dom('![説明](https://example.invalid/pixel)')
 expect(host.textContent).toContain('https://example.invalid/pixel')
 expect(host.textContent).toContain('説明')
 expect(host.querySelector('img,[src]')).toBeNull()
})
