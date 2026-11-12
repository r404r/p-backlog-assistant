import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import json from 'highlight.js/lib/languages/json'
import bash from 'highlight.js/lib/languages/bash'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import sql from 'highlight.js/lib/languages/sql'
import yaml from 'highlight.js/lib/languages/yaml'
import markdown from 'highlight.js/lib/languages/markdown'
import diff from 'highlight.js/lib/languages/diff'
for (const [name, grammar] of Object.entries({
  javascript,
  typescript,
  json,
  bash,
  go,
  python,
  xml,
  css,
  sql,
  yaml,
  markdown,
  diff,
}))
  hljs.registerLanguage(name, grammar)
const categories: Record<string, string> = {
  keyword: 'keyword',
  string: 'string',
  regexp: 'string',
  number: 'number',
  comment: 'comment',
  title: 'title',
  attr: 'attr',
  attribute: 'attr',
  literal: 'literal',
  meta: 'meta',
  type: 'title',
  built_in: 'title',
  symbol: 'literal',
  bullet: 'literal',
  addition: 'string',
  deletion: 'number',
  section: 'title',
  name: 'keyword',
  selector_tag: 'keyword',
  selector_class: 'attr',
  selector_id: 'attr',
}
export function highlightCode(source: string, language: string): string {
  if (!hljs.getLanguage(language)) throw new Error('unsupported')
  // ライブラリscopeを閉じた8カテゴリへ正規化。戻り値も最終DOMPurifyを通る。
  return hljs
    .highlight(source, { language, ignoreIllegals: true })
    .value.replace(/<span class="([^"]*)">/g, (_match, scopes: string) => {
      const scope =
        scopes
          .split(' ')
          .find((s) => s.startsWith('hljs-'))
          ?.slice(5) ?? ''
      return categories[scope] ? `<span class="md-syntax-${categories[scope]}">` : '<span>'
    })
}
