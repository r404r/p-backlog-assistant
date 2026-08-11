<script lang="ts" setup>
// 一括更新・追加画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
//
// 設計書 5 節「入力(一括更新・追加)」の操作フロー:
//   ① テンプレート出力 → ② 記入済み Excel の取り込み(検証 + dry-run)
//   → ③ プレビュー確認 → ④ 実行(進捗・キャンセル) → ⑤ 結果 → ⑥ ジョブ履歴(再開)
//
// この画面は Backlog のデータを変更する唯一の画面のため、
// 「実行前に必ず dry-run プレビューを見せる」「競合は黙って上書きしない」
// 「中断した sending 行は自動再送しない」を UI 上でも徹底する。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  getBackend,
  isMockBackend,
  onBulkProgress,
  type BulkImportResult,
  type BulkJobRow,
  type BulkJobRowDetail,
  type BulkRunResult,
  type MasterItem,
  type Project,
} from '../lib/backend'

const backend = getBackend()
const mock = isMockBackend()

/** 実行時間の目安(設計書 5 節: 1,000 件で 8〜10 分) */
const MINUTES_PER_1000 = 9

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

/** 件数から実行時間の目安を日本語で返す */
function estimateDuration(count: number): string {
  if (count <= 0) return '-'
  const minutes = (count / 1000) * MINUTES_PER_1000
  if (minutes < 1) return '1 分未満'
  return `約 ${Math.ceil(minutes)} 分`
}

const ACTION_LABEL: Record<string, string> = {
  create: '新規追加',
  update: '更新',
  skip: '対象外',
}

/** ジョブ行明細の状態表示(Go 側 job_rows.status と対) */
const ROW_STATUS_LABEL: Record<string, string> = {
  pending: '未処理',
  sending: '送信中',
  done: '完了',
  error: '失敗',
  conflict: '競合',
  skip: '対象外',
}

// ---------------------------------------------------------------------------
// アクティブプロファイル・プロジェクト
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const globalError = ref('')

const projects = ref<Project[]>([])
const selectedProjectId = ref(0)
const projectsLoading = ref(false)

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
  await loadMaster()
}

/**
 * プロジェクトを変えたら、取り込み済みの内容(別プロジェクト向け)は無効になるため破棄する。
 * 取り込み結果を残したままプロジェクトだけ切り替えて実行する事故を防ぐ。
 */
async function onProjectChange() {
  importResult.value = null
  runResult.value = null
  importCanceled.value = false
  exportPath.value = ''
  exportCanceled.value = false
  await loadMaster()
}

// ---------------------------------------------------------------------------
// マスタデータ(既定優先度の選択に使う)
// ---------------------------------------------------------------------------

const priorities = ref<MasterItem[]>([])
const defaultPriorityId = ref(0)
const masterError = ref('')

async function loadMaster() {
  if (!profileId.value || !selectedProjectId.value) {
    priorities.value = []
    defaultPriorityId.value = 0
    return
  }
  masterError.value = ''
  try {
    const m = await backend.getMasterData(profileId.value, selectedProjectId.value)
    priorities.value = m.priorities
    // 既定値は設計書 5 節に合わせて「中」。見つからなければ先頭を選ぶ。
    const middle = m.priorities.find((p) => p.name === '中')
    defaultPriorityId.value = middle?.id ?? m.priorities[0]?.id ?? 0
  } catch (e) {
    priorities.value = []
    defaultPriorityId.value = 0
    masterError.value = `マスタデータの取得に失敗しました: ${errorMessage(e)}`
  }
}

// ---------------------------------------------------------------------------
// ① テンプレート出力
// ---------------------------------------------------------------------------

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportCanceled = ref(false)
const exportError = ref('')

async function exportTemplate() {
  if (!profileId.value || !selectedProjectId.value || exporting.value) return
  exporting.value = true
  exportError.value = ''
  exportPath.value = ''
  exportCanceled.value = false
  try {
    // MVP では条件指定を持たず、対象プロジェクトの全件をテンプレート化する
    const res = await backend.exportBulkTemplate(profileId.value, selectedProjectId.value, {
      projectId: selectedProjectId.value,
    })
    if (!res.path) {
      exportCanceled.value = true
    } else {
      exportPath.value = res.path
      exportRows.value = res.rows
    }
  } catch (e) {
    exportError.value = `テンプレートの出力に失敗しました: ${errorMessage(e)}`
  } finally {
    exporting.value = false
  }
}

// ---------------------------------------------------------------------------
// ② Excel の取り込み(検証 + dry-run)
// ---------------------------------------------------------------------------

const importing = ref(false)
const importError = ref('')
const importCanceled = ref(false)
const importResult = ref<BulkImportResult | null>(null)

const canImport = computed(
  () => !!profileId.value && !!selectedProjectId.value && !importing.value && !running.value,
)

async function importFile() {
  if (!canImport.value) return
  importing.value = true
  importError.value = ''
  importCanceled.value = false
  importResult.value = null
  runResult.value = null
  try {
    const res = await backend.importBulkFile(
      profileId.value,
      selectedProjectId.value,
      defaultPriorityId.value,
    )
    // ファイル選択ダイアログをキャンセルした場合は jobId=0・0 行で返る
    if (!res.jobId && res.totalRows === 0) {
      importCanceled.value = true
    } else {
      importResult.value = res
    }
    await loadJobs()
  } catch (e) {
    importError.value = `Excel の取り込みに失敗しました: ${errorMessage(e)}`
  } finally {
    importing.value = false
  }
}

/** 実行対象(新規追加 + 更新)の件数 */
const targetCount = computed(() => {
  const r = importResult.value
  return r ? r.creates + r.updates : 0
})

/** 取り込み時点で競合警告が付いた行数 */
const conflictWarningCount = computed(
  () => importResult.value?.previews.filter((p) => p.conflictWarning).length ?? 0,
)

// ---------------------------------------------------------------------------
// ④ 実行
// ---------------------------------------------------------------------------

const running = ref(false)
const canceling = ref(false)
const runError = ref('')
const runResult = ref<BulkRunResult | null>(null)
const progress = ref({ processed: 0, total: 0 })

/** 実行確認ダイアログ(Wails の webview では window.confirm を使わず画面内で確認する) */
const confirming = ref(false)
/** 確認中の実行が「競合を上書き」かどうか */
const confirmForce = ref(false)
/** 確認中の実行で sending 行を再送するか(履歴からの再開時のみ true) */
const confirmResendSending = ref(false)
/** 確認中の実行対象ジョブ ID */
const confirmJobId = ref(0)
/** 確認中の実行対象件数(表示用) */
const confirmCount = ref(0)

const canRun = computed(
  () => !!importResult.value?.valid && targetCount.value > 0 && !running.value && !importing.value,
)

const progressPercent = computed(() => {
  const p = progress.value
  if (p.total <= 0) return 0
  return Math.min(100, Math.round((p.processed / p.total) * 100))
})

/** 実行確認を開く(ジョブ ID・件数・オプションを確定させる) */
function askRun(jobId: number, count: number, force: boolean, resendSending: boolean) {
  if (running.value || !jobId) return
  confirmJobId.value = jobId
  confirmCount.value = count
  confirmForce.value = force
  confirmResendSending.value = resendSending
  confirming.value = true
}

function cancelConfirm() {
  confirming.value = false
}

async function confirmRun() {
  confirming.value = false
  const jobId = confirmJobId.value
  if (!jobId || running.value) return
  running.value = true
  canceling.value = false
  runError.value = ''
  runResult.value = null
  progress.value = { processed: 0, total: confirmCount.value }
  try {
    runResult.value = await backend.runBulkJob(
      profileId.value,
      jobId,
      confirmForce.value,
      confirmResendSending.value,
    )
  } catch (e) {
    runError.value = `一括実行に失敗しました: ${errorMessage(e)}`
  } finally {
    running.value = false
    canceling.value = false
    await loadJobs()
  }
}

async function cancelRun() {
  if (!running.value || canceling.value || !confirmJobId.value) return
  canceling.value = true
  try {
    await backend.cancelBulkRun(profileId.value, confirmJobId.value)
  } catch (e) {
    runError.value = `中断の要求に失敗しました: ${errorMessage(e)}`
    canceling.value = false
  }
}

/** 競合分を強制上書きして再実行する */
function rerunWithForce() {
  const jobId = runResult.value?.jobId ?? importResult.value?.jobId ?? 0
  const count = runResult.value?.conflict ?? 0
  askRun(jobId, count > 0 ? count : targetCount.value, true, false)
}

// ---------------------------------------------------------------------------
// ⑥ ジョブ履歴
// ---------------------------------------------------------------------------

const jobs = ref<BulkJobRow[]>([])
const jobsError = ref('')

async function loadJobs() {
  if (!profileId.value) return
  jobsError.value = ''
  try {
    jobs.value = await backend.listBulkJobs(profileId.value)
  } catch (e) {
    jobsError.value = `ジョブ履歴の取得に失敗しました: ${errorMessage(e)}`
  }
  // 展開中の明細は実行・再読込で変わるため、開いたまま最新化する
  if (expandedJobId.value) await loadJobRows(expandedJobId.value)
}

/** 中断された可能性のあるジョブ(送信中のまま残った行がある) */
function hasSending(job: BulkJobRow): boolean {
  return job.sending > 0
}

/**
 * 通常の「再開」で処理できるジョブ(未処理・送信中が残っている)。
 *
 * 競合行は通常の再開では対象にならない(force 指定時のみ再実行される)ため、
 * 競合しか残っていないジョブに「再開」を出すと空振りする。
 * その場合は「競合を上書きして再実行」だけを表示する。
 */
function canResume(job: BulkJobRow): boolean {
  return job.pending > 0 || job.sending > 0
}

/**
 * 履歴から再開する。resendSending は sending 行を再送するかどうか。
 * 競合行は通常の再開では送信されないため件数に含めない。
 */
function resumeJob(job: BulkJobRow, resendSending: boolean) {
  const count = job.pending + (resendSending ? job.sending : 0)
  askRun(job.jobId, count, false, resendSending)
}

/** 履歴から競合行を強制上書きして再実行する */
function forceResumeJob(job: BulkJobRow) {
  askRun(job.jobId, job.conflict, true, false)
}

// --- 行明細の展開表示 -------------------------------------------------------

/** 明細を展開中のジョブ ID(0 なら折りたたみ。同時に 1 件だけ開く) */
const expandedJobId = ref(0)
const jobRowsLoading = ref(false)
const jobRowsError = ref('')
const jobRowDetails = ref<BulkJobRowDetail[]>([])

async function loadJobRows(jobId: number) {
  if (!profileId.value || !jobId) return
  jobRowsError.value = ''
  jobRowsLoading.value = true
  try {
    jobRowDetails.value = await backend.getBulkJobRows(profileId.value, jobId)
  } catch (e) {
    jobRowDetails.value = []
    jobRowsError.value = `行明細の取得に失敗しました: ${errorMessage(e)}`
  } finally {
    jobRowsLoading.value = false
  }
}

/** 明細の表示・非表示を切り替える(表示時に毎回取得して最新状態を出す) */
async function toggleJobRows(job: BulkJobRow) {
  if (expandedJobId.value === job.jobId) {
    expandedJobId.value = 0
    jobRowDetails.value = []
    jobRowsError.value = ''
    return
  }
  expandedJobId.value = job.jobId
  jobRowDetails.value = []
  await loadJobRows(job.jobId)
}

// --- 結果レポート(Excel 出力) ---------------------------------------------

/** 出力中のジョブ ID(0 なら出力していない。ボタンの二重押下防止に使う) */
const resultExportingJobId = ref(0)
const resultExportPath = ref('')
const resultExportRows = ref(0)
const resultExportCanceled = ref(false)
const resultExportError = ref('')
/** 直近に結果レポートを出力したジョブ ID(結果表示の対象) */
const resultExportJobId = ref(0)

async function exportResultExcel(jobId: number) {
  if (!profileId.value || !jobId || resultExportingJobId.value) return
  resultExportingJobId.value = jobId
  resultExportJobId.value = jobId
  resultExportError.value = ''
  resultExportPath.value = ''
  resultExportCanceled.value = false
  try {
    const res = await backend.exportBulkResultExcel(profileId.value, jobId)
    // 保存ダイアログをキャンセルした場合は path が空文字で返る
    if (!res.path) {
      resultExportCanceled.value = true
    } else {
      resultExportPath.value = res.path
      resultExportRows.value = res.rows
    }
  } catch (e) {
    resultExportError.value = `結果レポートの出力に失敗しました: ${errorMessage(e)}`
  } finally {
    resultExportingJobId.value = 0
  }
}

// ---------------------------------------------------------------------------
// 進捗イベント購読・初期化
// ---------------------------------------------------------------------------

let unsubscribeProgress: (() => void) | null = null

onMounted(async () => {
  unsubscribeProgress = onBulkProgress((p) => {
    // 実行中のジョブ以外のイベント(前回実行の残り等)は無視する
    if (p.jobId !== confirmJobId.value) return
    progress.value = { processed: p.processed, total: p.total || progress.value.total }
  })
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    globalError.value = `接続先プロファイルの取得に失敗しました: ${errorMessage(e)}`
  } finally {
    initializing.value = false
  }
  if (profileId.value) {
    await loadProjects()
    await loadJobs()
  }
})

onUnmounted(() => {
  if (unsubscribeProgress) unsubscribeProgress()
  unsubscribeProgress = null
})
</script>

<template>
  <div class="bulk">
    <h1>一括更新・追加</h1>

    <!-- 書き込み操作である旨の注意(この画面だけが Backlog を変更する) -->
    <p class="danger-note">
      この機能は Backlog のデータを変更します。まずテスト用プロジェクトでの試行を推奨します。
    </p>

    <p v-if="mock" class="mock-note">
      Wails ランタイム外で動作中のため、モックデータを表示しています(実データではありません)。
      実際の Backlog への書き込みは行われません。
    </p>

    <section class="panel flow">
      <h2>操作フロー</h2>
      <ol>
        <li>プロジェクトを選び、テンプレート Excel を出力する</li>
        <li>Excel に記入し、「Excel を取り込む」で読み込む(この時点では書き込まれません)</li>
        <li>検証エラーと dry-run プレビューで、変更内容を確認する</li>
        <li>「実行」で Backlog へ書き込む(進捗表示・中断可)</li>
        <li>結果サマリを確認する(競合した課題は上書きされません)</li>
        <li>中断した場合はジョブ履歴から再開する</li>
      </ol>
      <p class="hint">
        Excel の記入ルール(空欄 = 変更しない、クリアは #CLEAR#、issueKey 空欄 = 新規追加)は、
        テンプレートの「記入方法」シートに記載しています。
      </p>
      <p class="hint">
        カスタム属性は「属性:定義名」列に記入します(日付は yyyy-MM-dd、複数リスト・チェックボックスは選択肢名をカンマ区切り)。
        選択肢に無い値(「その他」の直接入力)は現在未対応です。
      </p>
    </section>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">読み込み中...</p>

    <p v-else-if="!profileId" class="notice">
      接続先プロファイルが選択されていません。「接続設定」画面でプロファイルを登録・選択してください。
    </p>

    <template v-else>
      <!-- ① プロジェクト選択・テンプレート出力 -->
      <section class="panel">
        <h2>① テンプレート出力</h2>
        <div class="row">
          <label for="b-project">プロジェクト</label>
          <select
            id="b-project"
            v-model.number="selectedProjectId"
            :disabled="projectsLoading || running"
            @change="onProjectChange"
          >
            <option v-if="projects.length === 0" :value="0">(プロジェクトがありません)</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ p.name }}({{ p.projectKey }})
            </option>
          </select>
          <button :disabled="projectsLoading || running" @click="loadProjects">再読込</button>
        </div>
        <p class="hint">
          対象プロジェクトの課題を全件テンプレート化します(条件による絞り込みは行いません)。
          テンプレートにはプロジェクトが固定で埋め込まれるため、行ごとにプロジェクトは変えられません。
          種別・状態・優先度・担当者は、名前列のドロップダウンで編集できます(名前列が空の行は ID 列の値を使います)。
          名前列に値がある行は常に名前列が優先され、食い違う ID 列は無視して警告を表示します。
        </p>

        <div class="row buttons">
          <button
            class="primary"
            :disabled="!selectedProjectId || exporting || running"
            @click="exportTemplate"
          >
            {{ exporting ? '出力中...' : 'テンプレート出力' }}
          </button>
          <span v-if="exporting" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="exportError" class="error">{{ exportError }}</p>
        <p v-if="exportCanceled" class="notice">テンプレート出力はキャンセルされました。</p>
        <div v-if="exportPath" class="result ok">
          <p class="result-title">テンプレートを出力しました({{ exportRows }} 件)</p>
          <p class="path">{{ exportPath }}</p>
        </div>
      </section>

      <!-- ② Excel の取り込み -->
      <section class="panel">
        <h2>② Excel を取り込む</h2>
        <div class="row">
          <label for="b-priority">既定の優先度</label>
          <select id="b-priority" v-model.number="defaultPriorityId" :disabled="importing || running">
            <option v-if="priorities.length === 0" :value="0">(取得できていません)</option>
            <option v-for="p in priorities" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <p class="hint">
          優先度が未入力の新規追加行に適用します(このプロジェクトの取り込みでのみ有効です)。
        </p>
        <p v-if="masterError" class="error">{{ masterError }}</p>

        <div class="row buttons">
          <button class="primary" :disabled="!canImport" @click="importFile">
            {{ importing ? '取り込み中...' : 'Excel を取り込む' }}
          </button>
          <span v-if="importing" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">
          ファイル選択ダイアログで記入済みの Excel を選びます。取り込みは検証と dry-run のみで、
          この操作では Backlog に書き込みません。
        </p>
        <p v-if="importError" class="error">{{ importError }}</p>
        <p v-if="importCanceled" class="notice">Excel の取り込みはキャンセルされました。</p>
      </section>

      <!-- ③ 検証結果・dry-run プレビュー -->
      <section v-if="importResult" class="panel">
        <h2>③ 検証結果・変更プレビュー(dry-run)</h2>
        <p class="summary">
          取り込み {{ importResult.totalRows }} 行 / 新規追加 {{ importResult.creates }} 件 /
          更新 {{ importResult.updates }} 件 / 対象外 {{ importResult.skips }} 件
        </p>

        <div v-if="importResult.warnings.length > 0" class="notice warn">
          <p class="result-title">取り込み時の警告</p>
          <ul>
            <li v-for="(w, i) in importResult.warnings" :key="i">{{ w }}</li>
          </ul>
        </div>
        <div v-if="importResult.errors.length > 0" class="result ng">
          <p class="result-title">検証エラー {{ importResult.errors.length }} 件(修正して取り込み直してください)</p>
          <ul>
            <li v-for="(e, i) in importResult.errors" :key="i">{{ e.rowNo }} 行目: {{ e.message }}</li>
          </ul>
        </div>
        <p v-else class="notice ok-note">検証エラーはありません。内容を確認して実行してください。</p>

        <p v-if="conflictWarningCount > 0" class="notice warn">
          {{ conflictWarningCount }} 件の課題は、テンプレート出力後にリモートで更新されています。
          実行時に競合として除外される可能性があります。
        </p>

        <div v-if="importResult.previews.length > 0" class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>行</th>
                <th>区分</th>
                <th>課題キー</th>
                <th>件名</th>
                <th>変更内容</th>
                <th>競合</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in importResult.previews" :key="p.rowNo" :class="{ skip: p.action === 'skip' }">
                <td class="nowrap">{{ p.rowNo }}</td>
                <td class="nowrap">
                  <span class="badge" :class="p.action">{{ ACTION_LABEL[p.action] ?? p.action }}</span>
                </td>
                <td class="nowrap">{{ p.issueKey || '(新規)' }}</td>
                <td>{{ p.summary }}</td>
                <td>
                  <ul v-if="p.changes.length > 0" class="changes">
                    <li v-for="(c, i) in p.changes" :key="i">{{ c }}</li>
                  </ul>
                  <span v-else>-</span>
                </td>
                <td class="nowrap">
                  <span v-if="p.conflictWarning" class="badge warn">要確認</span>
                  <span v-else>-</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- ④ 実行 -->
      <section v-if="importResult" class="panel">
        <h2>④ 実行</h2>
        <p class="hint">
          1 件ずつ Backlog へ書き込みます(バルク API が無いため)。
          目安: 1,000 件で 8〜10 分。今回の対象 {{ targetCount }} 件は {{ estimateDuration(targetCount) }}です。
        </p>

        <div class="row buttons">
          <button class="primary" :disabled="!canRun" @click="askRun(importResult.jobId, targetCount, false, false)">
            {{ running ? '実行中...' : '実行' }}
          </button>
          <button v-if="running" :disabled="canceling" @click="cancelRun">
            {{ canceling ? '中断要求中...' : 'キャンセル' }}
          </button>
        </div>
        <p v-if="!importResult.valid" class="hint warn">
          検証エラーがあるため実行できません。Excel を修正して取り込み直してください。
        </p>

        <!-- 実行確認 -->
        <div v-if="confirming" class="confirm">
          <p class="result-title">
            {{ confirmCount }} 件を Backlog に書き込みます。よろしいですか?
          </p>
          <p v-if="confirmForce" class="warn-text">
            リモートの変更を上書きします。競合した課題に対してリモートで行われた変更は失われます。
          </p>
          <p v-if="confirmResendSending" class="warn-text">
            作成済みの課題を確認してから再送します(二重作成は自動で防止されます)。
          </p>
          <p class="hint">所要時間の目安: {{ estimateDuration(confirmCount) }}</p>
          <div class="row buttons">
            <button class="primary" @click="confirmRun">書き込む</button>
            <button @click="cancelConfirm">やめる</button>
          </div>
        </div>

        <!-- 進捗 -->
        <div v-if="running" class="progress-box">
          <div class="progress">
            <div class="progress-bar" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <p class="hint">
            {{ progress.processed }} / {{ progress.total }} 件({{ progressPercent }}%)
            <span v-if="canceling">— 中断要求済み。送信中の 1 件を終えてから停止します。</span>
          </p>
        </div>

        <p v-if="runError" class="error">{{ runError }}</p>
      </section>

      <!-- ⑤ 結果サマリ -->
      <section v-if="runResult" class="panel">
        <h2>⑤ 実行結果</h2>
        <div class="result" :class="runResult.failed > 0 || runResult.conflict > 0 ? 'ng' : 'ok'">
          <p class="result-title">一括実行が終了しました</p>
          <ul>
            <li>成功: {{ runResult.done }} 件</li>
            <li>失敗: {{ runResult.failed }} 件</li>
            <li>競合: {{ runResult.conflict }} 件</li>
            <li>スキップ: {{ runResult.skipped }} 件(取り込み時の変更なし行)</li>
            <li>所要時間: {{ (runResult.durationMs / 1000).toFixed(1) }} 秒</li>
          </ul>
          <p class="hint">
            スキップは「取り込み時に変更なしと判定した行」の件数です。
            キャンセル・中断で処理しなかった行はここには含まれません。
            未処理の件数は下の警告と、ジョブ履歴の「未処理」「送信中」で確認してください。
          </p>
          <div v-if="runResult.warnings.length > 0" class="warnings">
            <p class="result-title">警告</p>
            <ul>
              <li v-for="(w, i) in runResult.warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </div>

        <div v-if="runResult.conflict > 0" class="notice warn conflict">
          <p>
            競合 {{ runResult.conflict }} 件: リモートが更新されています。
            最新を確認のうえ、強制上書きは再実行で「競合を上書き」を選択してください。
          </p>
          <div class="row buttons">
            <button :disabled="running" @click="rerunWithForce">競合を上書きして再実行</button>
          </div>
        </div>

        <!-- 結果レポート(行ごとの成否を Excel で確認する) -->
        <div class="row buttons">
          <button
            :disabled="running || resultExportingJobId !== 0"
            @click="exportResultExcel(runResult.jobId)"
          >
            {{ resultExportingJobId === runResult.jobId ? '出力中...' : '結果を Excel 出力' }}
          </button>
          <span v-if="resultExportingJobId === runResult.jobId" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">
          行ごとの処理結果(完了・失敗・競合・エラー内容)を Excel に出力します。
        </p>
      </section>

      <!-- 結果レポート出力の状態(実行結果・ジョブ履歴の双方の出力で共用する) -->
      <section
        v-if="resultExportError || resultExportCanceled || resultExportPath"
        class="panel"
      >
        <h2>結果レポートの出力</h2>
        <p v-if="resultExportError" class="error">{{ resultExportError }}</p>
        <p v-if="resultExportCanceled" class="notice">結果レポートの出力はキャンセルされました。</p>
        <div v-if="resultExportPath" class="result ok">
          <p class="result-title">
            ジョブ #{{ resultExportJobId }} の結果レポートを出力しました({{ resultExportRows }} 行)
          </p>
          <p class="path">{{ resultExportPath }}</p>
        </div>
      </section>

      <!-- ⑥ ジョブ履歴 -->
      <section class="panel">
        <h2>⑥ ジョブ履歴</h2>
        <div class="row buttons">
          <button :disabled="running" @click="loadJobs">履歴を更新</button>
        </div>
        <p v-if="jobsError" class="error">{{ jobsError }}</p>
        <p v-if="jobs.length === 0" class="notice">実行履歴はまだありません。</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>ジョブ</th>
                <th>作成日時</th>
                <th>種別</th>
                <th>状態</th>
                <th>対象</th>
                <th>完了</th>
                <th>失敗</th>
                <th>競合</th>
                <th>未処理</th>
                <th>送信中</th>
                <th>スキップ</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="j in jobs" :key="j.jobId">
                <tr>
                  <td class="nowrap">#{{ j.jobId }}</td>
                  <td class="nowrap">{{ formatDateTime(j.createdAt) }}</td>
                  <td class="nowrap">{{ j.kind }}</td>
                  <td class="nowrap">{{ j.status }}</td>
                  <td class="num">{{ j.total }}</td>
                  <td class="num">{{ j.done }}</td>
                  <td class="num">{{ j.failed }}</td>
                  <td class="num">{{ j.conflict }}</td>
                  <td class="num">{{ j.pending }}</td>
                  <td class="num">{{ j.sending }}</td>
                  <td class="num">{{ j.skipped }}</td>
                  <td class="nowrap actions">
                    <button v-if="canResume(j)" :disabled="running" @click="resumeJob(j, false)">
                      再開
                    </button>
                    <button
                      v-if="j.conflict > 0"
                      :disabled="running"
                      @click="forceResumeJob(j)"
                    >
                      競合を上書きして再実行
                    </button>
                    <button :disabled="running" @click="toggleJobRows(j)">
                      {{ expandedJobId === j.jobId ? '明細を閉じる' : '明細を表示' }}
                    </button>
                    <button
                      :disabled="running || resultExportingJobId !== 0"
                      @click="exportResultExcel(j.jobId)"
                    >
                      {{ resultExportingJobId === j.jobId ? '出力中...' : '結果を Excel 出力' }}
                    </button>
                  </td>
                </tr>

                <!-- 成否不明(sending が残った)行の説明と再送の導線 -->
                <tr v-if="hasSending(j)">
                  <td colspan="12" class="sending-note">
                    送信結果を確認できなかった行があります({{ j.sending }} 件)。
                    再開すると確認のうえ処理されます(作成済みの課題と突合するため、二重作成は自動で防止されます)。
                    <button class="inline" :disabled="running" @click="resumeJob(j, true)">
                      送信中の行も再送して再開
                    </button>
                  </td>
                </tr>

                <!-- 行明細(展開表示) -->
                <tr v-if="expandedJobId === j.jobId">
                  <td colspan="12" class="detail-cell">
                    <p v-if="jobRowsLoading" class="hint">行明細を読み込み中...</p>
                    <p v-else-if="jobRowsError" class="error">{{ jobRowsError }}</p>
                    <p v-else-if="jobRowDetails.length === 0" class="hint">
                      このジョブの行明細はありません。
                    </p>
                    <table v-else class="detail-table">
                      <thead>
                        <tr>
                          <th>行</th>
                          <th>課題キー</th>
                          <th>状態</th>
                          <th>エラー</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-for="r in jobRowDetails" :key="r.rowNo">
                          <td class="nowrap">{{ r.rowNo }}</td>
                          <td class="nowrap">
                            <template v-if="r.issueKey">{{ r.issueKey }}</template>
                            <template v-else-if="r.resultIssueId > 0">
                              (新規)作成済み ID: {{ r.resultIssueId }}
                            </template>
                            <template v-else>(新規)</template>
                          </td>
                          <td class="nowrap">
                            <span class="badge" :class="r.status">
                              {{ ROW_STATUS_LABEL[r.status] ?? r.status }}
                            </span>
                          </td>
                          <td>{{ r.error || '-' }}</td>
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
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.bulk {
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

.danger-note {
  background: #fdeceb;
  border: 1px solid #e2a09b;
  border-radius: 4px;
  padding: 0.6rem 0.75rem;
  font-size: 0.9rem;
  font-weight: 600;
  color: #8a2420;
  margin: 0 0 0.75rem;
}

.mock-note {
  background: #fff8e1;
  border: 1px solid #e6c96a;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}

.flow ol {
  margin: 0;
  padding-left: 1.3rem;
  font-size: 0.9rem;
}

.flow li {
  margin-bottom: 0.2rem;
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

.notice.ok-note {
  background: #e9f5ec;
  border-color: #7fbf90;
  color: #1a7f37;
}

.notice.conflict {
  margin-top: 0.75rem;
}

.notice.conflict p {
  margin: 0;
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
  min-width: 8rem;
}

.row.buttons {
  margin-top: 0.75rem;
  margin-bottom: 0;
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

.hint {
  font-size: 0.8rem;
  color: #57606a;
  margin: 0.5rem 0 0.75rem;
}

.hint.warn {
  color: #9a6700;
}

.warn-text {
  font-size: 0.85rem;
  color: #9a6700;
  margin: 0 0 0.4rem;
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

button.inline {
  padding: 0.2rem 0.6rem;
  font-size: 0.8rem;
  margin-left: 0.4rem;
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

.result.ng {
  background: #fdeceb;
  border: 1px solid #e2a09b;
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

.confirm {
  margin-top: 0.75rem;
  border: 1px solid #e6c96a;
  background: #fff8e1;
  border-radius: 4px;
  padding: 0.75rem 0.9rem;
  font-size: 0.9rem;
}

.progress-box {
  margin-top: 0.75rem;
}

.progress {
  height: 10px;
  border-radius: 5px;
  background: #eaeef2;
  overflow: hidden;
}

.progress-bar {
  height: 100%;
  background: #0b5cad;
  transition: width 0.2s linear;
}

.table-wrap {
  max-height: 420px;
  overflow: auto;
  border: 1px solid #d0d7de;
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

td.num {
  text-align: right;
}

tr.skip {
  color: #8c959f;
}

.nowrap {
  white-space: nowrap;
}

.changes {
  margin: 0;
  padding-left: 1.1rem;
}

.badge {
  display: inline-block;
  padding: 0.05rem 0.4rem;
  border-radius: 999px;
  font-size: 0.75rem;
  border: 1px solid #d0d7de;
  background: #f6f8fa;
}

.badge.create {
  background: #e9f5ec;
  border-color: #7fbf90;
  color: #1a7f37;
}

.badge.update {
  background: #ddebf7;
  border-color: #7fa8cf;
  color: #0b5cad;
}

.badge.warn {
  background: #fff8e1;
  border-color: #e6c96a;
  color: #9a6700;
}

/* 行明細の状態バッジ(pending / sending / done / error / conflict / skip) */
.badge.done {
  background: #e9f5ec;
  border-color: #7fbf90;
  color: #1a7f37;
}

.badge.sending {
  background: #ddebf7;
  border-color: #7fa8cf;
  color: #0b5cad;
}

.badge.error {
  background: #fdeceb;
  border-color: #e2a09b;
  color: #8a2420;
}

.badge.conflict {
  background: #fff8e1;
  border-color: #e6c96a;
  color: #9a6700;
}

.badge.pending,
.badge.skip {
  background: #f6f8fa;
  border-color: #d0d7de;
  color: #57606a;
}

.sending-note {
  background: #fff8e1;
  color: #9a6700;
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
  background: #f6f8fa;
  padding: 0.5rem 0.75rem;
}

.detail-table {
  background: #fff;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  font-size: 0.8rem;
}

/* 親テーブルのヘッダ固定は入れ子の明細テーブルには適用しない */
.detail-table th {
  position: static;
}

.detail-cell .hint,
.detail-cell .error {
  margin: 0;
}
</style>
