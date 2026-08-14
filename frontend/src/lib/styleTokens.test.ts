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
 * 検査対象は style.css と src 配下の全 .vue。固定リストに列挙すると画面を足した
 * ときに追加を忘れて検査から漏れるため、**fs で自動列挙**する。
 */
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { THEME_BACKGROUND_RGB } from './theme'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const SRC_DIR = resolve(process.cwd(), 'src')
const STYLE_CSS_PATH = resolve(SRC_DIR, 'style.css')

const styleCss = readFileSync(STYLE_CSS_PATH, 'utf8')

/** src 配下の .vue を再帰的に列挙する(テストは .ts のため自然に対象外になる) */
function listVueFiles(dir: string): string[] {
  const found: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      found.push(...listVueFiles(path))
    } else if (entry.name.endsWith('.vue')) {
      found.push(path)
    }
  }
  return found.sort()
}

/** 色を持ち得るソース(style.css + 全 .vue)。名前は frontend/ からの相対パス */
const STYLE_SOURCES: { name: string; source: string }[] = [
  STYLE_CSS_PATH,
  ...listVueFiles(SRC_DIR),
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
  ]

  /** トークン定義そのもの(:root の 2 ブロック)は色リテラルで良いので除外する */
  function withoutTokenBlocks(source: string): string {
    return stripCssComments(source)
      .replace(/:root\s*\{[^}]*\}/g, '')
      .replace(/:root\[data-theme="dark"\]\s*\{[^}]*\}/g, '')
  }

  it('トークン定義の外に色リテラルが残っていない', () => {
    for (const { name, source } of STYLE_SOURCES) {
      const body = withoutTokenBlocks(source)
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
 *   --overlay / --shadow / --shadow-subtle … 幕・影(半透明のため比率を算出できない)
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

/**
 * ライトのみ AA を免除する文字トークン。
 *
 * ライト値は「ダークモード対応で見た目を変えない」ことを最優先に、既存色のまま
 * 凍結している(設計 §3.1)。`--text-faint`(#8c959f)はその既存色で、
 * 白地に対して 3.04:1、表ヘッダ地(#f6f8fa)に対して 2.85:1 と AA に届かない。
 * これはダークモード対応で持ち込んだ問題ではなく元からの状態のため、ここでは
 * 現状を追認し、配色の改善は別課題として扱う。
 *
 * ダーク側は免除しない(設計 §3.1 が `--text-faint` について
 * 「通常文字として読める AA 値にする」と定めているため)。
 */
const LIGHT_EXEMPT_TOKENS = new Set(['--text-faint'])

const AA_NORMAL = 4.5

describe('コントラスト(WCAG AA)', () => {
  for (const [themeName, tokens] of [
    ['ライト', lightTokens],
    ['ダーク', darkTokens],
  ] as const) {
    it(`${themeName}: 文字と背景の組が AA を満たす`, () => {
      const failures: string[] = []
      for (const [fg, bg] of CONTRAST_PAIRS) {
        if (themeName === 'ライト' && LIGHT_EXEMPT_TOKENS.has(fg)) continue
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
