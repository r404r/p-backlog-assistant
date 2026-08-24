<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { CustomFieldDef } from '../lib/backend'
import {
  CUSTOM_FIELD_DATE,
  CUSTOM_FIELD_NUMERIC,
  isListCustomField,
  type CustomFieldCondition,
} from '../composables/useCustomFieldConditions'

const props = defineProps<{
  definitions: CustomFieldDef[]
  conditions: Record<number, CustomFieldCondition>
  filterCount: number
  open: boolean
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  'update:conditions': [conditions: Record<number, CustomFieldCondition>]
}>()

const { t } = useI18n()

function updateOpen(event: Event) {
  emit('update:open', (event.target as HTMLDetailsElement).open)
}

function updateText(
  definitionId: number,
  field: 'text' | 'min' | 'max',
  event: Event,
) {
  const current = props.conditions[definitionId]
  if (!current) return
  emit('update:conditions', {
    ...props.conditions,
    [definitionId]: { ...current, [field]: (event.target as HTMLInputElement).value },
  })
}

function toggleItem(definitionId: number, itemId: number, event: Event) {
  const current = props.conditions[definitionId]
  if (!current) return
  const selected = new Set(current.itemIds)
  if ((event.target as HTMLInputElement).checked) selected.add(itemId)
  else selected.delete(itemId)
  emit('update:conditions', {
    ...props.conditions,
    [definitionId]: { ...current, itemIds: [...selected] },
  })
}
</script>

<template>
  <details class="cf-filters" :open="open" @toggle="updateOpen">
    <summary>
      {{ t('issues.cf.summary') }}
      <span v-if="filterCount > 0" class="cf-count">
        {{ t('issues.cf.count', { count: filterCount }) }}
      </span>
    </summary>

    <div v-for="definition in definitions" :key="definition.id" class="row cf-row">
      <label :for="`i-cf-${definition.id}`">{{ definition.name }}</label>
      <template v-if="conditions[definition.id]">
        <template v-if="isListCustomField(definition)">
          <label v-for="item in definition.items" :key="item.id" class="checkbox">
            <input
              type="checkbox"
              :value="item.id"
              :checked="conditions[definition.id]!.itemIds.includes(item.id)"
              @change="toggleItem(definition.id, item.id, $event)"
            />
            {{ item.name }}
          </label>
        </template>
        <template v-else-if="definition.typeId === CUSTOM_FIELD_NUMERIC">
          <input
            :id="`i-cf-${definition.id}`"
            :value="conditions[definition.id]!.min"
            type="number"
            step="any"
            class="narrow"
            :placeholder="t('issues.cf.min')"
            @input="updateText(definition.id, 'min', $event)"
          />
          <span>{{ t('issues.rangeSeparator') }}</span>
          <input
            :value="conditions[definition.id]!.max"
            type="number"
            step="any"
            class="narrow"
            :placeholder="t('issues.cf.max')"
            @input="updateText(definition.id, 'max', $event)"
          />
        </template>
        <template v-else-if="definition.typeId === CUSTOM_FIELD_DATE">
          <input
            :id="`i-cf-${definition.id}`"
            :value="conditions[definition.id]!.min"
            type="date"
            @input="updateText(definition.id, 'min', $event)"
          />
          <span>{{ t('issues.rangeSeparator') }}</span>
          <input
            :value="conditions[definition.id]!.max"
            type="date"
            @input="updateText(definition.id, 'max', $event)"
          />
        </template>
        <template v-else>
          <input
            :id="`i-cf-${definition.id}`"
            :value="conditions[definition.id]!.text"
            type="text"
            class="wide"
            :placeholder="t('issues.cf.contains')"
            @input="updateText(definition.id, 'text', $event)"
          />
        </template>
      </template>
    </div>

    <p class="hint">{{ t('issues.cf.hint') }}</p>
  </details>
</template>

<style scoped>
.cf-filters {
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 0.75rem;
  background: var(--bg-muted);
}

.cf-filters > summary {
  cursor: pointer;
  font-size: 0.9rem;
  font-weight: 600;
}

.cf-filters[open] > summary {
  margin-bottom: 0.75rem;
}

.cf-count {
  font-weight: 400;
  color: var(--accent-fg);
}

.row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
  flex-wrap: wrap;
}

.row > label {
  font-weight: 600;
  font-size: 0.9rem;
}

.cf-row > label {
  min-width: 10rem;
}

.checkbox {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  font-size: 0.85rem;
}

input[type='text'],
input[type='date'] {
  padding: 0.4rem 0.5rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.9rem;
  background: var(--bg);
  color: var(--text);
}

input.wide {
  width: 320px;
}

input.narrow {
  width: 120px;
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0 0 0.75rem;
}
</style>
