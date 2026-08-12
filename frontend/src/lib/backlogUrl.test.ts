/**
 * backlogUrl.ts のテスト(TDD の Red を先に書く)。
 *
 * 課題キーのクリックでコピーする URL は、スペース URL の書き方(末尾スラッシュの
 * 有無)に左右されてはならない。組み立てできない入力(どちらかが空)では
 * 「壊れた URL」を作らず空文字を返すことも、ここで固定しておく。
 */
import { describe, expect, it } from 'vitest'
import { issueUrl } from './backlogUrl'

describe('issueUrl', () => {
  it('スペース URL と課題キーを /view/ で連結する', () => {
    expect(issueUrl('https://example.backlog.jp', 'SAMPLE-1')).toBe(
      'https://example.backlog.jp/view/SAMPLE-1',
    )
  })

  it('スペース URL の末尾スラッシュは重複しないよう取り除く', () => {
    expect(issueUrl('https://example.backlog.jp/', 'SAMPLE-1')).toBe(
      'https://example.backlog.jp/view/SAMPLE-1',
    )
    expect(issueUrl('https://example.backlog.jp///', 'SAMPLE-1')).toBe(
      'https://example.backlog.jp/view/SAMPLE-1',
    )
  })

  it('前後の空白は無視する', () => {
    expect(issueUrl('  https://example.backlog.com/  ', ' SAMPLE-12 ')).toBe(
      'https://example.backlog.com/view/SAMPLE-12',
    )
  })

  it('スペース URL が空なら空文字を返す(壊れた URL を作らない)', () => {
    expect(issueUrl('', 'SAMPLE-1')).toBe('')
    expect(issueUrl('   ', 'SAMPLE-1')).toBe('')
    // 末尾スラッシュだけの入力も、取り除くと空になるため URL は作らない
    expect(issueUrl('/', 'SAMPLE-1')).toBe('')
  })

  it('課題キーが空なら空文字を返す', () => {
    expect(issueUrl('https://example.backlog.jp', '')).toBe('')
    expect(issueUrl('https://example.backlog.jp', '  ')).toBe('')
  })

  it('サブディレクトリ付きのスペース URL でもパスを保つ', () => {
    expect(issueUrl('https://example.com/backlog/', 'SAMPLE-1')).toBe(
      'https://example.com/backlog/view/SAMPLE-1',
    )
  })

  it('アンダースコアを含む課題キーもエスケープせずそのまま結合する', () => {
    // プロジェクトキーには英数字・アンダースコアが使える(Backlog の仕様)。
    // いずれも URL の非予約文字なのでエスケープ不要
    expect(issueUrl('https://example.backlog.jp', 'MY_PROJ-12')).toBe(
      'https://example.backlog.jp/view/MY_PROJ-12',
    )
  })
})
