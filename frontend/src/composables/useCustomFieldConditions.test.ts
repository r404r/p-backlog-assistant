import { describe, expect, it } from 'vitest'
import { ref } from 'vue'
import type { CustomFieldDef } from '../lib/backend'
import { CUSTOM_FIELD_DATE, CUSTOM_FIELD_NUMERIC } from '../lib/customFieldTypes'
import { useCustomFieldConditions } from './useCustomFieldConditions'

function definition(id: number, typeId: number, items: CustomFieldDef['items'] = []): CustomFieldDef {
  return {
    id,
    typeId,
    typeName: '',
    name: `field-${id}`,
    description: '',
    required: false,
    applicableIssueTypes: [],
    allowInput: false,
    allowAddItem: false,
    items,
  }
}

describe('useCustomFieldConditions', () => {
  it('定義の型に応じた入力を検索Filterへ変換する', () => {
    const definitions = ref([
      definition(1, 1),
      definition(2, CUSTOM_FIELD_NUMERIC),
      definition(3, CUSTOM_FIELD_DATE),
      definition(4, 5, [{ id: 40, name: 'option' }]),
    ])
    const state = useCustomFieldConditions(definitions)
    state.reset()
    state.conditions.value[1]!.text = ' keyword '
    state.conditions.value[2]!.min = '10'
    state.conditions.value[2]!.max = '20'
    state.conditions.value[3]!.max = '2026-08-31'
    state.conditions.value[4]!.itemIds = [40]

    expect(state.buildFilters()).toEqual([
      { defId: 1, typeId: 1, text: 'keyword' },
      { defId: 2, typeId: CUSTOM_FIELD_NUMERIC, min: '10', max: '20' },
      { defId: 3, typeId: CUSTOM_FIELD_DATE, max: '2026-08-31' },
      { defId: 4, typeId: 5, itemIds: [40] },
    ])
    expect(state.filterCount.value).toBe(4)
    expect(state.isListField(definitions.value[3]!)).toBe(true)
  })

  it('定義を切り替えると以前の条件を残さず作り直す', () => {
    const definitions = ref([definition(1, 1)])
    const state = useCustomFieldConditions(definitions)
    state.reset()
    state.conditions.value[1]!.text = 'old'

    definitions.value = [definition(2, 1)]
    state.reset()
    expect(state.conditions.value).toEqual({
      2: { text: '', min: '', max: '', itemIds: [] },
    })
    expect(state.buildFilters()).toEqual([])
  })

  it('選択肢がないリスト型はテキスト入力へ縮退する', () => {
    const definitions = ref([definition(1, 5)])
    const state = useCustomFieldConditions(definitions)
    expect(state.isListField(definitions.value[0]!)).toBe(false)
  })
})
