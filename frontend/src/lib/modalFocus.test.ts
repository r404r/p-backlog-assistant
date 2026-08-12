/**
 * モーダルのフォーカス制御(modalFocus.ts)の純ヘルパのテスト。
 *
 * Vue のライフサイクルを伴う useModalFocus 本体は画面結合のため手動確認とし
 * (TDD 例外: GUI)、フォーカス可能要素の抽出と Tab の循環判定だけを固定する。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { focusableElementsIn, trapTabKey } from './modalFocus'

/** テスト用のモーダル相当コンテナを document へ挿す(要素 id は呼び出し側で確認) */
function mountContainer(html: string): HTMLElement {
  const container = document.createElement('div')
  container.innerHTML = html
  document.body.appendChild(container)
  return container
}

/** Tab キーのイベント(既定動作を止めたかは defaultPrevented で確認する) */
function tabEvent(shiftKey = false): KeyboardEvent {
  return new KeyboardEvent('keydown', { key: 'Tab', shiftKey, cancelable: true })
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('focusableElementsIn', () => {
  it('フォーカスできる要素を DOM 順で返す', () => {
    const container = mountContainer(`
      <button id="b1">1</button>
      <a id="a1" href="#">link</a>
      <input id="i1" />
      <select id="s1"></select>
      <textarea id="t1"></textarea>
      <div id="d1" tabindex="0"></div>
    `)
    expect(focusableElementsIn(container).map((el) => el.id)).toEqual([
      'b1',
      'a1',
      'i1',
      's1',
      't1',
      'd1',
    ])
  })

  it('無効化された要素・タブ順から外した要素は含めない', () => {
    const container = mountContainer(`
      <button id="b1">1</button>
      <button id="b2" disabled>2</button>
      <input id="i1" disabled />
      <div id="d1" tabindex="-1"></div>
      <a id="a1">href なし</a>
    `)
    expect(focusableElementsIn(container).map((el) => el.id)).toEqual(['b1'])
  })
})

describe('trapTabKey', () => {
  it('末尾で Tab を押すと先頭へ戻す', () => {
    const container = mountContainer('<button id="b1">1</button><button id="b2">2</button>')
    document.getElementById('b2')?.focus()

    const e = tabEvent()
    expect(trapTabKey(container, e)).toBe(true)
    expect(e.defaultPrevented).toBe(true)
    expect(document.activeElement?.id).toBe('b1')
  })

  it('先頭で Shift+Tab を押すと末尾へ移す', () => {
    const container = mountContainer('<button id="b1">1</button><button id="b2">2</button>')
    document.getElementById('b1')?.focus()

    const e = tabEvent(true)
    expect(trapTabKey(container, e)).toBe(true)
    expect(document.activeElement?.id).toBe('b2')
  })

  it('途中の要素では既定の移動に任せる', () => {
    const container = mountContainer(
      '<button id="b1">1</button><button id="b2">2</button><button id="b3">3</button>',
    )
    document.getElementById('b2')?.focus()

    const e = tabEvent()
    expect(trapTabKey(container, e)).toBe(false)
    expect(e.defaultPrevented).toBe(false)
  })

  it('モーダルの外にフォーカスがあれば引き戻す', () => {
    const outside = document.createElement('button')
    outside.id = 'outside'
    document.body.appendChild(outside)
    const container = mountContainer('<button id="b1">1</button><button id="b2">2</button>')
    outside.focus()

    const e = tabEvent()
    expect(trapTabKey(container, e)).toBe(true)
    expect(document.activeElement?.id).toBe('b1')
    // Shift+Tab なら末尾から入る
    outside.focus()
    const back = tabEvent(true)
    expect(trapTabKey(container, back)).toBe(true)
    expect(document.activeElement?.id).toBe('b2')
  })

  it('フォーカスできる要素が無くても背景へ抜けさせない', () => {
    const container = mountContainer('<p>本文だけのモーダル</p>')
    const e = tabEvent()
    expect(trapTabKey(container, e)).toBe(true)
    expect(e.defaultPrevented).toBe(true)
  })

  it('Tab 以外のキーには関与しない', () => {
    const container = mountContainer('<button id="b1">1</button>')
    const e = new KeyboardEvent('keydown', { key: 'Enter', cancelable: true })
    expect(trapTabKey(container, e)).toBe(false)
    expect(e.defaultPrevented).toBe(false)
  })
})
