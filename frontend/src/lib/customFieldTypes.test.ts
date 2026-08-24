import { describe, expect, it } from 'vitest'
import {
  CUSTOM_FIELD_CHECKBOX,
  CUSTOM_FIELD_DATE,
  CUSTOM_FIELD_MULTIPLE_LIST,
  CUSTOM_FIELD_NUMERIC,
  CUSTOM_FIELD_RADIO,
  CUSTOM_FIELD_SINGLE_LIST,
  isCustomFieldListType,
} from './customFieldTypes'

describe('customFieldTypes', () => {
  it('Backlog APIの既知typeIdを用途別に判定する', () => {
    expect(CUSTOM_FIELD_NUMERIC).toBe(3)
    expect(CUSTOM_FIELD_DATE).toBe(4)
    expect(
      [
        CUSTOM_FIELD_SINGLE_LIST,
        CUSTOM_FIELD_MULTIPLE_LIST,
        CUSTOM_FIELD_CHECKBOX,
        CUSTOM_FIELD_RADIO,
      ].every(isCustomFieldListType),
    ).toBe(true)
    expect(isCustomFieldListType(CUSTOM_FIELD_NUMERIC)).toBe(false)
    expect(isCustomFieldListType(CUSTOM_FIELD_DATE)).toBe(false)
  })
})
