<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { BulkJobRow, BulkJobRowDetail } from '../lib/backend'
import { translateRowStatus, type TranslateFn } from '../lib/enumLabels'
import { formatDateTime } from '../lib/format'

defineProps<{
  jobs: BulkJobRow[]
  jobsError: string
  running: boolean
  syncing: boolean
  expandedJobId: number
  rowsLoading: boolean
  rowsError: string
  rows: BulkJobRowDetail[]
  exportingJobId: number
  translate: TranslateFn
}>()

defineEmits<{
  refresh: []
  resume: [job: BulkJobRow, resendSending: boolean]
  force: [job: BulkJobRow]
  toggle: [job: BulkJobRow]
  exportResult: [jobId: number]
}>()

const { t } = useI18n()

function canResume(job: BulkJobRow): boolean {
  return job.pending > 0 || job.sending > 0
}

function jobRowIssueLabel(row: BulkJobRowDetail): string {
  if (row.resultIssueId > 0) {
    return row.issueKey
      ? t('bulk.jobRows.newWithKey', { issueKey: row.issueKey })
      : t('bulk.jobRows.newWithId', { issueId: row.resultIssueId })
  }
  return row.issueKey || t('bulk.newIssue')
}
</script>

<template>
  <section class="panel">
    <h2>{{ t('bulk.step6.title') }}</h2>
    <div class="row buttons">
      <button :disabled="running" @click="$emit('refresh')">{{ t('bulk.step6.refresh') }}</button>
    </div>
    <p v-if="jobsError" class="error">{{ jobsError }}</p>
    <p v-if="jobs.length === 0" class="notice">{{ t('bulk.step6.empty') }}</p>

    <div v-else class="table-wrap">
      <table>
        <thead>
          <tr>
            <th>{{ t('bulk.col.job') }}</th>
            <th>{{ t('bulk.col.createdAt') }}</th>
            <th>{{ t('bulk.col.kind') }}</th>
            <th>{{ t('common.label.status') }}</th>
            <th>{{ t('bulk.col.total') }}</th>
            <th>{{ t('bulk.col.done') }}</th>
            <th>{{ t('bulk.col.failed') }}</th>
            <th>{{ t('bulk.col.conflict') }}</th>
            <th>{{ t('bulk.col.pending') }}</th>
            <th>{{ t('bulk.col.sending') }}</th>
            <th>{{ t('bulk.col.skipped') }}</th>
            <th>{{ t('bulk.col.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="job in jobs" :key="job.jobId">
            <tr>
              <td class="nowrap">#{{ job.jobId }}</td>
              <td class="nowrap">{{ formatDateTime(job.createdAt) }}</td>
              <td class="nowrap">{{ job.kind }}</td>
              <td class="nowrap">{{ job.status }}</td>
              <td class="num">{{ job.total }}</td>
              <td class="num">{{ job.done }}</td>
              <td class="num">{{ job.failed }}</td>
              <td class="num">{{ job.conflict }}</td>
              <td class="num">{{ job.pending }}</td>
              <td class="num">{{ job.sending }}</td>
              <td class="num">{{ job.skipped }}</td>
              <td class="nowrap actions">
                <button
                  v-if="canResume(job)"
                  :disabled="running || syncing"
                  @click="$emit('resume', job, false)"
                >
                  {{ t('bulk.action.resume') }}
                </button>
                <button
                  v-if="job.conflict > 0"
                  :disabled="running || syncing"
                  @click="$emit('force', job)"
                >
                  {{ t('bulk.action.forceRerun') }}
                </button>
                <button :disabled="running" @click="$emit('toggle', job)">
                  {{
                    expandedJobId === job.jobId
                      ? t('bulk.action.hideRows')
                      : t('bulk.action.showRows')
                  }}
                </button>
                <button
                  :disabled="running || exportingJobId !== 0"
                  @click="$emit('exportResult', job.jobId)"
                >
                  {{
                    exportingJobId === job.jobId
                      ? t('common.state.exporting')
                      : t('bulk.action.exportResult')
                  }}
                </button>
              </td>
            </tr>

            <tr v-if="job.sending > 0">
              <td colspan="12" class="sending-note">
                {{ t('bulk.step6.sendingNote', { count: job.sending }) }}
                <button
                  class="inline"
                  :disabled="running || syncing"
                  @click="$emit('resume', job, true)"
                >
                  {{ t('bulk.action.resumeResend') }}
                </button>
              </td>
            </tr>

            <tr v-if="expandedJobId === job.jobId">
              <td colspan="12" class="detail-cell">
                <p v-if="rowsLoading" class="hint">{{ t('bulk.step6.rowsLoading') }}</p>
                <p v-else-if="rowsError" class="error">{{ rowsError }}</p>
                <p v-else-if="rows.length === 0" class="hint">{{ t('bulk.step6.rowsEmpty') }}</p>
                <table v-else class="detail-table">
                  <thead>
                    <tr>
                      <th>{{ t('bulk.col.row') }}</th>
                      <th>{{ t('bulk.col.issueKey') }}</th>
                      <th>{{ t('common.label.status') }}</th>
                      <th>{{ t('common.label.error') }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="row in rows" :key="row.rowNo">
                      <td class="nowrap">{{ row.rowNo }}</td>
                      <td class="nowrap">{{ jobRowIssueLabel(row) }}</td>
                      <td class="nowrap">
                        <span class="badge" :class="row.status">
                          {{ translateRowStatus(translate, row.status) }}
                        </span>
                      </td>
                      <td>{{ row.error || '-' }}</td>
                    </tr>
                  </tbody>
                </table>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.panel {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: var(--surface);
}

h2 {
  font-size: 1.05rem;
  margin: 0 0 0.75rem;
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

.notice {
  background: var(--bg-muted);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.error {
  color: var(--danger-text);
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.5rem 0 0.75rem;
}

button {
  padding: 0.4rem 0.9rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-muted);
  color: var(--text);
  font-size: 0.9rem;
  cursor: pointer;
}

button:hover:not(:disabled) {
  background: var(--bg-hover);
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

button.inline {
  padding: 0.2rem 0.6rem;
  font-size: 0.8rem;
  margin-left: 0.4rem;
}

.table-wrap {
  max-height: 420px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 4px;
  margin-top: 0.75rem;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

th,
td {
  border-bottom: 1px solid var(--border);
  padding: 0.35rem 0.6rem;
  text-align: left;
  vertical-align: top;
}

th {
  background: var(--bg-muted);
  font-weight: 600;
  position: sticky;
  top: 0;
  z-index: 1;
}

td.num {
  text-align: right;
}

.nowrap {
  white-space: nowrap;
}

.badge {
  display: inline-block;
  padding: 0.05rem 0.4rem;
  border-radius: 999px;
  font-size: 0.75rem;
  border: 1px solid var(--border);
  background: var(--bg-muted);
}

.badge.done {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
}

.badge.sending {
  background: var(--status-info-bg);
  border-color: var(--accent-muted);
  color: var(--accent-fg);
}

.badge.error {
  background: var(--danger-bg);
  border-color: var(--danger-border);
  color: var(--danger-strong);
}

.badge.conflict {
  background: var(--warning-bg);
  border-color: var(--warning-border);
  color: var(--warning-text);
}

.badge.pending,
.badge.skip {
  background: var(--bg-muted);
  border-color: var(--border);
  color: var(--text-muted);
}

.sending-note {
  background: var(--warning-bg);
  color: var(--warning-text);
  font-size: 0.8rem;
}

td.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.3rem;
}

td.actions button {
  padding: 0.2rem 0.5rem;
  font-size: 0.78rem;
}

.detail-cell {
  background: var(--bg-muted);
  padding: 0.5rem 0.75rem;
}

.detail-table {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.8rem;
}

.detail-table th {
  position: static;
}

.detail-cell .hint,
.detail-cell .error {
  margin: 0;
}
</style>
