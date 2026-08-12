/**
 * syncState.ts のテスト(R15)。
 *
 * 進捗イベントの購読元(backend.ts の onSyncProgress)はテストから制御したいので
 * モジュールごとモックする。実物は Wails ランタイムの有無で購読先が変わるため、
 * ここで実物を使うと「どちらの経路を試したか」が環境依存になってしまう。
 *
 * モジュールレベルの状態を持つため、各テストは vi.resetModules() + 動的 import で
 * まっさらな状態から始める。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { SyncPhase, SyncProgress } from './backend'

/** モックした onSyncProgress が受け取ったコールバック(購読解除済みも含む) */
const listeners: { cb: (p: SyncProgress) => void; unsubscribed: boolean }[] = []

vi.mock('./backend', () => ({
  onSyncProgress: (cb: (p: SyncProgress) => void) => {
    const entry = { cb, unsubscribed: false }
    listeners.push(entry)
    return () => {
      entry.unsubscribed = true
    }
  },
}))

/** 現在有効な購読の数 */
function activeSubscriptions(): number {
  return listeners.filter((l) => !l.unsubscribed).length
}

/** 有効な購読すべてへ進捗イベントを配る */
function emit(runId: string, phase: SyncPhase): void {
  const p: SyncProgress = { runId, profileId: 'p1', projectId: 1, phase, fetched: 0, total: 0 }
  for (const l of listeners) {
    if (!l.unsubscribed) l.cb(p)
  }
}

/** モジュールレベルの状態をリセットしたうえで読み込み直す */
async function freshModule() {
  vi.resetModules()
  listeners.length = 0
  return await import('./syncState')
}

describe('syncState', () => {
  beforeEach(() => {
    listeners.length = 0
  })

  it('初期状態は非実行中', async () => {
    const m = await freshModule()
    expect(m.activeIssueSync.value).toBeNull()
    expect(m.issueSyncRunning.value).toBe(false)
  })

  it('beginIssueSync で実行中になり、識別情報を保持する', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    expect(m.issueSyncRunning.value).toBe(true)
    expect(m.activeIssueSync.value).toEqual({ profileId: 'p1', projectId: 100, runId: 'run-1' })
  })

  it('beginIssueSync で進捗イベントを購読する', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    expect(activeSubscriptions()).toBe(1)
  })

  it('endIssueSync は runId が一致する場合だけ解除する', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    m.endIssueSync('run-other')
    expect(m.issueSyncRunning.value).toBe(true)
    m.endIssueSync('run-1')
    expect(m.issueSyncRunning.value).toBe(false)
    expect(m.activeIssueSync.value).toBeNull()
  })

  it('解除時に進捗イベントの購読も解除する', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    m.endIssueSync('run-1')
    expect(activeSubscriptions()).toBe(0)
  })

  it('endIssueSync は冪等(非実行中に呼んでも何も起きない)', async () => {
    const m = await freshModule()
    m.endIssueSync('run-1')
    expect(m.issueSyncRunning.value).toBe(false)
    m.beginIssueSync('p1', 100, 'run-1')
    m.endIssueSync('run-1')
    m.endIssueSync('run-1')
    expect(m.issueSyncRunning.value).toBe(false)
    expect(activeSubscriptions()).toBe(0)
  })

  it('連続した beginIssueSync は最新の実行で上書きし、購読は 1 つだけ持つ', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    m.beginIssueSync('p2', 200, 'run-2')
    expect(m.activeIssueSync.value).toEqual({ profileId: 'p2', projectId: 200, runId: 'run-2' })
    expect(activeSubscriptions()).toBe(1)
  })

  it('上書き後は古い runId では解除できない', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    m.beginIssueSync('p2', 200, 'run-2')
    m.endIssueSync('run-1')
    expect(m.issueSyncRunning.value).toBe(true)
    m.endIssueSync('run-2')
    expect(m.issueSyncRunning.value).toBe(false)
  })

  it('done イベント(runId 一致)で解除する(保険経路)', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    emit('run-1', 'done')
    expect(m.issueSyncRunning.value).toBe(false)
    expect(activeSubscriptions()).toBe(0)
  })

  it('done 以外のフェーズでは解除しない', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    emit('run-1', 'count')
    emit('run-1', 'fetch')
    emit('run-1', 'deleteScan')
    expect(m.issueSyncRunning.value).toBe(true)
  })

  it('別の実行の done イベントでは解除しない', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    emit('run-other', 'done')
    expect(m.issueSyncRunning.value).toBe(true)
  })

  it('解除後に再開すると購読し直す', async () => {
    const m = await freshModule()
    m.beginIssueSync('p1', 100, 'run-1')
    m.endIssueSync('run-1')
    m.beginIssueSync('p1', 100, 'run-2')
    expect(activeSubscriptions()).toBe(1)
    emit('run-2', 'done')
    expect(m.issueSyncRunning.value).toBe(false)
  })
})
