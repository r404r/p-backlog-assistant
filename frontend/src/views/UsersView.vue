<script lang="ts" setup>
// ユーザ抽出画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SyncResultPanel from '../components/SyncResultPanel.vue'
import {
  getBackend,
  isMockBackend,
  type ExportColumn,
  type PermissionStatus,
  type SyncResult,
  type UserQuery,
  type UserRow,
} from '../lib/backend'
import { columnLabel } from '../lib/columnLabels'
import { translateRoleType } from '../lib/enumLabels'
import { errorMessage, formatDateTime, formatElapsed } from '../lib/format'
import { useMessage } from '../lib/message'

const backend = getBackend()
const mock = isMockBackend()

const { t } = useI18n()

/** 画面に表示する最大件数(Excel 出力は条件に一致する全件が対象) */
const PREVIEW_LIMIT = 200

/**
 * ロール種別(Backlog の roleType)の選択肢。
 * 値は Go 側 store.RoleName(backlogclient.RoleType)の既知値に合わせる。
 * 表示名は機械値からフロントで翻訳する(設計 §3.1。lib/enumLabels.ts)。
 */
const ROLE_VALUES = [1, 2, 3, 4, 5, 6]

/** 複数値を 1 セル相当の表示にまとめる(Excel 出力と同じ区切り) */
function joinValues(values: string[]): string {
  return values.join(', ')
}

/**
 * ロールの表示名。
 * Go も解決済みの `roleName`(日本語)を返すが、**表示には使わない**
 * (英語 UI で日本語が混ざるため。設計 §3.1)。生の roleType から翻訳し、
 * 未知の値は「不明(N)」形式へ縮退する(translateRoleType の責務)。
 */
function roleLabel(u: UserRow): string {
  return translateRoleType(t, u.roleType)
}

// ---------------------------------------------------------------------------
// アクティブプロファイル・権限状態・鮮度
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const [globalError, setGlobalError] = useMessage(t)

const permission = ref<PermissionStatus | null>(null)
const [permissionError, setPermissionError] = useMessage(t)

/** ユーザデータの最終同期時刻(sync_state の dataKind='users' 行。未同期なら空文字) */
const lastSyncedAt = ref('')
/** 鮮度を取得できなかった場合は「未同期」と断定しない(誤解を招く表示を避ける) */
const syncStateUnknown = ref(false)

async function loadPermission() {
  if (!profileId.value) return
  setPermissionError(null)
  try {
    permission.value = await backend.getPermissionStatus(profileId.value)
  } catch (e) {
    permission.value = null
    setPermissionError('users.error.permission', { message: errorMessage(e) })
  }
}

async function loadSyncState() {
  if (!profileId.value) return
  try {
    const states = await backend.getSyncState(profileId.value)
    const row = states.find((s) => s.dataKind === 'users')
    lastSyncedAt.value = row?.lastSyncedAt ?? ''
    syncStateUnknown.value = false
  } catch {
    lastSyncedAt.value = ''
    syncStateUnknown.value = true
  }
}

onMounted(async () => {
  // 列の一覧はプロファイルに依存しないため、先に取りに行く
  void loadExportColumns()
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    setGlobalError('users.error.activeProfile', { message: errorMessage(e) })
  } finally {
    initializing.value = false
  }
  if (profileId.value) {
    await loadPermission()
    await loadSyncState()
    await search()
  }
})

/**
 * 権限状態の説明文(中 2)。
 * degraded は「ユーザ一覧・チーム一覧のどちらか一方でも利用不可」で真になるため、
 * 画面側で「ユーザ一覧の権限がない」と決め打ちせず、
 * バックエンドが個別の内容を入れて返す message をそのまま表示する。
 * message が空の場合(旧バージョンのバインディング等)のみ最小限の代替文を出す。
 */
const permissionMessage = computed(() => {
  const p = permission.value
  if (!p) return ''
  if (p.message) return p.message
  return p.degraded ? t('users.perm.degradedFallback') : t('users.perm.okFallback')
})

/** 一度も同期されていないか(鮮度が不明な場合は断定しない) */
const neverSynced = computed(() => !syncStateUnknown.value && !lastSyncedAt.value)

// ---------------------------------------------------------------------------
// 同期
// ---------------------------------------------------------------------------

const syncing = ref(false)
const syncResult = ref<SyncResult | null>(null)
const [syncError, setSyncError] = useMessage(t)

async function runSync() {
  if (!profileId.value || syncing.value) return
  syncing.value = true
  setSyncError(null)
  syncResult.value = null
  try {
    syncResult.value = await backend.syncUsers(profileId.value)
    await loadPermission() // 縮退状態が変わり得るため取り直す
    await loadSyncState() // 鮮度表示を更新
    await search()
  } catch (e) {
    setSyncError('users.error.sync', { message: errorMessage(e) })
  } finally {
    syncing.value = false
  }
}

// ---------------------------------------------------------------------------
// 抽出条件
// ---------------------------------------------------------------------------

const cond = reactive({
  keyword: '',
  roleType: 0, // 0 = すべて
})

/** 現在の条件を UserQuery に変換する(空の条件は送らない) */
function buildQuery(withLimit: boolean): UserQuery {
  const q: UserQuery = {}
  if (cond.keyword.trim()) q.keyword = cond.keyword.trim()
  if (cond.roleType) q.roleType = cond.roleType
  if (withLimit) q.limit = PREVIEW_LIMIT
  return q
}

function clearConditions() {
  cond.keyword = ''
  cond.roleType = 0
}

// ---------------------------------------------------------------------------
// 検索(ローカル DB)
// ---------------------------------------------------------------------------

const rows = ref<UserRow[]>([])
const total = ref(0)
const searching = ref(false)
const searched = ref(false)
const [searchError, setSearchError] = useMessage(t)

/** 表示件数が上限で切り詰められているか */
const truncated = computed(() => total.value > rows.value.length)

async function search() {
  if (!profileId.value || searching.value) return
  searching.value = true
  setSearchError(null)
  try {
    const res = await backend.listUsers(profileId.value, buildQuery(true))
    rows.value = res.rows
    total.value = res.total
    searched.value = true
  } catch (e) {
    setSearchError('users.error.search', { message: errorMessage(e) })
  } finally {
    searching.value = false
  }
}

// ---------------------------------------------------------------------------
// Excel 出力
// ---------------------------------------------------------------------------

/**
 * Excel 出力の列(列キー・ラベル・既定選択は Go 側 export の列定義から取得する。R14)。
 * 画面が独自の一覧を持つと Excel のヘッダとラベルがずれるため、定義は Go 側だけに置く。
 */
const exportColumns = ref<ExportColumn[]>([])

/** 列の取得に失敗した場合の説明(空 = 正常) */
const [exportColumnsError, setExportColumnsError] = useMessage(t)

// 初期値は空。列の取得(loadExportColumns)で既定列が入る
const selectedColumns = ref<string[]>([])

/**
 * 列選択を既定値で初期化済みか。
 * 再試行のたびに既定値へ戻して、利用者が変更した選択を捨てないようにする。
 */
let exportColumnsInitialized = false

/** 出力できる列を取得し、初回だけ既定の列選択を入れる(プロファイルに依存しない) */
async function loadExportColumns() {
  try {
    const cols = await backend.getUserExportColumns()
    exportColumns.value = cols
    setExportColumnsError(null)
    if (!exportColumnsInitialized) {
      selectedColumns.value = cols.filter((c) => c.byDefault).map((c) => c.key)
      exportColumnsInitialized = true
    }
  } catch (e) {
    setExportColumnsError('users.error.exportColumns', { message: errorMessage(e) })
  }
}

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportCanceled = ref(false)
const [exportError, setExportError] = useMessage(t)

const canExport = computed(
  () => !!profileId.value && selectedColumns.value.length > 0 && !exporting.value,
)

async function exportExcel() {
  if (!canExport.value) return
  exporting.value = true
  setExportError(null)
  exportPath.value = ''
  exportCanceled.value = false
  try {
    // 表示上限は付けない(条件に一致する全件を出力する)
    const columns = exportColumns.value
      .filter((c) => selectedColumns.value.includes(c.key))
      .map((c) => c.key)
    const res = await backend.exportUsersExcel(profileId.value, buildQuery(false), columns)
    if (!res.path) {
      exportCanceled.value = true
    } else {
      exportPath.value = res.path
      exportRows.value = res.rows
    }
  } catch (e) {
    setExportError('users.error.export', { message: errorMessage(e) })
  } finally {
    exporting.value = false
  }
}
</script>

<template>
  <div class="users">
    <h1>{{ t('users.title') }}</h1>

    <p v-if="mock" class="mock-note">{{ t('users.mockNote') }}</p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">{{ t('common.state.loading') }}</p>

    <p v-else-if="!profileId" class="notice">{{ t('users.noProfile') }}</p>

    <template v-else>
      <!-- 権限状態 -->
      <section class="panel">
        <h2>{{ t('users.perm.title') }}</h2>
        <p v-if="permissionError" class="error">{{ permissionError }}</p>
        <template v-else-if="permission">
          <!-- 何が利用不可かはバックエンドの message が持つ(画面では決め打ちしない。中 2) -->
          <p class="notice" :class="{ warn: permission.degraded }">{{ permissionMessage }}</p>
          <p v-if="permission.degraded" class="hint">{{ t('users.perm.degradedHint') }}</p>
        </template>
        <p v-else class="hint">{{ t('users.perm.checking') }}</p>
      </section>

      <!-- 同期 -->
      <section class="panel">
        <h2>{{ t('users.sync.title') }}</h2>
        <p class="freshness">
          {{ t('users.sync.freshnessLabel') }}
          <template v-if="syncStateUnknown">{{ t('users.sync.freshnessUnknown') }}</template>
          <template v-else-if="lastSyncedAt">
            {{
              t('users.sync.lastSynced', {
                datetime: formatDateTime(lastSyncedAt),
                elapsed: formatElapsed(lastSyncedAt, t),
              })
            }}
          </template>
          <template v-else>{{ t('common.state.notSynced') }}</template>
        </p>
        <p v-if="neverSynced" class="notice warn">{{ t('users.sync.neverSynced') }}</p>

        <div class="row buttons">
          <button class="primary" :disabled="syncing" @click="runSync">
            {{ syncing ? t('common.state.syncing') : t('users.sync.button') }}
          </button>
          <span v-if="syncing" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">{{ t('users.sync.hint') }}</p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <SyncResultPanel
          v-if="syncResult"
          :result="syncResult"
          :title="t('users.sync.resultTitle')"
        />
      </section>

      <!-- 抽出条件 -->
      <section class="panel">
        <h2>{{ t('users.cond.title') }}</h2>
        <div class="row">
          <label for="u-keyword">{{ t('users.cond.keyword') }}</label>
          <input
            id="u-keyword"
            v-model="cond.keyword"
            type="text"
            class="wide"
            :placeholder="t('users.cond.keywordPlaceholder')"
          />
        </div>

        <div class="row">
          <label for="u-role">{{ t('users.cond.role') }}</label>
          <select id="u-role" v-model.number="cond.roleType">
            <option :value="0">{{ t('common.state.all') }}</option>
            <option v-for="r in ROLE_VALUES" :key="r" :value="r">
              {{ translateRoleType(t, r) }}
            </option>
          </select>
        </div>
        <p class="hint">{{ t('users.cond.roleHint') }}</p>

        <div class="row buttons">
          <button class="primary" :disabled="searching" @click="search">
            {{ searching ? t('users.cond.searching') : t('users.cond.search') }}
          </button>
          <button :disabled="searching" @click="clearConditions">
            {{ t('common.action.clearConditions') }}
          </button>
          <span v-if="searching" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="searchError" class="error">{{ searchError }}</p>
      </section>

      <!-- 抽出結果 -->
      <section v-if="searched" class="panel">
        <h2>{{ t('users.result.title') }}</h2>
        <p class="summary">
          {{ t('users.result.total', { count: total }) }}
          <span v-if="truncated">{{ t('users.result.truncated', { count: rows.length }) }}</span>
        </p>
        <p v-if="truncated" class="hint">
          {{ t('users.result.truncatedHint', { limit: PREVIEW_LIMIT, total }) }}
        </p>

        <p v-if="rows.length === 0" class="notice">{{ t('users.result.empty') }}</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('common.column.user.name') }}</th>
                <th>{{ t('common.column.user.userCode') }}</th>
                <th>{{ t('common.column.user.mailAddress') }}</th>
                <th>{{ t('common.column.user.roleName') }}</th>
                <th>{{ t('common.column.user.teamNames') }}</th>
                <th>{{ t('common.column.user.projectKeys') }}</th>
                <th>{{ t('common.column.user.adminProjectKeys') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in rows" :key="u.id">
                <td>{{ u.name }}</td>
                <td class="nowrap">{{ u.userCode }}</td>
                <td class="nowrap">{{ u.mailAddress }}</td>
                <td class="nowrap">{{ roleLabel(u) }}</td>
                <td>{{ joinValues(u.teamNames) || '-' }}</td>
                <td>{{ joinValues(u.projectKeys) || '-' }}</td>
                <td>{{ joinValues(u.adminProjectKeys) || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- Excel 出力 -->
      <section class="panel">
        <h2>{{ t('users.export.title') }}</h2>
        <p class="hint">{{ t('users.export.hint') }}</p>
        <div class="columns">
          <label v-for="c in exportColumns" :key="c.key" class="checkbox">
            <input v-model="selectedColumns" type="checkbox" :value="c.key" />
            {{ columnLabel(t, 'user', c.key, c.label) }}
          </label>
        </div>
        <p v-if="exportColumnsError" class="hint warn">
          {{ exportColumnsError }}
          <button type="button" class="link" @click="loadExportColumns">
            {{ t('common.action.retry') }}
          </button>
        </p>
        <div class="row buttons">
          <button class="primary" :disabled="!canExport" @click="exportExcel">
            {{ exporting ? t('common.state.exporting') : t('users.export.button') }}
          </button>
          <span v-if="exporting" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="selectedColumns.length === 0" class="hint warn">
          {{ t('users.export.noColumns') }}
        </p>
        <p v-if="exportError" class="error">{{ exportError }}</p>
        <p v-if="exportCanceled" class="notice">{{ t('users.export.canceled') }}</p>
        <div v-if="exportPath" class="result ok">
          <p class="result-title">{{ t('users.export.doneTitle', { count: exportRows }) }}</p>
          <p class="path">{{ exportPath }}</p>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.users {
  max-width: none;
  width: 100%;
  box-sizing: border-box;
}

.mock-note {
  background: var(--warning-bg);
  border: 1px solid var(--warning-border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}

.notice {
  background: var(--bg-muted);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.notice.warn {
  background: var(--warning-bg);
  border-color: var(--warning-border);
  color: var(--warning-text);
}

.row > label {
  font-weight: 600;
  font-size: 0.9rem;
  min-width: 6rem;
}

input[type='text'],
select {
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

input:disabled,
select:disabled {
  background: var(--bg-muted);
  color: var(--text-faint);
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.5rem 0 0.75rem;
}

.hint.warn {
  color: var(--warning-text);
}

/* 文中に置く軽量なアクション(列情報の取得の再試行) */
button.link {
  border: none;
  background: none;
  padding: 0;
  font-size: inherit;
  color: var(--accent-fg);
  cursor: pointer;
  text-decoration: underline;
}

.freshness {
  font-size: 0.85rem;
  color: var(--text-muted);
  margin: 0 0 0.5rem;
}

.spinner {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent-fg);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.error {
  color: var(--danger-text);
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.path {
  margin: 0;
  font-family: monospace;
  word-break: break-all;
}

.summary {
  font-size: 0.9rem;
  font-weight: 600;
  margin: 0 0 0.5rem;
}

.table-wrap {
  max-height: 420px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 4px;
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

.nowrap {
  white-space: nowrap;
}

.columns {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem 1rem;
  margin-bottom: 0.5rem;
}

.checkbox {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  font-size: 0.85rem;
}
</style>
