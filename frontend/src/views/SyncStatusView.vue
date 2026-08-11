<script lang="ts" setup>
// 同期状態画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  getBackend,
  isMockBackend,
  type LogInfo,
  type Project,
  type RateLimitStatus,
  type SyncMode,
  type SyncResult,
  type SyncStateRow,
} from '../lib/backend'

const backend = getBackend()
const mock = isMockBackend()

/** データ種別の日本語表記 */
const DATA_KIND_LABELS: Record<string, string> = {
  projects: 'プロジェクト',
  issues: '課題',
  users: 'ユーザ',
  teams: 'チーム',
  project_users: 'プロジェクト参加ユーザ',
}

function dataKindLabel(kind: string): string {
  return DATA_KIND_LABELS[kind] ?? kind
}

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

function formatDateTime(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function formatElapsed(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const min = Math.floor((Date.now() - d.getTime()) / 60000)
  if (min < 1) return 'たった今'
  if (min < 60) return `${min} 分前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 時間前`
  return `${Math.floor(hour / 24)} 日前`
}

// ---------------------------------------------------------------------------
// 読み込み
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const loading = ref(false)
const globalError = ref('')

const states = ref<SyncStateRow[]>([])
const projects = ref<Project[]>([])
const selectedProjectId = ref<number>(0)
/** プロジェクト一覧の最新化に失敗した場合の警告(キャッシュ表示は継続する) */
const projectsWarning = ref('')

function projectLabel(projectId: number): string {
  if (!projectId) return '(スペース全体)'
  const p = projects.value.find((x) => x.id === projectId)
  return p ? `${p.name}(${p.projectKey})` : `プロジェクト ID ${projectId}`
}

async function reload() {
  // 動作ログのファイルは日付で切り替わる(ローテーション)ため、
  // マウント時に取得した出力先を表示し続けないよう再読込のたびに取り直す(低 2)。
  await loadLogInfo()
  if (!profileId.value) return
  await loadRateLimit()
  loading.value = true
  globalError.value = ''
  try {
    const [s, p] = await Promise.all([
      backend.getSyncState(profileId.value),
      backend.listProjects(profileId.value),
    ])
    states.value = s
    projects.value = p
    if (!projects.value.some((x) => x.id === selectedProjectId.value)) {
      selectedProjectId.value = projects.value.length > 0 ? projects.value[0].id : 0
    }
  } catch (e) {
    globalError.value = `同期状態の取得に失敗しました: ${errorMessage(e)}`
  } finally {
    loading.value = false
  }
}

/**
 * 画面表示時にプロジェクト一覧を最新化してから読み込む(高 1)。
 * 参加解除されたプロジェクトのキャッシュが手動同期まで残ると、
 * アクセス権を失った課題を表示し続けてしまうため、ローカルキャッシュを
 * 表示する前に必ず API と突合する。
 * 同期はベストエフォートで、失敗しても警告を出してキャッシュ表示は継続する。
 * 連打・多重実行は syncingProjects フラグで防ぐ。
 */
async function refreshProjects() {
  if (!profileId.value || syncingProjects.value) return
  syncingProjects.value = true
  projectsWarning.value = ''
  try {
    await backend.syncProjects(profileId.value)
  } catch {
    projectsWarning.value =
      'プロジェクト一覧を最新化できませんでした(オフライン等)。表示はローカルキャッシュです。'
  } finally {
    syncingProjects.value = false
  }
  await reload()
}

// ---------------------------------------------------------------------------
// レート制限の残量
// ---------------------------------------------------------------------------

const RATE_CATEGORY_LABELS: Record<string, string> = {
  read: '読み込み',
  update: '更新',
  search: '検索',
  icon: 'アイコン',
}

/** 残量の自動更新間隔(ミリ秒)。バックエンド呼び出しは追加の API 通信を伴わない */
const RATE_REFRESH_MS = 10_000

const rateLimit = ref<RateLimitStatus | null>(null)
let rateTimer: ReturnType<typeof setInterval> | null = null
/**
 * 画面が破棄済みかどうか。
 * onMounted は await を挟むため、その待機中にアンマウントされると
 * onUnmounted(破棄処理)が先に走り、後からタイマーが生成されて残り続ける。
 * タイマー生成前にこの旗を確認して、破棄済みなら生成しない。
 */
let unmounted = false

/** 残量の自動更新タイマーを開始する(破棄済み・生成済みなら何もしない) */
function startRateTimer() {
  if (unmounted || rateTimer) return
  rateTimer = setInterval(loadRateLimit, RATE_REFRESH_MS)
}

async function loadRateLimit() {
  if (!profileId.value) return
  try {
    rateLimit.value = await backend.getRateLimitStatus(profileId.value)
  } catch {
    // 補助情報のため、失敗しても画面全体のエラーにはしない
    rateLimit.value = null
  }
}

function formatResetTime(unix: number): string {
  if (!unix) return '-'
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

onUnmounted(() => {
  unmounted = true
  if (rateTimer) {
    clearInterval(rateTimer)
    rateTimer = null
  }
})

// ---------------------------------------------------------------------------
// 動作ログ
// ---------------------------------------------------------------------------

const logInfo = ref<LogInfo>({ path: '', enabled: false })

async function loadLogInfo() {
  try {
    logInfo.value = await backend.getLogInfo()
  } catch {
    // 動作ログの案内表示は補助情報のため、失敗しても画面全体のエラーにはしない
    logInfo.value = { path: '', enabled: false }
  }
}

onMounted(async () => {
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    globalError.value = `接続先プロファイルの取得に失敗しました: ${errorMessage(e)}`
  } finally {
    initializing.value = false
  }
  await loadLogInfo()
  if (profileId.value) {
    await refreshProjects()
    // 残量は 10 秒間隔で自動更新(追加の API 通信は発生しない)。
    // ここまでの await 中にアンマウントされている可能性があるため、
    // startRateTimer 側で破棄済み・二重生成を確認する
    startRateTimer()
  }
})

// ---------------------------------------------------------------------------
// 手動同期
// ---------------------------------------------------------------------------

// 既定は auto(未同期プロジェクトでは incremental が必ず失敗するため。低 1)
const syncMode = ref<SyncMode>('auto')
const syncing = ref(false)
const syncingProjects = ref(false)
const syncResult = ref<SyncResult | null>(null)
const syncResultProject = ref('')
const syncError = ref('')

const busy = computed(() => syncing.value || syncingProjects.value || loading.value)

const syncModeLabel = (mode: string) => (mode === 'full' ? 'フル同期' : '差分同期')

async function runIssueSync() {
  if (!selectedProjectId.value || busy.value) return
  syncing.value = true
  syncError.value = ''
  syncResult.value = null
  syncResultProject.value = projectLabel(selectedProjectId.value)
  try {
    syncResult.value = await backend.syncIssues(
      profileId.value,
      selectedProjectId.value,
      syncMode.value,
    )
    await reload()
  } catch (e) {
    syncError.value = `同期に失敗しました: ${errorMessage(e)}`
  } finally {
    syncing.value = false
  }
}

async function runProjectSync() {
  if (busy.value) return
  syncingProjects.value = true
  syncError.value = ''
  projectsWarning.value = ''
  try {
    await backend.syncProjects(profileId.value)
    await reload()
  } catch (e) {
    syncError.value = `プロジェクトの同期に失敗しました: ${errorMessage(e)}`
  } finally {
    syncingProjects.value = false
  }
}
</script>

<template>
  <div class="sync-status">
    <h1>同期状態</h1>

    <p v-if="mock" class="mock-note">
      Wails ランタイム外で動作中のため、モックデータを表示しています(実データではありません)。
    </p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="projectsWarning" class="notice warn">{{ projectsWarning }}</p>

    <p v-if="initializing">読み込み中...</p>

    <p v-else-if="!profileId" class="notice">
      接続先プロファイルが選択されていません。「接続設定」画面でプロファイルを登録・選択してください。
    </p>

    <template v-else>
      <!-- 同期状態一覧 -->
      <section class="panel">
        <h2>データ種別ごとの最終同期時刻</h2>

        <p v-if="loading">読み込み中...</p>

        <p v-else-if="states.length === 0" class="notice">
          同期の記録がありません。下の「手動同期」から同期を実行してください。
        </p>

        <table v-else>
          <thead>
            <tr>
              <th>データ種別</th>
              <th>プロジェクト</th>
              <th>最終同期時刻</th>
              <th>経過</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in states" :key="`${s.dataKind}-${s.projectId}`">
              <td>{{ dataKindLabel(s.dataKind) }}</td>
              <td>{{ projectLabel(s.projectId) }}</td>
              <td>{{ s.lastSyncedAt ? formatDateTime(s.lastSyncedAt) : '未同期' }}</td>
              <td>{{ s.lastSyncedAt ? formatElapsed(s.lastSyncedAt) : '-' }}</td>
            </tr>
          </tbody>
        </table>

        <div class="row buttons">
          <button :disabled="busy" @click="reload">再読込</button>
        </div>
      </section>

      <!-- 手動同期 -->
      <section class="panel">
        <h2>手動同期</h2>

        <div class="row">
          <label for="s-project">プロジェクト</label>
          <select id="s-project" v-model="selectedProjectId" :disabled="busy">
            <option v-if="projects.length === 0" :value="0">(プロジェクトがありません)</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ p.name }}({{ p.projectKey }})
            </option>
          </select>
        </div>

        <div class="row">
          <label>同期モード</label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="auto" :disabled="busy" />
            自動(初回はフル同期)
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="full" :disabled="busy" />
            フル同期
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="incremental" :disabled="busy" />
            差分同期
          </label>
        </div>

        <div class="row buttons">
          <button class="primary" :disabled="busy || !selectedProjectId" @click="runIssueSync">
            {{ syncing ? '課題を同期中...' : '課題を同期' }}
          </button>
          <button :disabled="busy" @click="runProjectSync">
            {{ syncingProjects ? 'プロジェクト同期中...' : 'プロジェクト一覧を同期' }}
          </button>
          <span v-if="syncing || syncingProjects" class="spinner" aria-hidden="true"></span>
        </div>

        <p class="hint">
          自動は同期状態から判定します(未同期・長期間未同期ならフル同期)。
          差分同期は前回同期以降の更新のみを取得します。不整合が疑われる場合はフル同期を選んでください。
          「プロジェクト一覧を同期」はプロジェクト一覧を最新化するだけで、課題は同期しません。
        </p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <div v-if="syncResult" class="result ok">
          <p class="result-title">
            {{ syncResultProject }} の{{ syncModeLabel(syncResult.mode) }}が完了しました
          </p>
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

      <!-- レート制限の残量(観測値のみ。10 秒間隔で自動更新) -->
      <section class="panel">
        <h2>レート制限の残量</h2>
        <table v-if="rateLimit" class="rate-table">
          <thead>
            <tr>
              <th>区分</th>
              <th>残量 / 上限(毎分)</th>
              <th>リセット時刻</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="c in rateLimit.categories" :key="c.name">
              <td>{{ RATE_CATEGORY_LABELS[c.name] ?? c.name }}</td>
              <template v-if="c.observed">
                <td>
                  <span :class="{ 'rate-low': c.limit > 0 && c.remaining < c.limit * 0.2 }">
                    {{ c.remaining }}
                  </span>
                  / {{ c.limit }}
                </td>
                <td>{{ formatResetTime(c.resetUnix) }}</td>
              </template>
              <template v-else>
                <td colspan="2" class="rate-unknown">未取得(API 利用後に表示されます)</td>
              </template>
            </tr>
          </tbody>
        </table>
        <p v-else class="hint">残量情報を取得できませんでした。</p>
        <p class="hint">
          サーバから観測した実測値を表示します(表示の更新に API は消費しません)。課題検索の同期は「検索」、一括更新の書き込みは「更新」の枠を使用します。
        </p>
      </section>
    </template>

    <!-- 動作ログの出力先(プロファイル未選択でも表示する) -->
    <p class="log-info">
      <template v-if="logInfo.enabled">
        動作ログ: <span class="log-path">{{ logInfo.path }}</span>
      </template>
      <template v-else>ログ出力は無効です</template>
    </p>
  </div>
</template>

<style scoped>
.sync-status {
  max-width: 820px;
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

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.88rem;
}

th,
td {
  border: 1px solid #d0d7de;
  padding: 0.4rem 0.6rem;
  text-align: left;
}

th {
  background: #f6f8fa;
  font-weight: 600;
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

.radio {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  font-size: 0.9rem;
  font-weight: 400;
  min-width: auto;
}

select {
  padding: 0.4rem 0.5rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  font-size: 0.9rem;
  background: #fff;
  color: #1f2328;
}

select:disabled {
  background: #f6f8fa;
  color: #8c959f;
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

.hint {
  font-size: 0.8rem;
  color: #57606a;
  margin: 0.5rem 0 0;
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

.log-info {
  margin-top: 1.5rem;
  font-size: 0.8rem;
  color: #57606a;
}

.log-path {
  font-family: monospace;
  word-break: break-all;
}
</style>

<style scoped>
.rate-table {
  border-collapse: collapse;
  min-width: 420px;
}
.rate-table th,
.rate-table td {
  border: 1px solid #d0d7de;
  padding: 6px 12px;
  text-align: left;
}
.rate-table th {
  background: #f6f8fa;
}
.rate-low {
  color: #d1242f;
  font-weight: 600;
}
.rate-unknown {
  color: #57606a;
}
</style>
