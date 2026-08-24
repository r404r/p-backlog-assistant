import { describe, expect, it } from 'vitest'
import { estimateBulkSeconds, estimateBulkSecondsRange } from './bulkEstimate'

describe('estimateBulkSeconds', () => {
  it('新規追加は1行1 API呼出、更新は確認GETと書込の2 API呼出で見積もる', () => {
    expect(estimateBulkSeconds(1000, 0)).toBe(1000)
    expect(estimateBulkSeconds(0, 1000)).toBe(2000)
    expect(estimateBulkSeconds(500, 500)).toBe(1500)
  })

  it('負数・非有限値は0件として扱う', () => {
    expect(estimateBulkSeconds(-1, Number.NaN)).toBe(0)
    expect(estimateBulkSeconds(Number.POSITIVE_INFINITY, 1)).toBe(2)
  })
})

describe('estimateBulkSecondsRange', () => {
  it('内訳不明では全件新規から全件更新までの範囲を返す', () => {
    expect(estimateBulkSecondsRange(1000)).toEqual({ min: 1000, max: 2000 })
  })

  it('0件以下では範囲を返さない', () => {
    expect(estimateBulkSecondsRange(0)).toEqual({ min: 0, max: 0 })
    expect(estimateBulkSecondsRange(-10)).toEqual({ min: 0, max: 0 })
  })
})
