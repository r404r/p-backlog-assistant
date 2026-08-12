<script lang="ts" setup>
// ユーザ抽出画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, reactive, ref } from 'vue'
import {
  getBackend,
  isMockBackend,
  type ExportColumn,
  type PermissionStatus,
  type SyncResult,
  type UserQuery,
  type UserRow,
} from '../lib/backend'
import { errorMessage, formatDateTime, formatElapsed } from '../lib/format'

const backend = getBackend()
const mock = isMockBackend()

/** 画面に表示する最大件数(Excel 出力は条件に一致する全件が対象) */
const PREVIEW_LIMIT = 200

/**
 * ロール種別(Backlog の roleType)の選択肢。
 * 値・表示名は Go 側 store.RoleName(backlogclient.RoleType)の既知値に合わせる。
 */
const ROLE_OPTIONS: { value: number; label: string }[] = [
  { value: 1, label: '管理者' },
  { value: 2, label: '一般ユーザ' },
  { value: 3, label: 'レポーター' },
  { value: 4, label: '閲覧者' },
  { value: 5, label: 'ゲストレポーター' },
  { value: 6, label: 'ゲスト閲覧者' },
]

/** 複数値を 1 セル相当の表示にまとめる(Excel 出力と同じ区切り) */
function joinValues(values: string[]): string {
  return values.join(', ')
}

/**
 * ロールの表示名。Go 側は未知の roleType を「不明(N)」で返すが、
 * 旧バージョンのバインディング等で空の場合も数値を落とさない。
 */
function roleLabel(u: UserRow): string {
  return u.roleName || `不明(${u.roleType})`
}

// ---------------------------------------------------------------------------
// アクティブプロファイル・権限状態・鮮度
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const globalError = ref('')

const permission = ref<PermissionStatus | null>(null)
const permissionError = ref('')

/** ユーザデータの最終同期時刻(sync_state の dataKind='users' 行。未同期なら空文字) */
const lastSyncedAt = ref('')
/** 鮮度を取得できなかった場合は「未同期」と断定しない(誤解を招く表示を避ける) */
const syncStateUnknown = ref(false)

async function loadPermission() {
  if (!profileId.value) return
  permissionError.value = ''
  try {
    permission.value = await backend.getPermissionStatus(profileId.value)
  } catch (e) {
    permission.value = null
    permissionError.value = `権限状態の取得に失敗しました: ${errorMessage(e)}`
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
    globalError.value = `接続先プロファイルの取得に失敗しました: ${errorMessage(e)}`
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
  return p.degraded
    ? '一部の管理者機能が利用できません(内容を取得できませんでした)。'
    : '管理者機能を利用できます。'
})

/** 一度も同期されていないか(鮮度が不明な場合は断定しない) */
const neverSynced = computed(() => !syncStateUnknown.value && !lastSyncedAt.value)

// ---------------------------------------------------------------------------
// 同期
// ---------------------------------------------------------------------------

const syncing = ref(false)
const syncResult = ref<SyncResult | null>(null)
const syncError = ref('')

async function runSync() {
  if (!profileId.value || syncing.value) return
  syncing.value = true
  syncError.value = ''
  syncResult.value = null
  try {
    syncResult.value = await backend.syncUsers(profileId.value)
    await loadPermission() // 縮退状態が変わり得るため取り直す
    await loadSyncState() // 鮮度表示を更新
    await search()
  } catch (e) {
    syncError.value = `ユーザの同期に失敗しました: ${errorMessage(e)}`
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
const searchError = ref('')

/** 表示件数が上限で切り詰められているか */
const truncated = computed(() => total.value > rows.value.length)

async function search() {
  if (!profileId.value || searching.value) return
  searching.value = true
  searchError.value = ''
  try {
    const res = await backend.listUsers(profileId.value, buildQuery(true))
    rows.value = res.rows
    total.value = res.total
    searched.value = true
  } catch (e) {
    searchError.value = `ユーザの抽出に失敗しました: ${errorMessage(e)}`
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
const exportColumnsError = ref('')

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
    exportColumnsError.value = ''
    if (!exportColumnsInitialized) {
      selectedColumns.value = cols.filter((c) => c.byDefault).map((c) => c.key)
      exportColumnsInitialized = true
    }
  } catch (e) {
    exportColumnsError.value = `出力する列の情報を取得できませんでした: ${errorMessage(e)}`
  }
}

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportCanceled = ref(false)
const exportError = ref('')

const canExport = computed(
  () => !!profileId.value && selectedColumns.value.length > 0 && !exporting.value,
)

async function exportExcel() {
  if (!canExport.value) return
  exporting.value = true
  exportError.value = ''
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
    exportError.value = `Excel 出力に失敗しました: ${errorMessage(e)}`
  } finally {
    exporting.value = false
  }
}
</script>

<template>
  <div class="users">
    <h1>ユーザ抽出</h1>

    <p v-if="mock" class="mock-note">
      Wails ランタイム外で動作中のため、モックデータを表示しています(実データではありません)。
    </p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">読み込み中...</p>

    <p v-else-if="!profileId" class="notice">
      接続先プロファイルが選択されていません。「接続設定」画面でプロファイルを登録・選択してください。
    </p>

    <template v-else>
      <!-- 権限状態 -->
      <section class="panel">
        <h2>権限状態</h2>
        <p v-if="permissionError" class="error">{{ permissionError }}</p>
        <template v-else-if="permission">
          <!-- 何が利用不可かはバックエンドの message が持つ(画面では決め打ちしない。中 2) -->
          <p class="notice" :class="{ warn: permission.degraded }">{{ permissionMessage }}</p>
          <p v-if="permission.degraded" class="hint">
            プロジェクト単位の取得に縮退している項目は、参加しているプロジェクトの範囲でしか取得できません。
          </p>
        </template>
        <p v-else class="hint">権限状態を確認中です...</p>
      </section>

      <!-- 同期 -->
      <section class="panel">
        <h2>同期</h2>
        <p class="freshness">
          データ鮮度:
          <template v-if="syncStateUnknown">鮮度を取得できませんでした(ログを確認してください)</template>
          <template v-else-if="lastSyncedAt">
            最終同期 {{ formatDateTime(lastSyncedAt) }}({{ formatElapsed(lastSyncedAt) }})
          </template>
          <template v-else>未同期</template>
        </p>
        <p v-if="neverSynced" class="notice warn">
          ユーザはまだ同期されていません。「ユーザを同期」を実行してください。
        </p>

        <div class="row buttons">
          <button class="primary" :disabled="syncing" @click="runSync">
            {{ syncing ? '同期中...' : 'ユーザを同期' }}
          </button>
          <span v-if="syncing" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">
          ユーザ一覧・所属チーム・プロジェクト参加/管理者情報を Backlog から取得してローカル DB を更新します。
          抽出はローカル DB に対して行うため、最新の状態を見るには先に同期してください。
        </p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <div v-if="syncResult" class="result ok">
          <p class="result-title">ユーザの同期が完了しました</p>
          <ul>
            <li>取得: {{ syncResult.fetched }} 件</li>
            <li>登録・更新: {{ syncResult.upserted }} 件</li>
            <li>削除: {{ syncResult.deleted }} 件</li>
            <li>所要時間: {{ (syncResult.durationMs / 1000).toFixed(1) }} 秒</li>
          </ul>
          <div v-if="syncResult.warnings.length > 0" class="warnings">
            <p class="result-title">警告</p>
            <ul>
              <li v-for="(w, i) in syncResult.warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </div>
      </section>

      <!-- 抽出条件 -->
      <section class="panel">
        <h2>抽出条件</h2>
        <div class="row">
          <label for="u-keyword">キーワード</label>
          <input
            id="u-keyword"
            v-model="cond.keyword"
            type="text"
            class="wide"
            placeholder="名前・ユーザID・メールアドレスの部分一致"
          />
        </div>

        <div class="row">
          <label for="u-role">ロール</label>
          <select id="u-role" v-model.number="cond.roleType">
            <option :value="0">すべて</option>
            <option v-for="r in ROLE_OPTIONS" :key="r.value" :value="r.value">{{ r.label }}</option>
          </select>
        </div>
        <p class="hint">
          ロールはスペース全体の権限(roleType)です。プロジェクトごとの権限は
          「参加プロジェクト」「管理者プロジェクト」列で確認してください。
        </p>

        <div class="row buttons">
          <button class="primary" :disabled="searching" @click="search">
            {{ searching ? '抽出中...' : '抽出' }}
          </button>
          <button :disabled="searching" @click="clearConditions">条件をクリア</button>
          <span v-if="searching" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="searchError" class="error">{{ searchError }}</p>
      </section>

      <!-- 抽出結果 -->
      <section v-if="searched" class="panel">
        <h2>抽出結果</h2>
        <p class="summary">
          該当 {{ total }} 件
          <span v-if="truncated">(画面には先頭 {{ rows.length }} 件のみ表示)</span>
        </p>
        <p v-if="truncated" class="hint">
          画面表示は {{ PREVIEW_LIMIT }} 件までです。Excel には条件に一致する全 {{ total }} 件が出力されます。
        </p>

        <p v-if="rows.length === 0" class="notice">条件に一致するユーザはありませんでした。</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名前</th>
                <th>ユーザID</th>
                <th>メールアドレス</th>
                <th>ロール</th>
                <th>所属チーム</th>
                <th>参加プロジェクト</th>
                <th>管理者プロジェクト</th>
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
        <h2>Excel 出力</h2>
        <p class="hint">出力する列を選択してください(現在の抽出条件に一致する全件が出力されます)。</p>
        <div class="columns">
          <label v-for="c in exportColumns" :key="c.key" class="checkbox">
            <input v-model="selectedColumns" type="checkbox" :value="c.key" />
            {{ c.label }}
          </label>
        </div>
        <p v-if="exportColumnsError" class="hint warn">
          {{ exportColumnsError }}
          <button type="button" class="link" @click="loadExportColumns">再試行</button>
        </p>
        <div class="row buttons">
          <button class="primary" :disabled="!canExport" @click="exportExcel">
            {{ exporting ? '出力中...' : 'Excel 出力' }}
          </button>
          <span v-if="exporting" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="selectedColumns.length === 0" class="hint warn">出力する列を 1 つ以上選択してください。</p>
        <p v-if="exportError" class="error">{{ exportError }}</p>
        <p v-if="exportCanceled" class="notice">Excel 出力はキャンセルされました。</p>
        <div v-if="exportPath" class="result ok">
          <p class="result-title">Excel 出力が完了しました({{ exportRows }} 件)</p>
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

h1 {
  font-size: 1.4rem;
  margin: 0 0 1rem;
}

h2 {
  font-size: 1.05rem;
  margin: 0 0 0.75rem;
}

.panel {
  border: 1px solid #d0d7de;
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: #fff;
}

.mock-note {
  background: #fff8e1;
  border: 1px solid #e6c96a;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}

.notice {
  background: #f6f8fa;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
  color: #57606a;
}

.notice.warn {
  background: #fff8e1;
  border-color: #e6c96a;
  color: #9a6700;
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
  min-width: 6rem;
}

.row.buttons {
  margin-top: 0.75rem;
  margin-bottom: 0;
}

input[type='text'],
select {
  padding: 0.4rem 0.5rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  font-size: 0.9rem;
  background: #fff;
  color: #1f2328;
}

input.wide {
  width: 320px;
}

input:disabled,
select:disabled {
  background: #f6f8fa;
  color: #8c959f;
}

.hint {
  font-size: 0.8rem;
  color: #57606a;
  margin: 0.5rem 0 0.75rem;
}

.hint.warn {
  color: #9a6700;
}

/* 文中に置く軽量なアクション(列情報の取得の再試行) */
button.link {
  border: none;
  background: none;
  padding: 0;
  font-size: inherit;
  color: #0b5cad;
  cursor: pointer;
  text-decoration: underline;
}

.freshness {
  font-size: 0.85rem;
  color: #57606a;
  margin: 0 0 0.5rem;
}

button {
  padding: 0.4rem 0.9rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  background: #f6f8fa;
  color: #1f2328;
  font-size: 0.9rem;
  cursor: pointer;
}

button:hover:not(:disabled) {
  background: #eaeef2;
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

button.primary {
  background: #0b5cad;
  border-color: #0b5cad;
  color: #fff;
}

button.primary:hover:not(:disabled) {
  background: #094c8f;
}

.spinner {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid #d0d7de;
  border-top-color: #0b5cad;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.error {
  color: #b52a2a;
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.result {
  margin-top: 0.75rem;
  border-radius: 4px;
  padding: 0.6rem 0.9rem;
  font-size: 0.9rem;
}

.result.ok {
  background: #e9f5ec;
  border: 1px solid #7fbf90;
}

.result-title {
  font-weight: 600;
  margin: 0 0 0.3rem;
}

.result ul {
  margin: 0;
  padding-left: 1.2rem;
}

.warnings {
  margin-top: 0.5rem;
  color: #9a6700;
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
  border: 1px solid #d0d7de;
  border-radius: 4px;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

th,
td {
  border-bottom: 1px solid #d0d7de;
  padding: 0.35rem 0.6rem;
  text-align: left;
  vertical-align: top;
}

th {
  background: #f6f8fa;
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
