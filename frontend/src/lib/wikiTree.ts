export interface WikiTreeNode {
  label: string
  path: string
  segments: string[]
  key: string
  pageIds: number[]
  descendantPageCount: number
  children: WikiTreeNode[]
}
export interface WikiTreeRow {
  rowKey: string
  node: WikiTreeNode
  parentRowKey: string | null
  level: number
  posinset: number
  setsize: number
  pageId: number
  hasChildren: boolean
}
/** 元文字列をUnicodeコードポイント順に比較する。正規化しない。 */
export function compareWikiNames(a: string, b: string): number {
  const left = a[Symbol.iterator](), right = b[Symbol.iterator]()
  while (true) {
    const x = left.next(), y = right.next()
    if (x.done || y.done) return x.done === y.done ? 0 : x.done ? -1 : 1
    const delta = x.value.codePointAt(0)! - y.value.codePointAt(0)!
    if (delta) return delta
  }
}
/** 階層は論理名のまま扱う。出力用のパス安全化はバックエンドに委ねる。 */
export function buildWikiTree(pages: readonly { id: number; name: string }[]): WikiTreeNode[] {
  const roots: WikiTreeNode[] = [], index = new Map<string, WikiTreeNode>(), seen = new Set<number>()
  for (const page of pages) {
    if (seen.has(page.id)) continue
    seen.add(page.id)
    const parts = page.name.split('/').filter(Boolean)
    if (!parts.length) parts.push('')
    let siblings = roots
    for (let i = 0; i < parts.length; i++) {
      const segments = parts.slice(0, i + 1), key = JSON.stringify(segments)
      let node = index.get(key)
      if (!node) {
        node = { label: parts[i]!, path: segments.join('/'), key, segments, pageIds: [], children: [], descendantPageCount: 0 }
        index.set(key, node)
        siblings.push(node)
      }
      if (i === parts.length - 1) node.pageIds.push(page.id)
      siblings = node.children
    }
  }
  const pending = [roots], order: WikiTreeNode[] = []
  while (pending.length) {
    const siblings = pending.pop()!
    siblings.sort((a, b) => compareWikiNames(a.label, b.label))
    for (const node of siblings) {
      node.pageIds.sort((a, b) => a - b)
      order.push(node)
      if (node.children.length) pending.push(node.children)
    }
  }
  for (let i = order.length - 1; i >= 0; i--)
    order[i]!.descendantPageCount = order[i]!.children.reduce((n, child) => n + child.pageIds.length + child.descendantPageCount, 0)
  return roots
}
export function flattenWikiTree(nodes: WikiTreeNode[], collapsed: ReadonlySet<string> = new Set()): WikiTreeRow[] {
  const rows: WikiTreeRow[] = []
  function group(nodes: WikiTreeNode[], parentRowKey: string | null, level: number): WikiTreeRow[] {
    const siblings = nodes.flatMap(node => (node.pageIds.length ? node.pageIds : [0]).map((pageId, i) => ({
      rowKey: pageId ? `p:${pageId}` : `d:${node.key}`, node, parentRowKey, level,
      posinset: 0, setsize: 0, pageId, hasChildren: i === 0 && !!node.children.length,
    })))
    siblings.forEach((row, i) => { row.posinset = i + 1; row.setsize = siblings.length })
    return siblings
  }
  const pending = group(nodes, null, 1).reverse()
  while (pending.length) {
    const row = pending.pop()!
    rows.push(row)
    if (row.hasChildren && !collapsed.has(row.node.key)) {
      const children = group(row.node.children, row.rowKey, row.level + 1)
      for (let i = children.length - 1; i >= 0; i--) pending.push(children[i]!)
    }
  }
  return rows
}
export function indexWikiTree(nodes: WikiTreeNode[]) {
  const rows = flattenWikiTree(nodes)
  return {
    rows: new Map(rows.map(row => [row.rowKey, row])),
    pages: new Map(rows.filter(row => row.pageId).map(row => [row.pageId, row])),
    branches: new Set(rows.filter(row => row.hasChildren).map(row => row.node.key)),
    keys: new Set(rows.map(row => row.node.key)),
  }
}
/** 行がはみ出す分だけ所有コンテナを動かす。 */
export function revealWikiRow(port: HTMLElement, row: HTMLElement) {
  const box = port.getBoundingClientRect(), target = row.getBoundingClientRect()
  if (target.top < box.top) port.scrollTop += target.top - box.top
  else if (target.bottom > box.bottom) port.scrollTop += target.bottom - box.bottom
}
