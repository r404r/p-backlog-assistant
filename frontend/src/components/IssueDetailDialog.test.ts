import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { h, nextTick, ref } from 'vue'
import type { IssueDetail } from '../lib/backend'
import { DETAIL_MARKDOWN_KEY } from '../lib/markdown'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import IssueDetailDialog from './IssueDetailDialog.vue'

// 整形表示のリンクは OS の既定ブラウザで開く(WebView 内で遷移させない)。
// 実際に呼ばれた URL を捕まえるため、外部リンクを開く関数だけ差し替える。
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    openExternalURL: (url: string) => opened.push(url),
  }
})

/** openExternalURL に渡された URL */
const opened: string[] = []

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
  textFormattingRule: '',
}

/** 検証したい項目だけ差し替えた課題詳細を作る */
function detailWith(overrides: Partial<IssueDetail>): IssueDetail {
  return { ...detail, ...overrides }
}

/** 課題詳細ポップアップを開いた状態でマウントする */
function mountDialog(target: IssueDetail, locale: 'ja' | 'en' = 'ja') {
  return mountWithI18n(
    {
      render: () =>
        h(IssueDetailDialog, {
          open: true,
          issueKey: target.issueKey,
          detail: target,
          loading: false,
          error: '',
          copyError: '',
          refreshError: '',
          refreshing: false,
          syncing: false,
          canCopy: false,
        }),
    },
    { locale },
  )
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

/**
 * Markdown の整形表示(設計 §3.3 / §3.4)。
 *
 * 判定は **課題詳細に載って届いた記法設定**だけで行う(画面は選択中プロジェクトを
 * 見ない)。Markdown 以外は従来のプレーン表示のままで、整形表示中でも
 * v-html を通した DOM に禁止要素・属性が現れてはならない。
 */
describe('IssueDetailDialog の Markdown 整形表示', () => {
  beforeEach(() => {
    localStorage.clear()
    opened.length = 0
  })
  afterEach(() => {
    localStorage.clear()
  })

  const markdownDetail = detailWith({
    textFormattingRule: 'markdown',
    description: '# 詳細の見出し\n\n- 箇条書き\n\n[リンク](https://example.com/issue)',
    comments: [
      { authorName: '投稿者', content: '## コメントの見出し', created: '2026-08-02T01:00:00Z' },
    ],
  })

  it('markdown のプロジェクトでは本文とコメントを整形表示する', () => {
    const mounted = mountDialog(markdownDetail)

    expect(mounted.host.querySelector('h1')?.textContent).toBe('詳細の見出し')
    expect(mounted.host.querySelector('li')?.textContent).toBe('箇条書き')
    // コメントも同じ扱い(見出し・リストが効く)
    expect(mounted.host.querySelector('.comment-body h2')?.textContent).toBe('コメントの見出し')
    // 記法のままの原文は出さない
    expect(mounted.host.textContent).not.toContain('# 詳細の見出し')
    mounted.unmount()
  })

  it('backlog 記法・判定不能のプロジェクトはプレーン表示のまま', () => {
    for (const rule of ['backlog', '']) {
      const mounted = mountDialog(detailWith({ ...markdownDetail, textFormattingRule: rule }))
      expect(mounted.host.querySelector('h1'), rule).toBeNull()
      // 原文がそのまま(記法の文字を含んだまま)表示される
      expect(mounted.host.textContent, rule).toContain('# 詳細の見出し')
      mounted.unmount()
    }
  })

  it('「整形表示 / 原文」を切り替えられ、選択を保存する', async () => {
    const mounted = mountDialog(markdownDetail)
    const buttonByLabel = (label: string) =>
      Array.from(mounted.host.querySelectorAll('button')).find(
        (b) => b.textContent?.trim() === label,
      )

    // 既定は整形表示
    expect(mounted.host.querySelector('h1')).not.toBeNull()

    buttonByLabel('原文')?.click()
    await nextTick()
    expect(mounted.host.querySelector('h1')).toBeNull()
    expect(mounted.host.textContent).toContain('# 詳細の見出し')
    expect(localStorage.getItem(DETAIL_MARKDOWN_KEY)).toBe('false')

    buttonByLabel('整形表示')?.click()
    await nextTick()
    expect(mounted.host.querySelector('h1')).not.toBeNull()
    expect(localStorage.getItem(DETAIL_MARKDOWN_KEY)).toBe('true')
    mounted.unmount()
  })

  it('保存済みの選択(原文)で開き直す', () => {
    localStorage.setItem(DETAIL_MARKDOWN_KEY, 'false')
    const mounted = mountDialog(markdownDetail)
    expect(mounted.host.querySelector('h1')).toBeNull()
    expect(mounted.host.textContent).toContain('# 詳細の見出し')
    mounted.unmount()
  })

  it('整形表示のリンクは href を持たず、クリックで既定ブラウザに渡す', () => {
    const mounted = mountDialog(markdownDetail)
    const anchor = mounted.host.querySelector('a')

    expect(anchor).not.toBeNull()
    expect(anchor?.hasAttribute('href')).toBe(false)
    anchor?.click()
    expect(opened).toEqual(['https://example.com/issue'])
    mounted.unmount()
  })

  it('v-html を通した DOM に禁止要素・属性が現れない', () => {
    const attack = [
      '<script>window.__xss = 1</script>',
      '<img src=x onerror="window.__xss = 1">',
      '<iframe src="https://example.com"></iframe>',
      '<svg><script>1</script></svg>',
      '[危険](javascript:window.__xss=1)',
      '![画像](https://example.com/a.png)',
      '<span style="position:fixed">重ねる</span>',
    ].join('\n\n')
    const mounted = mountDialog(
      detailWith({
        textFormattingRule: 'markdown',
        description: attack,
        comments: [{ authorName: '投稿者', content: attack, created: '2026-08-02T01:00:00Z' }],
      }),
    )

    const dialog = mounted.host.querySelector('.modal')!
    expect(dialog.querySelector('script, img, iframe, svg, math, form, input, style')).toBeNull()
    for (const attr of ['href', 'src', 'style', 'onerror', 'onclick', 'onload']) {
      expect(dialog.querySelector(`[${attr}]`), attr).toBeNull()
    }
    mounted.unmount()
  })

  it('判定は届いた課題詳細だけに従う(取得中のプロジェクト切替でずれない)', async () => {
    // 詳細の取得中に別プロジェクトへ切り替えても、表示の判定は「今表示している
    // 課題の詳細に載って届いた記法設定」で決まる(画面は選択中プロジェクトを見ない)
    const current = ref<IssueDetail>(markdownDetail)
    const mounted = mountWithI18n({
      render: () =>
        h(IssueDetailDialog, {
          open: true,
          issueKey: current.value.issueKey,
          detail: current.value,
          loading: false,
          error: '',
          copyError: '',
          refreshError: '',
          refreshing: false,
          syncing: false,
          canCopy: false,
        }),
    })
    expect(mounted.host.querySelector('h1')).not.toBeNull()

    // Backlog 記法のプロジェクトの課題が届いたら、その時点でプレーン表示へ戻る
    current.value = detailWith({ ...markdownDetail, textFormattingRule: 'backlog' })
    await nextTick()
    expect(mounted.host.querySelector('h1')).toBeNull()
    expect(mounted.host.textContent).toContain('# 詳細の見出し')
    mounted.unmount()
  })

  it('en でも整形表示の切替を操作できる', async () => {
    const mounted = mountDialog(markdownDetail, 'en')
    const buttonByLabel = (label: string) =>
      Array.from(mounted.host.querySelectorAll('button')).find(
        (b) => b.textContent?.trim() === label,
      )

    expect(buttonByLabel('Formatted')).toBeDefined()
    buttonByLabel('Source')?.click()
    await nextTick()
    expect(mounted.host.querySelector('h1')).toBeNull()
    expect(mounted.host.textContent).toContain('# 詳細の見出し')
    mounted.unmount()
  })
})
