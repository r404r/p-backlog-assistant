/** カスタム属性列の列キーの接頭辞(Go側 export パッケージの規約と対)。 */
export const CUSTOM_COLUMN_PREFIX = 'cf_'

export function customColumnKey(defId: number): string {
  return `${CUSTOM_COLUMN_PREFIX}${defId}`
}

// 旧バインディングと開発用モックの契約フィールドを埋めるための日本語fallback。
// 画面表示は enumLabels.ts で機械値を翻訳する。
export const ACTION_LABELS: Readonly<Record<string, string>> = {
  create: '新規追加',
  update: '更新',
  skip: '変更なし',
}

const ROW_STATUS_LABELS: Readonly<Record<string, string>> = {
  pending: '未処理',
  sending: '送信中(結果未確認)',
  done: '完了',
  error: '失敗',
  conflict: '競合',
  skip: '変更なし',
}

export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

export function rowStatusLabel(status: string): string {
  return ROW_STATUS_LABELS[status] ?? status
}
