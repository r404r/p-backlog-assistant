/** 一括処理のAPI呼出間隔。Go側 internal/bulk の1秒ペーシングと同じ値。 */
export const BULK_API_INTERVAL_SECONDS = 1

function rowCount(value: number): number {
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
}

/**
 * 一括処理の基準所要秒数を返す。
 * 新規はPOST 1回、更新は競合確認GET + PATCHの2回を1行ごとに行う。
 * 通信時間・レート制限待ちは含まないため、UIでは「最低目安」として表示する。
 */
export function estimateBulkSeconds(creates: number, updates: number): number {
  const apiCalls = rowCount(creates) + rowCount(updates) * 2
  return apiCalls * BULK_API_INTERVAL_SECONDS
}

/** 内訳不明のジョブについて、全件新規〜全件更新の基準所要秒数を返す。 */
export function estimateBulkSecondsRange(total: number): { min: number; max: number } {
  const rows = rowCount(total)
  return {
    min: estimateBulkSeconds(rows, 0),
    max: estimateBulkSeconds(0, rows),
  }
}
