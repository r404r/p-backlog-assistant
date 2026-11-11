export interface WikiTreeNode {
  label: string
  path: string
  pageIds: number[]
  children: WikiTreeNode[]
}

/** 階層は論理名のまま扱う。出力用のパス安全化はバックエンドに委ねる。 */
export function buildWikiTree(
  pages: readonly { id: number; name: string }[],
): WikiTreeNode[] {
  const roots: WikiTreeNode[] = []
  for (const page of [...pages].sort((a, b) => a.id - b.id)) {
    let nodes = roots
    let path = ''
    const parts = page.name.split('/').filter(Boolean)
    if (!parts.length) parts.push('')
    for (let i = 0; i < parts.length; i++) {
      const label = parts[i]!
      path = path ? `${path}/${label}` : label
      let node = nodes.find((n) => n.label === label)
      if (!node) {
        node = { label, path, pageIds: [], children: [] }
        nodes.push(node)
      }
      if (i === parts.length - 1) node.pageIds.push(page.id)
      nodes = node.children
    }
  }
  return roots
}
