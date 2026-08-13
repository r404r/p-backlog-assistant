/**
 * sidebarWidth.ts のテスト。
 *
 * 純粋関数(clampSidebarWidth / resolveDragWidth / parseStoredSidebarWidth)に加え、
 * localStorage への保存・復元も検証する(vitest の happy-dom 環境を利用)。
 * ポインタ操作そのもの(App.vue のドラッグハンドル)は TDD 例外(GUI)として手動確認で担保し、
 * ここでは幅の決定ロジックだけを対象にする。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  COLLAPSE_SIDEBAR_WIDTH,
  DEFAULT_SIDEBAR_WIDTH,
  MAX_SIDEBAR_WIDTH,
  MIN_SIDEBAR_WIDTH,
  SIDEBAR_WIDTH_KEY,
  clampSidebarWidth,
  loadSidebarWidth,
  parseStoredSidebarWidth,
  resolveDragWidth,
  saveSidebarWidth,
} from './sidebarWidth'

describe('定数', () => {
  it('折りたたみしきい値 < 最小幅 < 既定幅 < 最大幅 の順になっている', () => {
    expect(COLLAPSE_SIDEBAR_WIDTH).toBeLessThan(MIN_SIDEBAR_WIDTH)
    expect(MIN_SIDEBAR_WIDTH).toBeLessThan(DEFAULT_SIDEBAR_WIDTH)
    expect(DEFAULT_SIDEBAR_WIDTH).toBeLessThan(MAX_SIDEBAR_WIDTH)
  })
})

describe('clampSidebarWidth', () => {
  it('範囲内の幅はそのまま返す', () => {
    expect(clampSidebarWidth(MIN_SIDEBAR_WIDTH)).toBe(MIN_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(DEFAULT_SIDEBAR_WIDTH)).toBe(DEFAULT_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(MAX_SIDEBAR_WIDTH)).toBe(MAX_SIDEBAR_WIDTH)
  })

  it('範囲外の幅は最小・最大へ丸める', () => {
    expect(clampSidebarWidth(0)).toBe(MIN_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(MIN_SIDEBAR_WIDTH - 1)).toBe(MIN_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(MAX_SIDEBAR_WIDTH + 1)).toBe(MAX_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(10000)).toBe(MAX_SIDEBAR_WIDTH)
  })

  it('小数は整数へ丸める(1px 未満のずれで再描画が続かないように)', () => {
    expect(clampSidebarWidth(200.4)).toBe(200)
    expect(clampSidebarWidth(200.6)).toBe(201)
  })

  it('数値でない値・NaN は既定幅にする', () => {
    expect(clampSidebarWidth(Number.NaN)).toBe(DEFAULT_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(Number.POSITIVE_INFINITY)).toBe(MAX_SIDEBAR_WIDTH)
    expect(clampSidebarWidth(Number.NEGATIVE_INFINITY)).toBe(MIN_SIDEBAR_WIDTH)
  })
})

describe('resolveDragWidth', () => {
  it('しきい値以上ならクランプした幅で展開する', () => {
    expect(resolveDragWidth(DEFAULT_SIDEBAR_WIDTH)).toEqual({
      collapsed: false,
      width: DEFAULT_SIDEBAR_WIDTH,
    })
    expect(resolveDragWidth(MAX_SIDEBAR_WIDTH + 100)).toEqual({
      collapsed: false,
      width: MAX_SIDEBAR_WIDTH,
    })
  })

  it('しきい値と最小幅の間は展開したまま最小幅にする', () => {
    expect(resolveDragWidth(COLLAPSE_SIDEBAR_WIDTH)).toEqual({
      collapsed: false,
      width: MIN_SIDEBAR_WIDTH,
    })
    expect(resolveDragWidth(MIN_SIDEBAR_WIDTH - 1)).toEqual({
      collapsed: false,
      width: MIN_SIDEBAR_WIDTH,
    })
  })

  it('しきい値未満は折りたたむ(展開時の幅は最小幅を保持する)', () => {
    expect(resolveDragWidth(COLLAPSE_SIDEBAR_WIDTH - 1)).toEqual({
      collapsed: true,
      width: MIN_SIDEBAR_WIDTH,
    })
    expect(resolveDragWidth(0)).toEqual({ collapsed: true, width: MIN_SIDEBAR_WIDTH })
    expect(resolveDragWidth(-50)).toEqual({ collapsed: true, width: MIN_SIDEBAR_WIDTH })
  })

  it('NaN は既定幅で展開する(異常値で折りたたまれないように)', () => {
    expect(resolveDragWidth(Number.NaN)).toEqual({
      collapsed: false,
      width: DEFAULT_SIDEBAR_WIDTH,
    })
  })
})

describe('parseStoredSidebarWidth', () => {
  it('未保存(null)・空文字・非数は既定幅', () => {
    expect(parseStoredSidebarWidth(null)).toBe(DEFAULT_SIDEBAR_WIDTH)
    expect(parseStoredSidebarWidth('')).toBe(DEFAULT_SIDEBAR_WIDTH)
    expect(parseStoredSidebarWidth('abc')).toBe(DEFAULT_SIDEBAR_WIDTH)
  })

  it('保存値はクランプして返す', () => {
    expect(parseStoredSidebarWidth('240')).toBe(240)
    expect(parseStoredSidebarWidth('10')).toBe(MIN_SIDEBAR_WIDTH)
    expect(parseStoredSidebarWidth('9999')).toBe(MAX_SIDEBAR_WIDTH)
    expect(parseStoredSidebarWidth('240.7')).toBe(241)
  })
})

describe('loadSidebarWidth / saveSidebarWidth', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('保存値が無ければ既定幅', () => {
    expect(loadSidebarWidth()).toBe(DEFAULT_SIDEBAR_WIDTH)
  })

  it('保存した幅を復元する', () => {
    saveSidebarWidth(280)
    expect(localStorage.getItem(SIDEBAR_WIDTH_KEY)).toBe('280')
    expect(loadSidebarWidth()).toBe(280)
  })

  it('範囲外の値を保存してもクランプされる', () => {
    saveSidebarWidth(9999)
    expect(loadSidebarWidth()).toBe(MAX_SIDEBAR_WIDTH)
  })

  it('localStorage が例外を投げても既定幅で継続する', () => {
    const getItem = vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    try {
      expect(loadSidebarWidth()).toBe(DEFAULT_SIDEBAR_WIDTH)
      expect(() => saveSidebarWidth(240)).not.toThrow()
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })
})
