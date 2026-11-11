import MarkdownIt, { type MarkdownIt as MarkdownParser } from 'markdown-it'

/** 標準パーサと同じく、エスケープされたパイプは列に数えない。 */
function cells(line: string): string[] {
  const result: string[] = []
  let start = 0
  for (let i = 0; i < line.length; i++) {
    if (line[i] === '|' && (i === 0 || line[i - 1] !== '\\')) {
      result.push(line.slice(start, i))
      start = i + 1
    }
  }
  result.push(line.slice(start))
  if (result[0] === '') result.shift()
  if (result[result.length - 1] === '') result.pop()
  return result
}

/**
 * 古いテンプレートの「区切り列だけ不足する表」を表示時に補う。
 * 標準 table ルールを公開 API で取り出して包み、コードや原文の一括置換はしない。
 */
export function useBacklogTables(md: MarkdownParser): void {
  const tableOnly = new MarkdownIt('zero')
  tableOnly.block.ruler.enableOnly(['table'])
  const [standardTable] = tableOnly.block.ruler.getRules('')

  md.block.ruler.at('table', (state, startLine, endLine, silent) => {
    if (standardTable(state, startLine, endLine, silent)) return true
    // 互換構文で既存の段落を分断しない（複数行インラインコードも保護する）。
    if (silent) return false
    if (startLine + 2 >= endLine) return false
    const line = (n: number) => state.src.slice(state.bMarks[n] + state.tShift[n], state.eMarks[n]).trim()
    const header = line(startLine)
    const separator = cells(line(startLine + 1))
    const columns = cells(header).length
    // 不完全なインラインコードや罫線、データ列の不一致は推測しない。
    if (header.includes('`') || separator.length < 2 || separator.length >= columns) return false
    if (!separator.every((cell) => /^:?-{3,}:?$/.test(cell.trim()))) return false
    if (cells(line(startLine + 2)).length !== columns) return false
    for (let n = startLine; n <= startLine + 2; n++) {
      if (state.sCount[n] < state.blkIndent || state.sCount[n] - state.blkIndent >= 4) return false
    }

    // 区切り行の参照位置だけを一時的に差し替え、解析後は必ず戻す。
    // inline 解析用の内容・行番号と、他のブロックの位置は変更しない。
    const row = startLine + 1
    const source = state.src
    const begin = state.bMarks[row]
    const end = state.eMarks[row]
    const shift = state.tShift[row]
    const repaired = [...separator.map((cell) => cell.trim()), ...Array<string>(columns - separator.length).fill('---')].join(' | ')
    state.src = `${source}\n${repaired}`
    state.bMarks[row] = source.length + 1
    state.eMarks[row] = state.src.length
    state.tShift[row] = 0
    try {
      return standardTable(state, startLine, endLine, silent)
    } finally {
      state.src = source
      state.bMarks[row] = begin
      state.eMarks[row] = end
      state.tShift[row] = shift
    }
  }, { alt: ['paragraph', 'reference'] })
}
