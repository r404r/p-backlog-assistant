<script lang="ts" setup>
// 課題抽出画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  getBackend,
  isMockBackend,
  type CustomFieldDef,
  type IssueQuery,
  type IssueRow,
  type Project,
  type SyncMode,
  type SyncResult,
} from '../lib/backend'

const backend = getBackend()
const mock = isMockBackend()

/** 画面に表示する最大件数(Excel 出力は条件に一致する全件が対象) */
const PREVIEW_LIMIT = 200

/** Excel 出力の列(key は Go 側 export パッケージの列キー) */
interface ExportColumn {
  key: string
  label: string
}

/** 固定の出力列(キーは IssueRow のキー。Go 側の列キーと対応) */
const FIXED_EXPORT_COLUMNS: ExportColumn[] = [
  { key: 'issueKey', label: '課題キー' },
  { key: 'summary', label: '件名' },
  { key: 'statusName', label: '状態' },
  { key: 'assigneeName', label: '担当者' },
  { key: 'issueTypeName', label: '種別' },
  { key: 'priorityName', label: '優先度' },
  { key: 'created', label: '作成日' },
  { key: 'updated', label: '更新日' },
  { key: 'dueDate', label: '期限' },
]

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

/** RFC3339 を「YYYY-MM-DD HH:mm」に整形する(空文字はそのまま) */
function formatDateTime(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 最終同期時刻からの経過を日本語で表す */
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
// アクティブプロファイル・プロジェクト
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const globalError = ref('')

const projects = ref<Project[]>([])
const selectedProjectId = ref<number>(0)
const projectsLoading = ref(false)
const projectsSyncing = ref(false)
/** プロジェクト一覧の最新化に失敗した場合の警告(キャッシュ表示は継続する) */
const projectsWarning = ref('')

const selectedProject = computed(
  () => projects.value.find((p) => p.id === selectedProjectId.value) ?? null,
)

/** 選択中プロジェクトの同期状態(鮮度)を取得できなかったか(中 1) */
const syncStateUnknown = computed(() => !!selectedProject.value?.syncStateUnknown)

/**
 * 選択中プロジェクトが一度も同期されていないか。
 * 鮮度を取得できなかった場合は「未同期」と断定できないため false にする(中 1)。
 */
const neverSynced = computed(
  () => !!selectedProject.value && !syncStateUnknown.value && !selectedProject.value.lastSyncedAt,
)

async function loadProjects() {
  if (!profileId.value) return
  projectsLoading.value = true
  globalError.value = ''
  try {
    projects.value = await backend.listProjects(profileId.value)
    if (!projects.value.some((p) => p.id === selectedProjectId.value)) {
      selectedProjectId.value = projects.value.length > 0 ? projects.value[0].id : 0
    }
  } catch (e) {
    globalError.value = `プロジェクト一覧の取得に失敗しました: ${errorMessage(e)}`
  } finally {
    projectsLoading.value = false
  }
}

async function syncProjects() {
  if (!profileId.value || projectsSyncing.value) return
  projectsSyncing.value = true
  globalError.value = ''
  projectsWarning.value = ''
  try {
    await backend.syncProjects(profileId.value)
    await loadProjects()
  } catch (e) {
    globalError.value = `プロジェクトの同期に失敗しました: ${errorMessage(e)}`
  } finally {
    projectsSyncing.value = false
  }
}

/**
 * 画面表示時にプロジェクト一覧を最新化してから読み込む(高 1)。
 * 参加解除されたプロジェクトのキャッシュが手動同期まで残ると、
 * アクセス権を失った課題を表示し続けてしまうため、ローカルキャッシュを
 * 表示する前に必ず API と突合する。
 * 同期はベストエフォートで、失敗しても警告を出してキャッシュ表示は継続する。
 * 連打・多重実行は projectsSyncing フラグで防ぐ。
 */
async function refreshProjects() {
  if (!profileId.value || projectsSyncing.value) return
  projectsSyncing.value = true
  projectsWarning.value = ''
  try {
    await backend.syncProjects(profileId.value)
  } catch {
    projectsWarning.value =
      'プロジェクト一覧を最新化できませんでした(オフライン等)。表示はローカルキャッシュです。'
  } finally {
    projectsSyncing.value = false
  }
  await loadProjects()
}

onMounted(async () => {
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    globalError.value = `接続先プロファイルの取得に失敗しました: ${errorMessage(e)}`
  } finally {
    initializing.value = false
  }
  if (profileId.value) await refreshProjects()
})

// ---------------------------------------------------------------------------
// 条件フォーム
// ---------------------------------------------------------------------------

const cond = reactive({
  keyword: '',
  updatedFrom: '',
  updatedTo: '',
  createdFrom: '',
  createdTo: '',
  statusName: '',
  assigneeName: '',
})

const statusOptions = ref<string[]>([])
const assigneeOptions = ref<string[]>([])
const optionsLoading = ref(false)

async function loadFilterOptions() {
  statusOptions.value = []
  assigneeOptions.value = []
  if (!profileId.value || !selectedProjectId.value) return
  optionsLoading.value = true
  try {
    const opts = await backend.listFilterOptions(profileId.value, selectedProjectId.value)
    statusOptions.value = opts.statuses
    assigneeOptions.value = opts.assignees
    // 選択済みの値が候補に無くなった場合は「すべて」へ戻す
    if (cond.statusName && !opts.statuses.includes(cond.statusName)) cond.statusName = ''
    if (cond.assigneeName && !opts.assignees.includes(cond.assigneeName)) cond.assigneeName = ''
  } catch (e) {
    globalError.value = `絞り込み候補の取得に失敗しました: ${errorMessage(e)}`
  } finally {
    optionsLoading.value = false
  }
}

// プロジェクトを切り替えたら候補と結果をリセットする
watch(selectedProjectId, () => {
  rows.value = []
  total.value = 0
  searched.value = false
  searchError.value = ''
  syncResult.value = null
  syncError.value = ''
  exportPath.value = ''
  exportError.value = ''
  void loadFilterOptions()
  void loadCustomFields()
})

/** 現在の条件を IssueQuery に変換する(空文字の条件は送らない) */
function buildQuery(withLimit: boolean): IssueQuery {
  const q: IssueQuery = { projectId: selectedProjectId.value }
  if (cond.keyword.trim()) q.keyword = cond.keyword.trim()
  if (cond.updatedFrom) q.updatedFrom = cond.updatedFrom
  if (cond.updatedTo) q.updatedTo = cond.updatedTo
  if (cond.createdFrom) q.createdFrom = cond.createdFrom
  if (cond.createdTo) q.createdTo = cond.createdTo
  if (cond.statusName) q.statusName = cond.statusName
  if (cond.assigneeName) q.assigneeName = cond.assigneeName
  if (withLimit) q.limit = PREVIEW_LIMIT
  return q
}

function clearConditions() {
  cond.keyword = ''
  cond.updatedFrom = ''
  cond.updatedTo = ''
  cond.createdFrom = ''
  cond.createdTo = ''
  cond.statusName = ''
  cond.assigneeName = ''
}

// ---------------------------------------------------------------------------
// 検索(ローカル DB)
// ---------------------------------------------------------------------------

const rows = ref<IssueRow[]>([])
const total = ref(0)
const searching = ref(false)
const searched = ref(false)
const searchError = ref('')

/** 表示件数が上限で切り詰められているか */
const truncated = computed(() => total.value > rows.value.length)

async function search() {
  if (!selectedProjectId.value || searching.value) return
  searching.value = true
  searchError.value = ''
  try {
    const res = await backend.searchIssues(profileId.value, buildQuery(true))
    rows.value = res.rows
    total.value = res.total
    searched.value = true
  } catch (e) {
    searchError.value = `検索に失敗しました: ${errorMessage(e)}`
  } finally {
    searching.value = false
  }
}

// ---------------------------------------------------------------------------
// 同期
// ---------------------------------------------------------------------------

// 既定は auto(未同期プロジェクトでは incremental が必ず失敗するため。低 1)
const syncMode = ref<SyncMode>('auto')
const syncing = ref(false)
const syncResult = ref<SyncResult | null>(null)
const syncError = ref('')

const syncModeLabel = (mode: string) => (mode === 'full' ? 'フル同期' : '差分同期')

async function runSync() {
  if (!selectedProjectId.value || syncing.value) return
  syncing.value = true
  syncError.value = ''
  syncResult.value = null
  try {
    syncResult.value = await backend.syncIssues(
      profileId.value,
      selectedProjectId.value,
      syncMode.value,
    )
    await loadProjects() // 鮮度表示を更新
    await loadFilterOptions()
  } catch (e) {
    syncError.value = `同期に失敗しました: ${errorMessage(e)}`
  } finally {
    syncing.value = false
  }
}

// ---------------------------------------------------------------------------
// Excel 出力
// ---------------------------------------------------------------------------

/** カスタム属性列の列キーの接頭辞(Go 側 export パッケージの規約 cf_{定義ID} と対) */
const CUSTOM_COLUMN_PREFIX = 'cf_'

/** カスタム属性の定義 ID から列キーを作る */
const customColumnKey = (defId: number) => `${CUSTOM_COLUMN_PREFIX}${defId}`

/** 選択中プロジェクトのカスタム属性の定義(取得できない場合は空) */
const customFields = ref<CustomFieldDef[]>([])

/** カスタム属性の出力列(定義順) */
const customColumns = computed<ExportColumn[]>(() =>
  customFields.value.map((f) => ({ key: customColumnKey(f.id), label: f.name })),
)

/** 出力できる列(固定列 + カスタム属性列) */
const exportColumns = computed<ExportColumn[]>(() => [
  ...FIXED_EXPORT_COLUMNS,
  ...customColumns.value,
])

// 既定はカスタム属性列オフ(固定列のみ)
const selectedColumns = ref<string[]>(FIXED_EXPORT_COLUMNS.map((c) => c.key))

/** カスタム属性の取得失敗の表示用メッセージ(空 = 正常) */
const customFieldsError = ref('')

/**
 * loadCustomFields の世代番号。プロジェクトを A→B→A と素早く切り替えると
 * projectId の比較だけでは最初の A の古い応答を弾けないため、
 * 「最後に開始した要求」の応答だけを反映する。
 */
let customFieldsRequestSeq = 0

/**
 * 出力列に載せるカスタム属性の定義を取得する。
 *
 * 未対応プラン・権限不足はバックエンド側で空配列へ縮退済みのため、
 * ここに届く失敗は通信断等の障害。固定列の出力は妨げず、
 * 取得できなかった旨の警告と再試行の導線を表示する。
 */
async function loadCustomFields() {
  const seq = ++customFieldsRequestSeq
  // 前のプロジェクトの列が選択に残らないようにしてから取得する
  customFields.value = []
  customFieldsError.value = ''
  pruneUnavailableColumns()
  if (!profileId.value || !selectedProjectId.value) return
  try {
    const master = await backend.getMasterData(profileId.value, selectedProjectId.value)
    // より新しい要求が開始済みなら、この(古い)応答は反映しない
    if (seq !== customFieldsRequestSeq) return
    customFields.value = master.customFields
  } catch (e) {
    if (seq !== customFieldsRequestSeq) return
    customFieldsError.value =
      'カスタム属性の取得に失敗しました(固定列は出力できます): ' +
      (e instanceof Error ? e.message : String(e))
  }
}

/** 選択済みの列から、現在は選択できない列(切替前のカスタム属性列)を外す */
function pruneUnavailableColumns() {
  const available = new Set(exportColumns.value.map((c) => c.key))
  selectedColumns.value = selectedColumns.value.filter((k) => available.has(k))
}

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportCanceled = ref(false)
const exportError = ref('')

const canExport = computed(
  () => !!selectedProjectId.value && selectedColumns.value.length > 0 && !exporting.value,
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
    const res = await backend.exportIssuesExcel(profileId.value, buildQuery(false), columns)
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
  <div class="issues">
    <h1>課題抽出</h1>

    <p v-if="mock" class="mock-note">
      Wails ランタイム外で動作中のため、モックデータを表示しています(実データではありません)。
    </p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">読み込み中...</p>

    <p v-else-if="!profileId" class="notice">
      接続先プロファイルが選択されていません。「接続設定」画面でプロファイルを登録・選択してください。
    </p>

    <template v-else>
      <!-- プロジェクト選択 -->
      <section class="panel">
        <h2>プロジェクト</h2>
        <div class="row">
          <label for="i-project">プロジェクト</label>
          <select id="i-project" v-model="selectedProjectId" :disabled="projectsLoading">
            <option v-if="projects.length === 0" :value="0">(プロジェクトがありません)</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ p.name }}({{ p.projectKey }})
            </option>
          </select>
          <button
            :disabled="projectsSyncing || projectsLoading"
            title="プロジェクト一覧を最新化(課題は同期しません)"
            @click="syncProjects"
          >
            {{ projectsSyncing ? 'プロジェクト同期中...' : 'プロジェクト一覧を同期' }}
          </button>
          <span v-if="projectsSyncing" class="spinner" aria-hidden="true"></span>
        </div>

        <p class="hint">
          「プロジェクト一覧を同期」はプロジェクト一覧を最新化(課題は同期しません)。
          課題を取り込むには下の「同期」を実行してください。
        </p>

        <p v-if="projectsWarning" class="notice warn">{{ projectsWarning }}</p>

        <p v-if="selectedProject" class="freshness">
          データ鮮度:
          <template v-if="syncStateUnknown">鮮度を取得できませんでした(ログを確認してください)</template>
          <template v-else-if="selectedProject.lastSyncedAt">
            最終同期 {{ formatDateTime(selectedProject.lastSyncedAt) }}
            ({{ formatElapsed(selectedProject.lastSyncedAt) }})
          </template>
          <template v-else>未同期</template>
        </p>
        <p v-if="neverSynced" class="notice warn">
          このプロジェクトの課題はまだ同期されていません。下の「同期」ボタン(課題の同期)を実行してください
          (「プロジェクト一覧を同期」はプロジェクト一覧の更新のみです)。
        </p>
      </section>

      <!-- 同期(検索の前に実行する想定のため検索条件より上に配置) -->
      <section class="panel">
        <h2>同期</h2>
        <div class="row">
          <label>同期モード</label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="auto" :disabled="syncing" />
            自動(初回はフル同期)
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="full" :disabled="syncing" />
            フル同期
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="incremental" :disabled="syncing" />
            差分同期
          </label>
          <button :disabled="syncing || !selectedProjectId" @click="runSync">
            {{ syncing ? '同期中...' : '同期' }}
          </button>
          <span v-if="syncing" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">
          自動は同期状態から判定します(未同期・長期間未同期ならフル同期)。
          差分同期は前回同期以降の更新のみを取得します。不整合が疑われる場合はフル同期を選んでください。
        </p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <div v-if="syncResult" class="result ok">
          <p class="result-title">{{ syncModeLabel(syncResult.mode) }}が完了しました</p>
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

      <!-- 検索条件 -->
      <section class="panel">
        <h2>検索条件</h2>
        <div class="row">
          <label for="i-keyword">キーワード</label>
          <input
            id="i-keyword"
            v-model="cond.keyword"
            type="text"
            class="wide"
            placeholder="件名 + 詳細の部分一致"
          />
        </div>
        <p class="hint">
          キーワード検索はローカル DB に保存された<strong>件名と詳細</strong>に対する部分一致です。
          コメント・添付ファイル等は対象外で、Backlog サイト上のキーワード検索とは範囲が異なります。
        </p>

        <div class="row">
          <label for="i-updated-from">更新日</label>
          <input id="i-updated-from" v-model="cond.updatedFrom" type="date" />
          <span>〜</span>
          <input v-model="cond.updatedTo" type="date" />
        </div>

        <div class="row">
          <label for="i-created-from">作成日</label>
          <input id="i-created-from" v-model="cond.createdFrom" type="date" />
          <span>〜</span>
          <input v-model="cond.createdTo" type="date" />
        </div>

        <div class="row">
          <label for="i-status">状態</label>
          <select id="i-status" v-model="cond.statusName" :disabled="optionsLoading">
            <option value="">すべて</option>
            <option v-for="s in statusOptions" :key="s" :value="s">{{ s }}</option>
          </select>
          <label for="i-assignee" class="inline-label">担当者</label>
          <select id="i-assignee" v-model="cond.assigneeName" :disabled="optionsLoading">
            <option value="">すべて</option>
            <option v-for="a in assigneeOptions" :key="a" :value="a">{{ a }}</option>
          </select>
        </div>
        <p v-if="!optionsLoading && statusOptions.length === 0 && assigneeOptions.length === 0" class="hint">
          状態・担当者の候補は同期済みの課題から作成されます。同期後に選択できるようになります。
        </p>

        <div class="row buttons">
          <button class="primary" :disabled="searching || !selectedProjectId" @click="search">
            {{ searching ? '検索中...' : '検索' }}
          </button>
          <button :disabled="searching" @click="clearConditions">条件をクリア</button>
          <span v-if="searching" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="searchError" class="error">{{ searchError }}</p>
      </section>

      <!-- 検索結果 -->
      <section v-if="searched" class="panel">
        <h2>検索結果</h2>
        <p class="summary">
          該当 {{ total }} 件
          <span v-if="truncated">(画面には先頭 {{ rows.length }} 件のみ表示)</span>
        </p>
        <p v-if="truncated" class="hint">
          画面表示は {{ PREVIEW_LIMIT }} 件までです。Excel には条件に一致する全 {{ total }} 件が出力されます。
        </p>

        <p v-if="rows.length === 0" class="notice">条件に一致する課題はありませんでした。</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>課題キー</th>
                <th>件名</th>
                <th>状態</th>
                <th>担当者</th>
                <th>種別</th>
                <th>優先度</th>
                <th>作成日</th>
                <th>更新日</th>
                <th>期限</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.issueKey">
                <td class="nowrap">{{ r.issueKey }}</td>
                <td>{{ r.summary }}</td>
                <td class="nowrap">{{ r.statusName }}</td>
                <td class="nowrap">{{ r.assigneeName || '(未設定)' }}</td>
                <td class="nowrap">{{ r.issueTypeName }}</td>
                <td class="nowrap">{{ r.priorityName }}</td>
                <td class="nowrap">{{ formatDateTime(r.created) }}</td>
                <td class="nowrap">{{ formatDateTime(r.updated) }}</td>
                <td class="nowrap">{{ r.dueDate || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- Excel 出力 -->
      <section class="panel">
        <h2>Excel 出力</h2>
        <p class="hint">出力する列を選択してください(現在の検索条件に一致する全件が出力されます)。</p>
        <div class="columns">
          <label v-for="c in FIXED_EXPORT_COLUMNS" :key="c.key" class="checkbox">
            <input v-model="selectedColumns" type="checkbox" :value="c.key" />
            {{ c.label }}
          </label>
        </div>
        <template v-if="customColumns.length > 0">
          <p class="hint">カスタム属性(既定では出力しません)</p>
          <div class="columns">
            <label v-for="c in customColumns" :key="c.key" class="checkbox">
              <input v-model="selectedColumns" type="checkbox" :value="c.key" />
              {{ c.label }}
            </label>
          </div>
        </template>
        <p v-if="customFieldsError" class="hint warn">
          {{ customFieldsError }}
          <button type="button" class="link" @click="loadCustomFields">再試行</button>
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
.issues {
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

.row .inline-label {
  min-width: auto;
  margin-left: 0.75rem;
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

input[type='text'],
input[type='date'],
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
  margin: 0 0 0.75rem;
}

.hint.warn {
  color: #9a6700;
}

/* 文中に置く軽量なアクション(カスタム属性取得の再試行) */
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
