import { describe, expect, it, vi } from 'vitest'
import { h, nextTick, ref } from 'vue'
import type { IssueDetail } from '../lib/backend'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import IssueDetailDialog from './IssueDetailDialog.vue'

const detail: IssueDetail = {
  issueKey: 'TEST-1',
  summary: '課題の要約',
  description: '説明本文',
  statusName: '処理中',
  assigneeName: '担当者',
  issueTypeName: 'タスク',
  priorityName: '中',
  created: '2026-08-01T00:00:00Z',
  updated: '2026-08-02T00:00:00Z',
  dueDate: '2026-08-31',
  parentIssueKey: '',
  customFields: [{ name: '顧客', value: 'A社' }],
  fetchedAt: '2026-08-03T00:00:00Z',
  comments: [{ authorName: '投稿者', content: 'コメント本文', created: '2026-08-02T01:00:00Z' }],
  commentsFetchedAt: '2026-08-03T00:00:00Z',
  commentsHistoryOnly: 1,
  commentsTruncated: true,
  warnings: ['一部取得できませんでした'],
}

describe('IssueDetailDialog', () => {
  it('詳細内容を表示し、更新・コピー・ブラウザ・閉じる操作を通知する', async () => {
    const open = ref(false)
    const onRefresh = vi.fn()
    const onCopy = vi.fn()
    const onOpenBrowser = vi.fn()
    const onClose = vi.fn(() => (open.value = false))
    const mounted = mountWithI18n({
      render: () =>
        h(IssueDetailDialog, {
          open: open.value,
          issueKey: 'TEST-1',
          detail,
          loading: false,
          error: '',
          copyError: '',
          refreshError: '',
          refreshing: false,
          syncing: false,
          canCopy: true,
          onRefresh,
          onCopy,
          onOpenBrowser,
          onClose,
        }),
    })

    open.value = true
    await nextTick()
    expect(mounted.host.textContent).toContain('TEST-1')
    expect(mounted.host.textContent).toContain('課題の要約')
    expect(mounted.host.textContent).toContain('A社')
    expect(mounted.host.textContent).toContain('コメント本文')
    expect(mounted.host.textContent).toContain('一部取得できませんでした')

    const buttons = Array.from(mounted.host.querySelectorAll('button'))
    buttons.forEach((button) => button.click())
    expect(onRefresh).toHaveBeenCalledOnce()
    expect(onCopy).toHaveBeenCalledOnce()
    expect(onOpenBrowser).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
    mounted.unmount()
  })

  it('読み込み中と取得失敗を内容より優先して表示する', async () => {
    const loading = ref(true)
    const error = ref('')
    const mounted = mountWithI18n({
      render: () =>
        h(IssueDetailDialog, {
          open: true,
          issueKey: 'TEST-2',
          detail: null,
          loading: loading.value,
          error: error.value,
          copyError: '',
          refreshError: '',
          refreshing: false,
          syncing: false,
          canCopy: false,
        }),
    })

    expect(mounted.host.textContent).toContain('読み込み中')
    loading.value = false
    error.value = '取得に失敗しました'
    await nextTick()
    expect(mounted.host.textContent).toContain('取得に失敗しました')
    mounted.unmount()
  })
})
