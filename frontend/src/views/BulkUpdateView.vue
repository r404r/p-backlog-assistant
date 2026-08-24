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
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
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
import { translateAction, translateRowStatus, type TranslateFn } from '../lib/enumLabels'
import { estimateBulkSeconds, estimateBulkSecondsRange } from '../lib/bulkEstimate'
import { errorMessage, formatDateTime } from '../lib/format'
import { buildIssueQuery, newIssueConditions, resetIssueConditions } from '../lib/issueQuery'
import { useMessage } from '../lib/message'
import { issueSyncRunning } from '../lib/syncState'
import {
  resolveProjectSelection,
  restoreProjectSelection,
  selectedProjectId,
  useProjectSelectionGuard,
} from '../lib/projectSelection'

const backend = getBackend()
const mock = isMockBackend()

const { t } = useI18n()

/**
 * enumLabels の対応表へ渡す翻訳関数。vue-i18n の `t` は多数のオーバーロードを
 * 持つため、対応表側が期待する単純な形(TranslateFn)へ包んで渡す。
 */
const translate: TranslateFn = (key, named) => (named ? t(key, named) : t(key))

/**
 * 破棄済み・プロファイル切替後の画面が、後から届いた古い応答で
 * 共有のプロジェクト選択を書き換えてしまうのを防ぐガード(高 1)。
 */
const selectionGuard = useProjectSelectionGuard()

/** 秒数を利用者向けの切り上げ分数へ整形する。 */
function formatEstimate(seconds: number): string {
  if (seconds <= 0) return '-'
  if (seconds < 60) return t('bulk.estimate.lessThanMinute')
  return t('bulk.estimate.minutes', { minutes: Math.ceil(seconds / 60) })
}

/** 取込結果は新規／更新の内訳が分かるため、API呼出回数から見積もる。 */
function estimateImportedDuration(result: BulkImportResult): string {
  return formatEstimate(estimateBulkSeconds(result.creates, result.updates))
}

/** 履歴再開は内訳を持たないため、全件新規〜全件更新の範囲で示す。 */
function estimateUnknownDuration(count: number): string {
  const range = estimateBulkSecondsRange(count)
  if (range.min <= 0) return '-'
  return t('bulk.estimate.range', {
    min: Math.max(1, Math.ceil(range.min / 60)),
    max: Math.max(1, Math.ceil(range.max / 60)),
  })
}

// ---------------------------------------------------------------------------
// アクティブプロファイル・プロジェクト
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const [globalError, setGlobalError] = useMessage(t)

const projects = ref<Project[]>([])
// プロジェクト選択は画面をまたいで共有する(projectSelection モジュールが保持し、
// プロファイルごとに localStorage へ保存する)
const projectsLoading = ref(false)

async function loadProjects() {
  if (!profileId.value) return
  const token = selectionGuard.begin()
  projectsLoading.value = true
  setGlobalError(null)
  try {
    const list = await backend.listProjects(profileId.value)
    // 画面が破棄済み、またはプロファイルが切り替わっていたら反映しない
    // (古い応答で共有のプロジェクト選択を書き換えないため)
    if (!selectionGuard.isCurrent(token)) return
    projects.value = list
    // 復元した(または選択中の)プロジェクトが一覧に無ければ先頭へフォールバックする
    selectedProjectId.value = resolveProjectSelection(projects.value, selectedProjectId.value)
  } catch (e) {
    setGlobalError('bulk.error.loadProjects', { message: errorMessage(e) })
  } finally {
    projectsLoading.value = false
  }
}

/** 選択中プロジェクトに紐づくデータ(マスタ・絞り込み候補)を読み込む */
async function loadProjectData() {
  await loadMaster()
  await loadFilterOptions()
}

/**
 * 「再読込」ボタン: プロジェクト一覧と、選択中プロジェクトに紐づくデータを取り直す
 * (マスタ・候補の取得に失敗したときの再試行の導線でもある)。
 *
 * 選択が変わった場合は watch(selectedProjectId) が読み直すため、ここでは読まない
 * (二重に取りに行かないようにする)。
 */
async function reloadProjects() {
  const before = selectedProjectId.value
  await loadProjects()
  if (selectedProjectId.value === before) await loadProjectData()
}

/**
 * 初期化(プロジェクト選択の復元 → 一覧取得 → 選択の解決)が完了したか
 * (IssuesView の同名の仕組みと同じ理由)。
 *
 * 初期化中の選択は「保存値 → 一覧に無ければ先頭」と 2 段階で動きうるため、
 * その途中の値で watch を走らせるとマスタ・候補を二重に取りに行く。
 * 初期化中の変化は watch では扱わず、選択が確定してから onMounted で 1 回読む。
 */
let selectionInitialized = false

/**
 * プロジェクトの選択が変わったら、取り込み済みの内容(別プロジェクト向け)は
 * 無効になるため破棄する。取り込み結果を残したままプロジェクトだけ切り替えて
 * 実行する事故を防ぐ。
 *
 * テンプレート出力の検索条件も、状態・担当者の候補が別プロジェクトのものになるため
 * 初期化する(前のプロジェクトで選んだ状態名のまま出力して 0 件になる事故を防ぐ)。
 *
 * セレクタの @change ではなく選択そのものを watch する(1 回目 中 1)。
 * 参加解除等で選択中のプロジェクトが一覧から消え、resolveProjectSelection が
 * 別のプロジェクトへ自動フォールバックした場合、@change は発火しないため
 * 前のプロジェクト向けの条件・取り込み結果が残ってしまう。
 *
 * TDD 例外(GUI): 画面の結線のためフロントのテスト基盤では固定できず、
 * 手動確認で担保する(選択の解決規則そのものは projectSelection.test.ts が固定)。
 */
watch(selectedProjectId, () => {
  if (!selectionInitialized) return
  importResult.value = null
  runResult.value = null
  importCanceled.value = false
  exportPath.value = ''
  exportCanceled.value = false
  // 実行確認も破棄する。残すと、別プロジェクトの取り込み後に旧ジョブの確認が
  // 再表示され、旧プロジェクトへ書き込めてしまう
  confirming.value = false
  confirmJobId.value = 0
  resetIssueConditions(cond)
  void loadProjectData()
})

// ---------------------------------------------------------------------------
// マスタデータ(既定優先度の選択に使う)
// ---------------------------------------------------------------------------

const priorities = ref<MasterItem[]>([])
const defaultPriorityId = ref(0)
const [masterError, setMasterError] = useMessage(t)

/**
 * loadMaster の世代番号。ガードのトークンはプロファイル単位のため、
 * 同一プロファイル内でプロジェクトを A→B と切り替えた場合の古い応答を弾けない(中 1)。
 * 「最後に開始した要求」の応答だけを反映する。
 */
let masterRequestSeq = 0

async function loadMaster() {
  const seq = ++masterRequestSeq
  const token = selectionGuard.begin()
  if (!profileId.value || !selectedProjectId.value) {
    priorities.value = []
    defaultPriorityId.value = 0
    return
  }
  setMasterError(null)
  try {
    const m = await backend.getMasterData(profileId.value, selectedProjectId.value)
    // 破棄済み・プロファイル切替後、または後発の要求がある場合は反映しない
    if (!selectionGuard.isCurrent(token) || seq !== masterRequestSeq) return
    priorities.value = m.priorities
    // 既定値は設計書 5 節に合わせて「中」。見つからなければ先頭を選ぶ。
    const middle = m.priorities.find((p) => p.name === '中')
    defaultPriorityId.value = middle?.id ?? m.priorities[0]?.id ?? 0
  } catch (e) {
    if (!selectionGuard.isCurrent(token) || seq !== masterRequestSeq) return
    priorities.value = []
    defaultPriorityId.value = 0
    setMasterError('bulk.error.loadMaster', { message: errorMessage(e) })
  }
}

// ---------------------------------------------------------------------------
// ① テンプレート出力(検索条件・出力)
// ---------------------------------------------------------------------------

/**
 * テンプレートに載せる課題の絞り込み条件(空欄なら全件)。
 *
 * 条件の形と IssueQuery への変換は課題抽出(IssuesView)と共通のものを使う
 * (lib/issueQuery)。カスタム属性での絞り込みは今回の対象外
 * (必要になったら IssuesView と同じ形で追加する)。
 */
const cond = reactive(newIssueConditions())

const statusOptions = ref<string[]>([])
const assigneeOptions = ref<string[]>([])
const optionsLoading = ref(false)
/** 絞り込み候補の取得に失敗した場合の説明(空 = 正常。出力自体は行える) */
const [optionsError, setOptionsError] = useMessage(t)

/**
 * loadFilterOptions の世代番号(loadMaster の masterRequestSeq と同じ理由)。
 * ガードのトークンはプロファイル単位のため、同一プロファイル内で
 * プロジェクトを A→B と切り替えた場合の古い応答を弾けない。
 */
let filterOptionsRequestSeq = 0

/** 状態・担当者の候補を、同期済みのローカルデータから読み込む */
async function loadFilterOptions() {
  const seq = ++filterOptionsRequestSeq
  const token = selectionGuard.begin()
  statusOptions.value = []
  assigneeOptions.value = []
  setOptionsError(null) // 前回の失敗表示を残さない(再取得のたびに出し直す)
  if (!profileId.value || !selectedProjectId.value) {
    // 世代を進めた後の早期 return。最新要求であるこの経路で読込中表示を下ろす
    if (seq === filterOptionsRequestSeq) optionsLoading.value = false
    return
  }
  optionsLoading.value = true
  try {
    const opts = await backend.listFilterOptions(profileId.value, selectedProjectId.value)
    // 破棄済み・プロファイル切替後、または後発の要求がある場合は反映しない
    if (!selectionGuard.isCurrent(token) || seq !== filterOptionsRequestSeq) return
    statusOptions.value = opts.statuses
    assigneeOptions.value = opts.assignees
    // 選択済みの値が候補に無くなった場合は「すべて」へ戻す
    if (cond.statusName && !opts.statuses.includes(cond.statusName)) cond.statusName = ''
    if (cond.assigneeName && !opts.assignees.includes(cond.assigneeName)) cond.assigneeName = ''
  } catch (e) {
    if (!selectionGuard.isCurrent(token) || seq !== filterOptionsRequestSeq) return
    setOptionsError('bulk.error.loadFilterOptions', { message: errorMessage(e) })
  } finally {
    // 読込中表示は最新の要求だけが下ろす(古い応答が新しい要求の表示を消さないため)
    if (seq === filterOptionsRequestSeq) optionsLoading.value = false
  }
}

/** 条件をすべて未入力に戻す */
function clearConditions() {
  resetIssueConditions(cond)
}

/**
 * 検索条件の入力を固定するか。
 *
 * 出力中は条件が変わっても既に走っている出力には反映されないため固定する。
 * 実行中・課題同期中はそもそもテンプレート出力できない(exportTemplate のコメント参照)。
 */
const conditionsLocked = computed(() => exporting.value || running.value || issueSyncRunning.value)

/** 何らかの条件を指定しているか(「全件を出力します」の案内の出し分けに使う) */
const hasConditions = computed(
  () =>
    !!cond.keyword.trim() ||
    !!cond.updatedFrom ||
    !!cond.updatedTo ||
    !!cond.createdFrom ||
    !!cond.createdTo ||
    !!cond.statusName ||
    !!cond.assigneeName,
)

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportCanceled = ref(false)
const [exportError, setExportError] = useMessage(t)

/**
 * 課題同期中はテンプレート出力・取り込み・実行を行わない(R10)。
 *
 * - テンプレート出力: 読み取りトランザクションを保持したまま対象プロジェクトの
 *   課題を走査する(app.ExportBulkTemplate → service.IterateIssues)。
 *   同期の途中で走らせると、取り込み済みの課題だけが載った不完全なテンプレートが
 *   「条件に一致する全件」として保存されてしまう。単一 DB 接続(SetMaxOpenConns(1))の
 *   奪い合いで双方が長時間待たされる問題も課題抽出の Excel 出力と同じ。
 * - 取り込み(dry-run)・実行: どちらも Go 側で同期と直列化される
 *   (service.ImportBulkFile / RunBulkJob が syncMu を取る)。同期中に呼ぶと
 *   ロック待ちで同期が終わるまで画面が固まったように見える。加えて dry-run の
 *   現在値・競合判定はローカル DB を基準にするため、中途データでの判定になる。
 *
 * 履歴の閲覧・結果 Excel 出力は jobs / job_rows しか読まず短時間で終わるため、
 * 同期中でも許可する(明細の表示を止めると実行中の状況が確認できなくなるため)。
 */
async function exportTemplate() {
  if (!profileId.value || !selectedProjectId.value || exporting.value) return
  if (issueSyncRunning.value) return
  exporting.value = true
  setExportError(null)
  exportPath.value = ''
  exportCanceled.value = false
  try {
    // 検索条件に一致した課題だけをテンプレート化する(条件が空なら全件)。
    // 条件の解釈・検証は Go 側(store.ValidateIssueFilter)が行う
    const res = await backend.exportBulkTemplate(
      profileId.value,
      selectedProjectId.value,
      buildIssueQuery(selectedProjectId.value, cond),
    )
    if (!res.path) {
      exportCanceled.value = true
    } else {
      exportPath.value = res.path
      exportRows.value = res.rows
    }
  } catch (e) {
    setExportError('bulk.error.exportTemplate', { message: errorMessage(e) })
  } finally {
    exporting.value = false
  }
}

// ---------------------------------------------------------------------------
// ② Excel の取り込み(検証 + dry-run)
// ---------------------------------------------------------------------------

const importing = ref(false)
const [importError, setImportError] = useMessage(t)
const importCanceled = ref(false)
const importResult = ref<BulkImportResult | null>(null)

const canImport = computed(
  () =>
    !!profileId.value &&
    !!selectedProjectId.value &&
    !importing.value &&
    !running.value &&
    // 課題同期中は取り込まない(exportTemplate のコメント参照)
    !issueSyncRunning.value,
)

async function importFile() {
  if (!canImport.value) return
  importing.value = true
  setImportError(null)
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
    setImportError('bulk.error.import', { message: errorMessage(e) })
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

/**
 * 取り込み集計の 1 行(「取り込み N 行 / 新規追加 N 件 / …」)。
 *
 * 処理区分の表示名は行データからは取れない(0 件でも見出しは出す)ため、
 * 生の機械値 'create' / 'update' / 'skip' を enumLabels で翻訳して組み立てる
 * (Go が返す actionLabel は表示に使わない。設計 §3.1)。
 */
const summaryText = computed(() => {
  const r = importResult.value
  if (!r) return ''
  return t('bulk.step3.summary', {
    total: r.totalRows,
    createLabel: translateAction(translate, 'create'),
    creates: r.creates,
    updateLabel: translateAction(translate, 'update'),
    updates: r.updates,
    skipLabel: translateAction(translate, 'skip'),
    skips: r.skips,
  })
})

// ---------------------------------------------------------------------------
// ④ 実行
// ---------------------------------------------------------------------------

const running = ref(false)

/**
 * プロジェクト選択(①)を固定するか(R10)。
 *
 * この画面は課題同期を開始しないが、プロジェクト選択は 3 画面で共有しているため、
 * 課題抽出・同期状態の画面で始めた同期の最中にここで切り替えられると、
 * 同期の完了処理が別プロジェクトに作用してしまう。共有状態(syncState)を見て
 * セレクタを固定する(課題同期中に抑止する操作の範囲は exportTemplate のコメント参照)。
 */
const selectionLocked = computed(
  () => projectsLoading.value || running.value || issueSyncRunning.value,
)

const canceling = ref(false)
const [runError, setRunError] = useMessage(t)
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
/** 確認を開いた時点の所要時間見積り(初回は内訳あり、履歴再開は範囲) */
const confirmEstimate = ref('')

const canRun = computed(
  () =>
    !!importResult.value?.valid &&
    targetCount.value > 0 &&
    !running.value &&
    !importing.value &&
    // 課題同期中は実行しない(exportTemplate のコメント参照)
    !issueSyncRunning.value,
)

const progressPercent = computed(() => {
  const p = progress.value
  if (p.total <= 0) return 0
  return Math.min(100, Math.round((p.processed / p.total) * 100))
})

/**
 * 実行確認を開く(ジョブ ID・件数・オプションを確定させる)。
 * 実行・再開・強制再実行のすべてがここを通るため、課題同期中の抑止もここで行う。
 */
function askRun(
  jobId: number,
  count: number,
  force: boolean,
  resendSending: boolean,
  estimate = estimateUnknownDuration(count),
) {
  if (running.value || !jobId || issueSyncRunning.value) return
  confirmJobId.value = jobId
  confirmCount.value = count
  confirmEstimate.value = estimate
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
  // 確認ダイアログを開いている間に他画面で同期が始まることもあるため、実行直前にも見る
  if (!jobId || running.value || issueSyncRunning.value) return
  running.value = true
  canceling.value = false
  setRunError(null)
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
    setRunError('bulk.error.run', { message: errorMessage(e) })
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
    setRunError('bulk.error.cancel', { message: errorMessage(e) })
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
const [jobsError, setJobsError] = useMessage(t)

async function loadJobs() {
  if (!profileId.value) return
  setJobsError(null)
  try {
    jobs.value = await backend.listBulkJobs(profileId.value)
  } catch (e) {
    setJobsError('bulk.error.loadJobs', { message: errorMessage(e) })
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
const [jobRowsError, setJobRowsError] = useMessage(t)
const jobRowDetails = ref<BulkJobRowDetail[]>([])

async function loadJobRows(jobId: number) {
  if (!profileId.value || !jobId) return
  setJobRowsError(null)
  jobRowsLoading.value = true
  try {
    jobRowDetails.value = await backend.getBulkJobRows(profileId.value, jobId)
  } catch (e) {
    jobRowDetails.value = []
    setJobRowsError('bulk.error.loadJobRows', { message: errorMessage(e) })
  } finally {
    jobRowsLoading.value = false
  }
}

/**
 * 行明細の「課題キー」欄の表示。
 *
 * 作成済みの課題 ID が入るのは新規追加が成立した行だけ。その行には作成された
 * 課題のキーも記録されるため、キーの有無より先に見て「(新規)」の目印を残す
 * (キーだけを出すと更新行と区別が付かなくなる)。
 */
function jobRowIssueLabel(r: BulkJobRowDetail): string {
  if (r.resultIssueId > 0) {
    return r.issueKey
      ? t('bulk.jobRows.newWithKey', { issueKey: r.issueKey })
      : t('bulk.jobRows.newWithId', { issueId: r.resultIssueId })
  }
  return r.issueKey || t('bulk.newIssue')
}

/** 明細の表示・非表示を切り替える(表示時に毎回取得して最新状態を出す) */
async function toggleJobRows(job: BulkJobRow) {
  if (expandedJobId.value === job.jobId) {
    expandedJobId.value = 0
    jobRowDetails.value = []
    setJobRowsError(null)
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
const [resultExportError, setResultExportError] = useMessage(t)
/** 直近に結果レポートを出力したジョブ ID(結果表示の対象) */
const resultExportJobId = ref(0)

async function exportResultExcel(jobId: number) {
  if (!profileId.value || !jobId || resultExportingJobId.value) return
  resultExportingJobId.value = jobId
  resultExportJobId.value = jobId
  setResultExportError(null)
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
    setResultExportError('bulk.error.exportResult', { message: errorMessage(e) })
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
    setGlobalError('bulk.error.loadProfile', { message: errorMessage(e) })
  } finally {
    initializing.value = false
  }
  // getActiveProfile の待機中にアンマウントされていたら、共有状態には触れない(高 1)。
  // 触ると、既に別プロファイルで表示中の新しい画面の選択を古いプロファイルへ
  // 巻き戻してしまう。この時点ではプロファイルが未確定でトークン照合ができないため、
  // 生存確認のみを行う(画面は同時に 1 つしか表示されないため、生存 = 現在の画面)。
  if (profileId.value && selectionGuard.isAlive()) {
    // 保存済みの選択(他画面で選んだ値・前回起動時の値)を先に復元してから一覧を読む
    restoreProjectSelection(profileId.value)
    const token = selectionGuard.begin()
    await loadProjects()
    if (!selectionGuard.isCurrent(token)) return
    // 選択が確定してから、それに紐づくデータを 1 回だけ読む
    // (以降の切替は watch(selectedProjectId) が担う)
    selectionInitialized = true
    await loadProjectData()
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
    <h1>{{ t('bulk.title') }}</h1>

    <!-- 書き込み操作である旨の注意(この画面だけが Backlog を変更する) -->
    <p class="danger-note">{{ t('bulk.dangerNote') }}</p>

    <p v-if="mock" class="mock-note">{{ t('bulk.mockNote') }}</p>

    <section class="panel flow">
      <h2>{{ t('bulk.flow.title') }}</h2>
      <ol>
        <li>{{ t('bulk.flow.step1') }}</li>
        <li>{{ t('bulk.flow.step2') }}</li>
        <li>{{ t('bulk.flow.step3') }}</li>
        <li>{{ t('bulk.flow.step4') }}</li>
        <li>{{ t('bulk.flow.step5') }}</li>
        <li>{{ t('bulk.flow.step6') }}</li>
      </ol>
      <!-- Excel の記法(#CLEAR#・「属性:定義名」列・シート名)は仕様のため翻訳しない -->
      <p class="hint">{{ t('bulk.flow.excelNote') }}</p>
      <p class="hint">{{ t('bulk.flow.customFieldNote') }}</p>
    </section>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">{{ t('common.state.loading') }}</p>

    <p v-else-if="!profileId" class="notice">{{ t('bulk.noProfile') }}</p>

    <template v-else>
      <!-- ① プロジェクト選択・テンプレート出力 -->
      <section class="panel">
        <h2>{{ t('bulk.step1.title') }}</h2>
        <div class="row">
          <label for="b-project">{{ t('common.label.project') }}</label>
          <!-- 選択の変化は watch(selectedProjectId) で拾う(@change では
               一覧から消えたプロジェクトの自動フォールバックを検出できないため) -->
          <select id="b-project" v-model.number="selectedProjectId" :disabled="selectionLocked">
            <option v-if="projects.length === 0" :value="0">{{ t('bulk.project.empty') }}</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ t('bulk.project.option', { name: p.name, key: p.projectKey }) }}
            </option>
          </select>
          <button :disabled="selectionLocked" @click="reloadProjects">
            {{ t('common.action.reload') }}
          </button>
        </div>
        <!-- 課題同期中に止まる操作をまとめて案内する(R10。exportTemplate のコメント参照)。
             この画面は同期を開始しないため、実行中の同期は必ず他画面が始めたもの -->
        <p v-if="issueSyncRunning" class="hint warn">{{ t('bulk.syncRunningNote') }}</p>
        <p class="hint">{{ t('bulk.step1.projectNote') }}</p>

        <!-- テンプレートに載せる課題の絞り込み(空欄なら全件)。
             課題抽出の検索条件と同じ項目・同じ流儀で指定する。
             キーワード欄の Enter では出力しない(保存ダイアログが不意に開く
             誤操作を避けるため、出力は必ずボタン操作で行う) -->
        <h3>{{ t('bulk.filter.title') }}</h3>
        <div class="row">
          <label for="b-keyword">{{ t('bulk.filter.keyword') }}</label>
          <input
            id="b-keyword"
            v-model="cond.keyword"
            type="text"
            class="wide"
            :placeholder="t('bulk.filter.keywordPlaceholder')"
            :disabled="conditionsLocked"
          />
        </div>
        <div class="row">
          <label>{{ t('bulk.filter.keywordMode') }}</label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="and" :disabled="conditionsLocked" />
            {{ t('bulk.filter.keywordModeAnd') }}
          </label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="or" :disabled="conditionsLocked" />
            {{ t('bulk.filter.keywordModeOr') }}
          </label>
        </div>

        <div class="row">
          <label for="b-updated-from">{{ t('bulk.filter.updated') }}</label>
          <input
            id="b-updated-from"
            v-model="cond.updatedFrom"
            type="date"
            :disabled="conditionsLocked"
          />
          <span>{{ t('bulk.filter.rangeSeparator') }}</span>
          <input v-model="cond.updatedTo" type="date" :disabled="conditionsLocked" />
        </div>

        <div class="row">
          <label for="b-created-from">{{ t('bulk.filter.created') }}</label>
          <input
            id="b-created-from"
            v-model="cond.createdFrom"
            type="date"
            :disabled="conditionsLocked"
          />
          <span>{{ t('bulk.filter.rangeSeparator') }}</span>
          <input v-model="cond.createdTo" type="date" :disabled="conditionsLocked" />
        </div>

        <div class="row">
          <label for="b-status">{{ t('common.label.status') }}</label>
          <select id="b-status" v-model="cond.statusName" :disabled="conditionsLocked || optionsLoading">
            <option value="">{{ t('common.state.all') }}</option>
            <option v-for="s in statusOptions" :key="s" :value="s">{{ s }}</option>
          </select>
          <label for="b-assignee" class="inline-label">{{ t('bulk.filter.assignee') }}</label>
          <select
            id="b-assignee"
            v-model="cond.assigneeName"
            :disabled="conditionsLocked || optionsLoading"
          >
            <option value="">{{ t('common.state.all') }}</option>
            <option v-for="a in assigneeOptions" :key="a" :value="a">{{ a }}</option>
          </select>
          <button :disabled="conditionsLocked || !hasConditions" @click="clearConditions">
            {{ t('common.action.clearConditions') }}
          </button>
        </div>
        <p v-if="!optionsLoading && statusOptions.length === 0 && assigneeOptions.length === 0" class="hint">
          {{ t('bulk.filter.optionsEmptyNote') }}
        </p>
        <p v-if="optionsError" class="hint warn">{{ optionsError }}</p>
        <p class="hint">
          {{ t('bulk.filter.matchNote') }}
          <template v-if="hasConditions">{{ t('bulk.filter.conditionsSet') }}</template>
          <template v-else>{{ t('bulk.filter.conditionsNone') }}</template>
          {{ t('bulk.filter.localDataNote') }}
        </p>

        <div class="row buttons">
          <button
            class="primary"
            :disabled="!selectedProjectId || exporting || running || issueSyncRunning"
            @click="exportTemplate"
          >
            {{ exporting ? t('common.state.exporting') : t('bulk.step1.exportButton') }}
          </button>
          <span v-if="exporting" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="exportError" class="error">{{ exportError }}</p>
        <p v-if="exportCanceled" class="notice">{{ t('bulk.step1.canceled') }}</p>
        <div v-if="exportPath" class="result ok">
          <p class="result-title">{{ t('bulk.step1.exported', { count: exportRows }) }}</p>
          <p class="path">{{ exportPath }}</p>
        </div>
      </section>

      <!-- ② Excel の取り込み -->
      <section class="panel">
        <h2>{{ t('bulk.step2.title') }}</h2>
        <div class="row">
          <label for="b-priority">{{ t('bulk.step2.defaultPriority') }}</label>
          <select id="b-priority" v-model.number="defaultPriorityId" :disabled="importing || running">
            <option v-if="priorities.length === 0" :value="0">
              {{ t('bulk.step2.priorityUnavailable') }}
            </option>
            <option v-for="p in priorities" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <p class="hint">{{ t('bulk.step2.priorityNote') }}</p>
        <p v-if="masterError" class="error">{{ masterError }}</p>

        <div class="row buttons">
          <button class="primary" :disabled="!canImport" @click="importFile">
            {{ importing ? t('bulk.step2.importing') : t('bulk.step2.importButton') }}
          </button>
          <span v-if="importing" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">{{ t('bulk.step2.note') }}</p>
        <p v-if="importError" class="error">{{ importError }}</p>
        <p v-if="importCanceled" class="notice">{{ t('bulk.step2.canceled') }}</p>
      </section>

      <!-- ③ 検証結果・dry-run プレビュー -->
      <section v-if="importResult" class="panel">
        <h2>{{ t('bulk.step3.title') }}</h2>
        <p class="summary">{{ summaryText }}</p>

        <div v-if="importResult.warnings.length > 0" class="notice warn">
          <p class="result-title">{{ t('bulk.step3.importWarnings') }}</p>
          <ul>
            <!-- 警告の本文は Go 生成の自由文(フェーズ 1 では日本語のまま。設計 §3.1) -->
            <li v-for="(w, i) in importResult.warnings" :key="i">{{ w }}</li>
          </ul>
        </div>
        <div v-if="importResult.errors.length > 0" class="result ng">
          <p class="result-title">
            {{ t('bulk.step3.validationErrors', { count: importResult.errors.length }) }}
          </p>
          <ul>
            <!-- 検証エラーの本文も Go 生成の自由文のためそのまま表示する -->
            <li v-for="(e, i) in importResult.errors" :key="i">
              {{ t('bulk.step3.errorLine', { rowNo: e.rowNo, message: e.message }) }}
            </li>
          </ul>
        </div>
        <p v-else class="notice ok-note">{{ t('bulk.step3.noErrors') }}</p>

        <p v-if="conflictWarningCount > 0" class="notice warn">
          {{ t('bulk.step3.conflictWarning', { count: conflictWarningCount }) }}
        </p>

        <div v-if="importResult.previews.length > 0" class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('bulk.col.row') }}</th>
                <th>{{ t('bulk.col.action') }}</th>
                <th>{{ t('bulk.col.issueKey') }}</th>
                <th>{{ t('bulk.col.summary') }}</th>
                <th>{{ t('bulk.col.changes') }}</th>
                <th>{{ t('bulk.col.conflict') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in importResult.previews" :key="p.rowNo" :class="{ skip: p.action === 'skip' }">
                <td class="nowrap">{{ p.rowNo }}</td>
                <td class="nowrap">
                  <!-- 表示は生の機械値(action)をフロントで翻訳する。Go が返す
                       actionLabel(日本語)は契約として残るが表示には使わない -->
                  <span class="badge" :class="p.action">{{ translateAction(translate, p.action) }}</span>
                </td>
                <td class="nowrap">{{ p.issueKey || t('bulk.newIssue') }}</td>
                <td>{{ p.summary }}</td>
                <td>
                  <!-- 変更内容は Go 生成の自由文のためそのまま表示する -->
                  <ul v-if="p.changes.length > 0" class="changes">
                    <li v-for="(c, i) in p.changes" :key="i">{{ c }}</li>
                  </ul>
                  <span v-else>-</span>
                </td>
                <td class="nowrap">
                  <span v-if="p.conflictWarning" class="badge warn">
                    {{ t('bulk.step3.needsCheck') }}
                  </span>
                  <span v-else>-</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- ④ 実行 -->
      <section v-if="importResult" class="panel">
        <h2>{{ t('bulk.step4.title') }}</h2>
        <p class="hint">
          {{
            t('bulk.step4.note', {
              count: targetCount,
              estimate: estimateImportedDuration(importResult),
            })
          }}
        </p>

        <div class="row buttons">
          <button
            class="primary"
            :disabled="!canRun"
            @click="askRun(importResult.jobId, targetCount, false, false, estimateImportedDuration(importResult))"
          >
            {{ running ? t('bulk.step4.running') : t('common.action.run') }}
          </button>
          <button v-if="running" :disabled="canceling" @click="cancelRun">
            {{ canceling ? t('bulk.step4.canceling') : t('common.action.cancel') }}
          </button>
        </div>
        <p v-if="!importResult.valid" class="hint warn">{{ t('bulk.step4.invalidNote') }}</p>

        <!-- 実行確認 -->
        <div v-if="confirming" class="confirm">
          <p class="result-title">{{ t('bulk.confirm.title', { count: confirmCount }) }}</p>
          <p v-if="confirmForce" class="warn-text">{{ t('bulk.confirm.force') }}</p>
          <p v-if="confirmResendSending" class="warn-text">{{ t('bulk.confirm.resend') }}</p>
          <p class="hint">
            {{ t('bulk.confirm.estimate', { estimate: confirmEstimate }) }}
          </p>
          <div class="row buttons">
            <button class="primary" @click="confirmRun">{{ t('bulk.confirm.ok') }}</button>
            <button @click="cancelConfirm">{{ t('bulk.confirm.cancel') }}</button>
          </div>
        </div>

        <!-- 進捗 -->
        <div v-if="running" class="progress-box">
          <div class="progress">
            <div class="progress-bar" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <p class="hint">
            {{
              t('bulk.progress.count', {
                processed: progress.processed,
                total: progress.total,
                percent: progressPercent,
              })
            }}
            <span v-if="canceling">{{ t('bulk.progress.canceling') }}</span>
          </p>
        </div>

        <p v-if="runError" class="error">{{ runError }}</p>
      </section>

      <!-- ⑤ 結果サマリ -->
      <section v-if="runResult" class="panel">
        <h2>{{ t('bulk.step5.title') }}</h2>
        <div class="result" :class="runResult.failed > 0 || runResult.conflict > 0 ? 'ng' : 'ok'">
          <p class="result-title">{{ t('bulk.step5.finished') }}</p>
          <ul>
            <li>{{ t('bulk.step5.done', { count: runResult.done }) }}</li>
            <li>{{ t('bulk.step5.failed', { count: runResult.failed }) }}</li>
            <li>{{ t('bulk.step5.conflict', { count: runResult.conflict }) }}</li>
            <li>{{ t('bulk.step5.skipped', { count: runResult.skipped }) }}</li>
            <li>
              {{ t('bulk.step5.duration', { seconds: (runResult.durationMs / 1000).toFixed(1) }) }}
            </li>
          </ul>
          <p class="hint">{{ t('bulk.step5.skippedNote') }}</p>
          <div v-if="runResult.warnings.length > 0" class="warnings">
            <p class="result-title">{{ t('common.label.warning') }}</p>
            <ul>
              <!-- 警告の本文は Go 生成の自由文のためそのまま表示する -->
              <li v-for="(w, i) in runResult.warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </div>

        <div v-if="runResult.conflict > 0" class="notice warn conflict">
          <p>{{ t('bulk.step5.conflictNote', { count: runResult.conflict }) }}</p>
          <div class="row buttons">
            <button :disabled="running || issueSyncRunning" @click="rerunWithForce">
              {{ t('bulk.action.forceRerun') }}
            </button>
          </div>
        </div>

        <!-- 結果レポート(行ごとの成否を Excel で確認する) -->
        <div class="row buttons">
          <button
            :disabled="running || resultExportingJobId !== 0"
            @click="exportResultExcel(runResult.jobId)"
          >
            {{
              resultExportingJobId === runResult.jobId
                ? t('common.state.exporting')
                : t('bulk.action.exportResult')
            }}
          </button>
          <span v-if="resultExportingJobId === runResult.jobId" class="spinner" aria-hidden="true"></span>
        </div>
        <p class="hint">{{ t('bulk.step5.exportNote') }}</p>
      </section>

      <!-- 結果レポート出力の状態(実行結果・ジョブ履歴の双方の出力で共用する) -->
      <section
        v-if="resultExportError || resultExportCanceled || resultExportPath"
        class="panel"
      >
        <h2>{{ t('bulk.resultExport.title') }}</h2>
        <p v-if="resultExportError" class="error">{{ resultExportError }}</p>
        <p v-if="resultExportCanceled" class="notice">{{ t('bulk.resultExport.canceled') }}</p>
        <div v-if="resultExportPath" class="result ok">
          <p class="result-title">
            {{
              t('bulk.resultExport.done', {
                jobId: resultExportJobId,
                rows: resultExportRows,
              })
            }}
          </p>
          <p class="path">{{ resultExportPath }}</p>
        </div>
      </section>

      <!-- ⑥ ジョブ履歴 -->
      <section class="panel">
        <h2>{{ t('bulk.step6.title') }}</h2>
        <div class="row buttons">
          <button :disabled="running" @click="loadJobs">{{ t('bulk.step6.refresh') }}</button>
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
              <template v-for="j in jobs" :key="j.jobId">
                <tr>
                  <td class="nowrap">#{{ j.jobId }}</td>
                  <td class="nowrap">{{ formatDateTime(j.createdAt) }}</td>
                  <!-- ジョブ種別・ジョブ状態は Go の生値(bulk_update / done 等)を
                       そのまま出す。翻訳対象の機械値はフェーズ 1 では
                       処理区分・行状態に限る(設計 §3.1) -->
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
                    <!-- 再開・再実行は askRun を通るため、課題同期中は実行できない -->
                    <button
                      v-if="canResume(j)"
                      :disabled="running || issueSyncRunning"
                      @click="resumeJob(j, false)"
                    >
                      {{ t('bulk.action.resume') }}
                    </button>
                    <button
                      v-if="j.conflict > 0"
                      :disabled="running || issueSyncRunning"
                      @click="forceResumeJob(j)"
                    >
                      {{ t('bulk.action.forceRerun') }}
                    </button>
                    <button :disabled="running" @click="toggleJobRows(j)">
                      {{
                        expandedJobId === j.jobId
                          ? t('bulk.action.hideRows')
                          : t('bulk.action.showRows')
                      }}
                    </button>
                    <button
                      :disabled="running || resultExportingJobId !== 0"
                      @click="exportResultExcel(j.jobId)"
                    >
                      {{
                        resultExportingJobId === j.jobId
                          ? t('common.state.exporting')
                          : t('bulk.action.exportResult')
                      }}
                    </button>
                  </td>
                </tr>

                <!-- 成否不明(sending が残った)行の説明と再送の導線 -->
                <tr v-if="hasSending(j)">
                  <td colspan="12" class="sending-note">
                    {{ t('bulk.step6.sendingNote', { count: j.sending }) }}
                    <button
                      class="inline"
                      :disabled="running || issueSyncRunning"
                      @click="resumeJob(j, true)"
                    >
                      {{ t('bulk.action.resumeResend') }}
                    </button>
                  </td>
                </tr>

                <!-- 行明細(展開表示) -->
                <tr v-if="expandedJobId === j.jobId">
                  <td colspan="12" class="detail-cell">
                    <p v-if="jobRowsLoading" class="hint">{{ t('bulk.step6.rowsLoading') }}</p>
                    <p v-else-if="jobRowsError" class="error">{{ jobRowsError }}</p>
                    <p v-else-if="jobRowDetails.length === 0" class="hint">
                      {{ t('bulk.step6.rowsEmpty') }}
                    </p>
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
                        <tr v-for="r in jobRowDetails" :key="r.rowNo">
                          <td class="nowrap">{{ r.rowNo }}</td>
                          <td class="nowrap">{{ jobRowIssueLabel(r) }}</td>
                          <td class="nowrap">
                            <!-- 表示は生の機械値(status)をフロントで翻訳する。
                                 Go が返す statusLabel(日本語)は表示に使わない -->
                            <span class="badge" :class="r.status">
                              {{ translateRowStatus(translate, r.status) }}
                            </span>
                          </td>
                          <!-- エラー本文は Go 生成の自由文のためそのまま表示する -->
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

/* セクション内の小見出し(テンプレート出力の検索条件) */
h3 {
  font-size: 0.95rem;
  margin: 1rem 0 0.6rem;
}

.panel {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: var(--surface);
}

.danger-note {
  background: var(--danger-bg);
  border: 1px solid var(--danger-border);
  border-radius: 4px;
  padding: 0.6rem 0.75rem;
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--danger-strong);
  margin: 0 0 0.75rem;
}

.mock-note {
  background: var(--warning-bg);
  border: 1px solid var(--warning-border);
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

.notice.ok-note {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
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

/* 「担当者」のように 2 つ目以降に並ぶラベル(ラベル幅を詰めて隣へ置く) */
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

.warn-text {
  font-size: 0.85rem;
  color: var(--warning-text);
  margin: 0 0 0.4rem;
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

button.inline {
  padding: 0.2rem 0.6rem;
  font-size: 0.8rem;
  margin-left: 0.4rem;
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

.result.ng {
  background: var(--danger-bg);
  border: 1px solid var(--danger-border);
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
  border: 1px solid var(--warning-border);
  background: var(--warning-bg);
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
  background: var(--bg-hover);
  overflow: hidden;
}

.progress-bar {
  height: 100%;
  background: var(--accent-emphasis);
  transition: width 0.2s linear;
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

tr.skip {
  color: var(--text-faint);
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
  border: 1px solid var(--border);
  background: var(--bg-muted);
}

.badge.create {
  background: var(--success-bg);
  border-color: var(--success-border);
  color: var(--success-text);
}

.badge.update {
  background: var(--status-info-bg);
  border-color: var(--accent-muted);
  color: var(--accent-fg);
}

.badge.warn {
  background: var(--warning-bg);
  border-color: var(--warning-border);
  color: var(--warning-text);
}

/* 行明細の状態バッジ(pending / sending / done / error / conflict / skip) */
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

/* 親テーブルのヘッダ固定は入れ子の明細テーブルには適用しない */
.detail-table th {
  position: static;
}

.detail-cell .hint,
.detail-cell .error {
  margin: 0;
}
</style>
