/**
 * 列ラベルのフロント側翻訳(設計 §3.3)。
 *
 * Go の `ListExportColumns`(internal/export/columns.go)が返す label は
 * **日本語のまま**(Excel のヘッダと同一という契約を崩さないため)。画面は
 * 固定列 key をこの対応表でカタログのキーへ写して翻訳する。
 *
 * フォールバックの方針:
 *  - `cf_{定義ID}`(カスタム属性列)は**ユーザ定義**のため翻訳しない。Go label
 *    (= 属性の定義名)をそのまま表示する。
 *  - それ以外の未知 key は「固定列の翻訳漏れ」なので、実行時は Go label で縮退
 *    しつつ console 警告を出す。開発時は columnLabels.test.ts が
 *    internal/export/{issue,user}.go の列定義とキー集合を突き合わせて検知する。
 *
 * 課題列とユーザ列は名前空間を分ける(`name` や `roleType` のような同名 key が
 * 別物を指すため)。
 */

/** カスタム属性列の列キーの接頭辞(Go 側 customColumnPrefix と対) */
const CUSTOM_COLUMN_PREFIX = 'cf_'

/** 列ラベルの名前空間(課題抽出 / ユーザ抽出) */
export type ColumnNamespace = 'issue' | 'user'

/**
 * 課題抽出の固定列 key → カタログキー。
 * internal/export/issue.go の `columns` と 1 対 1(検査は columnLabels.test.ts)。
 */
export const ISSUE_COLUMN_LABEL_KEYS = {
  issueKey: 'common.column.issue.issueKey',
  summary: 'common.column.issue.summary',
  statusName: 'common.column.issue.statusName',
  assigneeName: 'common.column.issue.assigneeName',
  issueTypeName: 'common.column.issue.issueTypeName',
  priorityName: 'common.column.issue.priorityName',
  created: 'common.column.issue.created',
  updated: 'common.column.issue.updated',
  dueDate: 'common.column.issue.dueDate',
  description: 'common.column.issue.description',
  parentIssueKey: 'common.column.issue.parentIssueKey',
} as const

/**
 * ユーザ抽出の固定列 key → カタログキー。
 * internal/export/user.go の `userColumns` と 1 対 1(検査は columnLabels.test.ts)。
 */
export const USER_COLUMN_LABEL_KEYS = {
  userCode: 'common.column.user.userCode',
  name: 'common.column.user.name',
  mailAddress: 'common.column.user.mailAddress',
  roleName: 'common.column.user.roleName',
  roleType: 'common.column.user.roleType',
  teamNames: 'common.column.user.teamNames',
  projectKeys: 'common.column.user.projectKeys',
  adminProjectKeys: 'common.column.user.adminProjectKeys',
} as const

const LABEL_KEYS: Record<ColumnNamespace, Record<string, string>> = {
  issue: ISSUE_COLUMN_LABEL_KEYS,
  user: USER_COLUMN_LABEL_KEYS,
}

/**
 * 翻訳関数。vue-i18n の `t` をそのまま渡せる形にしておく
 * (lib/ は Composition API の外からも呼ばれるため、composer に依存させない)。
 */
export type TranslateFn = (key: string, named?: Record<string, unknown>) => string

/**
 * 列ラベルを表示用の文字列にする。
 *
 * @param t 翻訳関数(useI18n() の t)
 * @param namespace 課題列 / ユーザ列
 * @param key Go が返す列キー
 * @param goLabel Go が返した日本語ラベル(カスタム属性列・未知 key のときだけ使う)
 */
export function columnLabel(
  t: TranslateFn,
  namespace: ColumnNamespace,
  key: string,
  goLabel: string,
): string {
  const path = LABEL_KEYS[namespace][key]
  if (path) return t(path)

  // ユーザ定義のカスタム属性列。定義名をそのまま出すのが正しいので警告しない。
  if (key.startsWith(CUSTOM_COLUMN_PREFIX)) return goLabel

  // 固定列の翻訳漏れ。表示は Go label で縮退させ、気付けるように警告を残す。
  console.warn(
    `[columnLabels] 未知の固定列キーです(翻訳対応表に追加してください): namespace=${namespace}, key=${key}`,
  )
  return goLabel || key
}
