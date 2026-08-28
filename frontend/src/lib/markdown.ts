/**
 * 課題詳細・コメントの Markdown レンダリング(設計 §3.2)。
 *
 * 描画するのは **Backlog から届くリモート由来の文字列** であり、しかも描画先は
 * Go バインディングに到達できる WebView である。XSS はこの機能で最も重い故障に
 * なるため、次の 4 点を「実装の前提」として固定する(変更するときは
 * markdown.test.ts の欠陥注入テストごと見直すこと):
 *
 *  1. **生 HTML を通さない**: markdown-it は `html: false`。生 HTML は
 *     タグではなく文字列として表示される。
 *  2. **DOMPurify で二重サニタイズ**: 許可タグ・許可属性を明示した allow-list。
 *     `style` 属性・イベント属性・`img` / SVG / MathML / フォーム系は不許可。
 *  3. **リンクは href を出力しない**: 検証済み(http / https のみ)の URL を
 *     `data-href` に入れ、クリックハンドラから `openExternalURL` で開く。
 *     href が無いため、中クリック・修飾キー・キーボード操作でも WebView 内外への
 *     ネイティブ遷移は起こらない。
 *  4. **`<img>` を一切生成しない**: DOM へ挿入される前(markdown-it のレンダラ段階)で
 *     プレースホルダ文字列へ変換する。挿入後に置換する方式では、置換前に外部への
 *     画像リクエストが発生しうるため採らない。
 *
 * 対応構文は **CommonMark + table + strikethrough + linkify** に固定する
 * (markdown-it の標準機能のみ。タスクリスト・脚注・絵文字等のプラグインは
 * 対象外)。コードブロックはエスケープ表示のみで、シンタックスハイライトはしない。
 */
import DOMPurify, { type Config } from 'dompurify'
import MarkdownIt from 'markdown-it'

// ---------------------------------------------------------------------------
// 記法設定
// ---------------------------------------------------------------------------

/**
 * 整形表示の対象となる記法設定(Go の IssueDetailDTO.textFormattingRule)。
 *
 * Backlog のプロジェクトには Markdown 記法と Backlog 記法があり、Backlog 記法の
 * 本文を Markdown として解釈すると表示が崩れる。判定できない場合(空文字)も
 * 含め、この値以外はすべて従来のプレーン表示のままにする。
 */
const MARKDOWN_RULE = 'markdown'

/** 課題のプロジェクトが Markdown 記法か(これが真のときだけ整形表示する) */
export function isMarkdownRule(rule: string | undefined | null): boolean {
  return rule === MARKDOWN_RULE
}

// ---------------------------------------------------------------------------
// URL の検証
// ---------------------------------------------------------------------------

/**
 * リンク先として許可する URL だけを正規化して返す(許可しない場合は空文字)。
 *
 * 許可するのは **http / https だけ**。`javascript:` / `data:` / `vbscript:` /
 * `file:` はもちろん、相対 URL・`mailto:` も許可しない(WebView 内の画面遷移や
 * 外部アプリ起動の経路を作らないため)。
 */
function safeExternalUrl(raw: string | null | undefined): string {
  if (!raw) return ''
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    // 相対 URL・URL として解釈できない文字列はリンクにしない
    return ''
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') return ''
  return url.href
}

/**
 * クリックされた要素から、開くべき外部 URL を取り出す(無ければ空文字)。
 *
 * 整形表示のリンクは href を持たないため、画面はクリックをこの関数で解決して
 * `openExternalURL` に渡す。DOM を直接細工された場合に備え、取り出した値も
 * もう一度 http / https で検証する。
 */
export function markdownLinkHref(target: EventTarget | null): string {
  if (!(target instanceof Element)) return ''
  const anchor = target.closest('a[data-href]')
  if (!anchor) return ''
  return safeExternalUrl(anchor.getAttribute('data-href'))
}

// ---------------------------------------------------------------------------
// markdown-it(変換)
// ---------------------------------------------------------------------------

/**
 * markdown-it 本体。
 *
 * 既定プリセット = CommonMark + table + strikethrough。ここに linkify を足した
 * ものが対応構文のすべてで、プラグインは 1 つも読み込まない。
 * `html: false` により生 HTML はタグとして解釈されない。
 */
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: false,
  typographer: false,
})

/** コードブロックの言語名に付く class の接頭辞(許可する class はこの形だけ) */
const LANG_PREFIX = 'language-'

/** 許可する class(コードブロックの言語表示のみ。接頭辞 + 英数記号) */
const ALLOWED_CLASS = new RegExp(`^${LANG_PREFIX}[\\w.+-]+$`)

/**
 * リンクの開きタグ。href は出力せず、検証済み URL だけを data-href に入れる。
 * title 等その他の属性も落とす(表示に不要で、属性を増やすほど検査面が広がるため)。
 */
md.renderer.rules.link_open = (tokens, idx, options, _env, self) => {
  const token = tokens[idx]
  const href = token.attrGet('href')
  const safe = safeExternalUrl(typeof href === 'string' ? href : '')
  token.attrs = []
  if (safe) token.attrSet('data-href', safe)
  return self.renderToken(tokens, idx, options)
}

/**
 * 画像。**DOM へ挿入される前**にプレースホルダ文字列へ変換する。
 *
 * Backlog の添付画像は認証が必要で表示できず、外部画像は許可ドメイン外への通信に
 * なるため、いずれも読み込まない。代わりに元の記法(`![代替文字](URL)`)を
 * エスケープした文字列として残し、URL を読み取れるようにする。
 */
md.renderer.rules.image = (tokens, idx) => {
  const token = tokens[idx]
  const src = token.attrGet('src')
  return md.utils.escapeHtml(`![${token.content}](${typeof src === 'string' ? src : ''})`)
}

// ---------------------------------------------------------------------------
// DOMPurify(サニタイズ)
// ---------------------------------------------------------------------------

/**
 * 許可タグ(設計 §3.2)。ここに無いタグはすべて取り除かれる。
 * `img` / SVG / MathML / フォーム系要素・`style` は意図的に含めない。
 */
const ALLOWED_TAGS = [
  'p',
  'br',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'ul',
  'ol',
  'li',
  'blockquote',
  'pre',
  'code',
  'em',
  'strong',
  'del',
  's',
  'hr',
  'table',
  'thead',
  'tbody',
  'tr',
  'th',
  'td',
  'a',
  'span',
]

/**
 * 許可属性(設計 §3.2)。`a` の data-href と、コードブロックの言語 class だけ。
 * DOMPurify の allow-list は要素を区別しないため、要素との対応と値の形は
 * 後段のフック(enforceAttributeAllowList)で厳密化する。
 */
const ALLOWED_ATTR = ['data-href', 'class']

const SANITIZE_CONFIG: Config = {
  ALLOWED_TAGS,
  ALLOWED_ATTR,
  // data-* / aria-* を一括で許可しない(許可属性は上の 2 つだけ)
  ALLOW_DATA_ATTR: false,
  ALLOW_ARIA_ATTR: false,
  ALLOW_UNKNOWN_PROTOCOLS: false,
  // allow-list で足りるが、意図を明示するため主要な危険物は名指しでも禁じる
  FORBID_TAGS: ['script', 'style', 'img', 'svg', 'math', 'iframe', 'form', 'input'],
  FORBID_ATTR: ['style', 'href', 'src', 'srcset'],
  // 文字列を返す(RETURN_DOM 系は使わない)
  RETURN_DOM: false,
  RETURN_DOM_FRAGMENT: false,
}

/**
 * 属性の allow-list を「要素との対応」「値の形」まで含めて強制する
 * (DOMPurify の afterSanitizeAttributes フック。テストから直接呼んで検証する)。
 *
 * DOMPurify の ALLOWED_ATTR は要素を区別しないため、これが無いと
 * `span[data-href]` や任意の値の class が通ってしまう。ここで
 *  - data-href は `a` のみ、かつ検証済み URL と完全一致するものだけ
 *  - class は `code`(コードブロックの言語表示)のみ、かつ `language-…` の形だけ
 * に絞り、それ以外の属性はすべて取り除く。
 */
export function enforceAttributeAllowList(node: Element): void {
  for (const name of node.getAttributeNames()) {
    const value = node.getAttribute(name) ?? ''
    if (name === 'data-href' && node.tagName === 'A' && safeExternalUrl(value) === value) continue
    if (name === 'class' && node.tagName === 'CODE' && ALLOWED_CLASS.test(value)) continue
    node.removeAttribute(name)
  }
}

/**
 * この機能専用の DOMPurify インスタンス(フックを他の利用へ波及させない)。
 *
 * 生成は初回利用時に行う。モジュール読み込み時点では window が無い環境
 * (ビルドツール上での評価)がありうるため。
 */
let purifier: ReturnType<typeof DOMPurify> | null = null

function getPurifier(): ReturnType<typeof DOMPurify> {
  if (!purifier) {
    purifier = DOMPurify(window)
    purifier.addHook('afterSanitizeAttributes', enforceAttributeAllowList)
  }
  return purifier
}

// ---------------------------------------------------------------------------
// 変換の入口
// ---------------------------------------------------------------------------

/**
 * Markdown を、そのまま v-html で描画してよい HTML へ変換する。
 *
 * 変換(markdown-it)とサニタイズ(DOMPurify)の 2 段を必ず通る唯一の経路。
 * 画面はこの戻り値以外を v-html に渡してはならない。
 */
export function renderMarkdown(source: string): string {
  if (!source) return ''
  return getPurifier().sanitize(md.render(source), SANITIZE_CONFIG)
}

// ---------------------------------------------------------------------------
// 「整形表示 / 原文」の選択の保存(lib/sidebarWidth.ts と同じ流儀)
// ---------------------------------------------------------------------------

/** 表示の選択の保存先(次回起動時も維持する) */
export const DETAIL_MARKDOWN_KEY = 'ba.detailMarkdown'

/** 既定は整形表示(原文はコピー・検証のための切替) */
const DEFAULT_DETAIL_MARKDOWN = true

/** 保存済みの選択を読み出す(未保存・不正値・参照失敗はすべて既定) */
export function loadDetailMarkdown(): boolean {
  // localStorage は WebView の設定によっては参照時に例外になり得る
  try {
    const raw = localStorage.getItem(DETAIL_MARKDOWN_KEY)
    if (raw === 'true') return true
    if (raw === 'false') return false
    return DEFAULT_DETAIL_MARKDOWN
  } catch {
    return DEFAULT_DETAIL_MARKDOWN
  }
}

/** 選択を保存する(保存できなくてもセッション中の切替は成立するため失敗は無視する) */
export function saveDetailMarkdown(on: boolean): void {
  try {
    localStorage.setItem(DETAIL_MARKDOWN_KEY, String(on))
  } catch {
    // 保存できなくても表示自体は成立するため無視する
  }
}
