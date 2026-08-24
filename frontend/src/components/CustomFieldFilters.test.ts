import { describe, expect, it } from 'vitest'
import { h, ref } from 'vue'
import type { CustomFieldDef } from '../lib/backend'
import type { CustomFieldCondition } from '../composables/useCustomFieldConditions'
import { mountWithI18n } from '../lib/testing/mountWithI18n'
import CustomFieldFilters from './CustomFieldFilters.vue'

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

describe('CustomFieldFilters', () => {
  it('定義型に応じた入力を描画し、変更を親へ通知する', async () => {
    const definitions = [
      definition(1, 1),
      definition(2, 3),
      definition(3, 4),
      definition(4, 5, [{ id: 40, name: 'option' }]),
    ]
    const conditions = ref<Record<number, CustomFieldCondition>>(
      Object.fromEntries(
        definitions.map((definition) => [
          definition.id,
          { text: '', min: '', max: '', itemIds: [] } satisfies CustomFieldCondition,
        ]),
      ),
    )
    const open = ref(true)
    const mounted = mountWithI18n({
      render: () =>
        h(CustomFieldFilters, {
          definitions,
          conditions: conditions.value,
          filterCount: 2,
          open: open.value,
          'onUpdate:open': (value: boolean) => (open.value = value),
          'onUpdate:conditions': (value: Record<number, CustomFieldCondition>) =>
            (conditions.value = value),
        }),
    })

    expect(mounted.host.textContent).toContain('2 件指定中')
    expect(mounted.host.querySelectorAll('input[type="text"]')).toHaveLength(1)
    expect(mounted.host.querySelectorAll('input[type="number"]')).toHaveLength(2)
    expect(mounted.host.querySelectorAll('input[type="date"]')).toHaveLength(2)
    expect(mounted.host.querySelectorAll('input[type="checkbox"]')).toHaveLength(1)
    expect(mounted.host.textContent).toContain('option')

    const text = mounted.host.querySelector('input[type="text"]') as HTMLInputElement
    text.value = 'keyword'
    text.dispatchEvent(new Event('input', { bubbles: true }))
    expect(conditions.value[1]?.text).toBe('keyword')

    const checkbox = mounted.host.querySelector('input[type="checkbox"]') as HTMLInputElement
    checkbox.checked = true
    checkbox.dispatchEvent(new Event('change', { bubbles: true }))
    expect(conditions.value[4]?.itemIds).toEqual([40])
    mounted.unmount()
  })
})
