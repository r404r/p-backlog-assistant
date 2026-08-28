/**
 * Markdown レンダラ(lib/markdown.ts)の検証。
 *
 * 課題詳細・コメントの本文は **Backlog から届くリモート由来の文字列** であり、
 * これを HTML として描画する以上、XSS はこの機能で最も重い故障になる。
 * そのためテストは 2 本立てにする:
 *
 *  1. **構文適合**: 対応すると決めた構文(CommonMark + table + strikethrough +
 *     linkify)が期待どおり変換され、**対象外と決めた構文**(タスクリスト・絵文字等)が
 *     勝手に増えていないこと。
 *  2. **欠陥注入**: `<script>` / `<img onerror>` / `javascript:` / `data:` /
 *     イベント属性 / `<iframe>` / SVG・MathML の 7 種がすべて無害化されること、
 *     無害な生 HTML が**タグではなく文字列**として出ること(html:true 化の検出)、
 *     出力に `<img>` が 1 つも無いこと、`data-href` が http/https に限られること。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  DETAIL_MARKDOWN_KEY,
  enforceAttributeAllowList,
  isMarkdownRule,
  loadDetailMarkdown,
  markdownLinkHref,
  renderMarkdown,
  saveDetailMarkdown,
} from './markdown'

/** 変換結果を DOM へ入れて検査する(実際の v-html と同じ状態で確かめる) */
function renderToDom(source: string): HTMLElement {
  const host = document.createElement('div')
  host.innerHTML = renderMarkdown(source)
  return host
}

// ---------------------------------------------------------------------------
// 記法設定の判定
// ---------------------------------------------------------------------------

describe('記法設定の判定', () => {
  it('markdown のときだけ整形表示の対象になる', () => {
    expect(isMarkdownRule('markdown')).toBe(true)
    // Backlog 記法・判定不能(空文字)は従来のプレーン表示のまま
    expect(isMarkdownRule('backlog')).toBe(false)
    expect(isMarkdownRule('')).toBe(false)
    expect(isMarkdownRule('Markdown')).toBe(false)
    expect(isMarkdownRule(undefined)).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// 構文適合(対応すると決めた構文 / 対象外と決めた構文)
// ---------------------------------------------------------------------------

describe('対応構文の変換', () => {
  it('見出しを変換する', () => {
    expect(renderMarkdown('# 見出し')).toContain('<h1>見出し</h1>')
    expect(renderMarkdown('### 小見出し')).toContain('<h3>小見出し</h3>')
  })

  it('箇条書き・番号付きリストを変換する', () => {
    const ul = renderMarkdown('- 一つ目\n- 二つ目')
    expect(ul).toContain('<ul>')
    expect(ul).toContain('<li>一つ目</li>')
    const ol = renderMarkdown('1. 一つ目\n2. 二つ目')
    expect(ol).toContain('<ol>')
  })

  it('強調・打ち消し線を変換する', () => {
    expect(renderMarkdown('**強い**')).toContain('<strong>強い</strong>')
    expect(renderMarkdown('*弱い*')).toContain('<em>弱い</em>')
    expect(renderMarkdown('~~取り消し~~')).toContain('<s>取り消し</s>')
  })

  it('引用・水平線を変換する', () => {
    expect(renderMarkdown('> 引用')).toContain('<blockquote>')
    expect(renderMarkdown('---')).toContain('<hr>')
  })

  it('表を変換する', () => {
    const html = renderMarkdown('| 見出し |\n| --- |\n| 値 |')
    expect(html).toContain('<table>')
    expect(html).toContain('<th>見出し</th>')
    expect(html).toContain('<td>値</td>')
  })

  it('コードブロックはエスケープしたまま表示する(ハイライトはしない)', () => {
    const html = renderMarkdown('```js\nconst a = 1 < 2\n```')
    expect(html).toContain('<pre>')
    expect(html).toContain('<code class="language-js">')
    // 中身は文字列として出る(タグとして解釈されない)
    expect(html).toContain('1 &lt; 2')
  })

  it('インラインコードを変換する', () => {
    expect(renderMarkdown('`code`')).toContain('<code>code</code>')
  })

  it('linkify で裸の URL をリンクにする', () => {
    const anchor = renderToDom('https://example.com/a のページ').querySelector('a')
    expect(anchor).not.toBeNull()
    expect(anchor?.getAttribute('data-href')).toBe('https://example.com/a')
    expect(anchor?.textContent).toBe('https://example.com/a')
  })
})

describe('対象外と決めた構文(勝手に増えていない)', () => {
  it('タスクリストはチェックボックスにならない', () => {
    const host = renderToDom('- [ ] やること\n- [x] やったこと')
    expect(host.querySelector('input')).toBeNull()
    expect(host.textContent).toContain('[ ] やること')
  })

  it('絵文字ショートコードは文字のまま残る', () => {
    expect(renderToDom(':smile:').textContent).toContain(':smile:')
  })

  it('脚注は生成されない(CommonMark の参照リンクとして扱われるだけ)', () => {
    const host = renderToDom('本文[^1]\n\n[^1]: 注釈')
    // 脚注プラグインが入ると <sup class="footnote-ref"> と脚注一覧が出る
    expect(host.querySelector('sup')).toBeNull()
    expect(host.querySelector('.footnotes')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// 欠陥注入(設計 §3.4)
// ---------------------------------------------------------------------------

describe('欠陥注入(すべて無害化する)', () => {
  const cases: { name: string; source: string }[] = [
    { name: 'script タグ', source: '<script>window.__xss = 1</script>' },
    { name: 'img の onerror', source: '<img src=x onerror="window.__xss = 1">' },
    { name: 'javascript: リンク', source: '[危険](javascript:window.__xss=1)' },
    { name: 'data: URL リンク', source: '[危険](data:text/html;base64,PHNjcmlwdD4x)' },
    { name: 'イベント属性', source: '<div onclick="window.__xss = 1">押す</div>' },
    { name: 'iframe', source: '<iframe src="https://example.com"></iframe>' },
    { name: 'SVG の script', source: '<svg><script>window.__xss = 1</script></svg>' },
    { name: 'MathML', source: '<math><mtext><script>window.__xss = 1</script></mtext></math>' },
    { name: 'style 属性', source: '<span style="position:fixed">重ねる</span>' },
    { name: 'form 要素', source: '<form action="https://example.com"><input name="a"></form>' },
  ]

  for (const c of cases) {
    it(`${c.name} を無害化する`, () => {
      const html = renderMarkdown(c.source)
      const host = renderToDom(c.source)
      // 危険な要素・属性が 1 つも DOM に現れない
      expect(host.querySelector('script, img, iframe, svg, math, form, input, style')).toBeNull()
      for (const attr of ['onerror', 'onclick', 'onload', 'style', 'href', 'src']) {
        expect(
          host.querySelector(`[${attr}]`),
          `${c.name}: ${attr} 属性が残っています(${html})`,
        ).toBeNull()
      }
      // 生のタグとしては出さない(エスケープされた文字列として残るのは正しい挙動)
      expect(html.toLowerCase()).not.toContain('<script')
      // 危険なスキームが「属性値」として残っていない(本文の文字として残るのは可)
      for (const el of Array.from(host.querySelectorAll('*'))) {
        for (const name of el.getAttributeNames()) {
          expect(el.getAttribute(name) ?? '', `${c.name}: ${name}`).not.toMatch(
            /^\s*(javascript|data|vbscript):/i,
          )
        }
      }
    })
  }

  it('無害な生 HTML はタグではなく文字列として表示する(html:true 化の検出)', () => {
    const host = renderToDom('<b>太字</b> と <em>斜体</em>')
    expect(host.querySelector('b')).toBeNull()
    // markdown-it の em(*…*)と混同しないよう、生 HTML の em が消えていることまで見る
    expect(host.querySelector('em')).toBeNull()
    expect(host.textContent).toContain('<b>太字</b>')
    expect(host.textContent).toContain('<em>斜体</em>')
  })

  it('コードブロックの言語名に細工しても class は language- 接頭辞に限られる', () => {
    const host = renderToDom('```js"><b>x\nconst a = 1\n```')
    expect(host.querySelector('b')).toBeNull()
    for (const el of Array.from(host.querySelectorAll('[class]'))) {
      expect(el.getAttribute('class')).toMatch(/^language-[\w.+-]+$/)
    }
  })

  it('class を持てるのは code だけ・data-href を持てるのは a だけ', () => {
    // 許可リストは「属性名」だけでなく「どの要素に付いてよいか」まで絞る決まり
    // (設計 §3.2)。markdown-it は code 以外に language-* を出さないため、
    // 変換の出力からはこの規律を検証できない。サニタイズの最終段(DOMPurify の
    // フック)を直接呼び、要素との対応が効いていることを確かめる。
    const attributesAfter = (tag: string, name: string, value: string): string | null => {
      const el = document.createElement(tag)
      el.setAttribute(name, value)
      enforceAttributeAllowList(el)
      return el.getAttribute(name)
    }

    expect(attributesAfter('code', 'class', 'language-js')).toBe('language-js')
    for (const tag of ['span', 'pre', 'p', 'a', 'td']) {
      expect(attributesAfter(tag, 'class', 'language-js'), tag).toBeNull()
    }

    expect(attributesAfter('a', 'data-href', 'https://example.com/')).toBe('https://example.com/')
    expect(attributesAfter('a', 'role', 'link')).toBeNull()
    expect(attributesAfter('a', 'tabindex', '0')).toBeNull()
    for (const tag of ['span', 'code', 'p']) {
      expect(attributesAfter(tag, 'data-href', 'https://example.com/'), tag).toBeNull()
    }

    const anchor = document.createElement('a')
    anchor.setAttribute('data-href', 'https://example.com/')
    anchor.setAttribute('role', 'link')
    anchor.setAttribute('tabindex', '0')
    enforceAttributeAllowList(anchor)
    expect(anchor.getAttribute('role')).toBe('link')
    expect(anchor.getAttribute('tabindex')).toBe('0')

    const forged = document.createElement('a')
    forged.setAttribute('data-href', 'javascript:alert(1)')
    forged.setAttribute('role', 'link')
    forged.setAttribute('tabindex', '0')
    enforceAttributeAllowList(forged)
    expect(forged.getAttribute('data-href')).toBeNull()
    expect(forged.getAttribute('role')).toBeNull()
    expect(forged.getAttribute('tabindex')).toBeNull()
  })
})

describe('画像(<img> を一切生成しない)', () => {
  it('画像は URL のプレースホルダ文字列になる', () => {
    const host = renderToDom('![説明](https://example.com/a.png)')
    expect(host.querySelector('img')).toBeNull()
    expect(renderMarkdown('![説明](https://example.com/a.png)').toLowerCase()).not.toContain('<img')
    // 画像があったことと、その URL は読み取れる形で残す
    expect(host.textContent).toContain('https://example.com/a.png')
    expect(host.textContent).toContain('説明')
  })

  it('画像 URL に細工があってもタグにならない', () => {
    const host = renderToDom('![" onerror="window.__xss=1](x"><img src=y)')
    expect(host.querySelector('img')).toBeNull()
    expect(host.querySelector('[onerror]')).toBeNull()
  })

  it('リンクの中の画像も <img> にならない', () => {
    const host = renderToDom('[![説明](https://example.com/a.png)](https://example.com/)')
    expect(host.querySelector('img')).toBeNull()
    expect(host.querySelector('a')?.getAttribute('data-href')).toBe('https://example.com/')
  })
})

describe('リンク(href を出さず data-href に検証済み URL だけを入れる)', () => {
  it('http / https のリンクだけ data-href を持つ', () => {
    for (const url of ['https://example.com/a', 'http://example.com/b']) {
      const anchor = renderToDom(`[表示](${url})`).querySelector('a')
      expect(anchor?.getAttribute('data-href')).toBe(url)
      // href は出力しない(中クリック・修飾キー操作でも遷移しない)
      expect(anchor?.hasAttribute('href')).toBe(false)
      // キーボード操作は href ではなく role + tabindex + keydown ハンドラで提供する
      expect(anchor?.getAttribute('role')).toBe('link')
      expect(anchor?.getAttribute('tabindex')).toBe('0')
    }
  })

  it('http / https 以外は data-href を持たない', () => {
    const sources = [
      '[危険](javascript:alert(1))',
      '[危険](data:text/html,PHNjcmlwdD4x)',
      '[危険](vbscript:msgbox)',
      '[危険](file:///etc/passwd)',
      '[危険](./relative/path)',
      '[危険](mailto:someone@example.com)',
    ]
    for (const source of sources) {
      const host = renderToDom(source)
      expect(host.querySelector('[data-href]'), source).toBeNull()
      expect(host.querySelector('[href]'), source).toBeNull()
      expect(host.querySelector('[role]'), source).toBeNull()
      expect(host.querySelector('[tabindex]'), source).toBeNull()
      // リンクにならなくても本文の文字は失わない
      expect(host.textContent, source).toContain('危険')
    }
  })

  it('タイトル付きリンクでも余分な属性を出さない', () => {
    const anchor = renderToDom('[表示](https://example.com/ "説明")').querySelector('a')
    expect(anchor?.getAttribute('data-href')).toBe('https://example.com/')
    expect(anchor?.getAttributeNames().sort()).toEqual(['data-href', 'role', 'tabindex'])
  })
})

describe('markdownLinkHref(クリック位置から開く URL を取り出す)', () => {
  it('a[data-href] の中の要素からでも URL を取り出す', () => {
    const host = renderToDom('[**強調リンク**](https://example.com/a)')
    const inner = host.querySelector('strong')
    expect(inner).not.toBeNull()
    expect(markdownLinkHref(inner)).toBe('https://example.com/a')
  })

  it('リンク以外・data-href なしは空文字を返す', () => {
    const host = renderToDom('本文だけ')
    expect(markdownLinkHref(host.querySelector('p'))).toBe('')
    expect(markdownLinkHref(null)).toBe('')

    // DOM を直接細工された場合でも http/https 以外は開かない
    const forged = document.createElement('a')
    forged.setAttribute('data-href', 'javascript:alert(1)')
    expect(markdownLinkHref(forged)).toBe('')
  })
})

// ---------------------------------------------------------------------------
// 表示切替の保存(sidebarWidth と同じ流儀)
// ---------------------------------------------------------------------------

describe('整形表示 / 原文の選択の保存', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('既定は整形表示', () => {
    expect(loadDetailMarkdown()).toBe(true)
  })

  it('保存した選択を復元する', () => {
    saveDetailMarkdown(false)
    expect(localStorage.getItem(DETAIL_MARKDOWN_KEY)).toBe('false')
    expect(loadDetailMarkdown()).toBe(false)
    saveDetailMarkdown(true)
    expect(loadDetailMarkdown()).toBe(true)
  })

  it('壊れた値は既定(整形表示)へ倒す', () => {
    localStorage.setItem(DETAIL_MARKDOWN_KEY, 'ぐちゃぐちゃ')
    expect(loadDetailMarkdown()).toBe(true)
  })
})
