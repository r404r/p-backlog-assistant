/**
 * リモート本文は html:false → 閉じた manifest → DOMPurify の順で描画する。
 * href・原始HTML・外部画像は許可しない。画像は同Wikiの検証済みblobだけ。
 * 高亮・画像の完了後も同じsanitizeを通し、後からinnerHTMLを加工しない。
 */
import DOMPurify from 'dompurify'
import MarkdownIt, { type Token } from 'markdown-it'
import cjk from 'markdown-it-cjk-friendly'
import { useBacklogTables } from './backlogTables'
import { globalTranslate } from './format'
import type { WikiPage } from './backend/contract'

export interface Heading {
  id: string
  text: string
  level: number
}
export interface CodeBlock {
  source: string
  language: string
}
export interface ImageSlot {
  attachmentId: number
  name: string
  original: string
  alt: string
  code: string
}
export interface ReadyImage {
  url: string
  width: number
  height: number
}
export interface WikiContext {
  page: WikiPage
  pages: WikiPage[]
}
export interface RenderState {
  highlights?: Map<number, string>
  images?: Map<number, ReadyImage>
  errors?: Map<number, string>
}
const taskStates = new WeakMap<MarkdownDocument, Set<string>>()
const headingIDs = new WeakMap<MarkdownDocument, Map<string, number>>()
export const syntaxClasses = [
  'keyword',
  'string',
  'number',
  'comment',
  'title',
  'attr',
  'literal',
  'meta',
].map((s) => `md-syntax-${s}`)
const languages = new Set([
  'javascript',
  'typescript',
  'json',
  'bash',
  'go',
  'python',
  'xml',
  'css',
  'sql',
  'yaml',
  'markdown',
  'diff',
])
const aliases: Record<string, string> = {
  js: 'javascript',
  ts: 'typescript',
  sh: 'bash',
  shell: 'bash',
  py: 'python',
  html: 'xml',
  yml: 'yaml',
  md: 'markdown',
}
export function codeLanguage(info: string) {
  const first = info.trim().split(/\s+/)[0].toLowerCase()
  const name = aliases[first] ?? first
  return languages.has(name) ? name : 'text'
}
export function safeExternalUrl(raw: string | null | undefined): string {
  try {
    const u = new URL(raw ?? '')
    return /^https?:$/.test(u.protocol) ? u.href : ''
  } catch {
    return ''
  }
}
const parser = new MarkdownIt({ html: false, linkify: true, breaks: true, typographer: false })
  .use(useBacklogTables)
  .use(cjk)
const escape = parser.utils.escapeHtml
let instance = 0
export interface MarkdownDocument {
  headings: Heading[]
  codeBlocks: CodeBlock[]
  wikiLinks: number[]
  imageSlots: ImageSlot[]
  registerImage(index: number, image: ReadyImage): ReadyImage
  ownsImage(index: number, image: ReadyImage): boolean
  renderParts(state?: RenderState): string[]
  render(state?: RenderState): string
  sanitize(html: string, state?: RenderState): string
}
interface Env extends Record<PropertyKey, unknown> {
  doc: MarkdownDocument
  wiki?: WikiContext
  state: RenderState
  copy: string
  missing: string
  ambiguous: string
  image: string
  tasks: Set<string>
}

interface Metadata {
  index: number
  name: string
  alt: string
  code: number
  checked: boolean
  ambiguous: boolean
}
const meta = (token: Token) => token.meta as unknown as Metadata

/* eslint-disable no-control-regex -- リモート参照名に制御文字を許可しないための境界検査。 */
// inline rule は code/escape/既存リンク内を再解釈しない。
parser.inline.ruler.before('image', 'wiki_reference', (state, silent) => {
  if (silent || state.linkLevel > 0 || !state.env.wiki) return false
  const tail = state.src.slice(state.pos)
  const link = /^\[\[([^[\]\x00-\x1f\x7f]{1,255})\]\]/.exec(tail)
  const macro = /^#image\(([^()\x00-\x1f\x7f]{1,255})\)/.exec(tail)
  const ref = /^!\[([^\]\n]*)\]\[([^\]\x00-\x1f\x7f]{1,255})\]/.exec(tail)
  if (!link && !macro && !ref) return false
  if (ref && state.env.references?.[parser.utils.normalizeReference(ref[2])]) return false
  if (link && /(?:[|#]|:\/\/|^\.\.\/)/.test(link[1])) return false
  const raw = (link ?? macro ?? ref)![0]
  if (!silent) {
    const token = state.push(link ? 'wiki_link' : 'wiki_image', '', 0)
    token.content = raw
    token.meta = { name: link?.[1] ?? macro?.[1] ?? ref?.[2], alt: ref?.[1] ?? macro?.[1] ?? '' }
  }
  state.pos += raw.length
  return true
})
parser.renderer.rules.link_open = (tokens, i, options, _env, self) => {
  const token = tokens[i],
    safe = safeExternalUrl(String(token.attrGet('href') ?? ''))
  token.attrs = []
  if (safe) {
    token.attrSet('data-href', safe)
    token.attrSet('role', 'link')
    token.attrSet('tabindex', '0')
  }
  return self.renderToken(tokens, i, options)
}
parser.renderer.rules.table_open = () =>
  `<table tabindex="0" aria-label="${escape(globalTranslate('wiki.render.table'))}">`
parser.renderer.rules.image = (tokens, i) =>
  escape(`![${tokens[i].content}](${tokens[i].attrGet('src') ?? ''})`)
parser.renderer.rules.wiki_link = (tokens, i, _options, rawEnv) => {
  const env = rawEnv as unknown as Env
  const token = tokens[i],
    index: number = meta(token).index
  if (index < 0)
    return `${escape(token.content)} — ${escape(meta(token).ambiguous ? env.ambiguous : env.missing)}`
  return `<a data-md-wiki="${index}" role="link" tabindex="0">${escape(meta(token).name)}</a>`
}
parser.renderer.rules.wiki_image = (tokens, i, _options, rawEnv) => {
  const env = rawEnv as unknown as Env
  const index: number = meta(tokens[i]).index,
    slot = env.doc.imageSlots[index]
  const ready = env.state.images?.get(index)
  if (ready && !slot.code && env.doc.ownsImage(index, ready))
    return `<span class="md-image-slot" data-md-image="${index}"><img src="${escape(ready.url)}" alt="${escape(slot.alt.slice(0, 255))}" width="${ready.width}" height="${ready.height}"></span>`
  const reason = env.state.errors?.get(index) ?? slot.code
  return `<span class="${reason ? 'md-image-error' : 'md-image-slot'}" data-md-image="${index}">${escape(env.image)}: ${escape(slot.name)} — ${escape(slot.original)}${reason ? ` — ${escape(reason)}` : ''}</span>`
}
parser.renderer.rules.fence = (tokens, i, _options, rawEnv) => {
  const env = rawEnv as unknown as Env
  const index: number = meta(tokens[i]).code,
    block = env.doc.codeBlocks[index]
  return `<pre><span class="md-code-language">${block.language}</span><button type="button" data-md-action="copy" data-md-code="${index}">${escape(env.copy)}</button><code class="language-${block.language}">${env.state.highlights?.get(index) ?? escape(block.source)}</code></pre>\n`
}
parser.renderer.rules.code_block = parser.renderer.rules.fence
parser.renderer.rules.md_task = (tokens, i) =>
  `<span class="md-task" role="checkbox" aria-checked="${meta(tokens[i]).checked}" aria-disabled="true">${meta(tokens[i]).checked ? '☑' : '☐'}</span> `

const tags = [
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
  'button',
  'img',
]
const attrs = [
  'data-href',
  'class',
  'role',
  'tabindex',
  'id',
  'type',
  'data-md-action',
  'data-md-code',
  'data-md-wiki',
  'data-md-image',
  'aria-checked',
  'aria-disabled',
  'src',
  'alt',
  'width',
  'height',
  'aria-label',
]
/** 最終hookを直接検証できるよう公開。文脈無しでは従来の狭い許可だけ。 */
export function enforceDocumentAttributes(
  node: Element,
  doc?: MarkdownDocument,
  state: RenderState = {},
): void {
  const keep = new Set<string>(),
    tag = node.tagName
  const allow = (name: string, value: string) => {
    if (node.getAttribute(name) === value) keep.add(name)
  }
  const canonical = (value: string | null) =>
    value !== null && /^(0|[1-9]\d*)$/.test(value) ? Number(value) : -1
  const external =
    tag === 'A' &&
    !node.hasAttribute('data-md-wiki') &&
    !!node.getAttribute('data-href') &&
    safeExternalUrl(node.getAttribute('data-href')) === node.getAttribute('data-href')
  const wi = canonical(node.getAttribute('data-md-wiki'))
  const internal =
    tag === 'A' && !node.hasAttribute('data-href') && !!doc && doc.wikiLinks[wi] !== undefined
  if (external || internal) {
    if (external) keep.add('data-href')
    else keep.add('data-md-wiki')
    allow('role', 'link')
    allow('tabindex', '0')
  }
  if (tag === 'CODE' && /^language-[\w.+-]+$/.test(node.getAttribute('class') ?? ''))
    keep.add('class')
  if (doc) {
    if (tag === 'TABLE') {
      allow('tabindex', '0')
      allow('aria-label', globalTranslate('wiki.render.table'))
    }
    if (/^H[1-6]$/.test(tag) && headingIDs.get(doc)?.get(node.id) === Number(tag[1])) {
      keep.add('id')
      allow('tabindex', '-1')
    }
    if (
      tag === 'BUTTON' &&
      doc.codeBlocks[canonical(node.getAttribute('data-md-code'))] &&
      node.parentElement?.tagName === 'PRE' &&
      node.getAttribute('type') === 'button' &&
      node.getAttribute('data-md-action') === 'copy'
    ) {
      keep.add('type')
      keep.add('data-md-action')
      keep.add('data-md-code')
    }
    if (tag === 'SPAN') {
      const cl = node.getAttribute('class')
      if (cl && syntaxClasses.includes(cl) && node.closest('pre > code')) keep.add('class')
      if (
        cl === 'md-code-language' &&
        node.parentElement?.tagName === 'PRE' &&
        doc.codeBlocks.some((b) => b.language === node.textContent)
      )
        keep.add('class')
      if (
        cl === 'md-task' &&
        taskStates.get(doc)?.has(node.getAttribute('aria-checked') ?? '') &&
        node.closest('li') &&
        !node.closest('a') &&
        node.getAttribute('role') === 'checkbox' &&
        /^(true|false)$/.test(node.getAttribute('aria-checked') ?? '') &&
        node.getAttribute('aria-disabled') === 'true' &&
        node.textContent === (node.getAttribute('aria-checked') === 'true' ? '☑' : '☐')
      ) {
        for (const a of ['class', 'role', 'aria-checked', 'aria-disabled']) keep.add(a)
      }
      if ((cl === 'md-image-slot' || (cl === 'md-image-error' && (doc.imageSlots[canonical(node.getAttribute('data-md-image'))]?.code || state.errors?.has(canonical(node.getAttribute('data-md-image')))))) && doc.imageSlots[canonical(node.getAttribute('data-md-image'))]) {
        keep.add('class')
        keep.add('data-md-image')
      }
    }
    if (tag === 'IMG') {
      const parent = node.parentElement
      const index = canonical(parent?.getAttribute('data-md-image') ?? null)
      const slot = doc.imageSlots[index],
        ready = state.images?.get(index)
      if (
        slot &&
        !slot.code &&
        parent?.className === 'md-image-slot' &&
        ready &&
        doc.ownsImage(index, ready) &&
        ready.url.startsWith('blob:') &&
        node.getAttribute('src') === ready.url &&
        Number.isInteger(ready.width) &&
        Number.isInteger(ready.height) &&
        ready.width > 0 &&
        ready.height > 0 &&
        ready.width <= 8192 &&
        ready.height <= 8192 &&
        ready.width * ready.height <= 16000000
      ) {
        keep.add('src')
        allow('alt', slot.alt.slice(0, 255))
        allow('width', String(ready.width))
        allow('height', String(ready.height))
      }
    }
  }
  for (const name of node.getAttributeNames()) if (!keep.has(name)) node.removeAttribute(name)
}
export function prepareMarkdownDocument(
  source: string,
  wiki?: WikiContext,
  labels: { copy?: string; missing?: string; ambiguous?: string; image?: string } = {},
): MarkdownDocument {
  const id = ++instance,
    headings: Heading[] = [],
    codeBlocks: CodeBlock[] = [],
    wikiLinks: number[] = [],
    imageSlots: ImageSlot[] = []
  const registeredImages = new WeakMap<ReadyImage, number>()
  const doc: MarkdownDocument = {
    headings,
    codeBlocks,
    wikiLinks,
    imageSlots,
    render,
    renderParts,
    sanitize,
    registerImage(index, image) {
      const ready = Object.freeze({ ...image })
      registeredImages.set(ready, imageSlots[index]?.attachmentId ?? -1)
      return ready
    },
    ownsImage(index, image) {
      return (
        !!imageSlots[index]?.attachmentId &&
        registeredImages.get(image) === imageSlots[index].attachmentId
      )
    },
  }
  const env: Env = {
    doc,
    wiki,
    state: {},
    copy: labels.copy ?? globalTranslate('wiki.render.copy'),
    missing: labels.missing ?? globalTranslate('wiki.render.missing'),
    ambiguous: labels.ambiguous ?? globalTranslate('wiki.render.ambiguous'),
    image: labels.image ?? globalTranslate('wiki.render.image'),
    tasks: new Set(),
  }
  const tokens = parser.parse(source, env)
  const text = (children: typeof tokens): string =>
    children
      .map((t) =>
        t.children
          ? text(t.children)
          : ['text', 'code_inline', 'image'].includes(t.type)
            ? t.content
            : t.type === 'softbreak'
              ? ' '
              : '',
      )
      .join('')
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]
    if (token.type === 'heading_open') {
      const h = {
        id: `ba-md-${id}-h-${headings.length}`,
        text: text(tokens[i + 1].children ?? []),
        level: Number(token.tag[1]),
      }
      headings.push(h)
      token.attrSet('id', h.id)
      token.attrSet('tabindex', '-1')
    }
    if (token.type === 'fence' || token.type === 'code_block') {
      token.meta = { code: codeBlocks.length }
      codeBlocks.push({ source: token.content, language: codeLanguage(token.info) })
    }
    if (token.type !== 'inline' || !token.children) continue
    if (
      tokens[i - 1]?.type === 'paragraph_open' &&
      tokens[i - 2]?.type === 'list_item_open' &&
      /^\[[ xX]\]\s/.test(token.content) &&
      token.children[0]?.type === 'text'
    ) {
      const task = Object.assign(Object.create(Object.getPrototypeOf(token)), {
        type: 'md_task',
        tag: 'span',
        nesting: 0,
        attrs: null,
        children: null,
        content: '',
      }) as typeof token
      task.meta = { checked: token.content[1].toLowerCase() === 'x' }
      token.children[0].content = token.children[0].content.slice(4)
      token.children.unshift(task)
      env.tasks.add(String(task.meta!.checked))
    }
    for (const child of token.children) {
      if (child.type === 'wiki_link') {
        const matches = wiki!.pages.filter(
          (p) => p.projectId === wiki!.page.projectId && p.name === meta(child).name,
        )
        meta(child).index = matches.length === 1 ? wikiLinks.length : -1
        meta(child).ambiguous = matches.length > 1
        if (matches.length === 1) wikiLinks.push(matches[0].id)
      }
      if (child.type === 'wiki_image') {
        const name: string = meta(child).name
        const numeric = /^\d+$/.test(name)
        const candidates = wiki!.page.attachments.filter(
          (a) =>
            a.wikiId === wiki!.page.id &&
            (numeric ? /^[1-9]\d*$/.test(name) && String(a.id) === name : a.name === name),
        )
        const valid = !/[\x00-\x1f\x7f]/.test(name) && !/^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(name)
        const a = valid && candidates.length === 1 ? candidates[0] : undefined
        meta(child).index = imageSlots.length
        imageSlots.push({
          attachmentId: a?.id ?? 0,
          name: a?.name ?? name,
          alt: meta(child).alt,
          original: child.content,
          code: !a ? 'not_own_attachment' : a.size > 8 * 1024 * 1024 ? 'too_large' : '',
        })
      }
    }
  }
  taskStates.set(doc, env.tasks)
  headingIDs.set(doc, new Map(headings.map((h) => [h.id, h.level])))
  const purifier = DOMPurify(window)
  let currentState: RenderState = {}
  purifier.addHook('uponSanitizeElement', (node, data) => {
    if (data.tagName === 'img' && node instanceof Element) {
      const index = Number(node.parentElement?.getAttribute('data-md-image') ?? -1)
      const ready = currentState.images?.get(index)
      data.allowedTags.img =
        !!ready &&
        doc.ownsImage(index, ready) &&
        ready.url.startsWith('blob:') &&
        node.getAttribute('src') === ready.url &&
        ready.width > 0 &&
        ready.height > 0 &&
        ready.width <= 8192 &&
        ready.height <= 8192 &&
        ready.width * ready.height <= 16000000 &&
        Number.isInteger(ready.width) &&
        Number.isInteger(ready.height)
    }
  })
  const seenHeadingIDs = new Set<string>()
  purifier.addHook('afterSanitizeAttributes', (node) => {
    enforceDocumentAttributes(node, doc, currentState)
    if (node.hasAttribute('id')) {
      if (seenHeadingIDs.has(node.id)) {
        node.removeAttribute('id')
        node.removeAttribute('tabindex')
      } else seenHeadingIDs.add(node.id)
    }
  })
  function sanitize(html: string, state: RenderState = {}) {
    currentState = state
    seenHeadingIDs.clear()
    return purifier.sanitize(html, {
      ALLOWED_TAGS: tags,
      ALLOWED_ATTR: attrs,
      ALLOW_DATA_ATTR: false,
      ALLOW_ARIA_ATTR: false,
      ALLOW_UNKNOWN_PROTOCOLS: false,
      ADD_URI_SAFE_ATTR: ['src'],
      FORBID_TAGS: ['script', 'style', 'svg', 'math', 'iframe', 'form', 'input'],
      FORBID_ATTR: ['style', 'href', 'srcset', 'name'],
      RETURN_DOM: false,
      RETURN_DOM_FRAGMENT: false,
    })
  }
  // トップレベルblockごとに閉じたHTMLを生成する。非同期の高亮・画像で
  // 静的本文まで再sanitize/再挿入せず、同じ入口で変化したblockだけを検査する。
  const parts: {tokens:Token[]; codes:number[]; slots:number[]; previous:unknown[]; html:string|null; weight:number}[]=[]
  let group:Token[]=[], depth=0
  for(const token of tokens) {
    group.push(token);depth+=token.nesting
    if(depth!==0) continue
    const codes:number[]=[],slots:number[]=[]
    for(const item of group) {
      if(item.type==='fence' || item.type==='code_block') codes.push(meta(item).code)
      for(const child of item.children ?? []) if(child.type==='wiki_image') slots.push(meta(child).index)
    }
    const previous=parts.at(-1),weight=group.reduce((sum,item)=>sum+item.content.length,0)
    if(!codes.length && !slots.length && previous && !previous.codes.length && !previous.slots.length && previous.weight+weight<=16*1024 && previous.tokens.length+group.length<=192) {
      previous.tokens.push(...group);previous.weight+=weight
    } else parts.push({tokens:group,codes,slots,previous:[],html:null,weight})
    group=[]
  }
  function renderParts(state:RenderState={}) {
    env.state=state
    return parts.map(part=>{
      const values:unknown[]=[...part.codes.map(i=>state.highlights?.get(i)),...part.slots.flatMap(i=>[state.images?.get(i),state.errors?.get(i)])]
      if(part.html===null || values.some((value,i)=>value!==part.previous[i])) {
        part.html=sanitize(parser.renderer.render(part.tokens,parser.options,env),state)
        part.previous=values
      }
      return part.html
    })
  }
  function render(state: RenderState = {}) {return renderParts(state).join('')}
  return doc
}
