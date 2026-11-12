/**
 * デザイントークン(style.css)の静的検査。
 *
 * ダークモードは「色を意味単位のトークンへ集約し、`:root[data-theme="dark"]` で
 * 一括上書きする」方式(設計 §3.1)で成り立っている。この方式は、
 *
 *  - ライトにしか無いトークン(= ダークで色が変わらない取り残し)
 *  - 定義されていない `var(--…)` の参照(= 色が消える)
 *  - 変数化し忘れたハードコードの色(= ダークで浮く)
 *  - ダークで読めなくなる配色(コントラスト不足)
 *
 * のいずれかが 1 つでも入り込むと崩れる。人手のレビューでは見落とすため、
 * ソースを読み込む静的検査として継続実行する(設計 §4)。
 *
 * 検査対象は src 配下の全 .css と .vue。固定リストに列挙すると画面を足した
 * ときに追加を忘れて検査から漏れるため、**fs で自動列挙**する。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { compileStyle, parse } from '@vue/compiler-sfc'
import selectorParser from 'postcss-selector-parser'
import { describe, expect, it } from 'vitest'
import { THEME_BACKGROUND_RGB } from './theme'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const SRC_DIR = resolve(process.cwd(), 'src')
const STYLE_CSS_PATH = resolve(SRC_DIR, 'style.css')

const styleCss = readFileSync(STYLE_CSS_PATH, 'utf8')

/** src 配下の .vue と .css を再帰的に列挙する(テストは .ts のため自然に対象外になる) */
function listStyleFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      found.push(...listStyleFiles(path))
    } else if ((/\.(vue|css)$/).test(entry.name)) {
      found.push(path)
    }
  }
  return found.sort()
}

/** 色を持ち得るソース(全 .css + 全 .vue)。名前は frontend/ からの相対パス */
const STYLE_SOURCES: { name: string; source: string }[] = [
  ...listStyleFiles(SRC_DIR),
].map((path) => ({
  name: relative(process.cwd(), path),
  source: readFileSync(path, 'utf8'),
}))

/** CSS コメントを取り除く(コメント内の色や var() を検査対象にしないため) */
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, '')
}

/**
 * セレクタに対応する宣言ブロックの中身を取り出す。
 * トークン定義は入れ子を持たない平坦なブロックのため、最初の `}` までを見れば足りる。
 */
function extractBlock(css: string, selector: string): string {
  const head = `${selector} {`
  const start = css.indexOf(head)
  if (start < 0) throw new Error(`セレクタが見つかりません: ${selector}`)
  const bodyStart = start + head.length
  const end = css.indexOf('}', bodyStart)
  if (end < 0) throw new Error(`ブロックが閉じていません: ${selector}`)
  return css.slice(bodyStart, end)
}

/** 宣言ブロックから `--token: value` を取り出す */
function parseTokens(block: string): Map<string, string> {
  const tokens = new Map<string, string>()
  for (const m of block.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    tokens.set(m[1], m[2].trim())
  }
  return tokens
}

const cleanCss = stripCssComments(styleCss)
const lightTokens = parseTokens(extractBlock(cleanCss, ':root'))
const darkTokens = parseTokens(extractBlock(cleanCss, ':root[data-theme="dark"]'))

// ---------------------------------------------------------------------------
// トークン定義の整合
// ---------------------------------------------------------------------------

describe('デザイントークンの定義', () => {
  it('ライトとダークでトークン集合が一致する', () => {
    const light = [...lightTokens.keys()].sort()
    const dark = [...darkTokens.keys()].sort()
    expect(dark).toEqual(light)
  })

  it('トークンが定義されている(空の :root ではない)', () => {
    expect(lightTokens.size).toBeGreaterThan(20)
  })

  it('検査対象のソースを列挙できている(列挙の空振りで検査が素通りしない)', () => {
    const names = STYLE_SOURCES.map((f) => f.name)
    expect(names).toContain('src/style.css')
    expect(names).toContain('src/markdown.css')
    expect(names).toContain('src/App.vue')
    expect(names).toContain('src/views/AboutView.vue')
    // style.css + App.vue + 画面 6 本
    expect(STYLE_SOURCES.length).toBeGreaterThanOrEqual(8)
  })

  it('両ブロックが color-scheme を宣言する(ネイティブ描画をテーマへ追従させる)', () => {
    expect(extractBlock(cleanCss, ':root')).toMatch(/color-scheme:\s*light\s*;/)
    expect(extractBlock(cleanCss, ':root[data-theme="dark"]')).toMatch(/color-scheme:\s*dark\s*;/)
  })

  it('theme.ts のウィンドウ背景色が --bg と一致する', () => {
    expect(hexToRgb(lightTokens.get('--bg')!)).toEqual(THEME_BACKGROUND_RGB.light)
    expect(hexToRgb(darkTokens.get('--bg')!)).toEqual(THEME_BACKGROUND_RGB.dark)
  })
})

// ---------------------------------------------------------------------------
// 参照の整合・残存色
// ---------------------------------------------------------------------------

describe('トークンの参照', () => {
  it('未定義の var(--…) を参照していない', () => {
    for (const { name, source } of STYLE_SOURCES) {
      const referenced = new Set<string>()
      for (const m of stripCssComments(source).matchAll(/var\(\s*(--[\w-]+)/g)) {
        referenced.add(m[1])
      }
      const undefinedTokens = [...referenced].filter((t) => !lightTokens.has(t)).sort()
      expect(undefinedTokens, `${name} が未定義のトークンを参照しています`).toEqual([])
    }
  })
})

describe('ハードコードされた色の残存', () => {
  /** 色リテラル(hex / rgb() / rgba() / hsl() / hsla()) */
  const COLOR_PATTERNS: { label: string; re: RegExp }[] = [
    { label: 'hex', re: /#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g },
    { label: 'rgb()', re: /\brgba?\s*\(/g },
    { label: 'hsl()', re: /\bhsla?\s*\(/g },
    { label: '色関数', re: /\b(?:oklch|oklab|color-mix|lab|lch|color)\s*\(/g },
  ]

  /** トークン定義そのもの(:root の 2 ブロック)は色リテラルで良いので除外する */
  function withoutTokenBlocks(source: string): string {
    return stripCssComments(source)
      .replace(/:root\s*\{[^}]*\}/g, '')
      .replace(/:root\[data-theme="dark"\]\s*\{[^}]*\}/g, '')
  }

  it('トークン定義の外に色リテラルが残っていない', () => {
    for (const { name, source } of STYLE_SOURCES) {
      const body = name === 'src/style.css' ? withoutTokenBlocks(source) : stripCssComments(source)
      for (const { label, re } of COLOR_PATTERNS) {
        const found = body.match(re) ?? []
        expect(found, `${name} に ${label} の色が残っています`).toEqual([])
      }
    }
  })
})

// ---------------------------------------------------------------------------
// コントラスト(WCAG AA)
// ---------------------------------------------------------------------------

/** `#rrggbb` を 0〜255 の 3 要素へ変換する */
function hexToRgb(hex: string): [number, number, number] {
  const m = hex.trim().match(/^#([0-9a-fA-F]{6})$/)
  if (!m) throw new Error(`hex ではありません: ${hex}`)
  const v = parseInt(m[1], 16)
  return [(v >> 16) & 0xff, (v >> 8) & 0xff, v & 0xff]
}

/** 相対輝度(WCAG 2.x) */
function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map((c) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** コントラスト比(1〜21) */
function contrast(fg: string, bg: string): number {
  const a = luminance(fg)
  const b = luminance(bg)
  const [hi, lo] = a > b ? [a, b] : [b, a]
  return (hi + 0.05) / (lo + 0.05)
}

/** 文字を載せる背景のトークン */
const BG_TOKENS = [
  '--bg',
  '--surface',
  '--bg-muted',
  '--bg-hover',
  '--nav-active-bg',
  '--status-info-bg',
  '--selection-bg',
]

/**
 * AA(通常文字 4.5:1)を検査する組。
 *
 * 検査対象外(装飾・非文字のため文字コントラストの概念が無い):
 *   --border / --handle-hover-bg / --warning-border / --success-border /
 *   --danger-border / --danger-border-muted … 罫線
 *   --accent-muted … 押下不可の主ボタンの塗り(無効表示なので AA 対象外)
 *   --overlay / --shadow … 幕・影(半透明のため比率を算出できない)
 */
const CONTRAST_PAIRS: [string, string][] = [
  ...['--text', '--text-muted', '--accent-fg', '--accent-fg-hover'].flatMap(
    (fg): [string, string][] => BG_TOKENS.map((bg) => [fg, bg]),
  ),
  // 弱い文字は淡い面の上でしか使わない
  ...['--bg', '--surface', '--bg-muted', '--bg-hover'].map(
    (bg): [string, string] => ['--text-faint', bg],
  ),
  // 塗りボタン(白文字を載せる)
  ['--on-accent', '--accent-emphasis'],
  ['--on-accent', '--accent-emphasis-hover'],
  // 状態背景の上に置く本文(色を指定していない文字は --text を継承する)
  ...['--warning-bg', '--success-bg', '--danger-bg', '--danger-bg-hover', '--danger-bg-subtle'].map(
    (bg): [string, string] => ['--text', bg],
  ),
  // 状態色(文字 × 同系の背景)
  ['--warning-text', '--warning-bg'],
  ['--success-text', '--success-bg'],
  ['--danger-text', '--danger-bg'],
  ['--danger-text', '--danger-bg-hover'],
  ['--danger-strong', '--danger-bg'],
  ['--danger-emphasis-text', '--danger-bg-subtle'],
  // 状態色(文字 × 素の面。パネル上に直接置く警告・エラー文)
  ...['--warning-text', '--success-text', '--danger-text', '--danger-emphasis-text', '--danger-strong'].flatMap(
    (fg): [string, string][] => [
      [fg, '--bg'],
      [fg, '--surface'],
    ],
  ),
]

const AA_NORMAL = 4.5

describe('コントラスト(WCAG AA)', () => {
  for (const [themeName, tokens] of [
    ['ライト', lightTokens],
    ['ダーク', darkTokens],
  ] as const) {
    it(`${themeName}: 文字と背景の組が AA を満たす`, () => {
      const failures: string[] = []
      for (const [fg, bg] of CONTRAST_PAIRS) {
        const fgHex = tokens.get(fg)
        const bgHex = tokens.get(bg)
        expect(fgHex, `${fg} が未定義です`).toBeDefined()
        expect(bgHex, `${bg} が未定義です`).toBeDefined()
        const ratio = contrast(fgHex!, bgHex!)
        if (ratio < AA_NORMAL) {
          failures.push(
            `${fg}(${fgHex})× ${bg}(${bgHex})= ${ratio.toFixed(2)}:1(必要 ${AA_NORMAL}:1)`,
          )
        }
      }
      expect(failures).toEqual([])
    })
  }
})

describe('課題詳細の押下状態', () => {
  const source = stripCssComments(readFileSync(resolve(SRC_DIR, 'components/IssueDetailDialog.vue'), 'utf8'))
  const pressedHover = ".view-toggle button[aria-pressed='true']:hover"
  const globalHover = '.content button:hover:not(:disabled)'

  function colorToken(selector: string, property: string, css = source): string {
    const block = extractBlock(css, selector)
    const declaration = block.match(new RegExp(`(?:^|;)\\s*${property}:\\s*var\\((--[\\w-]+)\\)\\s*;`))
    if (!declaration) throw new Error(`${selector} の ${property} に色トークンが必要です`)
    return declaration[1]
  }

  function compareSpecificity(a: number[], b: number[]): number {
    const index = a.findIndex((value, i) => value !== b[i])
    return index < 0 ? 0 : a[index] - b[index]
  }

  function specificity(selector: string): number[] {
    function score(nodes: selectorParser.Node[]): number[] {
      return nodes.reduce((total, node) => {
        let part = [0, 0, 0]
        if (node.type === 'id') part = [1, 0, 0]
        if (node.type === 'class' || node.type === 'attribute') part = [0, 1, 0]
        if (node.type === 'tag') part = [0, 0, 1]
        if (node.type === 'pseudo') {
          if (node.value === ':where') return total
          if ([':not', ':is', ':has'].includes(node.value)) {
            part = node.nodes.map((child) => score(child.nodes)).sort(compareSpecificity).at(-1) ?? part
          } else {
            part = node.value.startsWith('::') ? [0, 0, 1] : [0, 1, 0]
          }
        }
        return total.map((value, i) => value + part[i])
      }, [0, 0, 0])
    }
    return score(selectorParser().astSync(selector).nodes[0].nodes)
  }

  it('表示切替の押下 hover は scoped 化後に後続の全体 hover より詳細度が高い', () => {
    // ブロック欠落も失敗にする。ソース順が後の全体ルールとは同点では足りない。
    extractBlock(source, pressedHover)
    extractBlock(cleanCss, globalHover)
    const { descriptor } = parse(source)
    const style = descriptor.styles.find((block) => block.scoped)!
    const compiled = compileStyle({ source: style.content, filename: 'IssueDetailDialog.vue', id: 'data-v-state-check', scoped: true })
    expect(compiled.errors).toEqual([])
    const selectors: string[] = []
    compiled.rawResult?.root.walkRules((rule) => {
      selectors.push(...selectorParser().astSync(rule.selector).nodes.map((node) => node.toString()))
    })
    const selector = selectors
      .find((value) => value.includes('.view-toggle') && value.includes('[aria-pressed=') && value.includes(':hover'))
    expect(selector).toBeDefined()
    const host = document.createElement('main')
    host.className = 'content'
    host.innerHTML = '<div class="modal"><div class="view-toggle"><button aria-pressed="true" class="hover-probe" data-v-state-check></button></div></div>'
    const button = host.querySelector('button')!
    const globalStyle = compileStyle({ source: cleanCss, filename: 'style.css', id: '', scoped: false })
    expect(globalStyle.errors).toEqual([])
    globalStyle.rawResult?.root.walkRules((rule) => {
      const relevant = rule.nodes.some((node) => node.type === 'decl' && ['background', 'background-color', 'color'].includes(node.prop))
      if (!relevant) return
      for (const globalSelector of selectorParser().astSync(rule.selector).nodes) {
        const original = globalSelector.toString()
        // DOMの静的マッチング用にのみhoverをクラスへ置換。詳細度は元セレクタで計算する。
        globalSelector.walkPseudos((node) => {
          if (node.value === ':hover') node.replaceWith(selectorParser.className({ value: 'hover-probe' }))
        })
        if (!button.matches(globalSelector.toString())) continue
        expect(compareSpecificity(specificity(selector!), specificity(original)), `${selector} / ${original}`)
          .toBeGreaterThan(0)
        for (const node of rule.nodes) {
          if (node.type === 'decl' && ['background', 'background-color', 'color'].includes(node.prop)) {
            expect(node.important, `${original}: ${node.prop}`).toBeFalsy()
          }
        }
      }
    })
  })

  for (const [themeName, tokens] of [['ライト', lightTokens], ['ダーク', darkTokens]] as const) {
    for (const [pressed, inactiveSelectors] of [
      [".view-toggle button[aria-pressed='true']", ['.view-toggle button']],
      [pressedHover, ['.view-toggle button']],
      ['.modal.maximized .maximize-toggle', ['.maximize-toggle', '.maximize-toggle:hover']],
      ['.modal.maximized .maximize-toggle:hover', ['.maximize-toggle', '.maximize-toggle:hover']],
    ] as const) {
      it(`${themeName}: ${pressed} の塗りで未押下状態を識別でき、文字も読める`, () => {
        const fill = colorToken(pressed, 'background')
        const foreground = colorToken(pressed, 'color')
        // 共通の描辺ではなく、押下時だけ変わる塗りを実際の宣言から検査する。
        const adjacent = [
          ...inactiveSelectors.map((selector) => colorToken(selector, 'background')),
          colorToken(globalHover, 'background', cleanCss),
          '--surface', '--bg-muted', '--bg',
        ]
        for (const background of adjacent) {
          expect(contrast(tokens.get(fill)!, tokens.get(background)!), `${pressed}: ${fill} / ${background}`)
            .toBeGreaterThanOrEqual(3)
        }
        expect(contrast(tokens.get(foreground)!, tokens.get(fill)!), `${pressed}: 文字 / 押下時の塗り`)
          .toBeGreaterThanOrEqual(4.5)
      })
    }
  }
})


/** CSS と Vue のインライン指定を同じ宣言規則で調べる。 */
function literalShapes(source: string): string[] {
  const clean = stripCssComments(source)
  const declarations = clean.matchAll(/(?:border-radius|border-(?:top|bottom)-(?:left|right)-radius|box-shadow|borderRadius|border(?:Top|Bottom)(?:Left|Right)Radius|boxShadow)["']?\s*:\s*(?:["']([^"']*)["']|([^;}"']+))/g)
  return [...declarations].map((m) => (m[1] ?? m[2]).trim())
    .filter((value) => !/^var\(--[\w-]+\)$/.test(value))
}

const SHAPE_TOKENS: Record<string, string> = {
  '--radius-ui': '4px', '--radius-code': '2px', '--radius-circle': '50%',
  '--radius-none': '0', '--radius-segment-start': '4px 0 0 4px',
  '--radius-segment-end': '0 4px 4px 0', '--shadow-none': 'none',
  '--shadow-card': '2px 2px 0 var(--shadow)',
  '--shadow-menu': '4px 4px 0 var(--floating-shadow-color)',
  '--shadow-dialog': '6px 6px 0 var(--floating-shadow-color)',
  '--shadow-button-hover': '3px 3px 0 var(--floating-shadow-color)',
  '--heading-weight': '800', '--subheading-weight': '700', '--strong-weight': '700',
  '--border-width': '1px', '--divider-width': '2px', '--quote-border-width': '4px',
  '--focus-width': '3px', '--focus-offset': '3px', '--button-lift': '-2px',
  '--motion-duration': '120ms',
}

describe('Pop の形状と配色', () => {
  it('形状検査が長形式・複合値・インライン指定も拒否する', () => {
    expect(literalShapes('border-radius: 4px; box-shadow: none; border-top-left-radius: 0;')).toHaveLength(3)
    expect(literalShapes('border-radius:\n  4px 0 0 4px;')).toHaveLength(1)
    expect(literalShapes("<div :style=\"{ 'border-radius': '4px' }\">")).toHaveLength(1)
    expect(literalShapes('borderRadius: "4px", boxShadow: "none"')).toHaveLength(2)
    expect(literalShapes('/* box-shadow: none; */ border-radius: var(--radius-ui);')).toEqual([])
    // 未定義参照は別の全ソース走査で拒否する。
    expect(literalShapes('border-radius: var(--missing);')).toEqual([])
  })
  for (const [name, tokens] of [['ライト', lightTokens], ['ダーク', darkTokens]] as const) {
    it(`${name}: 形状値と hex 色を定義する`, () => {
      for (const [token, value] of Object.entries(SHAPE_TOKENS)) expect(tokens.get(token), token).toBe(value)
      for (const [token, value] of tokens) {
        if (!(token in SHAPE_TOKENS)) expect(value, token).toMatch(/^#[0-9a-f]{6}(?:[0-9a-f]{2})?$/i)
      }
      expect(tokens.has('--shadow-subtle')).toBe(false)
      for (const token of ['--frame-bg', '--grid-border', '--focus-ring', '--floating-shadow-color']) expect(tokens.has(token), token).toBe(true)
    })
    it(`${name}: 焦点と状態の輪郭は隣接背景に 3:1 を満たす`, () => {
      for (const fg of ['--focus-ring', '--border']) {
        for (const bg of [...BG_TOKENS, '--frame-bg', '--warning-bg', '--success-bg', '--danger-bg', '--danger-bg-hover', '--danger-bg-subtle']) {
          expect(contrast(tokens.get(fg)!, tokens.get(bg)!), `${fg} / ${bg}`).toBeGreaterThanOrEqual(3)
        }
      }
      for (const fg of ['--text', '--text-muted', '--text-faint', '--accent-fg', '--accent-fg-hover']) expect(contrast(tokens.get(fg)!, tokens.get('--frame-bg')!), fg).toBeGreaterThanOrEqual(4.5)
    })
  }
  it('色・形状のリテラルは style.css のトークン定義内に閉じる', () => {
    for (const { name, source } of STYLE_SOURCES) {
      const clean = stripCssComments(source)
      const body = name === 'src/style.css' ? clean.replace(/:root(?:\[data-theme="dark"\])?\s*\{[^}]*\}/g, '') : clean
      expect(literalShapes(body), name).toEqual([])
    }
  })
})
