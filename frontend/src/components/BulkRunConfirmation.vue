<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{
  count: number
  estimate: string
  force: boolean
  resendSending: boolean
}>()

defineEmits<{
  confirm: []
  cancel: []
}>()

const { t } = useI18n()
</script>

<template>
  <div class="confirm">
    <p class="result-title">{{ t('bulk.confirm.title', { count }) }}</p>
    <p v-if="force" class="warn-text">{{ t('bulk.confirm.force') }}</p>
    <p v-if="resendSending" class="warn-text">{{ t('bulk.confirm.resend') }}</p>
    <p class="hint">{{ t('bulk.confirm.estimate', { estimate }) }}</p>
    <div class="row buttons">
      <button class="primary" @click="$emit('confirm')">{{ t('bulk.confirm.ok') }}</button>
      <button @click="$emit('cancel')">{{ t('bulk.confirm.cancel') }}</button>
    </div>
  </div>
</template>

<style scoped>
.confirm {
  margin-top: 0.75rem;
  border: 1px solid var(--warning-border);
  background: var(--warning-bg);
  border-radius: 4px;
  padding: 0.75rem 0.9rem;
  font-size: 0.9rem;
}

.warn-text {
  font-size: 0.85rem;
  color: var(--warning-text);
  margin: 0 0 0.4rem;
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.5rem 0 0.75rem;
}

.row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.row.buttons {
  margin-top: 0.75rem;
  margin-bottom: 0;
}

</style>
