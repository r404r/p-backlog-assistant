/**
 * format.ts のテスト(R15)。
 *
 * 日時の整形はタイムゾーン依存になりやすいため、入力はローカル時刻から
 * 組み立てた ISO 文字列を使い、期待値は固定文字列で書く
 * (どのタイムゾーンで実行しても同じ結果になる)。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import { errorMessage, formatDateTime, formatElapsed, syncModeLabel } from './format'

describe('errorMessage', () => {
  it('Error のメッセージを返す', () => {
    expect(errorMessage(new Error('接続に失敗しました'))).toBe('接続に失敗しました')
  })

  it('Error のサブクラスも message を返す', () => {
    class ApiError extends Error {}
    expect(errorMessage(new ApiError('401'))).toBe('401')
  })

  it('Error 以外は文字列化する', () => {
    expect(errorMessage('文字列のエラー')).toBe('文字列のエラー')
    expect(errorMessage(404)).toBe('404')
    expect(errorMessage(null)).toBe('null')
    expect(errorMessage(undefined)).toBe('undefined')
  })
})

describe('formatDateTime', () => {
  it('空文字はそのまま返す', () => {
    expect(formatDateTime('')).toBe('')
  })

  it('解釈できない値は元の文字列を返す(欠損と破損を区別するため)', () => {
    expect(formatDateTime('not-a-date')).toBe('not-a-date')
  })

  it('ローカル時刻の YYYY-MM-DD HH:mm に整形する', () => {
    const d = new Date(2026, 7, 12, 14, 30) // 2026-08-12 14:30(ローカル)
    expect(formatDateTime(d.toISOString())).toBe('2026-08-12 14:30')
  })

  it('月・日・時・分を 2 桁へゼロ詰めする', () => {
    const d = new Date(2026, 0, 5, 9, 7) // 2026-01-05 09:07(ローカル)
    expect(formatDateTime(d.toISOString())).toBe('2026-01-05 09:07')
  })
})

describe('formatElapsed', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  /** 現在時刻を固定し、そこから min 分前の ISO 文字列を返す */
  function isoMinutesAgo(min: number): string {
    const now = new Date(2026, 7, 12, 12, 0, 0)
    vi.useFakeTimers()
    vi.setSystemTime(now)
    return new Date(now.getTime() - min * 60_000).toISOString()
  }

  it('解釈できない値は空文字を返す', () => {
    expect(formatElapsed('not-a-date')).toBe('')
    expect(formatElapsed('')).toBe('')
  })

  it('1 分未満は「たった今」', () => {
    expect(formatElapsed(isoMinutesAgo(0))).toBe('たった今')
    expect(formatElapsed(isoMinutesAgo(0.5))).toBe('たった今')
  })

  it('未来の時刻も「たった今」にまるめる', () => {
    expect(formatElapsed(isoMinutesAgo(-10))).toBe('たった今')
  })

  it('60 分未満は分で表す', () => {
    expect(formatElapsed(isoMinutesAgo(1))).toBe('1 分前')
    expect(formatElapsed(isoMinutesAgo(59))).toBe('59 分前')
  })

  it('24 時間未満は時間で表す', () => {
    expect(formatElapsed(isoMinutesAgo(60))).toBe('1 時間前')
    expect(formatElapsed(isoMinutesAgo(60 * 23))).toBe('23 時間前')
  })

  it('24 時間以上は日で表す', () => {
    expect(formatElapsed(isoMinutesAgo(60 * 24))).toBe('1 日前')
    expect(formatElapsed(isoMinutesAgo(60 * 24 * 30))).toBe('30 日前')
  })
})

describe('syncModeLabel', () => {
  it('full はフル同期', () => {
    expect(syncModeLabel('full')).toBe('フル同期')
  })

  it('full 以外は差分同期として扱う(Go 側が auto を解決済みのため)', () => {
    expect(syncModeLabel('incremental')).toBe('差分同期')
    expect(syncModeLabel('auto')).toBe('差分同期')
    expect(syncModeLabel('')).toBe('差分同期')
  })
})
