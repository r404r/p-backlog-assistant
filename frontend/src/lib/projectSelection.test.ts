/**
 * projectSelection.ts のテスト(R15)。
 *
 * 純粋関数(projectSelectionKey / parseStoredProjectId / resolveProjectSelection)に加え、
 * localStorage への保存・復元も検証する(vitest の happy-dom 環境を利用)。
 * 保存・復元はモジュールレベルの状態(loadedProfileId)を持つため、
 * 状態を持つテストは vi.resetModules() + 動的 import で毎回まっさらな状態から始める。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { parseStoredProjectId, projectSelectionKey, resolveProjectSelection } from './projectSelection'

describe('projectSelectionKey', () => {
  it('プロファイル ID ごとに別のキーになる', () => {
    expect(projectSelectionKey('p1')).toBe('ba.selectedProjectId.p1')
    expect(projectSelectionKey('p2')).toBe('ba.selectedProjectId.p2')
    expect(projectSelectionKey('p1')).not.toBe(projectSelectionKey('p2'))
  })
})

describe('parseStoredProjectId', () => {
  it('未保存(null)・空文字は 0(未選択)', () => {
    expect(parseStoredProjectId(null)).toBe(0)
    expect(parseStoredProjectId('')).toBe(0)
  })

  it('正の整数はそのまま返す', () => {
    expect(parseStoredProjectId('1')).toBe(1)
    expect(parseStoredProjectId('123456')).toBe(123456)
  })

  it('0 以下・非数・小数・安全でない整数は 0 にする', () => {
    expect(parseStoredProjectId('0')).toBe(0)
    expect(parseStoredProjectId('-5')).toBe(0)
    expect(parseStoredProjectId('abc')).toBe(0)
    expect(parseStoredProjectId('12.5')).toBe(0)
    expect(parseStoredProjectId('9007199254740993')).toBe(0)
  })
})

describe('resolveProjectSelection', () => {
  const projects = [{ id: 10 }, { id: 20 }, { id: 30 }]

  it('一覧に含まれる選択はそのまま維持する', () => {
    expect(resolveProjectSelection(projects, 20)).toBe(20)
  })

  it('一覧に無い選択は先頭へフォールバックする', () => {
    expect(resolveProjectSelection(projects, 99)).toBe(10)
  })

  it('未選択(0)は先頭を選ぶ', () => {
    expect(resolveProjectSelection(projects, 0)).toBe(10)
  })

  it('一覧が空なら 0(未選択)', () => {
    expect(resolveProjectSelection([], 20)).toBe(0)
    expect(resolveProjectSelection([], 0)).toBe(0)
  })
})

describe('restoreProjectSelection と保存', () => {
  /** モジュールレベルの状態をリセットしたうえで読み込み直す */
  async function freshModule() {
    vi.resetModules()
    return await import('./projectSelection')
  }

  beforeEach(() => {
    localStorage.clear()
  })

  it('保存値が無ければ 0(未選択)のまま', async () => {
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    expect(m.selectedProjectId.value).toBe(0)
  })

  it('保存値を復元する', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    expect(m.selectedProjectId.value).toBe(42)
  })

  it('プロファイル ID が空なら何もしない', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.selectedProjectId.value = 7
    m.restoreProjectSelection('')
    expect(m.selectedProjectId.value).toBe(7)
  })

  it('選択の変更をプロファイルごとのキーへ保存する', async () => {
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    m.selectedProjectId.value = 55
    await nextTick()
    expect(localStorage.getItem(projectSelectionKey('p1'))).toBe('55')
  })

  it('未選択(0)は保存しない(一時的な取得失敗で保存値を消さないため)', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    m.selectedProjectId.value = 0
    await nextTick()
    expect(localStorage.getItem(projectSelectionKey('p1'))).toBe('42')
  })

  it('復元前(プロファイル未確定)は保存しない', async () => {
    const m = await freshModule()
    m.selectedProjectId.value = 55
    await nextTick()
    expect(localStorage.getItem(projectSelectionKey('p1'))).toBeNull()
  })

  it('同じプロファイルで選択済みなら保存値へ戻さない(画面切替で選択が巻き戻らないため)', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    m.selectedProjectId.value = 55
    await nextTick()
    m.restoreProjectSelection('p1')
    expect(m.selectedProjectId.value).toBe(55)
  })

  it('プロファイルが変わったら切替先の保存値へ差し替える', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    localStorage.setItem(projectSelectionKey('p2'), '77')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    expect(m.selectedProjectId.value).toBe(42)
    m.restoreProjectSelection('p2')
    expect(m.selectedProjectId.value).toBe(77)
  })

  it('切替先に保存値が無ければ 0 にする(前のプロファイルの選択を持ち越さない)', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    m.restoreProjectSelection('p2')
    expect(m.selectedProjectId.value).toBe(0)
  })

  it('プロファイル切替後の保存は切替先のキーへ書く', async () => {
    localStorage.setItem(projectSelectionKey('p1'), '42')
    const m = await freshModule()
    m.restoreProjectSelection('p1')
    m.restoreProjectSelection('p2')
    m.selectedProjectId.value = 88
    await nextTick()
    expect(localStorage.getItem(projectSelectionKey('p2'))).toBe('88')
    expect(localStorage.getItem(projectSelectionKey('p1'))).toBe('42')
  })

  it('localStorage が例外を投げても復元・保存は失敗しない', async () => {
    // Storage.prototype ではなく localStorage の実体へ差し込む
    // (DOM 実装によって getItem が prototype 側か実体側かが異なるため)
    const getItem = vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    try {
      const m = await freshModule()
      expect(() => m.restoreProjectSelection('p1')).not.toThrow()
      expect(m.selectedProjectId.value).toBe(0)
      m.selectedProjectId.value = 3
      await expect(nextTick()).resolves.toBeUndefined()
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })
})
