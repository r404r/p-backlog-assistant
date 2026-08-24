import { describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import BulkRunConfirmation from './BulkRunConfirmation.vue'

describe('BulkRunConfirmation', () => {
  it('対象件数・見積り・再送警告を表示し、利用者の選択を通知する', () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    const mounted = mountWithI18n({
      render: () =>
        h(BulkRunConfirmation, {
          count: 42,
          estimate: '約 2 分',
          force: true,
          resendSending: true,
          onConfirm,
          onCancel,
        }),
    })

    expect(mounted.host.textContent).toContain('42 件を Backlog に書き込みます')
    expect(mounted.host.textContent).toContain('約 2 分')
    expect(mounted.host.textContent).toContain('リモートの変更を上書きします')
    expect(mounted.host.textContent).toContain('作成済みの課題を確認してから再送します')

    const buttons = mounted.host.querySelectorAll('button')
    ;(buttons[0] as HTMLButtonElement).click()
    ;(buttons[1] as HTMLButtonElement).click()
    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onCancel).toHaveBeenCalledOnce()
    mounted.unmount()
  })

  it('指定されていない警告は表示しない', () => {
    const mounted = mountWithI18n({
      render: () =>
        h(BulkRunConfirmation, {
          count: 1,
          estimate: '約 1 分未満',
          force: false,
          resendSending: false,
        }),
    })

    expect(mounted.host.querySelectorAll('.warn-text')).toHaveLength(0)
    mounted.unmount()
  })
})
