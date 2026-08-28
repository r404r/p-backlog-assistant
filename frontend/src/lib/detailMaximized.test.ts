/**
 * detailMaximized.ts のテスト。
 *
 * 課題詳細ダイアログの最大化状態は「保存できなくても・壊れていても表示は成立する」
 * ことが要件(設計 §3)。未設定・不正値・localStorage の例外がすべて
 * 既定(非最大化)へ縮退することを、純関数と localStorage 入出力の両面で確かめる。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DETAIL_MAXIMIZED_KEY,
  loadDetailMaximized,
  parseStoredDetailMaximized,
  resetDetailMaximizedStorageState,
  saveDetailMaximized,
} from './detailMaximized'

describe('parseStoredDetailMaximized', () => {
  it("'1' だけを最大化として扱う", () => {
    expect(parseStoredDetailMaximized('1')).toBe(true)
  })

  it("'0' は非最大化", () => {
    expect(parseStoredDetailMaximized('0')).toBe(false)
  })

  it('未保存(null)・空文字は非最大化(既定)', () => {
    expect(parseStoredDetailMaximized(null)).toBe(false)
    expect(parseStoredDetailMaximized('')).toBe(false)
  })

  it('不正値はすべて非最大化へ縮退する', () => {
    for (const raw of ['true', 'yes', '2', '01', ' 1', 'null', '{}']) {
      expect(parseStoredDetailMaximized(raw), raw).toBe(false)
    }
  })
})

describe('loadDetailMaximized / saveDetailMaximized', () => {
  beforeEach(() => {
    localStorage.clear()
    resetDetailMaximizedStorageState()
  })

  it('保存値が無ければ非最大化', () => {
    expect(loadDetailMaximized()).toBe(false)
  })

  it("最大化を '1'、復元を '0' で保存し、そのまま復元する", () => {
    saveDetailMaximized(true)
    expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('1')
    expect(loadDetailMaximized()).toBe(true)

    saveDetailMaximized(false)
    expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('0')
    expect(loadDetailMaximized()).toBe(false)
  })

  it('保存された値が不正なら非最大化で開く', () => {
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, 'true')
    expect(loadDetailMaximized()).toBe(false)
  })

  it('localStorage が例外を投げても非最大化で継続する', () => {
    const getItem = vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    try {
      expect(loadDetailMaximized()).toBe(false)
      expect(() => saveDetailMaximized(true)).not.toThrow()
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })

  it('保存に失敗したら、以降は読める古い値も信用せず既定へ縮退する', () => {
    // 読み込みは成功するが保存だけ失敗する環境(クォータ超過・読み取り専用ストレージ)。
    // そのままだと「復元したのに古い '1' が残る」ため、開き直したときに最大化が
    // 復活してしまう(レビュー 1 回目 指摘 2)。書込に失敗した時点で記憶が
    // 成立しなくなったとみなし、設計どおり既定(非最大化)へ縮退する。
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, '1')
    expect(loadDetailMaximized()).toBe(true)

    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('quota exceeded')
    })
    try {
      saveDetailMaximized(false)
      // 保存値は古いままだが、読み出しは既定へ縮退する
      expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('1')
      expect(loadDetailMaximized()).toBe(false)
    } finally {
      setItem.mockRestore()
    }

    // 縮退はセッション内の判定。リセットすれば通常の読み出しに戻る
    resetDetailMaximizedStorageState()
    expect(loadDetailMaximized()).toBe(true)
  })

  it('保存に成功している間は縮退しない', () => {
    saveDetailMaximized(true)
    expect(loadDetailMaximized()).toBe(true)
  })
})
