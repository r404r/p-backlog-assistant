<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { SyncResult } from '../lib/backend'

defineProps<{
  title: string
  result: SyncResult
}>()

const { t } = useI18n()
</script>

<template>
  <div class="result ok">
    <p class="result-title">{{ title }}</p>
    <ul>
      <li>{{ t('sync.result.fetched', { count: result.fetched }) }}</li>
      <li>{{ t('sync.result.upserted', { count: result.upserted }) }}</li>
      <li>{{ t('sync.result.deleted', { count: result.deleted }) }}</li>
      <li>
        {{ t('sync.result.duration', { seconds: (result.durationMs / 1000).toFixed(1) }) }}
      </li>
    </ul>
    <div v-if="result.warnings.length > 0" class="warnings">
      <p class="result-title">{{ t('common.label.warning') }}</p>
      <ul>
        <li v-for="(warning, index) in result.warnings" :key="index">{{ warning }}</li>
      </ul>
    </div>
  </div>
</template>
