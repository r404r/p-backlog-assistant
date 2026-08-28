import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { h, nextTick, ref } from 'vue'
import type { IssueDetail } from '../lib/backend'
import {
  DETAIL_MAXIMIZED_KEY,
  resetDetailMaximizedStorageState,
} from '../lib/detailMaximized'
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

/**
 * 最大化 / 復元(設計 §3)。
 *
 * 最大化はヘッダのトグルボタンとタイトル背景のダブルクリックで切り替え、
 * 状態は localStorage(`ba.detailMaximized`)へ持ち越す。未設定・不正値・
 * ストレージ不可はすべて非最大化へ縮退し、Esc・閉じる・背景クリックの
 * 閉じ方は最大化中も変わらない。
 */
describe('IssueDetailDialog の最大化 / 復元', () => {
  beforeEach(() => {
    localStorage.clear()
    resetDetailMaximizedStorageState()
  })
  afterEach(() => {
    localStorage.clear()
    resetDetailMaximizedStorageState()
  })

  /** 最大化トグルボタン */
  function toggleButton(host: HTMLElement): HTMLButtonElement {
    const button = host.querySelector<HTMLButtonElement>('button.maximize-toggle')
    expect(button, '最大化トグルボタンが見つかりません').not.toBeNull()
    return button!
  }

  /** ダイアログ本体が最大化状態か */
  function isMaximized(host: HTMLElement): boolean {
    return host.querySelector('.modal')!.classList.contains('maximized')
  }

  /** ダブルクリックを発火する(バブリングさせて .self の判定まで再現する) */
  function doubleClick(element: Element): void {
    element.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
  }

  it('トグルボタンで最大化 / 復元を切り替える', async () => {
    const mounted = mountDialog(detail)
    const button = toggleButton(mounted.host)

    expect(isMaximized(mounted.host)).toBe(false)

    button.click()
    await nextTick()
    expect(isMaximized(mounted.host)).toBe(true)

    button.click()
    await nextTick()
    expect(isMaximized(mounted.host)).toBe(false)
    mounted.unmount()
  })

  it('aria-pressed は付けない(アクセシブル名を操作名へ切り替える構成のため)', async () => {
    // WAI-ARIA APG のボタンパターン: aria-pressed のトグルは名前を固定する。
    // 名前を「最大化」→「元のサイズに戻す」と切り替えるこの構成で aria-pressed を
    // 併用すると、支援技術には「『元のサイズに戻す』が押されている」と伝わり矛盾する
    // (レビュー 1 回目 指摘 3)。
    const mounted = mountDialog(detail)
    const button = toggleButton(mounted.host)

    expect(button.hasAttribute('aria-pressed')).toBe(false)

    button.click()
    await nextTick()
    expect(button.hasAttribute('aria-pressed')).toBe(false)
    mounted.unmount()
  })

  it("切替のたびに状態を '1' / '0' で保存する", async () => {
    const mounted = mountDialog(detail)
    const button = toggleButton(mounted.host)

    button.click()
    await nextTick()
    expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('1')

    button.click()
    await nextTick()
    expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('0')
    mounted.unmount()
  })

  it('保存済みの最大化状態で開き直す', () => {
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, '1')
    const mounted = mountDialog(detail)
    expect(isMaximized(mounted.host)).toBe(true)
    mounted.unmount()
  })

  it('保存値が不正なら非最大化で開く', () => {
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, 'true')
    const mounted = mountDialog(detail)
    expect(isMaximized(mounted.host)).toBe(false)
    mounted.unmount()
  })

  it('localStorage が使えなくても非最大化で開き、その場での切替はできる', async () => {
    const getItem = vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage disabled')
    })
    try {
      const mounted = mountDialog(detail)
      expect(isMaximized(mounted.host)).toBe(false)

      toggleButton(mounted.host).click()
      await nextTick()
      expect(isMaximized(mounted.host)).toBe(true)
      mounted.unmount()
    } finally {
      getItem.mockRestore()
      setItem.mockRestore()
    }
  })

  it('読めるが保存できない場合、開き直しても古い保存値へ戻らない(非最大化へ縮退)', async () => {
    // 保存だけが失敗する環境(クォータ超過・読み取り専用ストレージ)では、
    // 復元操作をしても localStorage には古い '1' が残る。そのまま読み直すと
    // 「戻したはずの最大化」が復活してしまうため、書込に失敗した時点で
    // 保存値を信用せず既定(非最大化)へ縮退する(レビュー 1 回目 指摘 2)。
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, '1')
    const setItem = vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new Error('quota exceeded')
    })
    try {
      const open = ref(true)
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
            canCopy: false,
          }),
      })

      // 保存値どおり最大化で開く
      expect(isMaximized(mounted.host)).toBe(true)

      // 復元する(保存は失敗し、localStorage には '1' が残ったまま)
      toggleButton(mounted.host).click()
      await nextTick()
      expect(isMaximized(mounted.host)).toBe(false)
      expect(localStorage.getItem(DETAIL_MAXIMIZED_KEY)).toBe('1')

      // 閉じて開き直しても、古い '1' は復活しない
      open.value = false
      await nextTick()
      open.value = true
      await nextTick()
      expect(isMaximized(mounted.host)).toBe(false)
      mounted.unmount()
    } finally {
      setItem.mockRestore()
    }
  })

  it('タイトルの背景をダブルクリックすると切り替わる', async () => {
    const mounted = mountDialog(detail)
    const title = mounted.host.querySelector('.detail-title')!

    doubleClick(title)
    await nextTick()
    expect(isMaximized(mounted.host)).toBe(true)

    doubleClick(title)
    await nextTick()
    expect(isMaximized(mounted.host)).toBe(false)
    mounted.unmount()
  })

  it('タイトル内の子要素のダブルクリックでは切り替わらない(文字選択の誤爆を防ぐ)', async () => {
    const mounted = mountDialog(detail)

    for (const selector of ['.detail-key', '.detail-summary', 'button.maximize-toggle']) {
      doubleClick(mounted.host.querySelector(selector)!)
      await nextTick()
      expect(isMaximized(mounted.host), selector).toBe(false)
    }
    mounted.unmount()
  })

  it('ja のラベルが状態に応じて切り替わる', async () => {
    const mounted = mountDialog(detail)
    const button = toggleButton(mounted.host)

    expect(button.getAttribute('aria-label')).toBe('最大化')
    expect(button.getAttribute('title')).toBe('最大化')

    button.click()
    await nextTick()
    expect(button.getAttribute('aria-label')).toBe('元のサイズに戻す')
    expect(button.getAttribute('title')).toBe('元のサイズに戻す')
    mounted.unmount()
  })

  it('en のラベルが状態に応じて切り替わる', async () => {
    const mounted = mountDialog(detail, 'en')
    const button = toggleButton(mounted.host)

    expect(button.getAttribute('aria-label')).toBe('Maximize')

    button.click()
    await nextTick()
    expect(button.getAttribute('aria-label')).toBe('Restore')
    mounted.unmount()
  })

  it('切替後もフォーカスがトグルボタンに残る', async () => {
    const mounted = mountDialog(detail)
    const button = toggleButton(mounted.host)

    button.click()
    await nextTick()
    expect(document.activeElement).toBe(button)

    // ダブルクリック経由でもフォーカスをダイアログ内(ボタン)に留める
    ;(document.activeElement as HTMLElement | null)?.blur()
    doubleClick(mounted.host.querySelector('.detail-title')!)
    await nextTick()
    expect(document.activeElement).toBe(button)
    mounted.unmount()
  })

  it('最大化中も Esc・閉じる・背景クリックで閉じられる(閉じ方は不変)', async () => {
    const open = ref(false)
    const onClose = vi.fn()
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
          canCopy: false,
          onClose,
        }),
    })

    open.value = true
    await nextTick()
    toggleButton(mounted.host).click()
    await nextTick()
    expect(isMaximized(mounted.host)).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(onClose).toHaveBeenCalledTimes(1)

    mounted.host.querySelector<HTMLElement>('.modal-overlay')!.click()
    expect(onClose).toHaveBeenCalledTimes(2)

    const closeButton = Array.from(mounted.host.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === '閉じる',
    )
    closeButton?.click()
    expect(onClose).toHaveBeenCalledTimes(3)
    mounted.unmount()
  })
})

/**
 * 最大化レイアウトの CSS 検査(レビュー 1 回目 指摘 1)。
 *
 * 最大化時のモーダルは `overflow: hidden` の flex 縦積みで、スクロールを担うのは
 * `.detail-body` だけ。ここでヘッダ(件数無制限の警告を含む)とフッタ(折り返し得る
 * エラーとボタン群)を縮小不可のままにすると、狭小ウィンドウ・200% ズームで
 * 合計高さが画面を超えたときに `.detail-body` が高さ 0 に潰れ、しかも固定領域自身も
 * スクロールできず読めなくなる。
 *
 * 実際の高さ計算は happy-dom では再現できない(レイアウトエンジンが無い)ため、
 * ここでは **崩れない構造が CSS に書かれていること**を静的に検査する
 * (styleTokens.test.ts と同じ発想)。実寸での確認は手動確認項目に残す。
 */
describe('IssueDetailDialog の最大化レイアウト(CSS)', () => {
  const source = readFileSync(
    resolve(process.cwd(), 'src/components/IssueDetailDialog.vue'),
    'utf8',
  )

  /** セレクタに対応する宣言ブロックの中身を返す(入れ子を持たない平坦なブロック前提) */
  function ruleBlock(selector: string): string {
    const head = `${selector} {`
    const start = source.indexOf(head)
    expect(start, `セレクタが見つかりません: ${selector}`).toBeGreaterThanOrEqual(0)
    const bodyStart = start + head.length
    const end = source.indexOf('}', bodyStart)
    expect(end, `ブロックが閉じていません: ${selector}`).toBeGreaterThan(bodyStart)
    return source.slice(bodyStart, end)
  }

  /** ブロックの max-height に書かれた % の値(見つからなければ null) */
  function maxHeightPercent(block: string): number | null {
    const m = block.match(/max-height:\s*(\d+(?:\.\d+)?)%/)
    return m ? Number(m[1]) : null
  }

  const FIXED_AREAS = ['.modal.maximized .detail-header', '.modal.maximized .detail-footer']

  it('固定領域(ヘッダ・フッタ)も縮小でき、上限を超えたら自身がスクロールする', () => {
    for (const selector of FIXED_AREAS) {
      const block = ruleBlock(selector)
      // 縮小可 + min-height: 0 が無いと、中身の最小高さのまま押し出してしまう
      expect(block, `${selector}: 縮小可(flex-shrink)にしてください`).toMatch(
        /flex:\s*0\s+1\s+auto/,
      )
      expect(block, `${selector}: min-height: 0 が必要です`).toMatch(/min-height:\s*0/)
      expect(block, `${selector}: 上限を超えた分の退避スクロールが必要です`).toMatch(
        /overflow-y:\s*auto/,
      )
      expect(maxHeightPercent(block), `${selector}: max-height の上限(%)が必要です`).not.toBeNull()
    }
  })

  it('固定領域の上限の合計が 100% 未満で、可変領域が高さ 0 に潰れない', () => {
    const total = FIXED_AREAS.reduce((sum, s) => sum + (maxHeightPercent(ruleBlock(s)) ?? 100), 0)
    expect(total, '固定領域の上限合計が 100% 以上だと本文の高さが残りません').toBeLessThan(100)
  })

  it('可変領域だけがスクロールを担う(二重スクロールにしない)', () => {
    const body = ruleBlock('.modal.maximized .detail-body')
    expect(body).toMatch(/flex:\s*1\s+1\s+auto/)
    expect(body).toMatch(/min-height:\s*0/)
    expect(body).toMatch(/overflow-y:\s*auto/)

    // モーダル自身は動かさない(可変領域との二重スクロールを避ける)
    expect(ruleBlock('.modal.maximized')).toMatch(/overflow:\s*hidden/)
    // 本文・コメントの内部スクロールは最大化中だけ解除する
    const inner = ruleBlock('.modal.maximized .detail-description,\n.modal.maximized .comment-list')
    expect(inner).toMatch(/max-height:\s*none/)
  })
})
