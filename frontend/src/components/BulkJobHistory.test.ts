import { describe, expect, it, vi } from 'vitest'
import { h } from 'vue'
import type { BulkJobRow, BulkJobRowDetail } from '../lib/backend'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import BulkJobHistory from './BulkJobHistory.vue'

const job: BulkJobRow = {
  jobId: 7,
  projectId: 1,
  createdAt: '2026-08-01T00:00:00Z',
  kind: 'bulk_update',
  status: 'running',
  total: 6,
  done: 1,
  failed: 1,
  conflict: 1,
  pending: 2,
  sending: 1,
  skipped: 0,
}

const detail: BulkJobRowDetail = {
  rowNo: 3,
  issueKey: 'TEST-3',
  status: 'done',
  error: '',
  resultIssueId: 30,
  statusLabel: '',
}

describe('BulkJobHistory', () => {
  it('履歴・行明細を表示し、各操作を親へ通知する', () => {
    const onRefresh = vi.fn()
    const onResume = vi.fn()
    const onForce = vi.fn()
    const onToggle = vi.fn()
    const onExportResult = vi.fn()
    const mounted = mountWithI18n({
      render: () =>
        h(BulkJobHistory, {
          jobs: [job],
          jobsError: '',
          running: false,
          syncing: false,
          expandedJobId: 7,
          rowsLoading: false,
          rowsError: '',
          rows: [detail],
          exportingJobId: 0,
          translate: (key: string) => (key.endsWith('.done') ? '完了' : key),
          onRefresh,
          onResume,
          onForce,
          onToggle,
          onExportResult,
        }),
    })

    expect(mounted.host.textContent).toContain('#7')
    expect(mounted.host.textContent).toContain('(新規)TEST-3')
    expect(mounted.host.textContent).toContain('完了')

    const buttons = Array.from(mounted.host.querySelectorAll('button'))
    buttons.forEach((button) => button.click())
    expect(onRefresh).toHaveBeenCalledOnce()
    expect(onResume).toHaveBeenCalledWith(job, false)
    expect(onResume).toHaveBeenCalledWith(job, true)
    expect(onForce).toHaveBeenCalledWith(job)
    expect(onToggle).toHaveBeenCalledWith(job)
    expect(onExportResult).toHaveBeenCalledWith(7)
    mounted.unmount()
  })

  it('履歴が空の場合は空表示を出す', () => {
    const mounted = mountWithI18n({
      render: () =>
        h(BulkJobHistory, {
          jobs: [],
          jobsError: '',
          running: false,
          syncing: false,
          expandedJobId: 0,
          rowsLoading: false,
          rowsError: '',
          rows: [],
          exportingJobId: 0,
          translate: (key: string) => key,
        }),
    })

    expect(mounted.host.textContent).toContain('実行履歴はまだありません')
    mounted.unmount()
  })
})
