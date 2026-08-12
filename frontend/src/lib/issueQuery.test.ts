/**
 * 課題の検索条件 → IssueQuery 変換のテスト。
 *
 * 課題抽出(IssuesView)と一括更新のテンプレート出力(BulkUpdateView)が
 * 同じ条件フォームを使うため、変換規則はここで固定する。
 */
import { describe, expect, it } from 'vitest'
import { buildIssueQuery, newIssueConditions, resetIssueConditions } from './issueQuery'

describe('newIssueConditions', () => {
  it('既定は空条件・AND 連結', () => {
    expect(newIssueConditions()).toEqual({
      keyword: '',
      keywordMode: 'and',
      updatedFrom: '',
      updatedTo: '',
      createdFrom: '',
      createdTo: '',
      statusName: '',
      assigneeName: '',
    })
  })
})

describe('buildIssueQuery', () => {
  it('未入力の条件は送らない(プロジェクト ID のみ)', () => {
    expect(buildIssueQuery(7, newIssueConditions())).toEqual({ projectId: 7 })
  })

  it('キーワードは前後の空白を落として連結方法と一緒に送る', () => {
    const cond = newIssueConditions()
    cond.keyword = '  ログイン 不具合  '
    cond.keywordMode = 'or'

    expect(buildIssueQuery(7, cond)).toEqual({
      projectId: 7,
      keyword: 'ログイン 不具合',
      keywordMode: 'or',
    })
  })

  it('キーワードが空白のみなら連結方法も送らない(意味を持たないため)', () => {
    const cond = newIssueConditions()
    cond.keyword = '   '
    cond.keywordMode = 'or'

    expect(buildIssueQuery(7, cond)).toEqual({ projectId: 7 })
  })

  it('日付範囲・状態・担当者はそのまま送る', () => {
    const cond = newIssueConditions()
    cond.updatedFrom = '2026-08-01'
    cond.updatedTo = '2026-08-31'
    cond.createdFrom = '2026-07-01'
    cond.createdTo = '2026-07-31'
    cond.statusName = '処理中'
    cond.assigneeName = '山田 太郎'

    expect(buildIssueQuery(7, cond)).toEqual({
      projectId: 7,
      updatedFrom: '2026-08-01',
      updatedTo: '2026-08-31',
      createdFrom: '2026-07-01',
      createdTo: '2026-07-31',
      statusName: '処理中',
      assigneeName: '山田 太郎',
    })
  })
})

describe('resetIssueConditions', () => {
  it('同じオブジェクトのまま初期値へ戻す(reactive の参照を保つため)', () => {
    const cond = newIssueConditions()
    cond.keyword = 'ログイン'
    cond.keywordMode = 'or'
    cond.statusName = '処理中'

    resetIssueConditions(cond)

    expect(cond).toEqual(newIssueConditions())
  })
})
