/**
 * backend.ts の純ヘルパのテスト(R15)。
 *
 * backend.ts 全体は Wails バインディングのラッパーだが、モジュールの読み込み時に
 * window へ触れる処理は無く(ランタイム探索はすべて関数の内側)、他モジュールへの
 * import も持たないため、純ヘルパだけを取り出すリファクタをせずそのまま import する。
 *
 * 数値の桁区切り(toLocaleString)は実行環境のロケールに依存するため、
 * 4 桁以上の値を含む期待値はテスト側でも toLocaleString を通して比較する。
 */
import { describe, expect, it } from 'vitest'
import {
  CUSTOM_COLUMN_PREFIX,
  actionLabel,
  customColumnKey,
  formatSyncProgress,
  newSyncRunId,
  rowStatusLabel,
  type SyncPhase,
  type SyncProgress,
} from './backend'

/** 進捗イベントを組み立てる(既定値は「取得中・総件数不明」) */
function progress(over: Partial<SyncProgress> & { phase: SyncPhase }): SyncProgress {
  return { runId: 'run-1', profileId: 'p1', projectId: 1, fetched: 0, total: 0, ...over }
}

describe('formatSyncProgress', () => {
  it('総件数の確認中', () => {
    expect(formatSyncProgress(progress({ phase: 'count' }))).toBe('総件数を確認中...')
  })

  it('取得中は総件数が分かっていれば分母も出す', () => {
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched: 30, total: 120 }))).toBe(
      '取得中 30 / 120 件',
    )
  })

  it('取得中で総件数が不明なら分母を出さない(0 件中と誤解させないため)', () => {
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched: 30, total: 0 }))).toBe(
      '取得中 30 件',
    )
  })

  it('4 桁以上は桁区切りを入れる', () => {
    const fetched = 12345
    const total = 67890
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched, total }))).toBe(
      `取得中 ${fetched.toLocaleString()} / ${total.toLocaleString()} 件`,
    )
  })

  it('削除検知中', () => {
    expect(formatSyncProgress(progress({ phase: 'deleteScan', fetched: 120 }))).toBe(
      '削除された課題を確認中(120 件取得済み)',
    )
  })

  it('完了', () => {
    expect(formatSyncProgress(progress({ phase: 'done', fetched: 120 }))).toBe(
      '取得完了 120 件(仕上げ中...)',
    )
  })

  it('未知のフェーズは空文字(表示しない)', () => {
    const unknown = progress({ phase: 'unknown' as SyncPhase })
    expect(formatSyncProgress(unknown)).toBe('')
  })
})

describe('customColumnKey', () => {
  it('Go 側の規約 cf_{定義ID} と同じ列キーを作る', () => {
    expect(customColumnKey(12)).toBe('cf_12')
    expect(customColumnKey(0)).toBe('cf_0')
  })

  it('接頭辞は公開定数と一致する', () => {
    expect(CUSTOM_COLUMN_PREFIX).toBe('cf_')
    expect(customColumnKey(9).startsWith(CUSTOM_COLUMN_PREFIX)).toBe(true)
  })
})

describe('actionLabel', () => {
  it('既知の処理区分は日本語の表示名にする', () => {
    expect(actionLabel('create')).toBe('新規追加')
    expect(actionLabel('update')).toBe('更新')
    expect(actionLabel('skip')).toBe('変更なし')
  })

  it('未知の値はそのまま返す', () => {
    expect(actionLabel('unknown')).toBe('unknown')
    expect(actionLabel('')).toBe('')
  })
})

describe('rowStatusLabel', () => {
  it('既知の行状態は日本語の表示名にする', () => {
    expect(rowStatusLabel('pending')).toBe('未処理')
    expect(rowStatusLabel('sending')).toBe('送信中(結果未確認)')
    expect(rowStatusLabel('done')).toBe('完了')
    expect(rowStatusLabel('error')).toBe('失敗')
    expect(rowStatusLabel('conflict')).toBe('競合')
    expect(rowStatusLabel('skip')).toBe('変更なし')
  })

  it('未知の値はそのまま返す', () => {
    expect(rowStatusLabel('unknown')).toBe('unknown')
    expect(rowStatusLabel('')).toBe('')
  })
})

describe('newSyncRunId', () => {
  it('sync- で始まる ID を返す', () => {
    expect(newSyncRunId()).toMatch(/^sync-\d+-\d+-[0-9a-z]+$/)
  })

  it('呼び出すたびに異なる ID を返す', () => {
    const ids = new Set(Array.from({ length: 100 }, () => newSyncRunId()))
    expect(ids.size).toBe(100)
  })
})
