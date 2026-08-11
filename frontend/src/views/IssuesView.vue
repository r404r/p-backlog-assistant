<script lang="ts" setup>
// 課題抽出画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  customColumnKey,
  getBackend,
  isMockBackend,
  type CustomFieldDef,
  type CustomFieldFilter,
  type IssueQuery,
  type IssueRow,
  type Project,
  type SyncMode,
  type SyncResult,
} from '../lib/backend'
import {
  resolveProjectSelection,
  restoreProjectSelection,
  selectedProjectId,
  useProjectSelectionGuard,
} from '../lib/projectSelection'

const backend = getBackend()
const mock = isMockBackend()

/**
 * 破棄済み・プロファイル切替後の画面が、後から届いた古い応答で
 * 共有のプロジェクト選択を書き換えてしまうのを防ぐガード(高 1)。
 */
const selectionGuard = useProjectSelectionGuard()

/** 画面に表示する最大件数(Excel 出力は条件に一致する全件が対象) */
const PREVIEW_LIMIT = 200

/** Excel 出力の列(key は Go 側 export パッケージの列キー) */
interface ExportColumn {
  key: string
  label: string
}

/**
 * カスタム属性の型 ID(Go 側 customfield の定数と対)。
 * 絞り込み UI の入力方法(テキスト / 範囲 / 選択肢)の切り替えに使う。
 */
const CF_TYPE_NUMERIC = 3
const CF_TYPE_DATE = 4
const CF_LIST_TYPE_IDS = [5, 6, 7, 8]

/**
 * 固定の出力列(キーは Go 側 export パッケージの列キーと対応)。
 *
 * 親課題キー(parentIssueKey)は Excel 出力専用の列で、一覧表示は行わない
 * (IssueRow は持たない)。親課題 ID → 課題キーの引き当てはローカル DB の
 * 走査を伴うため、選択されたときだけ Go 側で解決する。
 */
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
  { key: 'parentIssueKey', label: '親課題キー' },
]

/** 既定でチェックを入れる列(親課題キーは選択式のため既定では出力しない) */
const DEFAULT_EXPORT_COLUMN_KEYS = FIXED_EXPORT_COLUMNS.filter(
  (c) => c.key !== 'parentIssueKey',
).map((c) => c.key)

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
// プロジェクト選択は画面をまたいで共有する(サイドバー切替で破棄されないよう
// projectSelection モジュールが保持し、プロファイルごとに localStorage へ保存する)
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
  const token = selectionGuard.begin()
  projectsLoading.value = true
  globalError.value = ''
  try {
    const list = await backend.listProjects(profileId.value)
    // 画面が破棄済み、またはプロファイルが切り替わっていたら反映しない
    // (古い応答で共有のプロジェクト選択を書き換えないため)
    if (!selectionGuard.isCurrent(token)) return
    projects.value = list
    // 復元した(または選択中の)プロジェクトが一覧に無ければ先頭へフォールバックする
    selectedProjectId.value = resolveProjectSelection(projects.value, selectedProjectId.value)
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
  try {
    profileId.value = await backend.getActiveProfile()
  } catch (e) {
    globalError.value = `接続先プロファイルの取得に失敗しました: ${errorMessage(e)}`
  } finally {
    initializing.value = false
  }
  // getActiveProfile の待機中にアンマウントされていたら、共有状態には触れない(高 1)。
  // 触ると、既に別プロファイルで表示中の新しい画面の選択を古いプロファイルへ
  // 巻き戻してしまう。この時点ではプロファイルが未確定でトークン照合ができないため、
  // 生存確認のみを行う(画面は同時に 1 つしか表示されないため、生存 = 現在の画面)。
  if (!profileId.value || !selectionGuard.isAlive()) return
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
  customFields.value.map((f) => ({ key: customColumnKey(f.id), label: f.name })),
)

/** カスタム属性の取得失敗の表示用メッセージ(空 = 正常) */
const customFieldsError = ref('')

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
  customFieldsError.value = ''
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
    customFieldsError.value =
      'カスタム属性の取得に失敗しました(固定列は検索・出力できます): ' +
      (e instanceof Error ? e.message : String(e))
  }
}

// ---------------------------------------------------------------------------
// 条件フォーム
// ---------------------------------------------------------------------------

const cond = reactive({
  keyword: '',
  /** 複数キーワードの連結方法(既定は AND = すべて含む) */
  keywordMode: 'and' as 'and' | 'or',
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
    globalError.value = `絞り込み候補の取得に失敗しました: ${errorMessage(e)}`
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
  rows.value = []
  total.value = 0
  unverifiable.value = 0
  // 表示中のカスタム属性列は前のプロジェクトの定義なので一緒に片付ける
  shownCustomColumns.value = []
  searched.value = false
  searchError.value = ''
  syncResult.value = null
  syncError.value = ''
  exportPath.value = ''
  exportUnverifiable.value = 0
  exportCanceled.value = false
  exportError.value = ''
  void loadFilterOptions()
  void loadCustomFields()
})

/** 現在の条件を IssueQuery に変換する(空文字の条件は送らない) */
function buildQuery(withLimit: boolean): IssueQuery {
  const q: IssueQuery = { projectId: selectedProjectId.value }
  if (cond.keyword.trim()) {
    q.keyword = cond.keyword.trim()
    // キーワードが空なら連結方法は意味を持たないため送らない
    q.keywordMode = cond.keywordMode
  }
  if (cond.updatedFrom) q.updatedFrom = cond.updatedFrom
  if (cond.updatedTo) q.updatedTo = cond.updatedTo
  if (cond.createdFrom) q.createdFrom = cond.createdFrom
  if (cond.createdTo) q.createdTo = cond.createdTo
  if (cond.statusName) q.statusName = cond.statusName
  if (cond.assigneeName) q.assigneeName = cond.assigneeName
  // 未入力のカスタム属性は送らない(空の条件を送っても Go 側で無視されるが、
  // 送らない方が「カスタム属性条件あり」の 2 段階検索を無駄に起動させない)
  const customFieldFilters = buildCustomFieldFilters()
  if (customFieldFilters.length > 0) q.customFieldFilters = customFieldFilters
  if (withLimit) q.limit = PREVIEW_LIMIT
  return q
}

function clearConditions() {
  cond.keyword = ''
  cond.keywordMode = 'and'
  cond.updatedFrom = ''
  cond.updatedTo = ''
  cond.createdFrom = ''
  cond.createdTo = ''
  cond.statusName = ''
  cond.assigneeName = ''
  resetCustomFieldConditions()
}

// ---------------------------------------------------------------------------
// 検索(ローカル DB)
// ---------------------------------------------------------------------------

const rows = ref<IssueRow[]>([])
const total = ref(0)
const searching = ref(false)
const searched = ref(false)
const searchError = ref('')
/** カスタム属性条件を判定できなかった課題の件数(0 なら警告を出さない) */
const unverifiable = ref(0)

/**
 * search の世代番号(loadFilterOptions / loadCustomFields と同じ流儀。高 1)。
 *
 * プロジェクト A の検索中に B へ切り替えると、watch が結果を片付けた後に
 * A の応答が届き、rows・カスタム属性列・件数を書き戻してしまう。
 * 「最後に開始した要求」の応答だけを反映し、切替時は invalidateSearch で失効させる。
 */
let searchRequestSeq = 0

/** 表示件数が上限で切り詰められているか */
const truncated = computed(() => total.value > rows.value.length)

/** 選択中のカスタム属性列(Excel 出力の列選択と共用) */
const selectedCustomColumns = computed<ExportColumn[]>(() =>
  customColumns.value.filter((c) => selectedColumns.value.includes(c.key)),
)

/**
 * 結果テーブルに表示中のカスタム属性列。
 *
 * 検索した時点の選択を固定する。列選択を変えるたびに見出しだけ増減すると、
 * 取得済みの行に値が無い(空欄の)列が並び、データが無いのか列が増えただけなのか
 * 区別できなくなるため、次の検索まで見出しと値を揃えておく。
 */
const shownCustomColumns = ref<ExportColumn[]>([])

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
  searchRequestSeq++
  syncRequestSeq++
  exportRequestSeq++
}

async function search() {
  if (!selectedProjectId.value || searching.value) return
  const seq = ++searchRequestSeq
  searching.value = true
  searchError.value = ''
  // 値を取得する列は「この検索の時点で選ばれている列」に固定する
  const requestedColumns = selectedCustomColumns.value
  try {
    const res = await backend.searchIssues(
      profileId.value,
      buildQuery(true),
      requestedColumns.map((c) => c.key),
    )
    // より新しい要求が開始済み、またはプロジェクトが切り替わっていたら反映しない
    if (seq !== searchRequestSeq) return
    rows.value = res.rows
    total.value = res.total
    unverifiable.value = res.unverifiable
    shownCustomColumns.value = requestedColumns
    searched.value = true
  } catch (e) {
    if (seq !== searchRequestSeq) return
    searchError.value = `検索に失敗しました: ${errorMessage(e)}`
  } finally {
    // 検索は多重起動しないため、失効済みの応答でもここで必ず下ろす
    searching.value = false
  }
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
// 同期
// ---------------------------------------------------------------------------

// 既定は auto(未同期プロジェクトでは incremental が必ず失敗するため。低 1)
const syncMode = ref<SyncMode>('auto')
const syncing = ref(false)
const syncResult = ref<SyncResult | null>(null)
const syncError = ref('')

/** runSync の世代番号(検索・出力と同じ理由。プロジェクト切替で失効させる) */
let syncRequestSeq = 0

const syncModeLabel = (mode: string) => (mode === 'full' ? 'フル同期' : '差分同期')

async function runSync() {
  if (!selectedProjectId.value || syncing.value) return
  const seq = ++syncRequestSeq
  syncing.value = true
  syncError.value = ''
  syncResult.value = null
  try {
    const result = await backend.syncIssues(
      profileId.value,
      selectedProjectId.value,
      syncMode.value,
    )
    // プロジェクト切替後なら、前のプロジェクトの同期結果は表示しない
    if (seq !== syncRequestSeq) return
    syncResult.value = result
    await loadProjects() // 鮮度表示を更新
    await loadFilterOptions()
  } catch (e) {
    if (seq !== syncRequestSeq) return
    syncError.value = `同期に失敗しました: ${errorMessage(e)}`
  } finally {
    // 同期は多重起動しないため、失効済みの応答でもここで必ず下ろす
    syncing.value = false
  }
}

// ---------------------------------------------------------------------------
// Excel 出力
// ---------------------------------------------------------------------------

/** 出力できる列(固定列 + カスタム属性列) */
const exportColumns = computed<ExportColumn[]>(() => [
  ...FIXED_EXPORT_COLUMNS,
  ...customColumns.value,
])

// 既定はカスタム属性列・親課題キー列オフ
const selectedColumns = ref<string[]>([...DEFAULT_EXPORT_COLUMN_KEYS])

/** 選択済みの列から、現在は選択できない列(切替前のカスタム属性列)を外す */
function pruneUnavailableColumns() {
  const available = new Set(exportColumns.value.map((c) => c.key))
  selectedColumns.value = selectedColumns.value.filter((k) => available.has(k))
}

const exporting = ref(false)
const exportPath = ref('')
const exportRows = ref(0)
const exportUnverifiable = ref(0)
const exportCanceled = ref(false)
const exportError = ref('')

/**
 * exportExcel の世代番号(検索と同じ理由。高 1)。
 * 保存ダイアログを開いている間にプロジェクトを切り替えると、切替後の画面へ
 * 前のプロジェクトの出力結果・エラーが表示されてしまうため。
 */
let exportRequestSeq = 0

const canExport = computed(
  () => !!selectedProjectId.value && selectedColumns.value.length > 0 && !exporting.value,
)

async function exportExcel() {
  if (!canExport.value) return
  const seq = ++exportRequestSeq
  exporting.value = true
  exportError.value = ''
  exportPath.value = ''
  exportUnverifiable.value = 0
  exportCanceled.value = false
  try {
    // 表示上限は付けない(条件に一致する全件を出力する)
    const columns = exportColumns.value
      .filter((c) => selectedColumns.value.includes(c.key))
      .map((c) => c.key)
    const res = await backend.exportIssuesExcel(profileId.value, buildQuery(false), columns)
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
    exportError.value = `Excel 出力に失敗しました: ${errorMessage(e)}`
  } finally {
    // Excel 出力は多重起動しないため、失効済みの応答でもここで必ず下ろす
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
            placeholder="件名 + 詳細の部分一致(スペース区切りで複数指定)"
            @keydown.enter="onKeywordEnter"
          />
        </div>
        <div class="row">
          <label>複数キーワード</label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="and" :disabled="searching" />
            すべて含む(AND)
          </label>
          <label class="radio">
            <input v-model="cond.keywordMode" type="radio" value="or" :disabled="searching" />
            いずれかを含む(OR)
          </label>
        </div>
        <p class="hint">
          キーワード検索はローカル DB に保存された<strong>件名と詳細</strong>に対する部分一致です。
          コメント・添付ファイル等は対象外で、Backlog サイト上のキーワード検索とは範囲が異なります。
          スペース(半角・全角)で区切ると複数キーワードになります(スペースを含む語句そのものの検索はできません)。
          キーワード欄で Enter を押すと検索します。
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

        <!-- カスタム属性の絞り込み(定義があるプロジェクトでのみ表示) -->
        <details
          v-if="customFields.length > 0"
          class="cf-filters"
          :open="cfPanelOpen"
          @toggle="cfPanelOpen = ($event.target as HTMLDetailsElement).open"
        >
          <summary>
            カスタム属性で絞り込む
            <span v-if="customFieldFilterCount > 0" class="cf-count">
              ({{ customFieldFilterCount }} 件指定中)
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
                  placeholder="下限"
                />
                <span>〜</span>
                <input
                  v-model="cfCond[def.id].max"
                  type="number"
                  step="any"
                  class="narrow"
                  placeholder="上限"
                />
              </template>
              <!-- 日付: 範囲 -->
              <template v-else-if="def.typeId === CF_TYPE_DATE">
                <input :id="`i-cf-${def.id}`" v-model="cfCond[def.id].min" type="date" />
                <span>〜</span>
                <input v-model="cfCond[def.id].max" type="date" />
              </template>
              <!-- 文字列・文章(選択肢が取れないリスト系を含む): 部分一致 -->
              <template v-else>
                <input
                  :id="`i-cf-${def.id}`"
                  v-model="cfCond[def.id].text"
                  type="text"
                  class="wide"
                  placeholder="部分一致"
                />
              </template>
            </template>
          </div>

          <p class="hint">
            複数のカスタム属性を指定した場合はすべてを満たす課題(AND)が対象です。
            選択肢はいずれか 1 つに一致すれば対象になります。値が未入力の課題は、
            範囲や部分一致を指定した属性では対象外になります。
            絞り込みは同期済みのローカルデータに対して行われるため、
            件数の多いプロジェクトでは他の条件より時間がかかることがあります。
          </p>
        </details>

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

        <p v-if="unverifiable > 0" class="notice warn">
          {{ unverifiable }} 件はローカルの課題データが古く、カスタム属性の条件を判定できませんでした
          (上の該当件数には含まれていません)。フル同期を実行すると解消します。
        </p>

        <p v-if="customColumnsOutOfDate" class="hint warn">
          カスタム属性の列選択が変わりました。表示に反映するには再度検索してください。
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
                <!-- カスタム属性列は「Excel 出力」の列選択に連動する -->
                <th v-for="c in shownCustomColumns" :key="c.key">{{ c.label }}</th>
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
                <td v-for="c in shownCustomColumns" :key="c.key">{{ r.customFields[c.key] }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <p v-if="customColumns.length > 0" class="hint">
          一覧に表示するカスタム属性は、下の「Excel 出力」で選んだ列に連動します。
        </p>
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
          <p class="hint">
            カスタム属性(既定では出力しません)。ここで選んだ列は検索結果の一覧にも表示されます。
          </p>
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
          <p v-if="exportUnverifiable > 0" class="warnings">
            {{ exportUnverifiable }} 件はローカルの課題データが古く、カスタム属性の条件を判定できず
            出力に含まれていません。フル同期を実行すると解消します。
          </p>
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

input.narrow {
  width: 120px;
}

/* カスタム属性の絞り込み(折りたたみ) */
.cf-filters {
  border: 1px solid #d0d7de;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  margin-bottom: 0.75rem;
  background: #f6f8fa;
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
  color: #0b5cad;
}

/* 属性名は幅を揃えて、入力欄の左端を縦に並べる */
.cf-row > label {
  min-width: 10rem;
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
