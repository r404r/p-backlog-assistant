import { computed, ref, type ComputedRef, type Ref } from 'vue'
import type { CustomFieldDef, CustomFieldFilter } from '../lib/backend'
import { isCustomFieldListType } from '../lib/customFieldTypes'

export function isListCustomField(definition: CustomFieldDef): boolean {
  return isCustomFieldListType(definition.typeId) && definition.items.length > 0
}

export interface CustomFieldCondition {
  text: string
  min: string
  max: string
  itemIds: number[]
}

export interface CustomFieldConditionState {
  conditions: Ref<Record<number, CustomFieldCondition>>
  filterCount: ComputedRef<number>
  isListField(definition: CustomFieldDef): boolean
  reset(): void
  buildFilters(): CustomFieldFilter[]
}

/** カスタム属性定義に対応する検索条件の初期化・変換を所有する。 */
export function useCustomFieldConditions(
  definitions: Readonly<Ref<CustomFieldDef[]>>,
): CustomFieldConditionState {
  const conditions = ref<Record<number, CustomFieldCondition>>({})

  function isListField(definition: CustomFieldDef): boolean {
    return isListCustomField(definition)
  }

  function reset() {
    conditions.value = Object.fromEntries(
      definitions.value.map((definition) => [
        definition.id,
        { text: '', min: '', max: '', itemIds: [] },
      ]),
    )
  }

  function buildFilters(): CustomFieldFilter[] {
    const filters: CustomFieldFilter[] = []
    for (const definition of definitions.value) {
      const condition = conditions.value[definition.id]
      if (!condition) continue
      const filter: CustomFieldFilter = { defId: definition.id, typeId: definition.typeId }
      const text = condition.text.trim()
      if (text) filter.text = text
      if (condition.min) filter.min = condition.min
      if (condition.max) filter.max = condition.max
      if (condition.itemIds.length > 0) filter.itemIds = [...condition.itemIds]
      if (text || condition.min || condition.max || condition.itemIds.length > 0) {
        filters.push(filter)
      }
    }
    return filters
  }

  const filterCount = computed(() => buildFilters().length)

  return { conditions, filterCount, isListField, reset, buildFilters }
}
