/**
 * 機械値(enum)のフロント側翻訳(lib/enumLabels.ts)の検証。
 *
 * 設計 §3.1: 表示の正は**生の機械値**(action / status / roleType / mode)であり、
 * Go が解決済みで返す日本語ラベル(actionLabel / statusLabel / roleName)は
 * 表示に使わない。ここでは対応表とヘルパの振る舞いだけを固定し、実際の画面での
 * 差し替えは後続の画面変換担当が行う。
 */
import { describe, expect, it } from 'vitest'

import {
  ACTION_LABEL_KEYS,
  ROLE_TYPE_LABEL_KEYS,
  ROW_STATUS_LABEL_KEYS,
  SYNC_MODE_LABEL_KEYS,
  translateAction,
  translateRoleType,
  translateRowStatus,
  translateSyncMode,
} from './enumLabels'
import { messages, type Language } from './i18n'

/** カタログの実文字列を引く(実装と同じ経路を通さないための素朴な辞書引き) */
function lookup(locale: Language, path: string): string {
  return path
    .split('.')
    .reduce<unknown>((acc, k) => (acc as Record<string, unknown>)[k], messages[locale]) as string
}

/** テスト用の翻訳関数(名前付き補間にも対応する) */
function translator(locale: Language) {
  return (key: string, named?: Record<string, unknown>): string => {
    const raw = lookup(locale, key)
    if (typeof raw !== 'string') throw new Error(`カタログに無いキーです: ${key}`)
    if (!named) return raw
    return raw.replace(/\{(\w+)\}/g, (_m, name: string) => String(named[name] ?? ''))
  }
}

const ja = translator('ja')
const en = translator('en')

describe('対応表', () => {
  it('カタログキーは名前空間ごとに分かれている', () => {
    for (const path of Object.values(ACTION_LABEL_KEYS)) {
      expect(path.startsWith('common.enum.action.')).toBe(true)
    }
    for (const path of Object.values(ROW_STATUS_LABEL_KEYS)) {
      expect(path.startsWith('common.enum.rowStatus.')).toBe(true)
    }
    for (const path of Object.values(ROLE_TYPE_LABEL_KEYS)) {
      expect(path.startsWith('common.enum.roleType.')).toBe(true)
    }
    for (const path of Object.values(SYNC_MODE_LABEL_KEYS)) {
      expect(path.startsWith('common.enum.syncMode.')).toBe(true)
    }
  })

  it('Backlog API の roleType 実値(1〜6)をすべて持つ', () => {
    expect(Object.keys(ROLE_TYPE_LABEL_KEYS).sort()).toEqual(['1', '2', '3', '4', '5', '6'])
  })
})

describe('translateAction', () => {
  it('生の action を表示言語で訳す', () => {
    expect(translateAction(ja, 'create')).toBe('新規追加')
    expect(translateAction(ja, 'update')).toBe('更新')
    expect(translateAction(ja, 'skip')).toBe('変更なし')
    expect(translateAction(en, 'create')).toBe('Add new')
    expect(translateAction(en, 'skip')).toBe('No change')
  })

  it('未知の値はそのまま返す(内部値を素で見せるが、翻訳は捏造しない)', () => {
    expect(translateAction(ja, 'archive')).toBe('archive')
    expect(translateAction(ja, '')).toBe('')
  })
})

describe('translateRowStatus', () => {
  it('生の status を表示言語で訳す', () => {
    expect(translateRowStatus(ja, 'pending')).toBe('未処理')
    expect(translateRowStatus(ja, 'conflict')).toBe('競合')
    expect(translateRowStatus(en, 'done')).toBe('Done')
    expect(translateRowStatus(en, 'error')).toBe('Failed')
  })

  it('未知の値はそのまま返す', () => {
    expect(translateRowStatus(ja, 'queued')).toBe('queued')
  })
})

describe('translateRoleType', () => {
  it('roleType の実値を表示言語で訳す', () => {
    expect(translateRoleType(ja, 1)).toBe('管理者')
    expect(translateRoleType(ja, 6)).toBe('ゲスト閲覧者')
    expect(translateRoleType(en, 1)).toBe('Administrator')
    expect(translateRoleType(en, 3)).toBe('Reporter')
  })

  it('英語表記は Backlog 公式の用語に合わせる', () => {
    // roleType=2 は Backlog 公式英語 UI では "Normal User"(General User ではない)
    expect(translateRoleType(en, 2)).toBe('Normal User')
    expect(translateRoleType(en, 4)).toBe('Viewer')
    expect(translateRoleType(en, 5)).toBe('Guest Reporter')
    expect(translateRoleType(en, 6)).toBe('Guest Viewer')
  })

  it('未知の値は数値を含めて「不明」と表示する', () => {
    expect(translateRoleType(ja, 9)).toBe('不明(9)')
    expect(translateRoleType(en, 0)).toBe('Unknown (0)')
  })
})

describe('translateSyncMode', () => {
  it('同期モードを表示言語で訳す', () => {
    expect(translateSyncMode(ja, 'full')).toBe('フル同期')
    expect(translateSyncMode(ja, 'incremental')).toBe('差分同期')
    expect(translateSyncMode(en, 'auto')).toBe('Auto')
  })

  it('未知の値はそのまま返す', () => {
    expect(translateSyncMode(ja, 'partial')).toBe('partial')
  })
})

describe('Go 由来の日本語ラベルを使わないこと', () => {
  it('英語表示では、Go が日本語ラベルを返していても英語になる', () => {
    // 実データは { action: 'create', actionLabel: '新規追加' } の形で届くが、
    // 表示は生の action から解決する(設計 §3.1)。
    const row = { action: 'create', actionLabel: '新規追加', status: 'done', statusLabel: '完了' }
    expect(translateAction(en, row.action)).toBe('Add new')
    expect(translateRowStatus(en, row.status)).toBe('Done')
  })
})
