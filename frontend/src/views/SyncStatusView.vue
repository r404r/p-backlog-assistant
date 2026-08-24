<script lang="ts" setup>
// 同期状態画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SyncResultPanel from '../components/SyncResultPanel.vue'
import {
  formatSyncProgress,
  getBackend,
  isMockBackend,
  newSyncRunId,
  onSyncProgress,
  type LogInfo,
  type Project,
  type RateLimitStatus,
  type SyncMode,
  type SyncProgress,
  type SyncResult,
  type SyncStateRow,
} from '../lib/backend'
import { translateSyncMode } from '../lib/enumLabels'
import { errorMessage, formatDateTime, formatElapsed } from '../lib/format'
import type { Language } from '../lib/i18n'
import { useMessage } from '../lib/message'
import { runSharedProjectRefresh, shouldSkipProjectRefreshFor } from '../lib/projectRefresh'
import {
  resolveProjectSelection,
  restoreProjectSelection,
  selectedProjectId,
  useProjectSelectionGuard,
} from '../lib/projectSelection'
import {
  activeIssueSync,
  beginIssueSync,
  endIssueSync,
  issueSyncRunning,
} from '../lib/syncState'

const backend = getBackend()
const mock = isMockBackend()

// 翻訳関数と表示言語はこの画面の i18n インスタンスから取る
// (lib の既定値=グローバル Composer に任せると、独立インスタンスと食い違う)
const { t, locale } = useI18n()
const language = computed(() => locale.value as Language)

/**
 * 破棄済み・プロファイル切替後の画面が、後から届いた古い応答で
 * 共有のプロジェクト選択を書き換えてしまうのを防ぐガード(高 1)。
 */
const selectionGuard = useProjectSelectionGuard()

/**
 * データ種別 → カタログキーの対応表(設計 §3.3 の「キー対応表」)。
 * dataKind は Go 側 sync_state テーブルの機械値なのでフロントで翻訳できる。
 */
const DATA_KIND_LABEL_KEYS: Record<string, string> = {
  projects: 'sync.dataKind.projects',
  issues: 'sync.dataKind.issues',
  users: 'sync.dataKind.users',
  teams: 'sync.dataKind.teams',
  project_users: 'sync.dataKind.projectUsers',
}

/** 未知の種別は訳を捏造せず、機械値をそのまま出す(異常に気付けるように) */
function dataKindLabel(kind: string): string {
  const key = DATA_KIND_LABEL_KEYS[kind]
  return key ? t(key) : kind
}

// ---------------------------------------------------------------------------
// 読み込み
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const loading = ref(false)
const [globalError, setGlobalError] = useMessage(t)

const states = ref<SyncStateRow[]>([])
const projects = ref<Project[]>([])
// プロジェクト選択は画面をまたいで共有する(projectSelection モジュールが保持し、
// プロファイルごとに localStorage へ保存する)
/** プロジェクト一覧の最新化に失敗した場合の警告(キャッシュ表示は継続する) */
const [projectsWarning, setProjectsWarning] = useMessage(t)

function projectLabel(projectId: number): string {
  if (!projectId) return t('sync.project.whole')
  const p = projects.value.find((x) => x.id === projectId)
  return p
    ? t('sync.project.named', { name: p.name, key: p.projectKey })
    : t('sync.project.byId', { id: projectId })
}

async function reload() {
  // 動作ログのファイルは日付で切り替わる(ローテーション)ため、
  // マウント時に取得した出力先を表示し続けないよう再読込のたびに取り直す(低 2)。
  await loadLogInfo()
  if (!profileId.value) return
  await loadRateLimit()
  const token = selectionGuard.begin()
  loading.value = true
  setGlobalError(null)
  try {
    const [s, p] = await Promise.all([
      backend.getSyncState(profileId.value),
      backend.listProjects(profileId.value),
    ])
    // 画面が破棄済み、またはプロファイルが切り替わっていたら反映しない
    // (古い応答で共有のプロジェクト選択を書き換えないため)
    if (!selectionGuard.isCurrent(token)) return
    states.value = s
    projects.value = p
    // 復元した(または選択中の)プロジェクトが一覧に無ければ先頭へフォールバックする
    selectedProjectId.value = resolveProjectSelection(projects.value, selectedProjectId.value)
  } catch (e) {
    setGlobalError('sync.error.loadState', { message: errorMessage(e) })
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
 *
 * 課題同期の実行中は API による最新化を省略する(R10)。Go 側の同期処理は
 * 直列化されている(service の syncMu)ため、ここで待つと画面の初期表示が
 * 課題同期の完了(数分)までブロックされてしまう。ローカルキャッシュの
 * 読み込み(reload)は省略せず、一覧が空のままにならないようにする。
 *
 * 直近(10 分以内)に突合できている場合も API による最新化を省略する
 * (projectRefresh)。画面を行き来するたびに通信すると体感が重く、
 * レート制限も消費するため。省略は正常動作なので警告等は出さない。
 * 他画面が始めた突合が実行中なら、新たに始めずそれへ合流する。
 */
async function refreshProjects() {
  if (!profileId.value || syncingProjects.value) return
  if (issueSyncRunning.value) {
    setProjectsWarning('sync.warning.skippedDuringIssueSync')
    await reload()
    return
  }
  if (shouldSkipProjectRefreshFor(profileId.value)) {
    await reload()
    return
  }
  syncingProjects.value = true
  setProjectsWarning(null)
  try {
    // 成功時だけ起点が記録される(失敗時は記録されず、次の画面表示で再試行する)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
  } catch {
    setProjectsWarning('sync.warning.refreshFailed')
  } finally {
    syncingProjects.value = false
  }
  await reload()
}

// ---------------------------------------------------------------------------
// レート制限の残量
// ---------------------------------------------------------------------------

const RATE_CATEGORY_LABEL_KEYS: Record<string, string> = {
  read: 'sync.rate.category.read',
  update: 'sync.rate.category.update',
  search: 'sync.rate.category.search',
  icon: 'sync.rate.category.icon',
}

/** 未知の区分は訳を捏造せず、Go から届いた名前をそのまま出す */
function rateCategoryLabel(name: string): string {
  const key = RATE_CATEGORY_LABEL_KEYS[name]
  return key ? t(key) : name
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
    setGlobalError('sync.error.activeProfile', { message: errorMessage(e) })
  } finally {
    initializing.value = false
  }
  await loadLogInfo()
  // getActiveProfile・loadLogInfo の待機中にアンマウントされていたら、共有状態には触れない(高 1)。
  // 触ると、既に別プロファイルで表示中の新しい画面の選択を古いプロファイルへ
  // 巻き戻してしまう。この時点ではプロファイルが未確定でトークン照合ができないため、
  // 生存確認のみを行う(画面は同時に 1 つしか表示されないため、生存 = 現在の画面)。
  if (profileId.value && selectionGuard.isAlive()) {
    // 保存済みの選択(他画面で選んだ値・前回起動時の値)を先に復元してから一覧を読む
    restoreProjectSelection(profileId.value)
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
const [syncError, setSyncError] = useMessage(t)

/**
 * 課題同期が実行中か。ローカルの syncing に加えて共有状態も見る(R10)。
 * 画面を移動すると syncing は失われるが、Go 側の同期は走り続けるため。
 */
const issueSyncing = computed(() => syncing.value || issueSyncRunning.value)

/**
 * 他画面で実行中の同期の対象プロジェクト(共有状態から解決する。非実行中は空文字)。
 *
 * 名前を引けるのはこの画面が表示している接続先の一覧(projects)だけなので、
 * 同期中に接続先プロファイルを切り替えた場合は名前解決しない。プロジェクト ID は
 * スペースごとの採番のため、他スペースの同じ ID のプロジェクト名を
 * 実行中の対象として誤表示してしまう。
 */
const activeIssueSyncLabel = computed(() => {
  const running = activeIssueSync.value
  if (!running) return ''
  if (running.profileId !== profileId.value) return t('sync.project.otherProfile')
  return projectLabel(running.projectId)
})

const busy = computed(() => issueSyncing.value || syncingProjects.value || loading.value)

/**
 * 実行中の課題同期の進捗(未受信・非実行中は null)。
 * フル同期は数万件になり得るため、件数を出さないと無反応に見える。
 */
const syncProgress = ref<SyncProgress | null>(null)
const syncProgressText = computed(() =>
  // 翻訳関数・表示言語はこの画面の i18n インスタンスから渡す(IssuesView と同じ流儀)
  syncProgress.value ? formatSyncProgress(syncProgress.value, t, language.value) : '',
)
let unsubscribeSyncProgress: (() => void) | null = null

/**
 * 表示対象の実行 ID(この画面が今まさに走らせている同期。非実行中は空文字)。
 * プロファイル ID + プロジェクト ID の一致だけでは、同じ対象を続けて
 * 同期し直した場合や、別画面が同じ対象を同期している場合に旧実行の進捗を
 * 拾ってしまうため、実行ごとに一意な ID で突き合わせる(中 4)。
 */
let currentSyncRunId = ''

onMounted(() => {
  unsubscribeSyncProgress = onSyncProgress((p) => {
    // 自分が開始した実行のイベントだけを表示する
    // (他画面・他プロファイル・失効した実行・前回実行の残りは無視する)
    if (!currentSyncRunId || p.runId !== currentSyncRunId) return
    syncProgress.value = p
  })
})

onUnmounted(() => {
  if (unsubscribeSyncProgress) unsubscribeSyncProgress()
  unsubscribeSyncProgress = null
})

async function runIssueSync() {
  if (!selectedProjectId.value || busy.value) return
  syncing.value = true
  setSyncError(null)
  syncResult.value = null
  syncProgress.value = null
  syncResultProject.value = projectLabel(selectedProjectId.value)
  const runId = newSyncRunId()
  currentSyncRunId = runId
  // 画面をまたいで抑止するため、実行中であることを共有状態にも記録する(R10)
  const targetProjectId = selectedProjectId.value
  beginIssueSync(profileId.value, targetProjectId, runId)
  try {
    syncResult.value = await backend.syncIssues(
      profileId.value,
      targetProjectId,
      syncMode.value,
      runId,
    )
    await reload()
  } catch (e) {
    setSyncError('sync.error.syncIssues', { message: errorMessage(e) })
  } finally {
    syncing.value = false
    syncProgress.value = null
    // 応答後に届く進捗(あれば)を受け取らないよう、実行 ID も外す
    if (currentSyncRunId === runId) currentSyncRunId = ''
    // 共有状態の解除(主経路。syncState.ts のコメント参照)
    endIssueSync(runId)
  }
}

/**
 * 手動の「プロジェクト一覧を同期」。
 * 利用者が明示的に求めた操作のため、画面表示時のスロットリング
 * (10 分以内なら省略)は適用せず、常に API と突合する。
 * ただし他画面が始めた突合が実行中の場合はそれへ合流する
 * (同じ突合を二重に走らせないため。projectRefresh.ts 参照)。
 */
async function runProjectSync() {
  if (busy.value) return
  syncingProjects.value = true
  setSyncError(null)
  setProjectsWarning(null)
  try {
    // 成功すると自動突合の起点も更新される(手動同期でも突合は済んでいるため)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
    await reload()
  } catch (e) {
    setSyncError('sync.error.syncProjects', { message: errorMessage(e) })
  } finally {
    syncingProjects.value = false
  }
}
</script>

<template>
  <div class="sync-status">
    <h1>{{ t('sync.title') }}</h1>

    <p v-if="mock" class="mock-note">{{ t('sync.mockNote') }}</p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="projectsWarning" class="notice warn">{{ projectsWarning }}</p>

    <p v-if="initializing">{{ t('common.state.loading') }}</p>

    <p v-else-if="!profileId" class="notice">{{ t('sync.noProfile') }}</p>

    <template v-else>
      <!-- 同期状態一覧 -->
      <section class="panel">
        <h2>{{ t('sync.state.title') }}</h2>

        <p v-if="loading">{{ t('common.state.loading') }}</p>

        <p v-else-if="states.length === 0" class="notice">{{ t('sync.state.empty') }}</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('sync.state.colDataKind') }}</th>
                <th>{{ t('common.label.project') }}</th>
                <th>{{ t('sync.state.colLastSynced') }}</th>
                <th>{{ t('sync.state.colElapsed') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in states" :key="`${s.dataKind}-${s.projectId}`">
                <td>{{ dataKindLabel(s.dataKind) }}</td>
                <td>{{ projectLabel(s.projectId) }}</td>
                <td>
                  {{ s.lastSyncedAt ? formatDateTime(s.lastSyncedAt) : t('common.state.notSynced') }}
                </td>
                <td>{{ s.lastSyncedAt ? formatElapsed(s.lastSyncedAt, t) : '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="row buttons">
          <button :disabled="busy" @click="reload">{{ t('common.action.reload') }}</button>
        </div>
      </section>

      <!-- 手動同期 -->
      <section class="panel">
        <h2>{{ t('sync.manual.title') }}</h2>

        <div class="row">
          <label for="s-project">{{ t('common.label.project') }}</label>
          <select id="s-project" v-model="selectedProjectId" :disabled="busy">
            <option v-if="projects.length === 0" :value="0">
              {{ t('sync.manual.noProjects') }}
            </option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ t('sync.project.named', { name: p.name, key: p.projectKey }) }}
            </option>
          </select>
        </div>

        <div class="row">
          <label>{{ t('sync.manual.modeLabel') }}</label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="auto" :disabled="busy" />
            {{ t('sync.manual.modeAuto') }}
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="full" :disabled="busy" />
            {{ translateSyncMode(t, 'full') }}
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="incremental" :disabled="busy" />
            {{ translateSyncMode(t, 'incremental') }}
          </label>
        </div>

        <div class="row buttons">
          <button class="primary" :disabled="busy || !selectedProjectId" @click="runIssueSync">
            {{ syncing ? t('sync.manual.syncingIssues') : t('sync.manual.syncIssues') }}
          </button>
          <button :disabled="busy" @click="runProjectSync">
            {{ syncingProjects ? t('sync.manual.syncingProjects') : t('sync.manual.syncProjects') }}
          </button>
          <span v-if="syncing || syncingProjects" class="spinner" aria-hidden="true"></span>
          <span v-if="syncing && syncProgressText" class="sync-progress" aria-live="polite">
            {{ syncProgressText }}
          </span>
        </div>

        <!-- 他画面(課題抽出)で開始した同期。runId を知らないため進捗は出せないが、
             操作できない理由は伝える(R10) -->
        <p v-if="!syncing && issueSyncRunning" class="hint warn">
          {{ t('sync.manual.otherSyncRunning', { target: activeIssueSyncLabel }) }}
        </p>

        <p class="hint">{{ t('sync.manual.hint') }}</p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <SyncResultPanel
          v-if="syncResult"
          :result="syncResult"
          :title="
            t('sync.result.title', {
              project: syncResultProject,
              mode: translateSyncMode(t, syncResult.mode),
            })
          "
        />
      </section>

      <!-- レート制限の残量(観測値のみ。10 秒間隔で自動更新) -->
      <section class="panel">
        <h2>{{ t('sync.rate.title') }}</h2>
        <div v-if="rateLimit" class="table-wrap">
          <table class="rate-table">
            <thead>
              <tr>
                <th>{{ t('sync.rate.colCategory') }}</th>
                <th>{{ t('sync.rate.colRemaining') }}</th>
                <th>{{ t('sync.rate.colReset') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in rateLimit.categories" :key="c.name">
                <td>{{ rateCategoryLabel(c.name) }}</td>
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
                  <td colspan="2" class="rate-unknown">{{ t('sync.rate.notObserved') }}</td>
                </template>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="hint">{{ t('sync.rate.unavailable') }}</p>
        <p class="hint">{{ t('sync.rate.hint') }}</p>
      </section>
    </template>

    <!-- 動作ログの出力先(プロファイル未選択でも表示する) -->
    <p class="log-info">
      <template v-if="logInfo.enabled">
        {{ t('sync.log.label') }} <span class="log-path">{{ logInfo.path }}</span>
      </template>
      <template v-else>{{ t('sync.log.disabled') }}</template>
    </p>
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.sync-status {
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
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: var(--surface);
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

/* 幅が足りないときだけ横スクロールさせる(パネル自体は全幅を保つ) */
.table-wrap {
  width: 100%;
  overflow-x: auto;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.88rem;
}

th,
td {
  border: 1px solid var(--border);
  padding: 0.4rem 0.6rem;
  text-align: left;
}

th {
  background: var(--bg-muted);
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
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.9rem;
  background: var(--bg);
  color: var(--text);
}

select:disabled {
  background: var(--bg-muted);
  color: var(--text-faint);
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

/* 同期中の進捗(取得中 N / M 件) */
.sync-progress {
  font-size: 0.85rem;
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.5rem 0 0;
}

/* 注意喚起のヒント(課題抽出画面と同じ配色) */
.hint.warn {
  color: var(--warning-text);
}

.error {
  color: var(--danger-text);
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.log-info {
  margin-top: 1.5rem;
  font-size: 0.8rem;
  color: var(--text-muted);
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
  border: 1px solid var(--border);
  padding: 6px 12px;
  text-align: left;
}
.rate-table th {
  background: var(--bg-muted);
}
.rate-low {
  color: var(--danger-emphasis-text);
  font-weight: 600;
}
.rate-unknown {
  color: var(--text-muted);
}
</style>
