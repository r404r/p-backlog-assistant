/** Wiki パネルの表示設定。保存不可でも画面再訪時の状態を維持する。 */
const STORAGE_KEY = 'ba.wikiPanelCollapsed'
let sessionCollapsed: boolean | undefined

export function loadWikiPanelCollapsed(): boolean {
  if (sessionCollapsed !== undefined) return sessionCollapsed
  try {
    return localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

export function saveWikiPanelCollapsed(collapsed: boolean): void {
  sessionCollapsed = collapsed
  try {
    localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0')
  } catch {
    // WebView の保存制限時はセッション内の記憶だけを使う。
  }
}
