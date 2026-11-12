import { highlightCode } from './markdownHighlight'
self.onmessage = (event: MessageEvent<{ index: number; source: string; language: string }>) => {
  const { index, source, language } = event.data
  try {
    self.postMessage({ index, html: highlightCode(source, language) })
  } catch {
    self.postMessage({ index, html: '' })
  }
}
self.postMessage({ ready: true })
