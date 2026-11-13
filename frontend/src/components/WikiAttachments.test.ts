import { afterEach, expect, it, vi } from 'vitest'
import { h, nextTick, ref } from 'vue'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import { createMockBackend } from '../lib/backend/mock'
import WikiAttachments from './WikiAttachments.vue'
import type {
  WikiAttachmentDownloadProgress,
  WikiAttachmentDownloadResult,
} from '../lib/backend'
import en from '../locales/en/wiki.json'
import ja from '../locales/ja/wiki.json'
const holder = vi.hoisted(() => ({
  backend: null as unknown,
  progress: undefined as
    | ((p: WikiAttachmentDownloadProgress) => void)
    | undefined,
  off: vi.fn(),
}))
vi.mock('../lib/backend', async (original) => ({
  ...(await original<typeof import('../lib/backend')>()),
  getBackend: () => holder.backend,
  onWikiAttachmentProgress: (
    cb: (p: WikiAttachmentDownloadProgress) => void,
  ) => {
    holder.progress = cb
    return holder.off
  },
}))
let mounted: MountedApp | undefined
afterEach(() => {
  mounted?.unmount()
  mounted = undefined
  holder.off.mockClear()
})
const flush = async () => {
  for (let n = 0; n < 20; n++) await nextTick()
}
it('hides an empty section and displays saved results without exposing a path', async () => {
  const b = createMockBackend()
  holder.backend = b
  b.beginWikiAttachmentDownload = vi.fn(async () => ({
    runId: 'run',
    code: '',
  }))
  b.downloadWikiAttachment = vi.fn(async () => ({
    runId: 'run',
    state: 'saved' as const,
    code: '',
    bytes: 4,
    sizeChanged: false,
  }))
  mounted = mountWithI18n({
    render: () =>
      h(WikiAttachments, {
        projectId: 1,
        wikiId: 2,
        attachments: [{ id: 3, wikiId: 2, name: '架空.txt', size: 4 }],
        disabled: false,
      }),
  })
  await flush()
  ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
  await flush()
  expect(mounted.host.textContent).toContain('保存しました')
  expect(b.downloadWikiAttachment).toHaveBeenCalledWith(1, 2, 3)
  mounted.unmount()
  mounted = mountWithI18n({
    render: () =>
      h(WikiAttachments, {
        projectId: 1,
        wikiId: 2,
        attachments: [],
        disabled: false,
      }),
  })
  expect(mounted.host.querySelector('section')).toBeNull()
})

it.each(Object.keys(en.attachmentDownload.error))(
  '失敗原因 %s を日英で案内し再試行できる',
  async (key) => {
    const codes: Record<string, string> = {
      notFound: 'not_found',
      rateLimited: 'rate_limited',
      cleanupFailed: 'cleanup_failed',
      sizeMismatch: 'size_mismatch',
      localData: 'local_data',
    }
    const b = createMockBackend()
    holder.backend = b
    b.beginWikiAttachmentDownload = vi.fn(async () => ({
      runId: 'run',
      code: '',
    }))
    b.downloadWikiAttachment = vi.fn(async () => ({
      runId: 'run',
      state: 'failed' as const,
      code: codes[key] ?? key,
      bytes: 0,
      sizeChanged: false,
    }))
    mounted = mountWithI18n(
      {
        render: () =>
          h(WikiAttachments, {
            projectId: 1,
            wikiId: 2,
            attachments: [{ id: 3, wikiId: 2, name: 'sample.txt', size: 4 }],
            disabled: false,
          }),
      },
      { locale: 'en' },
    )
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(mounted.host.querySelector('.attachment-failed')?.textContent).toBe(
      en.attachmentDownload.error[
        key as keyof typeof en.attachmentDownload.error
      ],
    )
    expect(mounted.host.querySelector('[aria-live=polite]')?.textContent).toBe(
      en.attachmentDownload.error[
        key as keyof typeof en.attachmentDownload.error
      ],
    )
    expect(mounted.host.querySelector('button')?.textContent).toBe('Retry')
    mounted.i18n.global.locale.value = 'ja'
    await flush()
    expect(mounted.host.querySelector('.attachment-failed')?.textContent).toBe(
      ja.attachmentDownload.error[
        key as keyof typeof ja.attachmentDownload.error
      ],
    )
    expect(mounted.host.querySelector('button')?.textContent).toBe('再試行')
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(b.beginWikiAttachmentDownload).toHaveBeenCalledTimes(2)
  },
)

it('進捗・読上げ名・他行の排他・遅いイベントと結果・焦点復帰を検証する', async () => {
  const b = createMockBackend()
  holder.backend = b
  let finish!: (value: WikiAttachmentDownloadResult) => void
  b.beginWikiAttachmentDownload = vi.fn(async () => ({
    runId: 'run',
    code: '',
  }))
  b.downloadWikiAttachment = vi.fn(
    () =>
      new Promise<WikiAttachmentDownloadResult>((done) => {
        finish = done
      }),
  )
  mounted = mountWithI18n(
    {
      render: () =>
        h(WikiAttachments, {
          projectId: 1,
          wikiId: 2,
          attachments: [
            { id: 3, wikiId: 2, name: 'sample.txt', size: 4 },
            { id: 4, wikiId: 2, name: 'image.gif', size: 0 },
          ],
          disabled: false,
        }),
    },
    { locale: 'en' },
  )
  const first = mounted.host.querySelector<HTMLButtonElement>('button')!
  expect(first.getAttribute('aria-label')).toBe('Download sample.txt')
  expect(mounted.host.querySelector('h3')?.textContent).toBe('Attachments (2)')
  first.click()
  await flush()
  expect(mounted.host.querySelectorAll('button')[1]?.disabled).toBe(true)
  expect(mounted.host.querySelector('button')?.getAttribute('aria-label')).toBe(
    'Cancel downloading sample.txt',
  )
  const event: WikiAttachmentDownloadProgress = {
    runId: 'run',
    projectId: 1,
    wikiId: 2,
    attachmentId: 3,
    phase: 'downloading',
    receivedBytes: 2,
    totalBytes: -1,
    code: '',
  }
  holder.progress!(event)
  await flush()
  expect(mounted.host.querySelector('progress')?.hasAttribute('value')).toBe(
    false,
  )
  expect(mounted.host.textContent).toContain('size unknown')
  holder.progress!({ ...event, totalBytes: 4 })
  await flush()
  expect(mounted.host.querySelector('progress')?.getAttribute('value')).toBe(
    '50',
  )
  expect(
    mounted.host.querySelector('progress')?.getAttribute('aria-label'),
  ).toBe('Download progress for sample.txt')
  expect(
    mounted.host.querySelector('[aria-live=polite]')?.textContent,
  ).toContain('50%')
  for (const wrong of [
    { runId: 'old' },
    { projectId: 9 },
    { wikiId: 9 },
    { attachmentId: 4 },
  ])
    holder.progress!({ ...event, ...wrong, receivedBytes: 4, totalBytes: 4 })
  await flush()
  expect(mounted.host.querySelector('progress')?.getAttribute('value')).toBe(
    '50',
  )
  holder.progress!({
    ...event,
    phase: 'saving',
    receivedBytes: 4,
    totalBytes: 4,
  })
  await flush()
  expect(mounted.host.querySelector('.attachment-saved')).toBeNull()
  finish({
    runId: 'run',
    state: 'saved',
    code: '',
    bytes: 4,
    sizeChanged: false,
  })
  await flush()
  expect(mounted.host.querySelector('.attachment-saved')?.textContent).toBe(
    'Saved',
  )
  expect(document.activeElement?.id).toBe('wiki-attachment-download-3')
  holder.progress!(event)
  await flush()
  expect(mounted.host.querySelector('progress')).toBeNull()
  mounted.unmount()
  mounted = undefined
  expect(holder.off).toHaveBeenCalledOnce()
})

it.each([false, true])(
  '処理中は開始せず、予約の取消=%s の結果をエラー表示しない',
  async (cancelledTicket) => {
    const b = createMockBackend()
    holder.backend = b
    const disabled = ref(true)
    b.beginWikiAttachmentDownload = vi.fn(async () => ({
      runId: 'run',
      code: cancelledTicket ? 'cancelled' : '',
    }))
    b.downloadWikiAttachment = vi.fn(async () => ({
      runId: 'run',
      state: 'cancelled' as const,
      code: 'cancelled',
      bytes: 0,
      sizeChanged: false,
    }))
    mounted = mountWithI18n(
      {
        render: () =>
          h(WikiAttachments, {
            projectId: 1,
            wikiId: 2,
            attachments: [{ id: 3, wikiId: 2, name: 'sample.txt', size: 4 }],
            disabled: disabled.value,
          }),
      },
      { locale: 'en' },
    )
    expect(mounted.host.querySelector('button')?.disabled).toBe(true)
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(b.beginWikiAttachmentDownload).not.toHaveBeenCalled()
    disabled.value = false
    await flush()
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(
      mounted.host.querySelector('.attachment-failed,.attachment-saved'),
    ).toBeNull()
    expect(mounted.host.querySelector('[aria-live=polite]')?.textContent).toBe(
      'Cancelled',
    )
    expect(b.downloadWikiAttachment).toHaveBeenCalledTimes(
      cancelledTicket ? 0 : 1,
    )
  },
)

it('cancels a delayed reservation before download and waits before allowing retry', async () => {
  const b = createMockBackend()
  holder.backend = b
  let resolve!: (value: { runId: string; code: string }) => void
  b.beginWikiAttachmentDownload = vi.fn(
    () =>
      new Promise<{ runId: string; code: string }>((r) => {
        resolve = r
      }),
  )
  b.downloadWikiAttachment = vi.fn()
  b.cancelWikiAttachmentDownload = vi.fn(async () => {})
  mounted = mountWithI18n({
    render: () =>
      h(WikiAttachments, {
        projectId: 1,
        wikiId: 2,
        attachments: [{ id: 3, wikiId: 2, name: '架空.txt', size: 4 }],
        disabled: false,
      }),
  })
  await flush()
  ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
  await flush()
  ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
  await flush()
  resolve({ runId: 'late', code: '' })
  await flush()
  expect(b.downloadWikiAttachment).not.toHaveBeenCalled()
  expect(b.cancelWikiAttachmentDownload).toHaveBeenCalledWith(1, 2, 3)
  expect(mounted.host.textContent).not.toContain('保存しました')
  expect(mounted.host.querySelector('button')?.disabled).toBe(false)
})

it.each(['download', 'cancel'] as const)(
  '%s が先に完了しても両方の応答まで同じ行を再開始しない',
  async (first) => {
    const b = createMockBackend()
    holder.backend = b
    let finishDownload!: (value: WikiAttachmentDownloadResult) => void,
      finishCancel!: () => void
    b.beginWikiAttachmentDownload = vi.fn(async () => ({
      runId: 'run',
      code: '',
    }))
    b.downloadWikiAttachment = vi.fn(
      () =>
        new Promise<WikiAttachmentDownloadResult>((resolve) => {
          finishDownload = resolve
        }),
    )
    b.cancelWikiAttachmentDownload = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishCancel = resolve
        }),
    )
    mounted = mountWithI18n(
      {
        render: () =>
          h(WikiAttachments, {
            projectId: 1,
            wikiId: 2,
            attachments: [{ id: 3, wikiId: 2, name: 'sample.txt', size: 4 }],
            disabled: false,
          }),
      },
      { locale: 'en' },
    )
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(b.cancelWikiAttachmentDownload).toHaveBeenCalledWith(1, 2, 3)
    const completeDownload = () =>
      finishDownload({
        runId: 'run',
        state: 'cancelled',
        code: 'cancelled',
        bytes: 0,
        sizeChanged: false,
      })
    if (first === 'download') completeDownload()
    else finishCancel()
    await flush()
    expect(mounted.host.querySelector('#wiki-attachment-download-3')).toBeNull()
    expect(
      mounted.host.querySelector('button')?.getAttribute('aria-label'),
    ).toBe('Cancel downloading sample.txt')
    expect(b.beginWikiAttachmentDownload).toHaveBeenCalledOnce()
    if (first === 'download') finishCancel()
    else completeDownload()
    await flush()
    const retry = mounted.host.querySelector<HTMLButtonElement>(
      '#wiki-attachment-download-3',
    )!
    expect(retry.disabled).toBe(false)
    b.downloadWikiAttachment = vi.fn(async () => ({
      runId: 'run',
      state: 'cancelled' as const,
      code: 'cancelled',
      bytes: 0,
      sizeChanged: false,
    }))
    retry.click()
    await flush()
    expect(b.beginWikiAttachmentDownload).toHaveBeenCalledTimes(2)
  },
)

it('移動でキャンセルした行を戻った後も表示し、別の接続設定には引き継がない', async () => {
  const b = createMockBackend()
  holder.backend = b
  let finish!: (value: WikiAttachmentDownloadResult) => void
  b.beginWikiAttachmentDownload = vi.fn(async () => ({
    runId: 'run',
    code: '',
  }))
  b.downloadWikiAttachment = vi.fn(
    () =>
      new Promise<WikiAttachmentDownloadResult>((resolve) => {
        finish = resolve
      }),
  )
  b.cancelWikiAttachmentDownload = vi.fn(async () => {
    finish({
      runId: 'run',
      state: 'cancelled',
      code: 'cancelled',
      bytes: 0,
      sizeChanged: false,
    })
  })
  const component = ref<InstanceType<typeof WikiAttachments> | null>(null)
  const props = {
    profile: 'cancelled-profile',
    projectId: 1,
    wikiId: 2,
    attachments: [{ id: 3, wikiId: 2, name: '架空.txt', size: 4 }],
    disabled: false,
  }
  mounted = mountWithI18n(
    { render: () => h(WikiAttachments, { ...props, ref: component }) },
    { locale: 'en' },
  )
  ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
  await flush()
  await component.value!.cancel()
  await flush()
  expect(mounted.host.querySelector('.attachment-cancelled')?.textContent).toBe(
    'Cancelled',
  )
  mounted.unmount()
  mounted = mountWithI18n(
    { render: () => h(WikiAttachments, props) },
    { locale: 'en' },
  )
  expect(mounted.host.querySelector('.attachment-cancelled')?.textContent).toBe(
    'Cancelled',
  )
  mounted.i18n.global.locale.value = 'ja'
  await flush()
  expect(mounted.host.querySelector('.attachment-cancelled')?.textContent).toBe(
    'キャンセルしました',
  )
  mounted.unmount()
  mounted = mountWithI18n(
    {
      render: () => h(WikiAttachments, { ...props, profile: 'other-profile' }),
    },
    { locale: 'en' },
  )
  expect(mounted.host.querySelector('.attachment-cancelled')).toBeNull()
})

it.each(['failed', 'saved'] as const)(
  'キャンセルと競合した確定結果 %s を隠さない',
  async (state) => {
    const b = createMockBackend()
    holder.backend = b
    let finish!: (value: WikiAttachmentDownloadResult) => void
    b.beginWikiAttachmentDownload = vi.fn(async () => ({
      runId: 'run',
      code: '',
    }))
    b.downloadWikiAttachment = vi.fn(
      () =>
        new Promise<WikiAttachmentDownloadResult>((resolve) => {
          finish = resolve
        }),
    )
    b.cancelWikiAttachmentDownload = vi.fn(async () => {
      finish({
        runId: 'run',
        state,
        code: state === 'failed' ? 'cleanup_failed' : '',
        bytes: 4,
        sizeChanged: false,
      })
    })
    mounted = mountWithI18n(
      {
        render: () =>
          h(WikiAttachments, {
            projectId: 1,
            wikiId: 2,
            attachments: [{ id: 3, wikiId: 2, name: '架空.txt', size: 4 }],
            disabled: false,
          }),
      },
      { locale: 'en' },
    )
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    ;(mounted.host.querySelector('button') as HTMLButtonElement).click()
    await flush()
    expect(
      mounted.host.querySelector(`.attachment-${state}`)?.textContent,
    ).toBe(
      state === 'failed' ? en.attachmentDownload.error.cleanupFailed : 'Saved',
    )
    expect(mounted.host.querySelector('.attachment-cancelled')).toBeNull()
  },
)
