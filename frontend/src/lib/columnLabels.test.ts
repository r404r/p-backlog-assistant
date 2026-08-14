/**
 * 列ラベルのフロント側翻訳(lib/columnLabels.ts)の検証。
 *
 * 設計 §3.3: Go の `ListExportColumns` が返す label は日本語のまま(契約不変)で、
 * フロントが**固定列 key を辞書で翻訳**する。Go label へのフォールバックは
 * ユーザ定義と識別できる key(`cf_{定義ID}`)のみに限定し、未知の「固定」key は
 * 実行時は Go label 表示 + console 警告で縮退させつつ、**このテストで検知**する。
 *
 * さらに Go / フロントの境界を跨いだ検査として、`internal/export/issue.go` の
 * `columns` と `internal/export/user.go` の `userColumns` から列 key を抽出し、
 * 対応表のキー集合と完全一致することを確認する(Go 側にだけ列が増えた場合に、
 * フロントのテスト失敗として気付けるようにする)。
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'

import {
  columnLabel,
  ISSUE_COLUMN_LABEL_KEYS,
  USER_COLUMN_LABEL_KEYS,
} from './columnLabels'
import { messages } from './i18n'

// vitest の作業ディレクトリは frontend/(vite.config.ts のある場所)
const EXPORT_DIR = resolve(process.cwd(), '../internal/export')

/** 日本語カタログから翻訳する(実装と同じ経路を通さず、カタログの実文字列で検証する) */
function jaLabel(path: string): string {
  return path.split('.').reduce<unknown>((acc, k) => (acc as Record<string, unknown>)[k], messages.ja) as string
}

/** テスト用の翻訳関数(カタログのキーをそのまま引く) */
function t(key: string): string {
  return jaLabel(key)
}

// ---------------------------------------------------------------------------
// Go / フロント横断の契約検査
// ---------------------------------------------------------------------------

/** internal/export/*.go を連結したソース(定数の解決に使う) */
function readExportSources(): string {
  return ['issue.go', 'user.go', 'parent.go', 'columns.go']
    .map((f) => readFileSync(resolve(EXPORT_DIR, f), 'utf8'))
    .join('\n')
}

/** `var <name> = []<type>{ … }` の中身を取り出す(定義は行頭 `}` で閉じる) */
function extractVarBlock(source: string, declaration: string): string {
  const start = source.indexOf(declaration)
  if (start < 0) throw new Error(`定義が見つかりません: ${declaration}`)
  const bodyStart = start + declaration.length
  const end = source.indexOf('\n}', bodyStart)
  if (end < 0) throw new Error(`定義が閉じていません: ${declaration}`)
  return source.slice(bodyStart, end)
}

/** `const Name = "value"` を解決する(列 key が定数で書かれている場合に使う) */
function resolveGoConst(source: string, name: string): string {
  const m = source.match(new RegExp(`\\bconst\\s+${name}\\s*=\\s*"([^"]*)"`))
  if (!m) throw new Error(`定数を解決できません: ${name}`)
  return m[1]
}

/**
 * issue.go の `columns` から列 key を抽出する。
 *
 * pickerHidden(列選択に出さない列)も**抽出に含める**。出力自体は列キーを直接
 * 指定できるため、画面に出る可能性がある列は等しく翻訳対象とする。
 */
function goIssueColumnKeys(): { key: string; pickerHidden: boolean }[] {
  const source = readExportSources()
  const block = extractVarBlock(source, 'var columns = []column{')
  const out: { key: string; pickerHidden: boolean }[] = []
  // 1 列 1 行で `{key: "issueKey", …},` または `{key: ParentIssueKeyColumn, …},`
  for (const line of block.split('\n')) {
    const m = line.match(/\{key:\s*(?:"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))/)
    if (!m) continue
    const key = m[1] ?? resolveGoConst(source, m[2])
    out.push({ key, pickerHidden: /pickerHidden:\s*true/.test(line) })
  }
  return out
}

/** user.go の `userColumns` から列 key を抽出する(位置指定の構造体リテラル) */
function goUserColumnKeys(): string[] {
  const source = readExportSources()
  const block = extractVarBlock(source, 'var userColumns = []userColumn{')
  const out: string[] = []
  for (const line of block.split('\n')) {
    const m = line.match(/^\s*\{"([^"]+)",/)
    if (m) out.push(m[1])
  }
  return out
}

describe('Go の列定義との契約', () => {
  it('issue.go の columns から列 key を抽出できる(抽出自体の健全性)', () => {
    const keys = goIssueColumnKeys()
    expect(keys.length).toBeGreaterThan(5)
    expect(keys.map((c) => c.key)).toContain('issueKey')
    // 定数で書かれた列 key も解決できていること
    expect(keys.map((c) => c.key)).toContain('parentIssueKey')
    // pickerHidden の列(詳細)も抽出対象に含める
    expect(keys.find((c) => c.key === 'description')?.pickerHidden).toBe(true)
  })

  it('課題列の対応表が issue.go の固定列 key と完全一致する', () => {
    const goKeys = goIssueColumnKeys().map((c) => c.key).sort()
    expect(Object.keys(ISSUE_COLUMN_LABEL_KEYS).sort()).toEqual(goKeys)
  })

  it('ユーザ列の対応表が user.go の固定列 key と完全一致する', () => {
    const goKeys = goUserColumnKeys().sort()
    expect(goKeys.length).toBeGreaterThan(5)
    expect(Object.keys(USER_COLUMN_LABEL_KEYS).sort()).toEqual(goKeys)
  })

  it('課題列とユーザ列は名前空間が分かれている(同名 key が衝突しない)', () => {
    // roleName / statusName のような同名衝突を、名前空間で構造的に避ける
    for (const path of Object.values(ISSUE_COLUMN_LABEL_KEYS)) {
      expect(path.startsWith('common.column.issue.')).toBe(true)
    }
    for (const path of Object.values(USER_COLUMN_LABEL_KEYS)) {
      expect(path.startsWith('common.column.user.')).toBe(true)
    }
  })
})

// ---------------------------------------------------------------------------
// columnLabel
// ---------------------------------------------------------------------------

describe('columnLabel', () => {
  it('既知の課題列はカタログの訳を返す(Go label は使わない)', () => {
    expect(columnLabel(t, 'issue', 'issueKey', 'Go が返した日本語')).toBe('キー')
    expect(columnLabel(t, 'issue', 'parentIssueKey', '')).toBe('親課題キー')
  })

  it('既知のユーザ列はカタログの訳を返す', () => {
    expect(columnLabel(t, 'user', 'userCode', 'Go が返した日本語')).toBe('ユーザID')
    expect(columnLabel(t, 'user', 'roleType', '')).toBe('ロール値')
  })

  it('名前空間が違えば同じ key でも別の訳を引く', () => {
    // ユーザ列の name(名前)を課題列として引いても当たらない
    expect(columnLabel(t, 'user', 'name', 'Go label')).toBe('名前')
    expect(columnLabel(t, 'issue', 'name', 'Go label')).toBe('Go label')
  })

  it('カスタム属性列(cf_{定義ID})は Go label へフォールバックする(警告は出さない)', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})

    expect(columnLabel(t, 'issue', 'cf_123', '重要度(独自)')).toBe('重要度(独自)')

    expect(warn).not.toHaveBeenCalled()
    warn.mockRestore()
  })

  it('未知の固定列は Go label を表示しつつ console 警告を出す(翻訳漏れを隠さない)', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})

    expect(columnLabel(t, 'issue', 'milestoneName', 'マイルストーン')).toBe('マイルストーン')

    expect(warn).toHaveBeenCalledTimes(1)
    expect(String(warn.mock.calls[0][0])).toContain('milestoneName')
    warn.mockRestore()
  })

  it('未知の固定列で Go label も空なら列 key を出す(空欄にしない)', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})

    expect(columnLabel(t, 'user', 'nickname', '')).toBe('nickname')

    warn.mockRestore()
  })
})
