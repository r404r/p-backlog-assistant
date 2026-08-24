<script lang="ts" setup>
// 課題抽出画面。TDD 例外(GUI): 見た目・レイアウトは手動確認で担保する
// (状態遷移の配線は IssuesView.stale.test.ts / IssuesView.projectRefresh.test.ts、
//  英語表示は IssuesView.i18n.test.ts で検証している)。
//
// 表示文字列はすべてカタログ(locales/{ja,en}/issues.json・common.json)経由にする
// (設計 §3.3)。生の文字列が戻っていないことは lib/noHardcodedText.test.ts が検査する。
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  copyToClipboard,
  customColumnKey,
  formatSyncProgress,
  getBackend,
  isMockBackend,
  newSyncRunId,
  onSyncProgress,
  openExternalURL,
  type CustomFieldDef,
  type CustomFieldFilter,
  type ExportColumn,
  type IssueDetail,
  type IssueQuery,
  type Project,
  type SyncMode,
  type SyncProgress,
  type SyncResult,
} from '../lib/backend'
import { issueUrl } from '../lib/backlogUrl'
import { columnLabel } from '../lib/columnLabels'
import { translateSyncMode } from '../lib/enumLabels'
import { errorMessage, formatDateTime, formatElapsed } from '../lib/format'
import type { Language } from '../lib/i18n'
import { useIssuePagination } from '../lib/issuePagination'
import { buildIssueQuery, newIssueConditions, resetIssueConditions } from '../lib/issueQuery'
import { useMessage } from '../lib/message'
import { useModalFocus } from '../lib/modalFocus'
import { runSharedProjectRefresh, shouldSkipProjectRefreshFor } from '../lib/projectRefresh'
import {
  resolveProjectSelection,
  restoreProjectSelection,
  selectedProjectId,
  useProjectSelectionGuard,
} from '../lib/projectSelection'
import { beginIssueSync, endIssueSync, issueSyncRunning } from '../lib/syncState'

const backend = getBackend()
const mock = isMockBackend()

// 翻訳関数と表示言語は**この画面の i18n インスタンス**から取る。
// グローバル Composer を使う lib の既定値に任せると、テストが独立インスタンスで
// 別言語をマウントしたときに表示が混ざる(Codex レビュー指摘)。
const { t, locale } = useI18n()
const language = computed(() => locale.value as Language)

/**
 * 一覧の見出し・詳細の項目名は、Excel の列見出し(Go の label)とは別物なので
 * 画面側のカタログ(issues.result.column.* / issues.detail.field.*)で持つ。
 * Go が返す列(Excel 出力の列選択・カスタム属性の列見出し)は列キーで引く
 * columnLabel() を通す(設計 §3.3)。
 */
function exportColumnLabel(c: ExportColumn): string {
  return columnLabel(t, 'issue', c.key, c.label)
}

/**
 * 破棄済み・プロファイル切替後の画面が、後から届いた古い応答で
 * 共有のプロジェクト選択を書き換えてしまうのを防ぐガード(高 1)。
 */
const selectionGuard = useProjectSelectionGuard()

/**
 * 検索結果 1 ページの件数(Excel 出力は条件に一致する全件が対象)。
 * 画面はこの件数ずつ取得し、ページャで改ページする(lib/issuePagination)。
 */
const PAGE_SIZE = 200

/**
 * カスタム属性の型 ID(Go 側 customfield の定数と対)。
 * 絞り込み UI の入力方法(テキスト / 範囲 / 選択肢)の切り替えに使う。
 */
const CF_TYPE_NUMERIC = 3
const CF_TYPE_DATE = 4
const CF_LIST_TYPE_IDS = [5, 6, 7, 8]

/**
 * 固定の出力列(列キー・ラベル・既定選択は Go 側 export の列定義から取得する。R14)。
 *
 * 画面が独自の一覧を持つと Excel のヘッダとラベルがずれるため
 * (以前は画面「作成日」/ Excel「作成日時」)、定義は Go 側だけに置く。
 * 親課題キー(parentIssueKey)は Excel 出力専用の列で、一覧表示は行わない
 * (IssueRow は持たない)。親課題 ID → 課題キーの引き当てはローカル DB の
 * 走査を伴うため、選択されたときだけ Go 側で解決する。
 */
const fixedExportColumns = ref<ExportColumn[]>([])

/** 固定列の取得に失敗した場合の説明(空 = 正常) */
const [exportColumnsError, setExportColumnsError] = useMessage(t)

/**
 * 列選択を既定値で初期化済みか。
 * 再試行のたびに既定値へ戻して、利用者が変更した選択を捨てないようにする。
 */
let exportColumnsInitialized = false

/**
 * 出力できる固定列を取得し、初回だけ既定の列選択を入れる。
 * プロファイル・プロジェクトに依存しないため、画面表示時と再試行時にのみ呼ぶ。
 */
async function loadExportColumns() {
  try {
    const cols = await backend.getIssueExportColumns()
    fixedExportColumns.value = cols
    setExportColumnsError(null)
    if (!exportColumnsInitialized) {
      selectedColumns.value = cols.filter((c) => c.byDefault).map((c) => c.key)
      exportColumnsInitialized = true
    }
  } catch (e) {
    setExportColumnsError('issues.error.exportColumns', { message: errorMessage(e) })
  }
}

// ---------------------------------------------------------------------------
// アクティブプロファイル・プロジェクト
// ---------------------------------------------------------------------------

const profileId = ref('')
const initializing = ref(true)
const [globalError, setGlobalError] = useMessage(t)

const projects = ref<Project[]>([])
// プロジェクト選択は画面をまたいで共有する(サイドバー切替で破棄されないよう
// projectSelection モジュールが保持し、プロファイルごとに localStorage へ保存する)
const projectsLoading = ref(false)
const projectsSyncing = ref(false)
/** プロジェクト一覧の最新化に失敗した場合の警告(キャッシュ表示は継続する) */
const [projectsWarning, setProjectsWarning] = useMessage(t)

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

/**
 * 選択中プロジェクトの最終同期時刻の整形結果(未同期・プロジェクト未選択なら null)。
 * 見出し右側と同期セクションの鮮度表示で同じ値を使い、表記の揺れを防ぐ。
 *
 * 経過時間はプロジェクト一覧を読み直した時点(同期の完了・画面表示時の最新化)で
 * 計算し直す。時計に追従する再計算は行わない(元から鮮度表示に時計は無く、
 * 目安として十分なため)。
 */
const lastSyncedParts = computed(() => {
  const at = selectedProject.value?.lastSyncedAt
  if (!at) return null
  return { at: formatDateTime(at), elapsed: formatElapsed(at, t) }
})

/**
 * 見出し(h1)の右側に出す最終同期の表示(表示しない場合は null)。
 *
 * プロジェクト未選択のときは対象が無いため出さない。鮮度を取得できなかった
 * (syncStateUnknown)ときも、原因を説明する長い文言は見出しに載せず、
 * 同期セクションの鮮度表示(issues.freshness.unknown)に任せる。
 */
const headerLastSynced = computed<string | null>(() => {
  if (!selectedProject.value || syncStateUnknown.value) return null
  const parts = lastSyncedParts.value
  return parts ? t('issues.freshness.headerSynced', parts) : t('issues.freshness.headerNotSynced')
})

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
    setGlobalError('issues.error.loadProjects', { message: errorMessage(e) })
  } finally {
    projectsLoading.value = false
  }
}

/**
 * 手動の「プロジェクト一覧を同期」。
 * 利用者が明示的に求めた操作のため、画面表示時のスロットリング
 * (10 分以内なら省略)は適用せず、常に API と突合する。
 * ただし他画面が始めた突合が実行中の場合はそれへ合流する
 * (同じ突合を二重に走らせないため。projectRefresh.ts 参照)。
 */
async function syncProjects() {
  // busy 中(課題同期中を含む)は実行しない。ボタンは disabled だが、
  // 判定を UI だけに任せない(SyncStatusView と同じ流儀)
  if (!profileId.value || busy.value) return
  projectsSyncing.value = true
  setGlobalError(null)
  setProjectsWarning(null)
  try {
    // 成功すると自動突合の起点も更新される(手動同期でも突合は済んでいるため)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
    await loadProjects()
  } catch (e) {
    setGlobalError('issues.error.syncProjects', { message: errorMessage(e) })
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
 *
 * 課題同期の実行中は API による最新化を省略する(R10)。Go 側の同期処理は
 * 直列化されている(service の syncMu)ため、ここで待つと画面の初期表示が
 * 課題同期の完了(数分)までブロックされてしまう。ローカル一覧の読み込み
 * (loadProjects)は省略せず、セレクタが空のままにならないようにする。
 *
 * 直近(10 分以内)に突合できている場合も API による最新化を省略する
 * (projectRefresh)。画面を行き来するたびに通信すると体感が重く、
 * レート制限も消費するため。省略は正常動作なので警告等は出さない。
 * 他画面が始めた突合が実行中なら、新たに始めずそれへ合流する。
 */
async function refreshProjects() {
  if (!profileId.value || projectsSyncing.value) return
  if (issueSyncRunning.value) {
    setProjectsWarning('issues.project.refreshSkippedBySync')
    await loadProjects()
    return
  }
  if (shouldSkipProjectRefreshFor(profileId.value)) {
    await loadProjects()
    return
  }
  projectsSyncing.value = true
  setProjectsWarning(null)
  try {
    // 成功時だけ起点が記録される(失敗時は記録されず、次の画面表示で再試行する)
    await runSharedProjectRefresh(profileId.value, () => backend.syncProjects(profileId.value))
  } catch {
    setProjectsWarning('issues.project.refreshFailed')
  } finally {
    projectsSyncing.value = false
  }
  await loadProjects()
}

/**
 * 初期化(プロジェクト選択の復元 → 一覧取得 → 選択の解決)が完了したか。
 *
 * 初期化中は選択が「保存値 → 一覧に無ければ先頭」と 2 段階で動き得るため、
 * 途中の値で候補・カスタム属性を取りに行くと二重取得になり、
 * 古い ID の応答が後着して表示を上書きする余地も残る(中 2)。
 * そのため初期化中の変化は watch(selectedProjectId) では扱わず、
 * 選択が確定してから下の onMounted で 1 回だけ読み込む。
 * 初期化後のユーザ操作によるプロジェクト切替は、従来どおり watch が担う。
 */
let selectionInitialized = false

onMounted(async () => {
  // 列の一覧はプロファイル・プロジェクトに依存しないため、先に取りに行く
  void loadExportColumns()
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    setGlobalError('issues.error.activeProfile', { message: errorMessage(e) })
  } finally {
    initializing.value = false
  }
  // getActiveProfile の待機中にアンマウントされていたら、共有状態には触れない(高 1)。
  // 触ると、既に別プロファイルで表示中の新しい画面の選択を古いプロファイルへ
  // 巻き戻してしまう。この時点ではプロファイルが未確定でトークン照合ができないため、
  // 生存確認のみを行う(画面は同時に 1 つしか表示されないため、生存 = 現在の画面)。
  if (!profileId.value || !selectionGuard.isAlive()) return
  // 課題キーのクリックでコピーする URL の組み立てに使う(失敗時は機能を出さないだけ)
  void loadSpaceUrl()
  // 保存済みの選択(他画面で選んだ値・前回起動時の値)を復元し、
  // 一覧の取得とフォールバックまで済ませてから、選択に依存するデータを 1 回だけ読む。
  restoreProjectSelection(profileId.value)
  const token = selectionGuard.begin()
  await refreshProjects()
  if (!selectionGuard.isCurrent(token)) return
  selectionInitialized = true
  void loadFilterOptions()
  void loadCustomFields()
})

// ---------------------------------------------------------------------------
// カスタム属性(定義の取得・絞り込み条件・表示列)
// ---------------------------------------------------------------------------

/** 選択中プロジェクトのカスタム属性の定義(取得できない場合は空) */
const customFields = ref<CustomFieldDef[]>([])

/** カスタム属性の列(定義順。Excel 出力の列選択と一覧の表示列で共用する) */
const customColumns = computed<ExportColumn[]>(() =>
  // カスタム属性列は利用者が明示的に選んだときだけ出力する(既定では未選択)
  customFields.value.map((f) => ({ key: customColumnKey(f.id), label: f.name, byDefault: false })),
)

/** カスタム属性の取得失敗の表示用メッセージ(空 = 正常) */
const [customFieldsError, setCustomFieldsError] = useMessage(t)

/** カスタム属性 1 定義ぶんの絞り込み条件(型に応じて使うフィールドが変わる) */
interface CustomFieldCondition {
  /** テキスト系の部分一致 */
  text: string
  /** 数値・日付の下限 / 上限 */
  min: string
  max: string
  /** リスト系で選択した選択肢 ID */
  itemIds: number[]
}

/** 定義 ID → 絞り込み条件。定義の取得・切替のたびに作り直す */
const cfCond = ref<Record<number, CustomFieldCondition>>({})

/** 絞り込みセクションの開閉(利用者の操作を上書きしないよう ref で保持する) */
const cfPanelOpen = ref(false)

/** カスタム属性がリスト系(選択肢から選ぶ)かを判定する。
 * 選択肢が取れない定義は、選びようが無いのでテキストの部分一致へ縮退する。 */
function isListField(def: CustomFieldDef): boolean {
  return CF_LIST_TYPE_IDS.includes(def.typeId) && def.items.length > 0
}

/** 定義に合わせて条件の入れ物を作り直す(前のプロジェクトの条件を残さない) */
function resetCustomFieldConditions() {
  const next: Record<number, CustomFieldCondition> = {}
  for (const f of customFields.value) {
    next[f.id] = { text: '', min: '', max: '', itemIds: [] }
  }
  cfCond.value = next
}

/** 入力済みの条件だけを検索条件(Go 側 customfield.Filter)へ変換する */
function buildCustomFieldFilters(): CustomFieldFilter[] {
  const out: CustomFieldFilter[] = []
  for (const def of customFields.value) {
    const c = cfCond.value[def.id]
    if (!c) continue
    const filter: CustomFieldFilter = { defId: def.id, typeId: def.typeId }
    let used = false
    if (c.text.trim()) {
      filter.text = c.text.trim()
      used = true
    }
    if (c.min) {
      filter.min = c.min
      used = true
    }
    if (c.max) {
      filter.max = c.max
      used = true
    }
    if (c.itemIds.length > 0) {
      filter.itemIds = [...c.itemIds]
      used = true
    }
    if (used) out.push(filter)
  }
  return out
}

/** 指定中のカスタム属性条件の数(折りたたんだままでも指定に気づけるようにする) */
const customFieldFilterCount = computed(() => buildCustomFieldFilters().length)

/**
 * loadCustomFields の世代番号。プロジェクトを A→B→A と素早く切り替えると
 * projectId の比較だけでは最初の A の古い応答を弾けないため、
 * 「最後に開始した要求」の応答だけを反映する。
 */
let customFieldsRequestSeq = 0

/**
 * 絞り込み・表示・出力に使うカスタム属性の定義を取得する。
 *
 * 未対応プラン・権限不足はバックエンド側で空配列へ縮退済みのため、
 * ここに届く失敗は通信断等の障害。固定列の検索・出力は妨げず、
 * 取得できなかった旨の警告と再試行の導線を表示する。
 */
async function loadCustomFields() {
  const seq = ++customFieldsRequestSeq
  // 前のプロジェクトの定義・条件・列選択が残らないようにしてから取得する
  customFields.value = []
  setCustomFieldsError(null)
  resetCustomFieldConditions()
  pruneUnavailableColumns()
  if (!profileId.value || !selectedProjectId.value) return
  try {
    const master = await backend.getMasterData(profileId.value, selectedProjectId.value)
    // より新しい要求が開始済みなら、この(古い)応答は反映しない
    if (seq !== customFieldsRequestSeq) return
    customFields.value = master.customFields
    resetCustomFieldConditions()
  } catch (e) {
    if (seq !== customFieldsRequestSeq) return
    setCustomFieldsError('issues.error.customFields', { message: errorMessage(e) })
  }
}

// ---------------------------------------------------------------------------
// 条件フォーム
// ---------------------------------------------------------------------------

// 条件の定義と IssueQuery への変換は lib/issueQuery に置き、
// 一括更新のテンプレート出力(BulkUpdateView)と同じものを使う
const cond = reactive(newIssueConditions())

const statusOptions = ref<string[]>([])
const assigneeOptions = ref<string[]>([])
const optionsLoading = ref(false)

/**
 * loadFilterOptions の世代番号(loadCustomFields の customFieldsRequestSeq と同じ流儀。中 2)。
 * プロジェクトを素早く切り替えると古い応答が後着して候補・エラー表示を上書きし得るため、
 * 「最後に開始した要求」の応答だけを反映する。
 */
let filterOptionsRequestSeq = 0

async function loadFilterOptions() {
  const seq = ++filterOptionsRequestSeq
  statusOptions.value = []
  assigneeOptions.value = []
  if (!profileId.value || !selectedProjectId.value) {
    // 世代を進めた後の早期 return。先行要求が下ろせなくなるため、
    // 最新要求であるこの経路で読込中表示を下ろす(低 1)。
    if (seq === filterOptionsRequestSeq) optionsLoading.value = false
    return
  }
  optionsLoading.value = true
  try {
    const opts = await backend.listFilterOptions(profileId.value, selectedProjectId.value)
    // より新しい要求が開始済みなら、この(古い)応答は反映しない
    if (seq !== filterOptionsRequestSeq) return
    statusOptions.value = opts.statuses
    assigneeOptions.value = opts.assignees
    // 選択済みの値が候補に無くなった場合は「すべて」へ戻す
    if (cond.statusName && !opts.statuses.includes(cond.statusName)) cond.statusName = ''
    if (cond.assigneeName && !opts.assignees.includes(cond.assigneeName)) cond.assigneeName = ''
  } catch (e) {
    if (seq !== filterOptionsRequestSeq) return
    setGlobalError('issues.error.filterOptions', { message: errorMessage(e) })
  } finally {
    // 読込中表示は最新の要求だけが下ろす(古い応答が新しい要求の表示を消さないため)
    if (seq === filterOptionsRequestSeq) optionsLoading.value = false
  }
}

// プロジェクトを切り替えたら候補と結果をリセットする
// (初期化中の変化は扱わない。selectionInitialized の説明を参照)
watch(selectedProjectId, () => {
  if (!selectionInitialized) return
  // 実行中の検索・同期・出力を失効させてから片付ける。そうしないと、後から届いた
  // 前のプロジェクトの応答が、ここで消した結果を書き戻してしまう(高 1)
  invalidatePendingRequests()
  // 結果・ページ・検索スナップショット(表示中のカスタム属性列を含む)・stale を
  // まとめて片付ける。表示列は前のプロジェクトの定義なので残さない
  pagination.reset()
  // 消した一覧に対する「コピーしました」・コピー失敗の表示を残さない
  clearCopiedFeedback()
  setCopyError(null)
  // 前のプロジェクトの課題を表示したままにしない(取得中の要求も失効させる)
  closeIssueDetail()
  syncResult.value = null
  setSyncError(null)
  exportPath.value = ''
  exportUnverifiable.value = 0
  exportCanceled.value = false
  setExportError(null)
  void loadFilterOptions()
  void loadCustomFields()
})

/**
 * 現在の条件を IssueQuery に変換する(空文字の条件は送らない)。
 *
 * ページング(limit / offset)は載せない。検索経路では useIssuePagination が
 * ページごとに付け足し、Excel 出力・テンプレート出力はこの条件のまま全件を出力する。
 */
function buildQuery(): IssueQuery {
  const q: IssueQuery = buildIssueQuery(selectedProjectId.value, cond)
  // 未入力のカスタム属性は送らない(空の条件を送っても Go 側で無視されるが、
  // 送らない方が「カスタム属性条件あり」の 2 段階検索を無駄に起動させない)
  const customFieldFilters = buildCustomFieldFilters()
  if (customFieldFilters.length > 0) q.customFieldFilters = customFieldFilters
  return q
}

function clearConditions() {
  resetIssueConditions(cond)
  resetCustomFieldConditions()
}

// ---------------------------------------------------------------------------
// 検索(ローカル DB)・ページネーション
// ---------------------------------------------------------------------------

/**
 * 検索結果のページング状態(lib/issuePagination)。
 *
 * 1 ページ(PAGE_SIZE 件)ぶんだけを取得し、ページ移動のたびに offset を
 * 付け替えて取り直す。検索した時点の条件と表示列をスナップショットとして持ち、
 * 成功時にだけ結果一式を確定する(状態遷移の規則は issuePagination.ts を参照)。
 */
const pagination = useIssuePagination<ExportColumn>({
  pageSize: PAGE_SIZE,
  // 失敗メッセージはこの画面の i18n インスタンスで翻訳する(言語切替にも追従する)
  t,
  fetch: (query, columns) =>
    backend.searchIssues(
      profileId.value,
      query,
      columns.map((c) => c.key),
    ),
})

// テンプレートからは従来と同じ名前で参照する(ページング導入前の表示条件を保つ)
const {
  rows,
  total,
  /** カスタム属性条件を判定できなかった課題の件数(0 なら警告を出さない) */
  unverifiable,
  searching,
  searched,
  error: searchError,
  page: currentPage,
  totalPages,
  rangeStart,
  rangeEnd,
  hasPrev,
  hasNext,
  /** 表示中の結果が古くなった可能性があるか(再検索を促す) */
  stale: resultsStale,
} = pagination

/** 選択中のカスタム属性列(Excel 出力の列選択と共用) */
const selectedCustomColumns = computed<ExportColumn[]>(() =>
  customColumns.value.filter((c) => selectedColumns.value.includes(c.key)),
)

/**
 * 結果テーブルに表示中のカスタム属性列(検索スナップショットに固定された列)。
 *
 * 検索した時点の選択を固定する。列選択を変えるたびに見出しだけ増減すると、
 * 取得済みの行に値が無い(空欄の)列が並び、データが無いのか列が増えただけなのか
 * 区別できなくなるため、次の検索まで見出しと値を揃えておく
 * (ページ移動も同じスナップショットの列で取得する)。
 */
const shownCustomColumns = computed<ExportColumn[]>(() => pagination.snapshot.value?.columns ?? [])

/** 表示中の列と選択中の列がずれているか(再検索を促す案内に使う) */
const customColumnsOutOfDate = computed(() => {
  const shown = shownCustomColumns.value.map((c) => c.key).join(',')
  return searched.value && shown !== selectedCustomColumns.value.map((c) => c.key).join(',')
})

/**
 * プロジェクトに紐づく実行中の要求(検索・同期・Excel 出力)の応答を失効させる。
 *
 * 世代番号を進めるだけで、実行中フラグ(searching / syncing / exporting)は
 * 下ろさない。これらの処理は実行中なら早期 return する多重起動防止付きなので、
 * フラグは応答が届いた時点の finally で必ず下ろされる。ここで下ろしてしまうと、
 * 前の要求が飛んでいる最中に次の要求を始められてしまう。
 *
 * (候補・カスタム属性の取得は複数箇所から呼ばれ多重起動しうるため、
 *  そちらは「最新の要求だけが読込中表示を下ろす」方式を採っている)
 */
function invalidatePendingRequests() {
  pagination.invalidate()
  syncRequestSeq++
  exportRequestSeq++
  // 失効した実行の進捗を表示し続けないよう、表示と受理対象を消す
  syncProgress.value = null
  currentSyncRunId = ''
}

async function search() {
  // 同期中は検索しない(R10)。同期途中のローカル DB を読むと「取り込み済みの
  // ぶんだけ」の件数・一覧になり、完了後の結果と食い違って見えるため。
  // ボタンは disabled にしてあるが、キーワード欄の Enter からも入るのでここでも見る。
  // 判定は共有状態込みの issueSyncing(他画面で開始した同期も対象)。
  if (!selectedProjectId.value || searching.value || issueSyncing.value) return
  // 前の一覧に対するコピーの表示は、結果が入れ替わる前に消す
  clearCopiedFeedback()
  setCopyError(null)
  // 検索条件と値を取得する列は「この検索の時点」のものをスナップショットとして渡す
  // (以降のページ移動はこのスナップショットで取得する。フォーム・列選択を
  //  後から変えても表示中の結果には影響しない)
  await pagination.search({ query: buildQuery(), columns: selectedCustomColumns.value })
  syncPageInput()
}

/**
 * ページャの操作を受け付けられるか。
 *
 * 同期中は検索と同じ理由で不可(R10)。stale 中(表示中の結果より DB が
 * 新しくなっている)は、ページを跨いだ行のずれを避けるため再検索を促す。
 */
const canPage = computed(
  () => searched.value && !searching.value && !issueSyncing.value && !resultsStale.value,
)

/** ページャ: 指定ページへ移動する(範囲外は composable がクランプする) */
async function goToPage(n: number) {
  if (!canPage.value) return
  // 前の一覧に対するコピーの表示は、結果が入れ替わる前に消す
  clearCopiedFeedback()
  setCopyError(null)
  await pagination.goToPage(n)
  // クランプ・取得失敗で要求どおりのページにならないことがあるため、
  // 入力欄は確定済みのページへ必ず戻す
  syncPageInput()
}

/**
 * ページ番号の直接入力欄(表示は文字列。確定済みページに追従させる)。
 * 任意ページへのジャンプはこの欄で行う。
 */
const pageInput = ref('1')

/** 入力欄の表示を確定済みのページ番号へ戻す */
function syncPageInput() {
  pageInput.value = String(currentPage.value)
}

// 確定済みページが画面外の要因で変わったとき(プロジェクト切替のリセット等)も
// 入力欄の表示を合わせる
watch(currentPage, syncPageInput)

/**
 * ページ番号欄で Enter が押されたときにそのページへ移動する。
 * IME の変換確定 Enter を無視する判定は onKeywordEnter と同じ。
 */
function onPageInputEnter(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  const raw = pageInput.value.trim()
  const n = Number(raw)
  // 空欄・数値として読めない入力(全角数字等)では移動せず、
  // 表示を確定済みのページへ戻す(Number('') が 0 になるため空欄も弾く)
  if (!raw || !Number.isFinite(n)) {
    syncPageInput()
    return
  }
  void goToPage(n)
}

/**
 * キーワード欄で Enter が押されたときに検索する。
 *
 * IME(日本語入力)の変換確定 Enter で検索してしまわないよう、次の場合は無視する:
 * - `e.isComposing === true`: 変換中であることを示す標準プロパティ(WebView2 等)
 * - `e.keyCode === 229`: isComposing が立たない旧 WebKit 系(WKWebView)で、
 *   変換確定のキーイベントに現れる値。両方見ることで Windows / macOS 双方に対応する
 *
 * 検索可否の判定(プロジェクト未選択・検索中は実行しない)は search() 側と同じ。
 */
function onKeywordEnter(e: KeyboardEvent) {
  if (e.isComposing || e.keyCode === 229) return
  void search()
}

// ---------------------------------------------------------------------------
// 課題 URL のコピー(課題キーのクリック)
// ---------------------------------------------------------------------------

/**
 * アクティブプロファイルのスペース URL(例: https://example.backlog.jp)。
 *
 * 課題 URL は「スペース URL + /view/ + 課題キー」で組み立てられるため、
 * バックエンドに問い合わせず画面側で作る(追加の API 往復を発生させない)。
 * 取得できない場合は空文字のままにして、課題キーのコピー機能を出さない。
 */
const spaceUrl = ref('')

/** 課題キーをクリックで URL コピーできるか(スペース URL が分かる場合のみ) */
const canCopyIssueUrl = computed(() => !!spaceUrl.value)

/**
 * コピー完了のトーストに表示中の課題キー(空 = 非表示)。
 *
 * 以前は課題キーのセル内に「コピーしました」を出していたが、表示・消滅のたびに
 * 列幅が変わってテーブルがずれるため、レイアウトに影響しない固定位置の
 * トースト(画面下部中央)に変更した。
 */
const copyToastKey = ref('')

/** コピーに失敗したときの説明(空 = 正常) */
const [copyError, setCopyError] = useMessage(t)

/** トーストの表示時間(ミリ秒) */
const COPY_TOAST_MS = 2000

/**
 * トーストを消すタイマー。
 * 別の行を続けてクリックした場合・連打した場合に、前のタイマーが後の表示を
 * 消してしまわないよう、常に 1 本だけ持って張り替える。
 */
let copiedTimer: ReturnType<typeof setTimeout> | null = null

/**
 * コピー操作の要求番号。連打時に非同期のコピー完了順が逆転しても、
 * 「最後にクリックした操作」だけがトースト・タイマー・エラーを更新する
 * (古い完了が新しいトーストを上書き・消去しないため)。
 */
let copyRequestSeq = 0

function clearCopiedFeedback() {
  if (copiedTimer !== null) {
    clearTimeout(copiedTimer)
    copiedTimer = null
  }
  copyToastKey.value = ''
}

/**
 * アクティブプロファイルのスペース URL を解決する。
 *
 * プロファイル一覧の取得に失敗した場合はコピー機能を静かに無効化する
 * (課題キーは通常表示のまま)。検索・出力といった本来の機能には影響しない
 * 付随機能のため、画面上部のエラーで利用者を驚かせない。
 */
async function loadSpaceUrl() {
  spaceUrl.value = ''
  if (!profileId.value) return
  try {
    const profiles = await backend.listProfiles()
    spaceUrl.value = profiles.find((p) => p.id === profileId.value)?.spaceUrl ?? ''
  } catch {
    spaceUrl.value = ''
  }
}

/**
 * 課題 URL をクリップボードへコピーする(一覧のアイコン・詳細ポップアップの共通処理)。
 *
 * inDetail が真のときは失敗をポップアップ内に表示する。一覧側のエラー表示は
 * オーバーレイの背後になり、ポップアップを開いたままでは見えないため。
 */
async function copyIssueUrl(issueKey: string, inDetail = false) {
  const url = issueUrl(spaceUrl.value, issueKey)
  // スペース URL が分からない場合はボタン自体を出していないが、念のため何もしない
  if (!url) return
  const seq = ++copyRequestSeq
  try {
    await copyToClipboard(url)
    // 完了までの間に別の課題キーがクリックされていたら、この(古い)結果は反映しない
    if (seq !== copyRequestSeq) return
    setCopyError(null)
    setDetailCopyError(null)
    if (copiedTimer !== null) clearTimeout(copiedTimer)
    // 同じ課題を連続コピーしたときも支援技術(role="status")へ再通知されるよう、
    // 一度空にして次のティックで再設定する(DOM 内容が変化しないと読み上げられない)
    copyToastKey.value = ''
    await nextTick()
    if (seq !== copyRequestSeq) return
    copyToastKey.value = issueKey
    copiedTimer = setTimeout(() => {
      copyToastKey.value = ''
      copiedTimer = null
    }, COPY_TOAST_MS)
  } catch (e) {
    if (seq !== copyRequestSeq) return
    // 成功表示が残っていると失敗に気づけないため、先に消してからエラーを出す
    clearCopiedFeedback()
    // 表示先が違うだけで内容は同じ(翻訳は表示時に行うため、ここでは素材を渡す)
    const params = { message: errorMessage(e) }
    if (inDetail) {
      setDetailCopyError('issues.error.copyUrl', params)
    } else {
      setCopyError('issues.error.copyUrl', params)
    }
  }
}

// 画面を離れるときにタイマーを残さない(破棄後の ref 更新を避ける)
onUnmounted(() => {
  clearCopiedFeedback()
})

// ---------------------------------------------------------------------------
// 課題詳細のポップアップ(課題キーのクリック)
// ---------------------------------------------------------------------------

/**
 * 詳細を表示中の課題キー(空 = ポップアップを閉じている)。
 *
 * 取得結果(detail)とは別に持つ。読み込み中・失敗時もどの課題を開いたのかを
 * ヘッダに出し続けるため(空のダイアログにしない)。
 */
const detailIssueKey = ref('')

/** 取得した課題詳細(null = 未取得・取得失敗) */
const detail = ref<IssueDetail | null>(null)

const detailLoading = ref(false)
const [detailError, setDetailError] = useMessage(t)

/**
 * ポップアップ内の「URL をコピー」の失敗表示(空 = 正常)。
 *
 * 一覧側の copyError はオーバーレイの背後に隠れて見えないため、
 * ポップアップから実行したコピーの失敗はポップアップ内に出す。
 */
const [detailCopyError, setDetailCopyError] = useMessage(t)

/** 「最新の状態を取得」を実行中か(ボタンの無効化・スピナー表示) */
const detailRefreshing = ref(false)

/**
 * 「最新の状態を取得」の失敗表示(空 = 正常)。
 *
 * detailCopyError とは分けている。原因(コピー / 取得)も対処も異なるため、
 * 同じ領域を使い回すと片方の失敗がもう片方の文言で上書きされて紛らわしい。
 */
const [detailRefreshError, setDetailRefreshError] = useMessage(t)

/** 詳細ポップアップを開いているか */
const detailOpen = computed(() => detailIssueKey.value !== '')

/**
 * 詳細取得の要求番号。行を続けてクリックした場合や連打した場合に、
 * 古い応答が後着して別の課題の内容を表示しないようにする(検索と同じ流儀)。
 */
let detailRequestSeq = 0

/** 閉じたときにフォーカスを戻す先(詳細を開いた課題キーのボタン) */
let detailOpener: HTMLElement | null = null

/** ポップアップの「閉じる」ボタン(開いた直後のフォーカス移動先) */
const detailCloseButton = ref<HTMLButtonElement | null>(null)

/** ポップアップ本体(フォーカスをこの中へ閉じ込める範囲) */
const detailModal = ref<HTMLElement | null>(null)

// 開いている間はフォーカスをポップアップ内に閉じ込め、ESC で閉じる。
// 戻り先は「開いた課題キーのボタン」を明示する(クリックでフォーカスが
// 移らない WebView でも確実に戻すため)
useModalFocus(detailModal, detailOpen, {
  initialFocus: () => detailCloseButton.value,
  returnFocus: () => detailOpener,
  onEscape: () => closeIssueDetail(),
})

/** 課題キーのクリック: 課題詳細をポップアップで表示する */
async function openIssueDetail(issueKey: string, e: MouseEvent) {
  // 同期中は開かない(R10)。同期途中のローカル DB を読むと、完了後の内容と
  // 食い違う中途半端な詳細を見せてしまう。ボタンは disabled にしてあるが、
  // 判定を UI だけに任せない(検索・Excel 出力と同じ流儀)
  if (issueSyncing.value) return
  // 閉じたときに戻すフォーカス先は、非同期の前(currentTarget が有効なうち)に控える
  detailOpener = (e.currentTarget as HTMLElement | null) ?? null
  const seq = ++detailRequestSeq
  detailIssueKey.value = issueKey
  detail.value = null
  setDetailError(null)
  setDetailCopyError(null)
  setDetailRefreshError(null)
  detailRefreshing.value = false
  detailLoading.value = true
  try {
    const res = await backend.getIssueDetail(profileId.value, selectedProjectId.value, issueKey)
    // 別の行を開き直した・閉じた後なら、この(古い)応答は反映しない
    if (seq !== detailRequestSeq) return
    detail.value = res
  } catch (err) {
    if (seq !== detailRequestSeq) return
    setDetailError('issues.error.issueDetail', { message: errorMessage(err) })
  } finally {
    if (seq === detailRequestSeq) detailLoading.value = false
  }
}

/**
 * 「最新の状態を取得」: この課題 1 件だけを Backlog から取得し直し、
 * ローカル DB へ反映したうえで表示を更新する。
 *
 * 取得できた内容はローカル DB に入っているため、検索結果一覧の該当行は
 * ここでは書き換えない(次回の検索で反映される)。一覧と詳細で値が食い違って
 * 見えることはあるが、正しいのは「DB に入っている = 詳細に出ている」方である。
 */
async function refreshIssueDetail() {
  // 連打(実行中の再実行)は無視する。ボタンも無効化しているが判定を UI に任せない
  if (detailRefreshing.value || detailLoading.value) return
  // 同期中は実行しない(同期中はポップアップ自体を開けないが多重防御。R10)
  if (issueSyncing.value) return
  const issueKey = detailIssueKey.value
  if (!issueKey) return

  // 閉じた・別の課題を開いた後に届いた応答を反映しないためのガード
  // (開いた時点の要求番号と照合する。openIssueDetail と同じ流儀)
  const seq = detailRequestSeq
  // 検索結果の stale 判定に使う起点プロジェクト(非同期の前に控える)
  const originProjectId = selectedProjectId.value
  detailRefreshing.value = true
  setDetailRefreshError(null)
  try {
    const res = await backend.refreshIssueDetail(profileId.value, originProjectId, issueKey)
    if (seq !== detailRequestSeq) return
    detail.value = res
    // 取得に成功したので、開いたときの取得失敗(未同期等)の表示は消す
    setDetailError(null)
  } catch (err) {
    if (seq !== detailRequestSeq) return
    setDetailRefreshError('issues.error.refreshDetail', { message: errorMessage(err) })
  } finally {
    if (seq === detailRequestSeq) detailRefreshing.value = false
    // 試行が終わった時点で、成功・失敗を問わずローカル DB は変わり得る
    // (課題本体だけ先に upsert して後段で失敗する経路がある)。
    // 判定はモーダルの世代(detailRequestSeq)に依存させず、起点プロジェクトと
    // 表示中の結果のプロジェクトの一致だけで行う(モーダルを閉じた後の完了でも
    // stale にし、プロジェクト切替後の新しい結果は stale にしないため)
    pagination.markStaleForProject(originProjectId)
  }
}

/**
 * 内容の時点を伝える注記。
 *
 * 詳細はローカル DB の内容であり、Backlog 側の最新とは限らないため必ず出す。
 * 「最新の状態を取得」で 1 件だけ取り込み直すと fetchedAt がその時刻になるため、
 * 表示する時刻は常に「この課題をローカルへ取り込んだ時刻」を指す。
 */
const detailNote = computed(() => {
  const at = detail.value?.fetchedAt ? formatDateTime(detail.value.fetchedAt) : ''
  // 時刻の有無で文が変わるため、断片をつなぐのではなく文ごとにキーを分ける
  return at ? t('issues.detail.note.fetched', { at }) : t('issues.detail.note.unknown')
})

/**
 * コメントの取得状況を伝える注意書き(ポップアップ最上部)。
 *
 * コメントは同期の対象外で、「最新の状態を取得」を押した課題にだけ入る。
 * 空のコメント欄を見て「コメントが無い課題」と誤解されないよう、
 * 未取得と取得済み(いつ時点か)をここで必ず区別して伝える。
 */
const commentNote = computed(() => {
  const at = detail.value?.commentsFetchedAt ? formatDateTime(detail.value.commentsFetchedAt) : ''
  return at
    ? t('issues.detail.commentNote.fetched', { at })
    : t('issues.detail.commentNote.notFetched')
})

/** コメントを取得済みか(取得済みで 0 件 = 「コメントなし」と未取得を区別する) */
const commentsFetched = computed(() => (detail.value?.commentsFetchedAt ?? '') !== '')

/**
 * 詳細ポップアップを閉じる(実行中の取得は失効させる)。
 * 閉じた後のフォーカス復帰は useModalFocus が行う(detailOpener は
 * その戻り先として参照されるため、ここでは消さない)。
 */
function closeIssueDetail() {
  if (!detailOpen.value) return
  detailRequestSeq++
  // モーダル起点で進行中のコピーも失効させる。閉じた後に完了した古いコピーの
  // 失敗が、次に開いたモーダルの detailCopyError へ混入するのを防ぐ
  copyRequestSeq++
  detailIssueKey.value = ''
  detail.value = null
  setDetailError(null)
  setDetailCopyError(null)
  setDetailRefreshError(null)
  // 実行中の取得は上の detailRequestSeq++ で失効させているため、
  // その応答では解除されない(次に開いたときへ持ち越さないよう、ここで戻す)
  detailRefreshing.value = false
  detailLoading.value = false
}

/** 詳細ポップアップの課題を既定のブラウザで開く */
function openIssueInBrowser() {
  const url = issueUrl(spaceUrl.value, detailIssueKey.value)
  if (!url) return
  openExternalURL(url)
}

// ---------------------------------------------------------------------------
// 同期
// ---------------------------------------------------------------------------

// 既定は auto(未同期プロジェクトでは incremental が必ず失敗するため。低 1)
const syncMode = ref<SyncMode>('auto')
const syncing = ref(false)
const syncResult = ref<SyncResult | null>(null)
const [syncError, setSyncError] = useMessage(t)

/**
 * 課題同期が実行中か。
 *
 * この画面が開始した同期(syncing)に加えて、共有状態(syncState)も見る。
 * ローカル ref だけで判定すると、サイドバーで一度別画面へ移動して戻ってきた
 * 時点で syncing が false の新しい画面になり、Go 側で走り続けている同期を
 * 無視して選択を切り替えられてしまうため(R10)。
 */
const issueSyncing = computed(() => syncing.value || issueSyncRunning.value)

// 同期が始まったら開いている詳細を閉じる(R10)。
// 表示中の内容は同期の進行とともに古くなり、同期途中の DB を読み直すこともできない。
// 他画面で開始された同期(issueSyncRunning)も対象にするため、この画面の
// runSync ではなく issueSyncing の変化で判定する。
watch(issueSyncing, (running, wasRunning) => {
  if (running) {
    closeIssueDetail()
    return
  }
  // 同期の完了で課題の集合が変わり得るため、表示中の検索結果は stale にする。
  // ページを跨いだ行のずれ(飛び・重複)を避けるため、以降は再検索を促す
  if (wasRunning) pagination.markStale()
})

/**
 * プロジェクト選択・同期系操作を固定する状態(R10。SyncStatusView の busy と同じ流儀)。
 *
 * 課題同期中にプロジェクトを切り替えられると、完了後の再読込
 * (loadProjects / loadFilterOptions)や結果表示が切替先の画面に作用し得る。
 * 世代番号(syncRequestSeq)で表示は守れるが、実行中に選択が動くこと自体が
 * 「どのプロジェクトを同期しているのか」を分かりにくくするため、
 * 実行中はプロジェクトセレクタと同期系ボタンを固定する。
 */
const busy = computed(() => issueSyncing.value || projectsSyncing.value || projectsLoading.value)

/** runSync の世代番号(検索・出力と同じ理由。プロジェクト切替で失効させる) */
let syncRequestSeq = 0

/**
 * 実行中の同期の進捗(未受信・非実行中は null)。
 * フル同期は数万件になり得るため、件数を出さないと無反応に見える。
 */
const syncProgress = ref<SyncProgress | null>(null)
const syncProgressText = computed(() =>
  syncProgress.value ? formatSyncProgress(syncProgress.value, t, language.value) : '',
)
let unsubscribeSyncProgress: (() => void) | null = null

/**
 * 表示対象の実行 ID(この画面が今まさに走らせている同期。非実行中は空文字)。
 * プロファイル ID + プロジェクト ID の一致だけでは、A→B→A と切り替えて
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

async function runSync() {
  if (!selectedProjectId.value || syncBlocked.value) return
  const seq = ++syncRequestSeq
  syncing.value = true
  setSyncError(null)
  syncResult.value = null
  syncProgress.value = null
  const runId = newSyncRunId()
  currentSyncRunId = runId
  // 画面をまたいで抑止するため、実行中であることを共有状態にも記録する(R10)。
  // この画面が破棄されてもローカルの syncing は失われるが、共有状態は残る。
  const targetProjectId = selectedProjectId.value
  beginIssueSync(profileId.value, targetProjectId, runId)
  try {
    const result = await backend.syncIssues(
      profileId.value,
      targetProjectId,
      syncMode.value,
      runId,
    )
    // プロジェクト切替後なら、前のプロジェクトの同期結果は表示しない
    if (seq !== syncRequestSeq) return
    syncResult.value = result
    await loadProjects() // 鮮度表示を更新
    await loadFilterOptions()
  } catch (e) {
    if (seq !== syncRequestSeq) return
    setSyncError('issues.error.sync', { message: errorMessage(e) })
  } finally {
    // 同期は多重起動しないため、失効済みの応答でもここで必ず下ろす
    syncing.value = false
    syncProgress.value = null
    // 応答後に届く進捗(あれば)を受け取らないよう、実行 ID も外す
    if (currentSyncRunId === runId) currentSyncRunId = ''
    // 共有状態の解除(主経路)。この継続は画面が破棄されても走るため、
    // 成功・失敗のどちらでも確実に解除される(syncState.ts のコメント参照)。
    endIssueSync(runId)
  }
}

// ---------------------------------------------------------------------------
// Excel 出力
// ---------------------------------------------------------------------------

/** 出力できる列(固定列 + カスタム属性列) */
const exportColumns = computed<ExportColumn[]>(() => [
  ...fixedExportColumns.value,
  ...customColumns.value,
])

// 初期値は空。固定列の取得(loadExportColumns)で既定列が入る
const selectedColumns = ref<string[]>([])

/** 選択済みの列から、現在は選択できない列(切替前のカスタム属性列)を外す */
function pruneUnavailableColumns() {
  const available = new Set(exportColumns.value.map((c) => c.key))
  selectedColumns.value = selectedColumns.value.filter((k) => available.has(k))
}

const exporting = ref(false)

/**
 * 課題同期を開始できない状態(同期モード・同期ボタンの disabled と runSync のガード)。
 *
 * busy に加えて検索・Excel 出力の実行中も含める(逆方向の排他)。
 * 出力は読み取りトランザクションを保持したまま全件を走査し(service.IterateIssues)、
 * 単一 DB 接続(SetMaxOpenConns(1))を同期の書き込みと奪い合うため、
 * 同時に始めると双方が長時間待たされる。検索も、開始済みのものが終わるまでは
 * 同期を待たせて中途データとの混在を避ける。
 * (exporting を参照するため、宣言はこの位置に置く)
 */
const syncBlocked = computed(() => busy.value || searching.value || exporting.value)

const exportPath = ref('')
const exportRows = ref(0)
const exportUnverifiable = ref(0)
const exportCanceled = ref(false)
const [exportError, setExportError] = useMessage(t)

/**
 * exportExcel の世代番号(検索と同じ理由。高 1)。
 * 保存ダイアログを開いている間にプロジェクトを切り替えると、切替後の画面へ
 * 前のプロジェクトの出力結果・エラーが表示されてしまうため。
 */
let exportRequestSeq = 0

/**
 * Excel 出力を実行できるか。
 *
 * 同期中は出力しない(R10)。出力は条件一致の全件を読み取りトランザクションを
 * 保持したまま走査する(service.IterateIssues)ため、同期の書き込みと
 * 単一 DB 接続を奪い合って双方が長時間待たされる。加えて、途中まで取り込んだ
 * 状態のブックが「完全なデータ」として保存されてしまうのを避ける。
 */
const canExport = computed(
  () =>
    !!selectedProjectId.value &&
    selectedColumns.value.length > 0 &&
    !exporting.value &&
    !issueSyncing.value,
)

async function exportExcel() {
  if (!canExport.value) return
  const seq = ++exportRequestSeq
  exporting.value = true
  setExportError(null)
  exportPath.value = ''
  exportUnverifiable.value = 0
  exportCanceled.value = false
  try {
    // 表示上限は付けない(条件に一致する全件を出力する)
    const columns = exportColumns.value
      .filter((c) => selectedColumns.value.includes(c.key))
      .map((c) => c.key)
    const res = await backend.exportIssuesExcel(profileId.value, buildQuery(), columns)
    // プロジェクト切替後(または再実行後)なら、古い結果は表示しない
    if (seq !== exportRequestSeq) return
    if (!res.path) {
      exportCanceled.value = true
    } else {
      exportPath.value = res.path
      exportRows.value = res.rows
      exportUnverifiable.value = res.unverifiable
    }
  } catch (e) {
    if (seq !== exportRequestSeq) return
    setExportError('issues.error.export', { message: errorMessage(e) })
  } finally {
    // Excel 出力は多重起動しないため、失効済みの応答でもここで必ず下ろす
    exporting.value = false
  }
}
</script>

<template>
  <div class="issues">
    <!-- 見出しの右側に選択中プロジェクトの最終同期を出す(同期セクションまで
         スクロールしなくても鮮度が分かるようにするため) -->
    <div class="page-header">
      <h1>{{ t('issues.title') }}</h1>
      <span v-if="headerLastSynced" class="header-freshness">{{ headerLastSynced }}</span>
    </div>

    <p v-if="mock" class="mock-note">{{ t('issues.mockNote') }}</p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="initializing">{{ t('common.state.loading') }}</p>

    <p v-else-if="!profileId" class="notice">{{ t('issues.noProfile') }}</p>

    <template v-else>
      <!-- プロジェクト選択 -->
      <section class="panel">
        <h2>{{ t('common.label.project') }}</h2>
        <div class="row">
          <label for="i-project">{{ t('common.label.project') }}</label>
          <!-- 同期中は選択を固定する(R10。切り替えると同期完了後の再読込・
               結果表示が切替先に作用してしまうため) -->
          <select id="i-project" v-model="selectedProjectId" :disabled="busy">
            <option v-if="projects.length === 0" :value="0">{{ t('issues.project.empty') }}</option>
            <option v-for="p in projects" :key="p.id" :value="p.id">
              {{ t('issues.project.option', { name: p.name, key: p.projectKey }) }}
            </option>
          </select>
          <button :disabled="busy" :title="t('issues.project.syncTitle')" @click="syncProjects">
            {{ projectsSyncing ? t('issues.project.syncing') : t('issues.project.sync') }}
          </button>
          <span v-if="projectsSyncing" class="spinner" aria-hidden="true"></span>
        </div>

        <p v-if="issueSyncing" class="hint warn">{{ t('issues.project.lockedBySync') }}</p>

        <p class="hint">{{ t('issues.project.hint') }}</p>

        <p v-if="projectsWarning" class="notice warn">{{ projectsWarning }}</p>

        <p v-if="selectedProject" class="freshness">
          {{ t('issues.freshness.label') }}
          <template v-if="syncStateUnknown">{{ t('issues.freshness.unknown') }}</template>
          <template v-else-if="lastSyncedParts">
            {{ t('issues.freshness.lastSynced', lastSyncedParts) }}
          </template>
          <template v-else>{{ t('common.state.notSynced') }}</template>
        </p>
        <p v-if="neverSynced" class="notice warn">{{ t('issues.project.neverSynced') }}</p>
      </section>

      <!-- 同期(検索の前に実行する想定のため検索条件より上に配置) -->
      <section class="panel">
        <h2>{{ t('issues.sync.title') }}</h2>
        <div class="row">
          <label>{{ t('issues.sync.mode') }}</label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="auto" :disabled="syncBlocked" />
            {{ t('issues.sync.modeAuto') }}
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="full" :disabled="syncBlocked" />
            {{ t('common.enum.syncMode.full') }}
          </label>
          <label class="radio">
            <input v-model="syncMode" type="radio" value="incremental" :disabled="syncBlocked" />
            {{ t('common.enum.syncMode.incremental') }}
          </label>
          <button :disabled="syncBlocked || !selectedProjectId" @click="runSync">
            {{ syncing ? t('common.state.syncing') : t('issues.sync.run') }}
          </button>
          <span v-if="syncing" class="spinner" aria-hidden="true"></span>
          <!-- 進捗・結果に対象プロジェクト名は添えない(R10)。同期中は上のセレクタが
               固定されて対象が画面に出ており、完了後にプロジェクトを切り替えると
               watch(selectedProjectId) が結果を消すため、取り違えは起きない。
               (SyncStatusView は同期状態一覧に他プロジェクトの行が並ぶため名前を添えている) -->
          <span v-if="syncing && syncProgressText" class="sync-progress" aria-live="polite">
            {{ syncProgressText }}
          </span>
        </div>
        <!-- 他画面で開始した同期(この画面は runId を知らないため進捗は出せない)。
             実行中であることだけは伝えないと、操作できない理由が分からなくなる -->
        <p v-if="!syncing && issueSyncRunning" class="hint warn">
          {{ t('issues.sync.runningElsewhere') }}
        </p>
        <p class="hint">{{ t('issues.sync.hint') }}</p>

        <p v-if="syncError" class="error">{{ syncError }}</p>

        <div v-if="syncResult" class="result ok">
          <!-- モードは Go が解決した機械値(full / incremental)を表示の正とする(設計 §3.1) -->
          <p class="result-title">
            {{ t('issues.sync.result.title', { mode: translateSyncMode(t, syncResult.mode) }) }}
          </p>
          <ul>
            <li>{{ t('issues.sync.result.fetched', { count: syncResult.fetched }) }}</li>
            <li>{{ t('issues.sync.result.upserted', { count: syncResult.upserted }) }}</li>
            <li>{{ t('issues.sync.result.deleted', { count: syncResult.deleted }) }}</li>
            <li>
              {{
                t('issues.sync.result.duration', {
                  seconds: (syncResult.durationMs / 1000).toFixed(1),
                })
              }}
            </li>
          </ul>
          <div v-if="syncResult.warnings.length > 0" class="warnings">
            <p class="result-title">{{ t('common.label.warning') }}</p>
            <ul>
              <li v-for="(w, i) in syncResult.warnings" :key="i">{{ w }}</li>
            </ul>
          </div>
        </div>
      </section>

      <!-- 検索条件 -->
      <section class="panel">
        <h2>{{ t('issues.search.title') }}</h2>
        <div class="row">
          <label for="i-keyword">{{ t('issues.search.keyword') }}</label>
          <input
            id="i-keyword"
            v-model="cond.keyword"
            type="text"
            class="wide"
            :placeholder="t('issues.search.keywordPlaceholder')"
            @keydown.enter="onKeywordEnter"
          />
        </div>
        <div class="row">
          <label>{{ t('issues.search.keywordMode') }}</label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="and" :disabled="searching" />
            {{ t('issues.search.keywordAnd') }}
          </label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="or" :disabled="searching" />
            {{ t('issues.search.keywordOr') }}
          </label>
        </div>
        <!-- 強調(<strong>)を挟むため、この案内だけは前・強調・後の 3 キーに分ける
             (マークアップをカタログへ入れて v-html で描画するのは避ける)。
             改行を入れると強調の前後に空白が入るため、1 行で並べている -->
        <p class="hint">
          {{ t('issues.search.keywordHintPrefix') }}<strong>{{ t('issues.search.keywordHintTarget') }}</strong>{{ t('issues.search.keywordHintSuffix') }}
        </p>

        <div class="row">
          <label for="i-updated-from">{{ t('issues.search.updated') }}</label>
          <input id="i-updated-from" v-model="cond.updatedFrom" type="date" />
          <span>{{ t('issues.rangeSeparator') }}</span>
          <input v-model="cond.updatedTo" type="date" />
        </div>

        <div class="row">
          <label for="i-created-from">{{ t('issues.search.created') }}</label>
          <input id="i-created-from" v-model="cond.createdFrom" type="date" />
          <span>{{ t('issues.rangeSeparator') }}</span>
          <input v-model="cond.createdTo" type="date" />
        </div>

        <div class="row">
          <label for="i-status">{{ t('common.label.status') }}</label>
          <select id="i-status" v-model="cond.statusName" :disabled="optionsLoading">
            <option value="">{{ t('common.state.all') }}</option>
            <option v-for="s in statusOptions" :key="s" :value="s">{{ s }}</option>
          </select>
          <label for="i-assignee" class="inline-label">{{ t('issues.search.assignee') }}</label>
          <select id="i-assignee" v-model="cond.assigneeName" :disabled="optionsLoading">
            <option value="">{{ t('common.state.all') }}</option>
            <option v-for="a in assigneeOptions" :key="a" :value="a">{{ a }}</option>
          </select>
        </div>
        <p v-if="!optionsLoading && statusOptions.length === 0 && assigneeOptions.length === 0" class="hint">
          {{ t('issues.search.optionsHint') }}
        </p>

        <!-- カスタム属性の絞り込み(定義があるプロジェクトでのみ表示) -->
        <details
          v-if="customFields.length > 0"
          class="cf-filters"
          :open="cfPanelOpen"
          @toggle="cfPanelOpen = ($event.target as HTMLDetailsElement).open"
        >
          <summary>
            {{ t('issues.cf.summary') }}
            <span v-if="customFieldFilterCount > 0" class="cf-count">
              {{ t('issues.cf.count', { count: customFieldFilterCount }) }}
            </span>
          </summary>

          <div v-for="def in customFields" :key="def.id" class="row cf-row">
            <label :for="`i-cf-${def.id}`">{{ def.name }}</label>
            <template v-if="cfCond[def.id]">
              <!-- リスト系: 選択肢の複数選択(いずれか一致) -->
              <template v-if="isListField(def)">
                <label v-for="it in def.items" :key="it.id" class="checkbox">
                  <input v-model="cfCond[def.id].itemIds" type="checkbox" :value="it.id" />
                  {{ it.name }}
                </label>
              </template>
              <!-- 数値: 範囲 -->
              <template v-else-if="def.typeId === CF_TYPE_NUMERIC">
                <input
                  :id="`i-cf-${def.id}`"
                  v-model="cfCond[def.id].min"
                  type="number"
                  step="any"
                  class="narrow"
                  :placeholder="t('issues.cf.min')"
                />
                <span>{{ t('issues.rangeSeparator') }}</span>
                <input
                  v-model="cfCond[def.id].max"
                  type="number"
                  step="any"
                  class="narrow"
                  :placeholder="t('issues.cf.max')"
                />
              </template>
              <!-- 日付: 範囲 -->
              <template v-else-if="def.typeId === CF_TYPE_DATE">
                <input :id="`i-cf-${def.id}`" v-model="cfCond[def.id].min" type="date" />
                <span>{{ t('issues.rangeSeparator') }}</span>
                <input v-model="cfCond[def.id].max" type="date" />
              </template>
              <!-- 文字列・文章(選択肢が取れないリスト系を含む): 部分一致 -->
              <template v-else>
                <input
                  :id="`i-cf-${def.id}`"
                  v-model="cfCond[def.id].text"
                  type="text"
                  class="wide"
                  :placeholder="t('issues.cf.contains')"
                />
              </template>
            </template>
          </div>

          <p class="hint">{{ t('issues.cf.hint') }}</p>
        </details>

        <div class="row buttons">
          <!-- 同期中は検索しない(R10。search() のコメント参照) -->
          <button
            class="primary"
            :disabled="searching || issueSyncing || !selectedProjectId"
            @click="search"
          >
            {{ searching ? t('issues.search.searching') : t('common.action.search') }}
          </button>
          <button :disabled="searching" @click="clearConditions">
            {{ t('common.action.clearConditions') }}
          </button>
          <span v-if="searching" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="issueSyncing" class="hint warn">{{ t('issues.search.blockedBySync') }}</p>
        <p v-if="searchError" class="error">{{ searchError }}</p>
      </section>

      <!-- 検索結果 -->
      <section v-if="searched" class="panel">
        <h2>{{ t('issues.result.title') }}</h2>
        <p class="summary">
          {{ t('issues.result.total', { count: total }) }}
          <span v-if="rows.length > 0">
            {{ t('issues.result.range', { from: rangeStart, to: rangeEnd }) }}
          </span>
        </p>
        <p v-if="totalPages > 1" class="hint">
          {{ t('issues.result.exportAllNote', { count: total }) }}
        </p>

        <!-- 表示中の結果より後にローカル DB が変わった(同期・詳細の再取得)。
             ページを跨いだ行のずれを避けるため、ページャを止めて再検索を促す -->
        <p v-if="resultsStale" class="notice warn">{{ t('issues.result.stale') }}</p>

        <p v-if="unverifiable > 0" class="notice warn">
          {{ t('issues.result.unverifiable', { count: unverifiable }) }}
        </p>

        <p v-if="customColumnsOutOfDate" class="hint warn">
          {{ t('issues.result.columnsOutOfDate') }}
        </p>

        <p v-if="rows.length === 0" class="notice">{{ t('issues.result.empty') }}</p>

        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('issues.result.column.issueKey') }}</th>
                <th>{{ t('issues.result.column.summary') }}</th>
                <th>{{ t('issues.result.column.statusName') }}</th>
                <th>{{ t('issues.result.column.assigneeName') }}</th>
                <th>{{ t('issues.result.column.issueTypeName') }}</th>
                <th>{{ t('issues.result.column.priorityName') }}</th>
                <th>{{ t('issues.result.column.created') }}</th>
                <th>{{ t('issues.result.column.updated') }}</th>
                <th>{{ t('issues.result.column.dueDate') }}</th>
                <!-- カスタム属性列は「Excel 出力」の列選択に連動する
                     (属性名は利用者データなので翻訳しない。columnLabel が素通しする) -->
                <th v-for="c in shownCustomColumns" :key="c.key">{{ exportColumnLabel(c) }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.issueKey">
                <!-- 課題キーのクリックで詳細をポップアップ表示し、
                     右隣のクリップボードのアイコンで課題 URL をコピーする
                     (コピーはスペース URL が分かる場合のみ) -->
                <td class="nowrap">
                  <button
                    type="button"
                    class="issue-key"
                    :disabled="issueSyncing"
                    :title="
                      issueSyncing
                        ? t('issues.result.detailDisabled')
                        : t('issues.result.detailTitle')
                    "
                    @click="openIssueDetail(r.issueKey, $event)"
                  >
                    {{ r.issueKey }}
                  </button>
                  <!-- アイコンは常時表示にして、ホバーで出し入れしない
                       (表示・非表示のたびに列幅が変わってテーブルがずれるため) -->
                  <button
                    v-if="canCopyIssueUrl"
                    type="button"
                    class="copy-icon"
                    :title="t('issues.result.copyUrl')"
                    :aria-label="t('issues.result.copyUrl')"
                    @click="copyIssueUrl(r.issueKey)"
                  >
                    <!-- クリップボード(線画)。外部アイコンライブラリを持ち込まず、
                         色は currentColor で周囲の文字色に追従させる -->
                    <svg
                      width="14"
                      height="14"
                      viewBox="0 0 16 16"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.3"
                      stroke-linejoin="round"
                      aria-hidden="true"
                      focusable="false"
                    >
                      <rect x="3.5" y="3" width="9" height="11.5" rx="1.5" />
                      <rect x="6" y="1.5" width="4" height="2.5" rx="0.75" />
                    </svg>
                  </button>
                </td>
                <td>{{ r.summary }}</td>
                <td class="nowrap">{{ r.statusName }}</td>
                <td class="nowrap">{{ r.assigneeName || t('issues.value.unset') }}</td>
                <td class="nowrap">{{ r.issueTypeName }}</td>
                <td class="nowrap">{{ r.priorityName }}</td>
                <td class="nowrap">{{ formatDateTime(r.created) }}</td>
                <td class="nowrap">{{ formatDateTime(r.updated) }}</td>
                <td class="nowrap">{{ r.dueDate || '-' }}</td>
                <td v-for="c in shownCustomColumns" :key="c.key">{{ r.customFields[c.key] }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- ページャ(総ページ数が 1 以下なら出さない)。
             ページ番号の入力欄で任意のページへ直接移動できる(範囲外はクランプ)。
             検索中・同期中・stale 中は操作できない -->
        <div v-if="totalPages > 1" class="row pager">
          <button type="button" :disabled="!canPage || !hasPrev" @click="goToPage(1)">
            {{ t('issues.pager.first') }}
          </button>
          <button type="button" :disabled="!canPage || !hasPrev" @click="goToPage(currentPage - 1)">
            {{ t('issues.pager.prev') }}
          </button>
          <label for="i-page" class="pager-label">{{ t('common.label.page') }}</label>
          <input
            id="i-page"
            v-model="pageInput"
            type="text"
            inputmode="numeric"
            class="page-input"
            :disabled="!canPage"
            :aria-label="t('issues.pager.inputLabel')"
            @keydown.enter="onPageInputEnter"
            @blur="syncPageInput"
          />
          <span class="pager-total">{{ t('issues.pager.total', { count: totalPages }) }}</span>
          <button type="button" :disabled="!canPage || !hasNext" @click="goToPage(currentPage + 1)">
            {{ t('issues.pager.next') }}
          </button>
          <button type="button" :disabled="!canPage || !hasNext" @click="goToPage(totalPages)">
            {{ t('issues.pager.last') }}
          </button>
          <span v-if="searching" class="spinner" aria-hidden="true"></span>
        </div>

        <p v-if="copyError" class="error">{{ copyError }}</p>

        <p v-if="rows.length > 0" class="hint">
          {{ t('issues.result.hintDetail') }}
          <template v-if="canCopyIssueUrl">{{ t('issues.result.hintCopy') }}</template>
        </p>

        <p v-if="customColumns.length > 0" class="hint">
          {{ t('issues.result.hintCustomColumns') }}
        </p>
      </section>

      <!-- Excel 出力 -->
      <section class="panel">
        <h2>{{ t('issues.export.title') }}</h2>
        <p class="hint">{{ t('issues.export.hint') }}</p>
        <div class="columns">
          <!-- 固定列のラベルは Go が返す日本語ではなく列キーから引く(設計 §3.3) -->
          <label v-for="c in fixedExportColumns" :key="c.key" class="checkbox">
            <input v-model="selectedColumns" type="checkbox" :value="c.key" />
            {{ exportColumnLabel(c) }}
          </label>
        </div>
        <template v-if="customColumns.length > 0">
          <p class="hint">{{ t('issues.export.customHint') }}</p>
          <div class="columns">
            <label v-for="c in customColumns" :key="c.key" class="checkbox">
              <input v-model="selectedColumns" type="checkbox" :value="c.key" />
              {{ exportColumnLabel(c) }}
            </label>
          </div>
        </template>
        <p v-if="customFieldsError" class="hint warn">
          {{ customFieldsError }}
          <button type="button" class="link" @click="loadCustomFields">
            {{ t('common.action.retry') }}
          </button>
        </p>
        <p v-if="exportColumnsError" class="hint warn">
          {{ exportColumnsError }}
          <button type="button" class="link" @click="loadExportColumns">
            {{ t('common.action.retry') }}
          </button>
        </p>
        <div class="row buttons">
          <button class="primary" :disabled="!canExport" @click="exportExcel">
            {{ exporting ? t('common.state.exporting') : t('issues.export.run') }}
          </button>
          <span v-if="exporting" class="spinner" aria-hidden="true"></span>
        </div>
        <p v-if="issueSyncing" class="hint warn">{{ t('issues.export.blockedBySync') }}</p>
        <p v-if="selectedColumns.length === 0" class="hint warn">
          {{ t('issues.export.noColumns') }}
        </p>
        <p v-if="exportError" class="error">{{ exportError }}</p>
        <p v-if="exportCanceled" class="notice">{{ t('issues.export.canceled') }}</p>
        <div v-if="exportPath" class="result ok">
          <p class="result-title">{{ t('issues.export.done', { count: exportRows }) }}</p>
          <p class="path">{{ exportPath }}</p>
          <p v-if="exportUnverifiable > 0" class="warnings">
            {{ t('issues.export.unverifiable', { count: exportUnverifiable }) }}
          </p>
        </div>
      </section>
    </template>

    <!-- 課題詳細のポップアップ。
         背景クリック(@click.self)と ESC で閉じるのは、接続設定の削除確認
         ダイアログ(SettingsView)と同じ流儀。内容はローカル DB へ取り込んだ
         時点のもので、開くだけでは API を呼ばない(呼ぶのは「最新の状態を取得」
         を押したときだけ) -->
    <div v-if="detailOpen" class="modal-overlay" @click.self="closeIssueDetail">
      <div
        ref="detailModal"
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="issue-detail-title"
      >
        <!-- コメントの取得状況は最上部に出す(コメント欄まで読まないと
             「同期では取得されない」ことに気づけないため)。詳細を取得できて
             いないときは注意書きの対象が無いので出さない -->
        <p v-if="detail" class="notice comment-note">{{ commentNote }}</p>

        <!-- 部分失敗(課題本体は取得できたがコメントだけ失敗した等)。
             詳細は有効なので、表示は消さず警告だけを添える -->
        <p v-for="(w, i) in detail?.warnings ?? []" :key="i" class="notice warn comment-note">
          {{ w }}
        </p>

        <h2 id="issue-detail-title" class="detail-title">
          <span class="detail-key">{{ detailIssueKey }}</span>
          <span v-if="detail" class="detail-summary">{{ detail.summary }}</span>
        </h2>

        <p v-if="detailLoading" class="notice">{{ t('common.state.loading') }}</p>
        <p v-else-if="detailError" class="error">{{ detailError }}</p>

        <template v-else-if="detail">
          <dl class="detail-grid">
            <dt>{{ t('issues.detail.field.status') }}</dt>
            <dd>{{ detail.statusName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.issueType') }}</dt>
            <dd>{{ detail.issueTypeName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.priority') }}</dt>
            <dd>{{ detail.priorityName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.assignee') }}</dt>
            <dd>{{ detail.assigneeName || t('issues.value.unset') }}</dd>
            <dt>{{ t('issues.detail.field.dueDate') }}</dt>
            <dd>{{ detail.dueDate || '-' }}</dd>
            <dt>{{ t('issues.detail.field.created') }}</dt>
            <dd>{{ formatDateTime(detail.created) || '-' }}</dd>
            <dt>{{ t('issues.detail.field.updated') }}</dt>
            <dd>{{ formatDateTime(detail.updated) || '-' }}</dd>
            <dt>{{ t('issues.detail.field.parentIssue') }}</dt>
            <dd>{{ detail.parentIssueKey || t('issues.value.none') }}</dd>
          </dl>

          <!-- カスタム属性(定義があり、値を持つ課題でのみ表示)。属性名は利用者データ -->
          <template v-if="detail.customFields.length > 0">
            <h3 class="detail-section">{{ t('issues.detail.customFields') }}</h3>
            <dl class="detail-grid">
              <template v-for="(f, i) in detail.customFields" :key="i">
                <dt>{{ f.name }}</dt>
                <dd>{{ f.value || t('issues.value.unset') }}</dd>
              </template>
            </dl>
          </template>

          <h3 class="detail-section">{{ t('issues.detail.description') }}</h3>
          <pre v-if="detail.description" class="detail-description">{{ detail.description }}</pre>
          <p v-else class="hint">{{ t('issues.detail.noDescription') }}</p>

          <!-- コメント(オンデマンド取得)。同期では取得されないため、
               未取得・取得済み 0 件・取得済みありの 3 状態を出し分ける -->
          <h3 class="detail-section">{{ t('issues.detail.comments') }}</h3>
          <p v-if="!commentsFetched" class="hint">
            {{ t('issues.detail.commentsNotFetched') }}
          </p>
          <p v-else-if="detail.comments.length === 0" class="hint">
            {{ t('issues.detail.noComments') }}
          </p>
          <ol v-else class="comment-list">
            <li v-for="(c, i) in detail.comments" :key="i" class="comment">
              <p class="comment-meta">
                <span class="comment-author">{{
                  c.authorName || t('issues.detail.unknownAuthor')
                }}</span>
                <span class="comment-date">{{ formatDateTime(c.created) }}</span>
              </p>
              <pre class="comment-body">{{ c.content }}</pre>
            </li>
          </ol>
          <!-- 本文を持たない項目(状態変更等)は件数だけを伝える -->
          <p v-if="commentsFetched && detail.commentsHistoryOnly > 0" class="hint">
            {{ t('issues.detail.historyOnly', { count: detail.commentsHistoryOnly }) }}
          </p>
          <p v-if="detail.commentsTruncated" class="hint warn">
            {{ t('issues.detail.commentsTruncated') }}
          </p>

          <p class="hint detail-note">{{ detailNote }}</p>
        </template>

        <!-- コピー・再取得の失敗はここに出す(一覧側のエラーはオーバーレイの背後で見えない) -->
        <p v-if="detailCopyError" class="error detail-error">{{ detailCopyError }}</p>
        <p v-if="detailRefreshError" class="error detail-error">{{ detailRefreshError }}</p>

        <div class="row buttons detail-buttons">
          <!-- この課題 1 件だけを Backlog から取得し直してローカル DB へ反映する
               (プロジェクト全体の同期は行わないため、最終同期時刻は変わらない) -->
          <button
            type="button"
            :disabled="detailRefreshing || detailLoading || issueSyncing"
            @click="refreshIssueDetail"
          >
            {{ detailRefreshing ? t('issues.detail.refreshing') : t('issues.detail.refresh') }}
          </button>
          <span v-if="detailRefreshing" class="spinner" aria-hidden="true"></span>
          <button v-if="canCopyIssueUrl" type="button" @click="copyIssueUrl(detailIssueKey, true)">
            {{ t('issues.detail.copyUrl') }}
          </button>
          <button v-if="canCopyIssueUrl" type="button" @click="openIssueInBrowser">
            {{ t('issues.detail.openInBrowser') }}
          </button>
          <button ref="detailCloseButton" type="button" @click="closeIssueDetail">
            {{ t('common.action.close') }}
          </button>
        </div>
      </div>
    </div>

    <!-- コピー完了の通知(トースト)。
         行内に出すと課題キー列の幅が変わってテーブルがずれるため、
         レイアウトに影響しない固定位置(画面下部中央)へ出す。
         今のところ使うのはこの画面だけなので lib/ へは切り出さない
         (2 画面目で必要になった時点で共通コンポーネント化する) -->
    <Transition name="toast">
      <p v-if="copyToastKey" class="copy-toast" role="status">
        {{ t('issues.toast.copied', { key: copyToastKey }) }}
      </p>
    </Transition>
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.issues {
  max-width: none;
  width: 100%;
  box-sizing: border-box;
}

/* 見出しと最終同期を同じ行に置く(最終同期は右寄せ)。
   下の余白は h1 の margin をそのまま使うため、ここでは付けない */
.page-header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
}

h1 {
  font-size: 1.4rem;
  margin: 0 0 1rem;
}

/* 見出しの主役は画面名のため、鮮度表示(.freshness)と同じ控えめな見た目にする */
.header-freshness {
  font-size: 0.85rem;
  color: var(--text-muted);
  text-align: right;
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
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.9rem;
  background: var(--bg);
  color: var(--text);
}

input.wide {
  width: 320px;
}

input.narrow {
  width: 120px;
}

/* カスタム属性の絞り込み(折りたたみ) */
.cf-filters {
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 0.75rem;
  background: var(--bg-muted);
}

.cf-filters > summary {
  cursor: pointer;
  font-size: 0.9rem;
  font-weight: 600;
}

.cf-filters[open] > summary {
  margin-bottom: 0.75rem;
}

.cf-count {
  font-weight: 400;
  color: var(--accent-fg);
}

/* 属性名は幅を揃えて、入力欄の左端を縦に並べる */
.cf-row > label {
  min-width: 10rem;
}

input:disabled,
select:disabled {
  background: var(--bg-muted);
  color: var(--text-faint);
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0 0 0.75rem;
}

.hint.warn {
  color: var(--warning-text);
}

/* 文中に置く軽量なアクション(カスタム属性取得の再試行) */
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

/* ページャ(結果テーブルの下)。ボタン・入力欄の見た目は既存のものを流用する */
.pager {
  margin-top: 0.5rem;
  margin-bottom: 0.5rem;
  gap: 0.35rem;
}

/* .row > label の幅指定(6rem)を打ち消して、ページ番号欄の左に詰める */
.pager > label.pager-label {
  min-width: auto;
  margin-left: 0.5rem;
}

.page-input {
  width: 4rem;
  text-align: right;
  /* 桁数が変わっても幅がぶれないようにする */
  font-variant-numeric: tabular-nums;
}

.pager-total {
  font-size: 0.85rem;
  color: var(--text-muted);
  margin-right: 0.5rem;
}

/* 課題キー(クリックで URL をコピー)。button.link と同じ「文中のアクション」の見た目 */
button.issue-key {
  border: none;
  background: none;
  padding: 0;
  font-size: inherit;
  font-family: inherit;
  color: var(--accent-fg);
  cursor: pointer;
  text-decoration: underline;
}

/* 上の button:hover の背景(灰色のボタン面)が付かないよう打ち消す */
button.issue-key:hover {
  background: none;
  color: var(--accent-fg-hover);
}

/* 課題 URL コピーのアイコンボタン(課題キーの右隣に常時表示)。
   ボタン面(枠・背景)を消して、アイコンだけが並ぶようにする */
button.copy-icon {
  border: none;
  background: none;
  padding: 0;
  margin-left: 0.35rem;
  color: var(--text-faint);
  cursor: pointer;
  /* 行の文字とベースラインをそろえる(1 行の高さを変えない) */
  vertical-align: -0.15em;
  line-height: 0;
}

button.copy-icon:hover:not(:disabled) {
  background: none;
  color: var(--accent-fg);
}

/* ---- 課題詳細のポップアップ ---- */

/* 配色・重なり順は SettingsView の削除確認ダイアログに合わせる */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  padding: 1rem;
  box-sizing: border-box;
}

.modal {
  background: var(--surface);
  border-radius: 6px;
  padding: 1.25rem 1.5rem;
  width: min(720px, 92vw);
  /* 長い課題でもウインドウから溢れないよう、中身をスクロールさせる */
  max-height: 85vh;
  overflow: auto;
  box-shadow: 0 8px 24px var(--shadow);
  font-size: 0.9rem;
}

.detail-title {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin: 0 0 0.75rem;
}

.detail-key {
  font-family: monospace;
  color: var(--text-muted);
}

.detail-summary {
  font-size: 1.05rem;
}

.detail-section {
  font-size: 0.9rem;
  margin: 1rem 0 0.4rem;
}

/* 項目名と値の 2 列。項目名の幅は内容に合わせ、値だけを伸ばす */
.detail-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  column-gap: 0.75rem;
  row-gap: 0.3rem;
  margin: 0;
}

.detail-grid dt {
  font-weight: 600;
  color: var(--text-muted);
}

.detail-grid dd {
  margin: 0;
  word-break: break-word;
}

/* 詳細本文は改行・空白を保ったまま折り返す(長文はスクロール) */
.detail-description {
  margin: 0;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-muted);
  font-family: inherit;
  font-size: 0.85rem;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}

.detail-note {
  margin: 0.75rem 0 0;
}

/* コメントの注意書き(最上部)。タイトルの前に置くため下側にだけ余白を取る */
.comment-note {
  margin: 0 0 0.75rem;
}

/* コメント一覧。件数が多い課題でもポップアップが伸び続けないよう、
   この領域だけを高さ上限付きでスクロールさせる */
.comment-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 16rem;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: 4px;
}

.comment {
  padding: 0.5rem 0.75rem;
}

.comment + .comment {
  border-top: 1px solid var(--border);
}

.comment-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin: 0 0 0.25rem;
  font-size: 0.8rem;
  color: var(--text-muted);
}

.comment-author {
  font-weight: 600;
}

/* 本文は改行・空白を保ったまま折り返す(詳細本文と同じ扱い) */
.comment-body {
  margin: 0;
  font-family: inherit;
  font-size: 0.85rem;
  white-space: pre-wrap;
  word-break: break-word;
}

.detail-error {
  margin: 0.75rem 0 0;
}

.detail-buttons {
  margin-top: 1rem;
  margin-bottom: 0;
}

/* コピー成功のトースト(数秒で自動的に消える)。
   position: fixed のためテーブルの列幅・スクロール位置に影響しない。
   配色は既存の成功表示(.result.ok)に合わせる */
.copy-toast {
  position: fixed;
  left: 50%;
  bottom: 2rem;
  transform: translateX(-50%);
  margin: 0;
  padding: 0.5rem 1rem;
  border: 1px solid var(--success-border);
  border-radius: 999px;
  background: var(--success-bg);
  color: var(--success-text);
  font-size: 0.85rem;
  white-space: nowrap;
  box-shadow: 0 4px 12px var(--shadow-subtle);
  /* 表の固定ヘッダ(z-index: 1)と課題詳細のポップアップ(z-index: 100)より
     手前に出す(ポップアップの「URL をコピー」の結果が隠れないように)。
     クリックは下の要素へ通す */
  z-index: 200;
  pointer-events: none;
}

/* 表示・消滅のフェード(消える瞬間が唐突にならないように) */
.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.2s ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
}

/* 動きを抑える設定(App.vue のサイドバーと同じ流儀)ではフェードしない */
@media (prefers-reduced-motion: reduce) {
  .toast-enter-active,
  .toast-leave-active {
    transition: none;
  }
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
