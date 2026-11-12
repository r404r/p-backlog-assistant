/**
 * Markdown の設定・URL操作と、編集プレビュー用の互換renderer。
 * Wiki閲覧・課題詳細・コメントは MarkdownBody と markdownDocument を使用する。
 * リモート本文は Go binding に到達できる WebView へ表示するため、次を守る。
 *
 * 1. 生HTMLは html:false で文字列として保持する。
 * 2. 出力は必ずDOMPurifyを通し、style/event/SVG/MathML/form/inputを許可しない。
 *    共通rendererの追加タグ・属性・classは要素/値/manifestごとに限定する。
 * 3. リンクはhref無し。外部はHTTP(S)検証、内部は現世代のWiki索引で解決する。
 * 4. 外部画像は変換時から文字保持。同Wikiの添付をGoで検証・再encodeし、
 *    共通rendererが自ら生成・登録した現世代のblobだけを例外的に表示する。
 *    このファイルの文脈無しrenderMarkdownは従来どおり画像を一切生成しない。
 *
 * 互換入口は表・単一改行と安全なplain codeを維持する。共通rendererは別途
 * CJK強調・読み取りtask・生成見出しID・コードWorker・限定Wiki参照を扱う。
 * 両入口の欠陥注入と互換試験を弱めず、非Markdownは画面側で原文表示する。
 */
import DOMPurify, { type Config } from 'dompurify'
import MarkdownIt from 'markdown-it'
import { useBacklogTables } from './backlogTables'

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
 * 既定プリセット = CommonMark + table + strikethrough に linkify と改行を追加。
 * 旧テンプレートの表は useBacklogTables で限定的に補う。外部プラグインは使わない。
 * `html: false` により生 HTML はタグとして解釈されない。
 */
const md = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
  typographer: false,
})

md.use(useBacklogTables)

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
  if (safe) {
    token.attrSet('data-href', safe)
    token.attrSet('role', 'link')
    token.attrSet('tabindex', '0')
  }
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
 * 許可属性(設計 §3.2)。`a` の data-href / role / tabindex と、コードブロックの
 * 言語 class だけ。DOMPurify の allow-list は要素を区別しないため、要素との対応と
 * 値の形は後段のフック(enforceAttributeAllowList)で厳密化する。
 */
const ALLOWED_ATTR = ['data-href', 'class', 'role', 'tabindex']

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
 *  - role / tabindex は `a[data-href]` のキーボード操作に必要な固定値だけ
 *  - class は `code`(コードブロックの言語表示)のみ、かつ `language-…` の形だけ
 * に絞り、それ以外の属性はすべて取り除く。
 */
export function enforceAttributeAllowList(node: Element): void {
  for (const name of node.getAttributeNames()) {
    const value = node.getAttribute(name) ?? ''
    const href = node.getAttribute('data-href') ?? ''
    const isSafeAnchor = node.tagName === 'A' && href !== '' && safeExternalUrl(href) === href
    if (name === 'data-href' && isSafeAnchor) continue
    if (name === 'role' && isSafeAnchor && value === 'link') continue
    if (name === 'tabindex' && isSafeAnchor && value === '0') continue
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
 * 互換プレビューでも変換とサニタイズの2段を必ず通す。
 * 共通MarkdownBodyはmarkdownDocument.renderPartsの検査済み出力を使う。
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
