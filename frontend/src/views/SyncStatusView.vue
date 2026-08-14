<script lang="ts" setup>
// 同期状態画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, onUnmounted, ref } from 'vue'
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
import { errorMessage, formatDateTime, formatElapsed, syncModeLabel } from '../lib/format'
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

/**
 * 破棄済み・プロファイル切替後の画面が、後から届いた古い応答で
 * 共有のプロジェクト選択を書き換えてしまうのを防ぐガード(高 1)。
 */
const selectionGuard = useProjectSelectionGuard()

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

// ---------------------------------------------------------------------------
// 読み込み
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const loading = ref(false)
const globalError = ref('')

const states = ref<SyncStateRow[]>([])
const projects = ref<Project[]>([])
// プロジェクト選択は画面をまたいで共有する(projectSelection モジュールが保持し、
// プロファイルごとに localStorage へ保存する)
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
  const token = selectionGuard.begin()
  loading.value = true
  globalError.value = ''
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
    projectsWarning.value =
      '課題の同期中のため、プロジェクト一覧の最新化は省略しました(表示はローカルキャッシュです)。'
    await reload()
    return
  }
  if (shouldSkipProjectRefreshFor(profileId.value)) {
    await reload()
    return
  }
  syncingProjects.value = true
  projectsWarning.value = ''
  try {
    // 成功時だけ起点が記録される(失敗時は記録されず、次の画面表示で再試行する)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
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
const syncError = ref('')

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
  if (running.profileId !== profileId.value) return '別の接続先のプロジェクト'
  return projectLabel(running.projectId)
})

const busy = computed(() => issueSyncing.value || syncingProjects.value || loading.value)

/**
 * 実行中の課題同期の進捗(未受信・非実行中は null)。
 * フル同期は数万件になり得るため、件数を出さないと無反応に見える。
 */
const syncProgress = ref<SyncProgress | null>(null)
const syncProgressText = computed(() =>
  syncProgress.value ? formatSyncProgress(syncProgress.value) : '',
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
  syncError.value = ''
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
    syncError.value = `同期に失敗しました: ${errorMessage(e)}`
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
  syncError.value = ''
  projectsWarning.value = ''
  try {
    // 成功すると自動突合の起点も更新される(手動同期でも突合は済んでいるため)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
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

        <div v-else class="table-wrap">
          <table>
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
        </div>

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
          <span v-if="syncing && syncProgressText" class="sync-progress" aria-live="polite">
            {{ syncProgressText }}
          </span>
        </div>

        <!-- 他画面(課題抽出)で開始した同期。runId を知らないため進捗は出せないが、
             操作できない理由は伝える(R10) -->
        <p v-if="!syncing && issueSyncRunning" class="hint warn">
          他の画面で開始した課題同期({{ activeIssueSyncLabel }})が実行中です。
          完了するまでプロジェクトの切り替えと同期は実行できません。
        </p>

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
        <div v-if="rateLimit" class="table-wrap">
          <table class="rate-table">
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
        </div>
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

button.primary {
  background: var(--accent-emphasis);
  border-color: var(--accent-emphasis);
  color: var(--on-accent);
}

button.primary:hover:not(:disabled) {
  background: var(--accent-emphasis-hover);
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

.result {
  margin-top: 0.75rem;
  border-radius: 4px;
  padding: 0.6rem 0.9rem;
  font-size: 0.9rem;
}

.result.ok {
  background: var(--success-bg);
  border: 1px solid var(--success-border);
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
  color: var(--warning-text);
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
