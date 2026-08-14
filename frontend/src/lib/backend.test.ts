/**
 * backend.ts の純ヘルパのテスト(R15)。
 *
 * backend.ts 全体は Wails バインディングのラッパーだが、モジュールの読み込み時に
 * window へ触れる処理は無く(ランタイム探索はすべて関数の内側)、他モジュールへの
 * import も持たないため、純ヘルパだけを取り出すリファクタをせずそのまま import する。
 *
 * 数値の桁区切り(toLocaleString)は実行環境のロケールに依存するため、
 * 4 桁以上の値を含む期待値はテスト側でも toLocaleString を通して比較する。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  CUSTOM_COLUMN_PREFIX,
  actionLabel,
  copyToClipboard,
  customColumnKey,
  formatSyncProgress,
  getBackend,
  newSyncRunId,
  rowStatusLabel,
  type SyncPhase,
  type SyncProgress,
} from './backend'

/** 進捗イベントを組み立てる(既定値は「取得中・総件数不明」) */
function progress(over: Partial<SyncProgress> & { phase: SyncPhase }): SyncProgress {
  return { runId: 'run-1', profileId: 'p1', projectId: 1, fetched: 0, total: 0, ...over }
}

describe('formatSyncProgress', () => {
  it('総件数の確認中', () => {
    expect(formatSyncProgress(progress({ phase: 'count' }))).toBe('総件数を確認中...')
  })

  it('取得中は総件数が分かっていれば分母も出す', () => {
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched: 30, total: 120 }))).toBe(
      '取得中 30 / 120 件',
    )
  })

  it('取得中で総件数が不明なら分母を出さない(0 件中と誤解させないため)', () => {
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched: 30, total: 0 }))).toBe(
      '取得中 30 件',
    )
  })

  it('4 桁以上は桁区切りを入れる', () => {
    const fetched = 12345
    const total = 67890
    expect(formatSyncProgress(progress({ phase: 'fetch', fetched, total }))).toBe(
      `取得中 ${fetched.toLocaleString()} / ${total.toLocaleString()} 件`,
    )
  })

  it('削除検知中', () => {
    expect(formatSyncProgress(progress({ phase: 'deleteScan', fetched: 120 }))).toBe(
      '削除された課題を確認中(120 件取得済み)',
    )
  })

  it('完了', () => {
    expect(formatSyncProgress(progress({ phase: 'done', fetched: 120 }))).toBe(
      '取得完了 120 件(仕上げ中...)',
    )
  })

  it('未知のフェーズは空文字(表示しない)', () => {
    const unknown = progress({ phase: 'unknown' as SyncPhase })
    expect(formatSyncProgress(unknown)).toBe('')
  })
})

describe('customColumnKey', () => {
  it('Go 側の規約 cf_{定義ID} と同じ列キーを作る', () => {
    expect(customColumnKey(12)).toBe('cf_12')
    expect(customColumnKey(0)).toBe('cf_0')
  })

  it('接頭辞は公開定数と一致する', () => {
    expect(CUSTOM_COLUMN_PREFIX).toBe('cf_')
    expect(customColumnKey(9).startsWith(CUSTOM_COLUMN_PREFIX)).toBe(true)
  })
})

describe('actionLabel', () => {
  it('既知の処理区分は日本語の表示名にする', () => {
    expect(actionLabel('create')).toBe('新規追加')
    expect(actionLabel('update')).toBe('更新')
    expect(actionLabel('skip')).toBe('変更なし')
  })

  it('未知の値はそのまま返す', () => {
    expect(actionLabel('unknown')).toBe('unknown')
    expect(actionLabel('')).toBe('')
  })
})

describe('rowStatusLabel', () => {
  it('既知の行状態は日本語の表示名にする', () => {
    expect(rowStatusLabel('pending')).toBe('未処理')
    expect(rowStatusLabel('sending')).toBe('送信中(結果未確認)')
    expect(rowStatusLabel('done')).toBe('完了')
    expect(rowStatusLabel('error')).toBe('失敗')
    expect(rowStatusLabel('conflict')).toBe('競合')
    expect(rowStatusLabel('skip')).toBe('変更なし')
  })

  it('未知の値はそのまま返す', () => {
    expect(rowStatusLabel('unknown')).toBe('unknown')
    expect(rowStatusLabel('')).toBe('')
  })
})

describe('newSyncRunId', () => {
  it('sync- で始まる ID を返す', () => {
    expect(newSyncRunId()).toMatch(/^sync-\d+-\d+-[0-9a-z]+$/)
  })

  it('呼び出すたびに異なる ID を返す', () => {
    const ids = new Set(Array.from({ length: 100 }, () => newSyncRunId()))
    expect(ids.size).toBe(100)
  })
})

/**
 * copyToClipboard は Wails ランタイム(window.runtime)と navigator.clipboard を
 * 実行時に探すだけの薄いヘルパのため、両方を差し替えて経路の選択を検証する
 * (実際にクリップボードへ書き込めるかは GUI での手動確認。TDD 例外)。
 */
describe('copyToClipboard', () => {
  /** window.runtime を差し替える(削除する場合は undefined を渡す) */
  function setRuntime(rt: unknown) {
    const w = window as unknown as Record<string, unknown>
    if (rt === undefined) delete w['runtime']
    else w['runtime'] = rt
  }

  /** navigator.clipboard を差し替える(happy-dom では getter のため defineProperty で上書きする) */
  function setClipboard(clipboard: unknown) {
    Object.defineProperty(navigator, 'clipboard', {
      value: clipboard,
      configurable: true,
      writable: true,
    })
  }

  afterEach(() => {
    setRuntime(undefined)
    setClipboard(undefined)
  })

  it('Wails ランタイムがあれば ClipboardSetText を使う', async () => {
    const setText = vi.fn().mockResolvedValue(true)
    const writeText = vi.fn().mockResolvedValue(undefined)
    setRuntime({ ClipboardSetText: setText })
    setClipboard({ writeText })

    await copyToClipboard('https://example.backlog.jp/view/SAMPLE-1')

    expect(setText).toHaveBeenCalledWith('https://example.backlog.jp/view/SAMPLE-1')
    // ランタイムがある場合はブラウザ API を使わない(WebView の権限に依存させないため)
    expect(writeText).not.toHaveBeenCalled()
  })

  it('ClipboardSetText が false を返したら失敗として扱う', async () => {
    setRuntime({ ClipboardSetText: vi.fn().mockResolvedValue(false) })
    await expect(copyToClipboard('text')).rejects.toThrow()
  })

  it('ClipboardSetText が失敗したらそのエラーを伝える', async () => {
    setRuntime({ ClipboardSetText: vi.fn().mockRejectedValue(new Error('クリップボード異常')) })
    await expect(copyToClipboard('text')).rejects.toThrow('クリップボード異常')
  })

  it('ランタイムが無ければ navigator.clipboard へフォールバックする', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setRuntime(undefined)
    setClipboard({ writeText })

    await copyToClipboard('text')

    expect(writeText).toHaveBeenCalledWith('text')
  })

  it('古いランタイム(ClipboardSetText 無し)でも navigator.clipboard を使う', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    setRuntime({ EventsOn: vi.fn() })
    setClipboard({ writeText })

    await copyToClipboard('text')

    expect(writeText).toHaveBeenCalledWith('text')
  })

  it('どちらも使えない環境ではエラーにする(黙って成功したことにしない)', async () => {
    setRuntime(undefined)
    setClipboard(undefined)
    await expect(copyToClipboard('text')).rejects.toThrow()
  })
})

/**
 * モックバックエンドの課題詳細(Wails 外の画面確認で使う)。
 *
 * Wails ランタイムが無い環境では getBackend() がモックを返すため、
 * 詳細ポップアップが期待する契約(必須項目・カスタム属性の形・
 * 見つからない課題はエラー)をここで固定する。
 */
describe('モックバックエンドの getIssueDetail', () => {
  /** モックの初期データはプロジェクト 101(SAMPLE)に投入されている */
  const projectId = 101

  it('課題キーで詳細を返す', async () => {
    const detail = await getBackend().getIssueDetail('p1', projectId, 'SAMPLE-1')

    expect(detail.issueKey).toBe('SAMPLE-1')
    expect(detail.summary).not.toBe('')
    // 本文・取得時刻は「<日時> 時点の内容です」の注記に使う
    expect(detail.description).not.toBe('')
    expect(detail.fetchedAt).not.toBe('')
    // カスタム属性は常に配列(null を返さない)
    expect(Array.isArray(detail.customFields)).toBe(true)
    for (const f of detail.customFields) {
      expect(typeof f.name).toBe('string')
      expect(typeof f.value).toBe('string')
    }
  })

  it('親課題は課題キー・ID 表記・親なしの 3 通りを返す', async () => {
    const backend = getBackend()
    // 5 の倍数は 1 つ前の課題が親(ローカルにある親 = 課題キー表示)
    expect((await backend.getIssueDetail('p1', projectId, 'SAMPLE-5')).parentIssueKey).toBe(
      'SAMPLE-4',
    )
    // 7 の倍数はローカルに無い親(ID:<数値> 表示)
    expect((await backend.getIssueDetail('p1', projectId, 'SAMPLE-7')).parentIssueKey).toBe(
      'ID:999999',
    )
    // それ以外は親なし(空文字)
    expect((await backend.getIssueDetail('p1', projectId, 'SAMPLE-3')).parentIssueKey).toBe('')
  })

  it('ローカルに無い課題はエラーにする(空の詳細を返さない)', async () => {
    await expect(getBackend().getIssueDetail('p1', projectId, 'SAMPLE-99999')).rejects.toThrow()
  })
})

/**
 * モックバックエンドの refreshIssueDetail(詳細ポップアップの「最新の状態を取得」)。
 *
 * モックは Backlog を持たないため、Go 側の「取得した内容でローカルを上書きし、
 * 反映後の詳細を返す」という契約だけを再現する(件名に印を付け、取得時刻を進める)。
 */
describe('モックバックエンドの refreshIssueDetail', () => {
  /** モックの初期データはプロジェクト 101(SAMPLE)に投入されている */
  const projectId = 101

  it('反映後の詳細を返し、以降の詳細取得にも反映されている', async () => {
    const backend = getBackend()
    const before = await backend.getIssueDetail('p1', projectId, 'SAMPLE-2')

    const after = await backend.refreshIssueDetail('p1', projectId, 'SAMPLE-2')

    expect(after.issueKey).toBe('SAMPLE-2')
    // 「取得したことが分かる変化」があること(内容と注記の時刻の両方)
    expect(after.summary).not.toBe(before.summary)
    expect(after.fetchedAt).not.toBe(before.fetchedAt)

    // ローカルへ反映済みなので、通常の詳細取得でも同じ内容が返る
    const again = await backend.getIssueDetail('p1', projectId, 'SAMPLE-2')
    expect(again.summary).toBe(after.summary)
    expect(again.fetchedAt).toBe(after.fetchedAt)
  })

  it('ローカルに無い課題はエラーにする(空の詳細を返さない)', async () => {
    await expect(
      getBackend().refreshIssueDetail('p1', projectId, 'SAMPLE-99999'),
    ).rejects.toThrow()
  })
})

/**
 * モックバックエンドのコメント(オンデマンド取得)。
 *
 * コメントは同期では取得されず、「最新の状態を取得」を押したときだけ入る。
 * 画面はこの契約(未取得 / 取得済み / 変更履歴のみの件数 / 上限到達)で
 * 注意書きとコメントセクションを出し分けるため、ここで固定する。
 */
describe('モックバックエンドのコメント', () => {
  /** モックの初期データはプロジェクト 101(SAMPLE)に投入されている */
  const projectId = 101

  it('取得前はコメント未取得の状態を返す', async () => {
    const detail = await getBackend().getIssueDetail('p1', projectId, 'SAMPLE-3')

    // 空文字 = 未取得(画面は「『最新の状態を取得』で取得できます」と案内する)
    expect(detail.commentsFetchedAt).toBe('')
    expect(detail.comments).toEqual([])
    expect(detail.commentsHistoryOnly).toBe(0)
    expect(detail.commentsTruncated).toBe(false)
    // 部分失敗の警告は常に配列(null を返さない)
    expect(detail.warnings).toEqual([])
  })

  it('「最新の状態を取得」でコメントと取得時刻が入る', async () => {
    const detail = await getBackend().refreshIssueDetail('p1', projectId, 'SAMPLE-4')

    expect(detail.commentsFetchedAt).not.toBe('')
    expect(detail.comments.length).toBeGreaterThan(0)
    for (const c of detail.comments) {
      expect(typeof c.authorName).toBe('string')
      expect(c.content).not.toBe('')
      expect(c.created).not.toBe('')
    }
    // 本文が無い項目(状態変更等の変更履歴)は本文を持たず、件数だけで伝える
    expect(detail.commentsHistoryOnly).toBeGreaterThan(0)

    // 取得後は通常の詳細取得(ローカル参照)でも同じコメントが返る
    const again = await getBackend().getIssueDetail('p1', projectId, 'SAMPLE-4')
    expect(again.commentsFetchedAt).toBe(detail.commentsFetchedAt)
    expect(again.comments.length).toBe(detail.comments.length)
  })

  it('コメントは新しい順に並ぶ', async () => {
    const detail = await getBackend().refreshIssueDetail('p1', projectId, 'SAMPLE-6')
    const created = detail.comments.map((c) => c.created)
    expect(created).toEqual([...created].sort().reverse())
  })

  it('取得上限に達した課題は上限到達を伝える', async () => {
    // 10 の倍数の課題で「以前のコメントは Backlog で確認」の表示を手元で確認できる
    const detail = await getBackend().refreshIssueDetail('p1', projectId, 'SAMPLE-10')
    expect(detail.commentsTruncated).toBe(true)
  })
})

/**
 * モックバックエンドの searchIssues のページング(limit / offset)。
 *
 * 画面(IssuesView)は 1 ページぶんだけを取得して改ページするため、
 * Wails 外の画面確認でもページ移動が同じ意味論で動く必要がある。
 * Go 側(store.IssueFilter の Limit / Offset)と同じく
 * 「一致した全件のうち offset 件を読み飛ばして limit 件を返す・
 *  total は切り出し前の一致件数」を固定する。
 */
describe('モックバックエンドの searchIssues のページング', () => {
  /** モックの初期データはプロジェクト 101(SAMPLE)に投入されている */
  const projectId = 101

  it('offset 無し・limit 指定は先頭から切り出す', async () => {
    const res = await getBackend().searchIssues('p1', { projectId, limit: 5 })

    expect(res.rows).toHaveLength(5)
    // total は切り出し前の一致件数(Excel 出力の全件性の案内に使う)
    expect(res.total).toBeGreaterThan(5)
  })

  it('offset のぶんだけ読み飛ばす(ページ移動)', async () => {
    const backend = getBackend()
    const head = await backend.searchIssues('p1', { projectId, limit: 10 })
    const second = await backend.searchIssues('p1', { projectId, limit: 5, offset: 5 })

    expect(second.rows.map((r) => r.issueKey)).toEqual(
      head.rows.slice(5, 10).map((r) => r.issueKey),
    )
    expect(second.total).toBe(head.total)
  })

  it('総件数を超える offset は空になる(total は維持する)', async () => {
    const backend = getBackend()
    const all = await backend.searchIssues('p1', { projectId })
    const over = await backend.searchIssues('p1', {
      projectId,
      limit: 10,
      offset: all.total + 10,
    })

    expect(over.rows).toEqual([])
    expect(over.total).toBe(all.total)
  })

  it('最終ページは残りの件数だけ返す', async () => {
    const backend = getBackend()
    const all = await backend.searchIssues('p1', { projectId })
    const pageSize = 10
    const lastOffset = Math.floor((all.total - 1) / pageSize) * pageSize

    const last = await backend.searchIssues('p1', {
      projectId,
      limit: pageSize,
      offset: lastOffset,
    })

    expect(last.rows).toHaveLength(all.total - lastOffset)
  })

  it('負の offset は先頭として扱う(Go 側の 0 丸めと同じ)', async () => {
    const res = await getBackend().searchIssues('p1', { projectId, limit: 3, offset: -5 })
    const head = await getBackend().searchIssues('p1', { projectId, limit: 3 })

    expect(res.rows.map((r) => r.issueKey)).toEqual(head.rows.map((r) => r.issueKey))
  })
})

/**
 * モックバックエンドのテンプレート出力(一括更新・追加の①)。
 *
 * テンプレートは検索条件で絞り込めるため(条件なし = 全件)、
 * モックでも条件を反映することを固定する。Wails 外の画面確認で
 * 「条件を入れても件数が変わらない」という食い違いを起こさないため。
 */
describe('モックバックエンドの exportBulkTemplate', () => {
  /** モックの初期データはプロジェクト 101(SAMPLE)に投入されている */
  const projectId = 101

  it('条件なしなら全件を出力する', async () => {
    const all = await getBackend().exportBulkTemplate('p1', projectId, { projectId })
    expect(all.rows).toBeGreaterThan(0)
  })

  it('検索条件に一致した課題だけを出力する', async () => {
    const backend = getBackend()
    const all = await backend.exportBulkTemplate('p1', projectId, { projectId })
    const filtered = await backend.exportBulkTemplate('p1', projectId, {
      projectId,
      statusName: '処理中',
    })

    expect(filtered.rows).toBeGreaterThan(0)
    expect(filtered.rows).toBeLessThan(all.rows)
  })
})
