/** Backlog API のカスタム属性 typeId（数値） */
export const CUSTOM_FIELD_NUMERIC = 3
/** Backlog API のカスタム属性 typeId（日付） */
export const CUSTOM_FIELD_DATE = 4
/** Backlog API のカスタム属性 typeId（単一リスト） */
export const CUSTOM_FIELD_SINGLE_LIST = 5
/** Backlog API のカスタム属性 typeId（複数リスト） */
export const CUSTOM_FIELD_MULTIPLE_LIST = 6
/** Backlog API のカスタム属性 typeId（チェックボックス） */
export const CUSTOM_FIELD_CHECKBOX = 7
/** Backlog API のカスタム属性 typeId（ラジオ） */
export const CUSTOM_FIELD_RADIO = 8

const CUSTOM_FIELD_LIST_TYPES = new Set([
  CUSTOM_FIELD_SINGLE_LIST,
  CUSTOM_FIELD_MULTIPLE_LIST,
  CUSTOM_FIELD_CHECKBOX,
  CUSTOM_FIELD_RADIO,
])

export function isCustomFieldListType(typeId: number): boolean {
  return CUSTOM_FIELD_LIST_TYPES.has(typeId)
}
