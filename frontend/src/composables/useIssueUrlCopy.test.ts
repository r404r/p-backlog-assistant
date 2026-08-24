import { describe, expect, it, vi } from 'vitest'
import { h, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import { useIssueUrlCopy, type IssueUrlCopyState } from './useIssueUrlCopy'

function mountCopyState(copy: (text: string) => Promise<void>) {
  let state: IssueUrlCopyState | undefined
  const mounted = mountWithI18n({
    setup() {
      const { t } = useI18n()
      state = useIssueUrlCopy(ref('https://example.backlog.jp'), t, { copy, toastMs: 10 })
      return () => h('div')
    },
  })
  if (!state) throw new Error('copy state was not initialized')
  return { mounted, state }
}

describe('useIssueUrlCopy', () => {
  it('課題URLを組み立ててコピーし、成功通知を一定時間後に消す', async () => {
    vi.useFakeTimers()
    const copy = vi.fn(async () => undefined)
    const { mounted, state } = mountCopyState(copy)

    await state.copyIssueUrl('TEST-1')
    expect(copy).toHaveBeenCalledWith('https://example.backlog.jp/view/TEST-1')
    expect(state.toastKey.value).toBe('TEST-1')

    await vi.advanceTimersByTimeAsync(10)
    expect(state.toastKey.value).toBe('')
    mounted.unmount()
    vi.useRealTimers()
  })

  it('コピー失敗を一覧と詳細の別領域に保持し、クリアできる', async () => {
    const copy = vi.fn(async () => {
      throw new Error('clipboard unavailable')
    })
    const { mounted, state } = mountCopyState(copy)

    await state.copyIssueUrl('TEST-1')
    expect(state.listError.value).toContain('clipboard unavailable')
    expect(state.detailError.value).toBe('')

    state.clearListFeedback()
    await state.copyIssueUrl('TEST-2', true)
    expect(state.listError.value).toBe('')
    expect(state.detailError.value).toContain('clipboard unavailable')

    state.invalidateAndClearDetail()
    expect(state.detailError.value).toBe('')
    mounted.unmount()
  })

  it('コピー完了順が逆転しても最後に要求した課題だけを通知する', async () => {
    const resolvers: Array<() => void> = []
    const copy = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolvers.push(resolve)
        }),
    )
    const { mounted, state } = mountCopyState(copy)

    const first = state.copyIssueUrl('TEST-1')
    const second = state.copyIssueUrl('TEST-2')
    resolvers[1]?.()
    await second
    expect(state.toastKey.value).toBe('TEST-2')

    resolvers[0]?.()
    await first
    expect(state.toastKey.value).toBe('TEST-2')
    mounted.unmount()
  })

  it('スペースURLが空の場合はコピー処理を呼ばない', async () => {
    const copy = vi.fn(async () => undefined)
    let state: IssueUrlCopyState | undefined
    const mounted = mountWithI18n({
      setup() {
        const { t } = useI18n()
        state = useIssueUrlCopy(ref(''), t, { copy })
        return () => h('div')
      },
    })

    await state?.copyIssueUrl('TEST-1')
    expect(copy).not.toHaveBeenCalled()
    mounted.unmount()
  })
})
