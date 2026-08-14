/**
 * 機械値(enum・構造化値)のフロント側翻訳(設計 §3.1 / §3.3)。
 *
 * Backlog / Go 側から届く値のうち、**機械値**(処理区分 action・行状態 status・
 * ロール種別 roleType・同期モード mode)は列挙が有限なのでフロントで翻訳できる。
 * フェーズ 1 ではこれらを翻訳対象に含める。
 *
 * **表示の正は生の機械値**である。Go は互換のため解決済みの日本語ラベル
 * (`actionLabel` / `statusLabel` / `roleName`)も返し続けるが、**表示経路では
 * 使わない**(英語 UI で日本語が混ざるため)。契約フィールド自体は残す。
 * 既存の lib/backend.ts の ACTION_LABELS / ROW_STATUS_LABELS(および
 * actionLabel() / rowStatusLabel())は、この対応表に置き換わっていく方向であり、
 * 画面変換が全て済んだ時点で表示経路からは呼ばれなくなる
 * (モックバックエンドの値生成用途が残るため、削除は後続の統合時に判断する)。
 *
 * 動的キーの規律(設計 §3.3): `t()` に渡すのは文字列リテラルのみとし、
 * 集合はこのファイルの **const 対応表**(値がカタログキーのリテラル)を経由する。
 * localeCatalog.test.ts はこの対応表の値も「参照済みキー」として解析する。
 */
import type { TranslateFn } from './columnLabels'

export type { TranslateFn }

/** 一括更新の処理区分(Go 側 bulk.Action と対) */
export const ACTION_LABEL_KEYS = {
  create: 'common.enum.action.create',
  update: 'common.enum.action.update',
  skip: 'common.enum.action.skip',
} as const

/** 一括更新の行状態(Go 側 bulk の行ステータスと対) */
export const ROW_STATUS_LABEL_KEYS = {
  pending: 'common.enum.rowStatus.pending',
  sending: 'common.enum.rowStatus.sending',
  done: 'common.enum.rowStatus.done',
  error: 'common.enum.rowStatus.error',
  conflict: 'common.enum.rowStatus.conflict',
  skip: 'common.enum.rowStatus.skip',
} as const

/**
 * スペースのロール種別。キーは **Backlog API の実値**(1〜6)。
 * internal/backlogclient/roletype.go の定数と対応する。
 */
export const ROLE_TYPE_LABEL_KEYS: Record<number, string> = {
  1: 'common.enum.roleType.role1',
  2: 'common.enum.roleType.role2',
  3: 'common.enum.roleType.role3',
  4: 'common.enum.roleType.role4',
  5: 'common.enum.roleType.role5',
  6: 'common.enum.roleType.role6',
}

/** 未知のロール種別(数値を添えて表示する) */
const ROLE_TYPE_UNKNOWN_KEY = 'common.enum.roleType.unknown'

/** 同期モード(SyncMode / SyncResult.mode) */
export const SYNC_MODE_LABEL_KEYS = {
  auto: 'common.enum.syncMode.auto',
  full: 'common.enum.syncMode.full',
  incremental: 'common.enum.syncMode.incremental',
} as const

/**
 * 対応表から訳を引く。未知の値は**そのまま返す**
 * (訳を捏造せず、内部値が見えることで異常に気付けるようにする)。
 */
function translate(t: TranslateFn, keys: Record<string, string>, value: string): string {
  const path = keys[value]
  return path ? t(path) : value
}

/** 処理区分(action)の表示名 */
export function translateAction(t: TranslateFn, action: string): string {
  return translate(t, ACTION_LABEL_KEYS, action)
}

/** 行状態(status)の表示名 */
export function translateRowStatus(t: TranslateFn, status: string): string {
  return translate(t, ROW_STATUS_LABEL_KEYS, status)
}

/** ロール種別(roleType の実値)の表示名。未知の値は「不明(N)」形式 */
export function translateRoleType(t: TranslateFn, roleType: number): string {
  const path = ROLE_TYPE_LABEL_KEYS[roleType]
  return path ? t(path) : t(ROLE_TYPE_UNKNOWN_KEY, { value: roleType })
}

/** 同期モード(mode)の表示名 */
export function translateSyncMode(t: TranslateFn, mode: string): string {
  return translate(t, SYNC_MODE_LABEL_KEYS, mode)
}
