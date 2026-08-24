// Wails外（vite dev・テスト）で使う状態付き開発backend。
// 疑似データとBackend実装だけを所有し、公開経路は../backend.tsへ委ねる。

import type {
  Backend,
  BulkJobRow,
  BulkJobRowDetail,
  BulkPreviewRow,
  BulkProgress,
  BulkValidationError,
  ConnectionTestResult,
  CustomFieldFilter,
  ExportColumn,
  IssueComment,
  IssueDetail,
  IssueQuery,
  IssueRow,
  MasterData,
  Profile,
  Project,
  SyncProgress,
  SyncMode,
  SyncPhase,
  SyncStateRow,
  UserQuery,
  UserRow,
} from './contract'
import {
  ACTION_LABELS,
  CUSTOM_COLUMN_PREFIX,
  customColumnKey,
  rowStatusLabel,
} from './shared'

const URL_PATTERN = /^https:\/\/[a-z0-9][a-z0-9-]*\.backlog\.(jp|com)\/?$/i

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

// --- モック用ダミーデータ(実在のスペース・プロジェクト・課題は含まない) ---

const MOCK_STATUSES = ['未対応', '処理中', '処理済み', '完了']
const MOCK_ASSIGNEES = ['モック 太郎', 'モック 花子', 'モック 次郎', '']
const MOCK_ISSUE_TYPES = ['タスク', 'バグ', '要望']
const MOCK_PRIORITIES = ['高', '中', '低']
const MOCK_SUMMARY_WORDS = ['ログイン', '一覧表示', 'CSV 取り込み', '通知メール', '権限チェック']
// カスタム属性(文字列)のサンプル値。実在の取引先は含まない。
// 全角・半角の混在を含め、正規化つき部分一致の動作を手元で確認できるようにする
const MOCK_CUSTOMERS = ['モック商事', 'ＡＢＣ工業', 'ABC商会', 'サンプル製作所']

/**
 * モック用の課題抽出の列定義(Go 側 internal/export/issue.go の列定義に合わせる)。
 * Wails 実行時は Go から供給されるため使われない(モックは Wails 外の
 * 画面確認専用で、契約と同様に手書きになる)。
 */
const MOCK_ISSUE_EXPORT_COLUMNS: ExportColumn[] = [
  { key: 'issueKey', label: 'キー', byDefault: true },
  { key: 'summary', label: '件名', byDefault: true },
  { key: 'statusName', label: '状態', byDefault: true },
  { key: 'assigneeName', label: '担当者', byDefault: true },
  { key: 'issueTypeName', label: '種別', byDefault: true },
  { key: 'priorityName', label: '優先度', byDefault: true },
  { key: 'created', label: '作成日時', byDefault: true },
  { key: 'updated', label: '更新日時', byDefault: true },
  { key: 'dueDate', label: '期限', byDefault: true },
  { key: 'parentIssueKey', label: '親課題キー', byDefault: false },
]

/** モック用のユーザ抽出の列定義(Go 側 internal/export/user.go の列定義に合わせる) */
const MOCK_USER_EXPORT_COLUMNS: ExportColumn[] = [
  { key: 'userCode', label: 'ユーザID', byDefault: true },
  { key: 'name', label: '名前', byDefault: true },
  { key: 'mailAddress', label: 'メールアドレス', byDefault: true },
  { key: 'roleName', label: 'ロール', byDefault: true },
  { key: 'roleType', label: 'ロール値', byDefault: false },
  { key: 'teamNames', label: '所属チーム', byDefault: true },
  { key: 'projectKeys', label: '参加プロジェクト', byDefault: true },
  { key: 'adminProjectKeys', label: '管理者プロジェクト', byDefault: true },
]

const MOCK_PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル開発プロジェクト', lastSyncedAt: '', syncStateUnknown: false },
  { id: 102, projectKey: 'DEMO', name: 'デモ運用プロジェクト', lastSyncedAt: '', syncStateUnknown: false },
  { id: 103, projectKey: 'TRIAL', name: '検証用プロジェクト', lastSyncedAt: '', syncStateUnknown: false },
]

/** モック用のダミーユーザ(実在の氏名・メールアドレスは含まない) */
const MOCK_USERS: UserRow[] = [
  {
    id: 12345,
    userCode: 'mock.taro',
    name: 'モック 太郎',
    mailAddress: 'mock.taro@example.invalid',
    roleType: 1,
    roleName: '管理者',
    teamNames: ['開発チーム', '運用チーム'],
    projectKeys: ['SAMPLE', 'DEMO', 'TRIAL'],
    adminProjectKeys: ['SAMPLE', 'DEMO'],
  },
  {
    id: 12346,
    userCode: 'mock.hanako',
    name: 'モック 花子',
    mailAddress: 'mock.hanako@example.invalid',
    roleType: 2,
    roleName: '一般ユーザ',
    teamNames: ['開発チーム'],
    projectKeys: ['SAMPLE'],
    adminProjectKeys: [],
  },
  {
    id: 12347,
    userCode: 'mock.jiro',
    name: 'モック 次郎',
    mailAddress: 'mock.jiro@example.invalid',
    roleType: 2,
    roleName: '一般ユーザ',
    teamNames: [],
    projectKeys: ['DEMO', 'TRIAL'],
    adminProjectKeys: ['TRIAL'],
  },
  {
    id: 12348,
    userCode: 'mock.saburo',
    name: 'モック 三郎',
    mailAddress: 'mock.saburo@example.invalid',
    roleType: 3,
    roleName: 'レポーター',
    teamNames: ['運用チーム'],
    projectKeys: ['DEMO'],
    adminProjectKeys: [],
  },
  {
    id: 12349,
    userCode: 'mock.shiro',
    name: 'モック 四郎',
    mailAddress: 'mock.shiro@example.invalid',
    roleType: 4,
    roleName: 'ビューアー',
    teamNames: [],
    projectKeys: [],
    adminProjectKeys: [],
  },
]

/** モックのローカル検索(Go 側の部分一致検索に相当する簡易版) */
function filterMockUsers(rows: UserRow[], query: UserQuery): UserRow[] {
  const keyword = (query.keyword ?? '').trim().toLowerCase()
  return rows.filter((u) => {
    if (query.roleType && u.roleType !== query.roleType) return false
    if (!keyword) return true
    return (
      u.name.toLowerCase().includes(keyword) ||
      u.userCode.toLowerCase().includes(keyword) ||
      u.mailAddress.toLowerCase().includes(keyword)
    )
  })
}

/** 日付を YYYY-MM-DD 形式で返す */
function ymd(d: Date): string {
  return d.toISOString().slice(0, 10)
}

/** カスタム属性の型 ID(モックの比較方法の切り替えに使う。Go 側 customfield の定数と対) */
const CF_TYPE_NUMERIC = 3

/**
 * モック課題のカスタム属性値(表示文字列)を決定的に組み立てる。
 *
 * 50 件に 1 件は空にして「ローカルの課題データが古く、カスタム属性を判定できない」
 * 課題(Go 側では raw_json が空・破損の行)を再現する。判定不能件数の警告表示を
 * Wails ランタイム外でも手動確認できるようにするため。
 */
function buildMockCustomFields(i: number, dueDate: string): Record<string, string> {
  if (i % 50 === 49) return {}
  const impact = MOCK_MASTER.customFields.find((f) => f.id === 3004)?.items ?? []
  const envs = MOCK_MASTER.customFields.find((f) => f.id === 3005)?.items ?? []
  return {
    cf_3001: MOCK_CUSTOMERS[i % MOCK_CUSTOMERS.length],
    // 数値は Go 側 FormatValue と同じく最短表現の数値文字列
    cf_3002: String(((i % 8) + 1) * 2.5),
    // 日付は yyyy-MM-dd(期限が空の課題は未入力にして「値なし」も再現する)
    cf_3003: dueDate,
    cf_3004: impact.length > 0 ? impact[i % impact.length].name : '',
    // 複数リストは選択肢名の ", " 区切り(Go 側 FormatValue と同じ規約)
    cf_3005: envs
      .filter((_, n) => (i + n) % 3 !== 0)
      .map((it) => it.name)
      .join(', '),
  }
}

/** 決定的にダミー課題を生成する(モック専用) */
function buildMockIssues(project: Project, count: number): IssueRow[] {
  const rows: IssueRow[] = []
  const base = Date.now()
  for (let i = 0; i < count; i += 1) {
    const created = new Date(base - (count - i) * 6 * 3600 * 1000)
    const updated = new Date(created.getTime() + ((i % 7) + 1) * 3600 * 1000)
    const due = new Date(updated.getTime() + ((i % 11) + 1) * 24 * 3600 * 1000)
    const dueDate = i % 3 === 0 ? '' : ymd(due)
    rows.push({
      issueKey: `${project.projectKey}-${i + 1}`,
      summary: `${MOCK_SUMMARY_WORDS[i % MOCK_SUMMARY_WORDS.length]}の対応 #${i + 1}`,
      statusName: MOCK_STATUSES[i % MOCK_STATUSES.length],
      assigneeName: MOCK_ASSIGNEES[i % MOCK_ASSIGNEES.length],
      issueTypeName: MOCK_ISSUE_TYPES[i % MOCK_ISSUE_TYPES.length],
      priorityName: MOCK_PRIORITIES[i % MOCK_PRIORITIES.length],
      created: created.toISOString(),
      updated: updated.toISOString(),
      dueDate,
      customFields: buildMockCustomFields(i, dueDate),
    })
  }
  return rows
}

/**
 * モック用: 「最新の状態を取得」で付ける件名の印。
 * 何度押しても増えないよう、既に付いていれば付け直さない。
 */
const MOCK_REFRESHED_SUFFIX = '(更新済み)'

/** モック用: 1 課題ぶんのコメント取得結果(Go 側の DTO と同じ形) */
interface MockCommentState {
  comments: IssueComment[]
  fetchedAt: string
  historyOnly: number
  truncated: boolean
}

/**
 * モック用: 課題キーからコメントを組み立てる。
 *
 * 画面の確認に必要な状態を課題番号で作り分ける:
 *   - 10 の倍数: 取得上限に達した課題(「以前のコメントは Backlog で確認」の表示)
 *   - それ以外 : 本文ありのコメント数件 + 変更履歴のみの項目数件
 */
function buildMockComments(issueKey: string, at: string): MockCommentState {
  const n = Number(issueKey.slice(issueKey.lastIndexOf('-') + 1))
  const truncated = Number.isInteger(n) && n > 0 && n % 10 === 0
  const count = truncated ? 5 : 3
  const base = Date.parse(at)
  const authors = ['山田 太郎', '佐藤 花子', '鈴木 一郎']
  const comments: IssueComment[] = []
  for (let i = 0; i < count; i += 1) {
    comments.push({
      authorName: authors[i % authors.length],
      content:
        i === 0
          ? `${issueKey} のコメント(モックデータ)。\n複数行の本文も折り返して表示されます。`
          : `${issueKey} への返信 ${count - i}(モックデータ)。`,
      // 新しい順に並べる(1 件ごとに 1 時間ずつ古くする)
      created: new Date(base - i * 3600 * 1000).toISOString(),
    })
  }
  // 状態変更等、本文を持たない項目は件数だけを伝える
  return { comments, fetchedAt: at, historyOnly: truncated ? 12 : 2, truncated }
}

/**
 * モック用: 課題詳細に出す本文(改行を含む複数行)。
 * 詳細ポップアップの折り返し・スクロールを Wails 外でも確認できるようにする。
 */
function mockDescription(row: IssueRow): string {
  return [
    `${row.summary} の詳細(モックデータ)。`,
    '',
    '再現手順:',
    '1. サンプル画面を開く',
    '2. 入力欄に値を入れて保存する',
    '3. 一覧に戻ると反映されていない',
    '',
    '※ これはモック用のダミー本文であり、実在の課題ではありません。',
  ].join('\n')
}

/**
 * モック用: 課題キーから親課題の表記を決める(Go 側の CF5 と同じ形)。
 *
 * 5 の倍数は 1 つ前の課題を親に、7 の倍数はローカルに無い親(ID:<数値>)にして、
 * 「課題キー表示」「ID 表示」「親なし」の 3 通りを手元で確認できるようにする。
 */
function mockParentIssueKey(issueKey: string): string {
  const sep = issueKey.lastIndexOf('-')
  const n = Number(issueKey.slice(sep + 1))
  if (sep < 0 || !Number.isInteger(n)) return ''
  if (n % 7 === 0) return 'ID:999999'
  if (n % 5 === 0 && n > 1) return `${issueKey.slice(0, sep)}-${n - 1}`
  return ''
}

/**
 * モック用: リスト系の表示文字列(選択肢名の ", " 区切り)を選択肢 ID へ戻す。
 * Go 側は生 JSON から ID を直接取り出すが、モックは表示文字列しか持たないため
 * マスタの選択肢名から引き当てる(既知の簡易化)。
 */
function mockItemIdsOf(defId: number, display: string): number[] {
  if (display === '') return []
  const def = MOCK_MASTER.customFields.find((f) => f.id === defId)
  if (!def) return []
  const names = display.split(', ')
  return def.items.filter((it) => names.includes(it.name)).map((it) => it.id)
}

/**
 * モック用: カスタム属性 1 条件を課題 1 件に適用する
 * (Go 側 customfield.MatchValues の簡易版。条件内の複数指定は AND)。
 */
function matchMockCustomField(row: IssueRow, f: CustomFieldFilter): boolean {
  // Go 側と同じく、表示文字列に対して判定する
  const display = row.customFields[customColumnKey(f.defId)] ?? ''
  const normalize = (s: string) => s.normalize('NFKC').toLowerCase()
  const text = (f.text ?? '').trim()
  if (text !== '' && !normalize(display).includes(normalize(text))) return false
  if (f.min || f.max) {
    // 未入力の値は、上限だけの条件でも一致させない(Go 側と同じ)
    if (display === '') return false
    if (f.typeId === CF_TYPE_NUMERIC) {
      const n = Number(display)
      if (Number.isNaN(n)) return false
      if (f.min && n < Number(f.min)) return false
      if (f.max && n > Number(f.max)) return false
    } else {
      // 日付(YYYY-MM-DD)は桁が揃っているため辞書順 = 時系列順
      if (f.min && display < f.min) return false
      if (f.max && display > f.max) return false
    }
  }
  if (f.itemIds && f.itemIds.length > 0) {
    const selected = mockItemIdsOf(f.defId, display)
    if (!f.itemIds.some((id) => selected.includes(id))) return false
  }
  return true
}

/** モック検索の結果(Go 側と同じく、判定できなかった件数も返す) */
interface MockFilterResult {
  rows: IssueRow[]
  /** カスタム属性を判定できなかった課題の件数(モックでは値を持たない課題) */
  unverifiable: number
}

/**
 * モックのローカル検索(Go 側の LIKE 部分一致に相当する簡易版)。
 * Go 側は課題キー + 件名 + 詳細の正規化テキストを検索するが、モックデータは
 * 詳細を持たないため課題キーと件名のみを対象とする(既知の簡易化)。
 */
function filterMockIssues(rows: IssueRow[], query: IssueQuery): MockFilterResult {
  // Go 側(NFKC + ケースフォールド)に近づけた正規化。
  // toLowerCase は完全な Unicode ケースフォールドではない(ß ≠ ss 等)が、
  // モック(開発時のみ・日本語サンプルデータ)では十分な近似とする(既知の簡易化)
  const normalize = (s: string) => s.normalize('NFKC').toLowerCase()
  // Go 側と同じ規則で空白(全角スペースを含む)区切りの複数語に分割する
  const terms = (query.keyword ?? '')
    .split(/\s+/)
    .filter((t) => t !== '')
    .map(normalize)
  const orMode = query.keywordMode === 'or'
  // 条件が空のものは Go 側(ActiveFilters)と同じく無視する
  const customFilters = (query.customFieldFilters ?? []).filter(
    (f) => (f.text ?? '').trim() !== '' || f.min || f.max || (f.itemIds?.length ?? 0) > 0,
  )
  let unverifiable = 0
  const matched = rows.filter((r) => {
    if (terms.length > 0) {
      // Go 側の search_text と同じ並び(課題キー + 件名。詳細はモックに無い)
      const text = normalize(`${r.issueKey}\n${r.summary}`)
      const hit = orMode
        ? terms.some((t) => text.includes(t))
        : terms.every((t) => text.includes(t))
      if (!hit) return false
    }
    if (query.updatedFrom && r.updated.slice(0, 10) < query.updatedFrom) return false
    if (query.updatedTo && r.updated.slice(0, 10) > query.updatedTo) return false
    if (query.createdFrom && r.created.slice(0, 10) < query.createdFrom) return false
    if (query.createdTo && r.created.slice(0, 10) > query.createdTo) return false
    if (query.statusName && r.statusName !== query.statusName) return false
    if (query.assigneeName && r.assigneeName !== query.assigneeName) return false
    if (customFilters.length > 0) {
      // 値を持たない課題は判定できない。結果には含めず件数だけ数える(Go 側と同じ)
      if (Object.keys(r.customFields).length === 0) {
        unverifiable += 1
        return false
      }
      // カスタム属性条件は AND(Go 側の 2 段階検索と同じ結果になるようにする)
      if (!customFilters.every((f) => matchMockCustomField(r, f))) return false
    }
    return true
  })
  return { rows: matched, unverifiable }
}

/**
 * モック用: 要求された列(cf_{定義ID})のカスタム属性だけを残す。
 * Go 側も要求された列しか詰めないため、モックでも同じ形にして
 * 「列を選んでいないのに表示される」といった食い違いを防ぐ。
 */
function pickMockCustomFields(
  values: Record<string, string>,
  columns: string[] | undefined,
): Record<string, string> {
  const out: Record<string, string> = {}
  for (const key of columns ?? []) {
    if (!key.startsWith(CUSTOM_COLUMN_PREFIX)) continue
    out[key] = values[key] ?? ''
  }
  return out
}

/** モック用のマスタデータ(実在のプロジェクト設定は含まない) */
const MOCK_MASTER: MasterData = {
  issueTypes: [
    { id: 1001, name: 'タスク' },
    { id: 1002, name: 'バグ' },
    { id: 1003, name: '要望' },
  ],
  priorities: [
    { id: 2, name: '高' },
    { id: 3, name: '中' },
    { id: 4, name: '低' },
  ],
  statuses: [
    { id: 1, name: '未対応' },
    { id: 2, name: '処理中' },
    { id: 3, name: '処理済み' },
    { id: 4, name: '完了' },
  ],
  // 代表的な 5 型のカスタム属性(実在のプロジェクト設定は含まない)
  customFields: [
    {
      id: 3001,
      typeId: 1,
      typeName: '文字列',
      name: '顧客名',
      description: '取引先の名称',
      required: false,
      applicableIssueTypes: [],
      allowInput: false,
      allowAddItem: false,
      items: [],
    },
    {
      id: 3002,
      typeId: 3,
      typeName: '数値',
      name: '見積工数(時間)',
      description: '',
      required: false,
      applicableIssueTypes: [1001],
      allowInput: false,
      allowAddItem: false,
      items: [],
    },
    {
      id: 3003,
      typeId: 4,
      typeName: '日付',
      name: 'リリース予定日',
      description: '',
      required: false,
      applicableIssueTypes: [],
      allowInput: false,
      allowAddItem: false,
      items: [],
    },
    {
      id: 3004,
      typeId: 5,
      typeName: '単一リスト',
      name: '影響範囲',
      description: '',
      required: true,
      applicableIssueTypes: [1002],
      allowInput: true,
      allowAddItem: false,
      items: [
        { id: 30041, name: '軽微' },
        { id: 30042, name: '中程度' },
        { id: 30043, name: '重大' },
      ],
    },
    {
      id: 3005,
      typeId: 6,
      typeName: '複数リスト',
      name: '対象環境',
      description: '',
      required: false,
      applicableIssueTypes: [],
      allowInput: false,
      allowAddItem: false,
      items: [
        { id: 30051, name: 'Windows' },
        { id: 30052, name: 'macOS' },
        { id: 30053, name: 'Linux' },
      ],
    },
  ],
}

// --- モックの進捗イベント配信 ---------------------------------------------
// Wails ランタイム外では window.runtime が無く EventsOn を使えないため、
// モック実行時のみ、この簡易エミッタ経由で 'bulk:progress' / 'sync:progress'
// 相当を配信する(画面側は onBulkProgress / onSyncProgress を呼ぶだけで、
// どちらの経路かを意識しない)。

type BulkProgressCallback = (p: BulkProgress) => void

const mockProgressListeners = new Set<BulkProgressCallback>()

export function onMockBulkProgress(cb: BulkProgressCallback): () => void {
  mockProgressListeners.add(cb)
  return () => mockProgressListeners.delete(cb)
}

function emitMockProgress(p: BulkProgress): void {
  for (const cb of mockProgressListeners) cb(p)
}

type SyncProgressCallback = (p: SyncProgress) => void

const mockSyncProgressListeners = new Set<SyncProgressCallback>()

export function onMockSyncProgress(cb: SyncProgressCallback): () => void {
  mockSyncProgressListeners.add(cb)
  return () => mockSyncProgressListeners.delete(cb)
}

function emitMockSyncProgress(p: SyncProgress): void {
  for (const cb of mockSyncProgressListeners) cb(p)
}

export function createMockBackend(): Backend {
  // メモリ上のみ。リロードで消える。
  const profiles: Profile[] = []
  const keys = new Map<string, string>() // profileId -> apiKey(モック用)
  let seq = 0
  let activeId = ''

  // 課題抽出・同期のモック状態。プロジェクト 101 のみ「同期済み」の初期状態とし、
  // 他は未同期にして「未同期プロジェクトの導線」も確認できるようにする。
  const projects: Project[] = MOCK_PROJECTS.map((p) => ({ ...p }))
  const users: UserRow[] = MOCK_USERS.map((u) => ({ ...u }))
  const issuesByProject = new Map<number, IssueRow[]>()
  const syncState: SyncStateRow[] = []
  /**
   * 課題単位の取得時刻(`{プロジェクトID}:{課題キー}` -> RFC3339)。
   * 「最新の状態を取得」で 1 件だけ取り込み直した課題は、プロジェクトの
   * 最終同期時刻より新しくなるため(Go 側の fetched_at 相当)。
   */
  const issueFetchedAt = new Map<string, string>()
  /**
   * 課題単位のコメント取得結果(`{プロジェクトID}:{課題キー}` -> 状態)。
   * コメントは同期では取得されないため、「最新の状態を取得」を実行した
   * 課題にだけ入る(未実行の課題はキーが無い = 未取得)。
   */
  const issueComments = new Map<string, MockCommentState>()

  // 一括更新のモック状態。ジョブは新しい順に保持する。
  const jobs: BulkJobRow[] = []
  /**
   * ジョブ ID -> 行明細(履歴の展開表示・結果レポートの確認用)。
   * 表示名(statusLabel)は状態が実行中に変わるため保持せず、取得時に解決する。
   */
  const jobRows = new Map<number, Omit<BulkJobRowDetail, 'statusLabel'>[]>()
  const canceledJobs = new Set<number>()
  let jobSeq = 0
  // 1 回目の取り込みは検証エラーあり、2 回目以降はエラー無しにして、
  // 「実行できない状態」と「実行できる状態」の両方を手動確認できるようにする。
  let importSeq = 0

  /** 課題単位の取得時刻のキー */
  function issueFetchedAtKey(projectId: number, issueKey: string): string {
    return `${projectId}:${issueKey}`
  }

  /**
   * モックの課題詳細を組み立てる(getIssueDetail / refreshIssueDetail 共通)。
   * ローカルに無い課題は Go 側と同じく明確なエラーにする。
   */
  function buildIssueDetail(projectId: number, issueKey: string): IssueDetail {
    const all = issuesByProject.get(projectId) ?? []
    const row = all.find((r) => r.issueKey === issueKey)
    if (!row) {
      throw new Error('課題がローカルに見つかりません(同期後に削除されたか、まだ同期されていません)')
    }
    // 未取得の課題は空の状態(fetchedAt が空文字)を返す
    const cm: MockCommentState = issueComments.get(issueFetchedAtKey(projectId, issueKey)) ?? {
      comments: [],
      fetchedAt: '',
      historyOnly: 0,
      truncated: false,
    }
    return {
      ...row,
      description: mockDescription(row),
      parentIssueKey: mockParentIssueKey(row.issueKey),
      // モックは表示文字列しか持たないため、定義順に並べる
      // (Wails 実行時は課題レスポンスの順になる。既知の簡易化)
      customFields: MOCK_MASTER.customFields
        .filter((def) => customColumnKey(def.id) in row.customFields)
        .map((def) => ({ name: def.name, value: row.customFields[customColumnKey(def.id)] })),
      // 1 件だけ取り込み直した課題はその時刻、それ以外はプロジェクトの最終同期時刻
      fetchedAt:
        issueFetchedAt.get(issueFetchedAtKey(projectId, issueKey)) ??
        syncState.find((s) => s.dataKind === 'issues' && s.projectId === projectId)?.lastSyncedAt ??
        '',
      comments: cm.comments.map((c) => ({ ...c })),
      commentsFetchedAt: cm.fetchedAt,
      commentsHistoryOnly: cm.historyOnly,
      commentsTruncated: cm.truncated,
      // モックは部分失敗を再現しない(Go 側はコメント取得だけ失敗した場合に警告を載せる)
      warnings: [],
    }
  }

  function putSyncState(dataKind: string, projectId: number, at: string) {
    const found = syncState.find((s) => s.dataKind === dataKind && s.projectId === projectId)
    if (found) {
      found.lastSyncedAt = at
      return
    }
    syncState.push({ dataKind, projectId, lastSyncedAt: at })
  }

  {
    const initial = new Date(Date.now() - 90 * 60 * 1000).toISOString()
    projects[0].lastSyncedAt = initial
    issuesByProject.set(projects[0].id, buildMockIssues(projects[0], 342))
    putSyncState('projects', 0, initial)
    putSyncState('users', 0, initial)
    putSyncState('issues', projects[0].id, initial)
  }

  return {
    async listProfiles() {
      await delay(100)
      return profiles.map((p) => ({ ...p }))
    },

    async saveProfile(input) {
      await delay(200)
      if (input.id) {
        const p = profiles.find((x) => x.id === input.id)
        if (!p) throw new Error('プロファイルが見つかりません')
        p.name = input.name
        p.spaceUrl = input.spaceUrl
        if (input.apiKey) keys.set(p.id, input.apiKey)
        p.lastUserName = 'モック 太郎'
        p.lastUserId = 12345
        return { ...p }
      }
      seq += 1
      const p: Profile = {
        id: `mock-${seq}`,
        name: input.name,
        spaceUrl: input.spaceUrl,
        lastUserName: 'モック 太郎',
        lastUserId: 12345,
      }
      profiles.push(p)
      keys.set(p.id, input.apiKey)
      return { ...p }
    },

    async deleteProfile(id) {
      await delay(200)
      const idx = profiles.findIndex((x) => x.id === id)
      if (idx >= 0) profiles.splice(idx, 1)
      keys.delete(id)
      if (activeId === id) activeId = ''
    },

    async testConnection(profileId, spaceUrl, apiKey) {
      await delay(500)
      const fail = (message: string): ConnectionTestResult => ({
        ok: false,
        userId: 0,
        userName: '',
        roleType: 0,
        adminAvailable: false,
        message,
      })
      if (!URL_PATTERN.test(spaceUrl.trim())) {
        return fail('スペース URL は https://<スペース名>.backlog.jp または .backlog.com の形式で入力してください')
      }
      const effectiveKey = apiKey || (profileId ? keys.get(profileId) ?? '' : '')
      if (!effectiveKey) {
        return fail('API キーを入力してください')
      }
      return {
        ok: true,
        userId: 12345,
        userName: 'モック 太郎',
        roleType: 1,
        adminAvailable: true,
        message: '',
      }
    },

    async getPermissionStatus() {
      await delay(100)
      return { adminAvailable: true, degraded: false, message: '管理者機能を利用できます(モック)' }
    },

    async getActiveProfile() {
      await delay(50)
      return profiles.some((p) => p.id === activeId) ? activeId : ''
    },

    async setActiveProfile(id) {
      await delay(50)
      if (id && !profiles.some((p) => p.id === id)) {
        throw new Error('プロファイルが見つかりません')
      }
      activeId = id
    },

    async listProjects() {
      await delay(150)
      return projects.map((p) => ({ ...p }))
    },

    async syncProjects() {
      await delay(600)
      putSyncState('projects', 0, new Date().toISOString())
    },

    async syncIssues(profileId, projectId, mode, runId) {
      const project = projects.find((p) => p.id === projectId)
      if (!project) throw new Error('プロジェクトが見つかりません')
      const started = Date.now()
      const existing = issuesByProject.get(projectId) ?? []
      // auto は Go 側と同じく「同期実績が無ければフル同期」に解決する
      const effectiveMode: SyncMode =
        mode === 'auto' ? (existing.length === 0 ? 'full' : 'incremental') : mode
      // 進捗表示を Wails 外でも手動確認できるよう、Go 側と同じ段階を配信する
      const emit = (phase: SyncPhase, fetchedNow: number, total: number) =>
        emitMockSyncProgress({ runId, profileId, projectId, phase, fetched: fetchedNow, total })
      let fetched: number
      let upserted: number
      if (effectiveMode === 'full' || existing.length === 0) {
        const count = existing.length > 0 ? existing.length : 120 + (projectId % 7) * 13
        await delay(200)
        emit('count', 0, count)
        // 取得を 4 回に分けて進捗を進める(合計の待ち時間は従来と同程度)
        for (let step = 1; step <= 4; step += 1) {
          await delay(200)
          emit('fetch', Math.round((count * step) / 4), count)
        }
        emit('deleteScan', count, count)
        await delay(200)
        issuesByProject.set(projectId, buildMockIssues(project, count))
        fetched = count
        upserted = count
      } else {
        // 差分同期: 先頭数件だけ更新されたことにする(総件数は不明のまま進む)
        fetched = Math.min(existing.length, 8)
        upserted = fetched
        await delay(600)
        emit('fetch', fetched, 0)
        emit('deleteScan', fetched, 0)
        await delay(600)
        const now = new Date().toISOString()
        for (let i = 0; i < fetched; i += 1) existing[i].updated = now
      }
      emit('done', fetched, fetched)
      const at = new Date().toISOString()
      project.lastSyncedAt = at
      putSyncState('issues', projectId, at)
      return {
        // 実行されたモードを返す(Go 側も auto を解決した結果を返す)
        mode: effectiveMode,
        fetched,
        upserted,
        deleted: effectiveMode === 'full' ? 1 : 0,
        warnings:
          effectiveMode === 'full'
            ? ['(モック)削除候補 1 件を個別確認で確定しました']
            : [],
        durationMs: Date.now() - started,
      }
    },

    async searchIssues(_profileId, query, columns) {
      await delay(300)
      const all = issuesByProject.get(query.projectId) ?? []
      const { rows: matched, unverifiable } = filterMockIssues(all, query)
      const limit = query.limit && query.limit > 0 ? query.limit : matched.length
      // Go 側と同じページングの意味論(offset 件を読み飛ばして limit 件・
      // total は切り出し前の一致件数)。負値は 0(先頭)へ丸める
      const offset = query.offset && query.offset > 0 ? query.offset : 0
      const rows = matched.slice(offset, offset + limit).map((r) => ({
        ...r,
        // Go 側と同じく、要求された列のカスタム属性だけを返す
        customFields: pickMockCustomFields(r.customFields, columns),
      }))
      return { rows, total: matched.length, unverifiable }
    },

    async getIssueDetail(_profileId, projectId, issueKey) {
      await delay(200)
      // Go 側と同じく、ローカルに無い課題は明確なエラーにする(buildIssueDetail 内)
      return buildIssueDetail(projectId, issueKey)
    },

    async refreshIssueDetail(_profileId, projectId, issueKey) {
      await delay(400)
      const all = issuesByProject.get(projectId) ?? []
      const row = all.find((r) => r.issueKey === issueKey)
      // モックには Backlog 側が無いため、ローカルに無い課題を「削除された」扱いにする
      // (Go 側で 404 になったときと同じ案内)
      if (!row) {
        throw new Error(
          '課題を Backlog 上で確認できませんでした。削除された可能性があります' +
            '(ローカルの内容はそのままです。削除はフル同期で反映されます)',
        )
      }
      // Go 側は取得した内容で行を上書きする。モックは取得元が無いので、
      // 「取得できたことが画面で分かる」最小限の変化(件名の印と更新日時)を入れる
      const at = new Date().toISOString()
      if (!row.summary.endsWith(MOCK_REFRESHED_SUFFIX)) {
        row.summary += MOCK_REFRESHED_SUFFIX
      }
      row.updated = at
      issueFetchedAt.set(issueFetchedAtKey(projectId, issueKey), at)
      // コメントもこのタイミングでだけ取得する(同期では取得されない)
      issueComments.set(issueFetchedAtKey(projectId, issueKey), buildMockComments(issueKey, at))
      return buildIssueDetail(projectId, issueKey)
    },

    async listFilterOptions(_profileId, projectId) {
      await delay(150)
      const all = issuesByProject.get(projectId) ?? []
      const statuses = [...new Set(all.map((r) => r.statusName))].filter((s) => s !== '')
      const assignees = [...new Set(all.map((r) => r.assigneeName))].filter((s) => s !== '')
      return { statuses, assignees }
    },

    async getSyncState() {
      await delay(150)
      return syncState.map((s) => ({ ...s }))
    },

    async getIssueExportColumns() {
      await delay(50)
      return MOCK_ISSUE_EXPORT_COLUMNS.map((c) => ({ ...c }))
    },

    async exportIssuesExcel(_profileId, query, columns) {
      await delay(800)
      if (columns.length === 0) throw new Error('出力する列を 1 つ以上選択してください')
      const all = issuesByProject.get(query.projectId) ?? []
      const { rows: matched, unverifiable } = filterMockIssues(all, query)
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return {
        path: '(モック)保存ダイアログは Wails 実行時のみ表示されます',
        rows: matched.length,
        unverifiable,
      }
    },

    async syncUsers() {
      await delay(1000)
      const started = Date.now()
      putSyncState('users', 0, new Date().toISOString())
      return {
        mode: 'full',
        fetched: users.length,
        upserted: users.length,
        deleted: 0,
        warnings: [],
        durationMs: Date.now() - started,
      }
    },

    async listUsers(_profileId, query) {
      await delay(250)
      const matched = filterMockUsers(users, query)
      const limit = query.limit && query.limit > 0 ? query.limit : matched.length
      return {
        rows: matched.slice(0, limit).map((u) => ({
          ...u,
          teamNames: [...u.teamNames],
          projectKeys: [...u.projectKeys],
          adminProjectKeys: [...u.adminProjectKeys],
        })),
        total: matched.length,
      }
    },

    async getUserExportColumns() {
      await delay(50)
      return MOCK_USER_EXPORT_COLUMNS.map((c) => ({ ...c }))
    },

    async exportUsersExcel(_profileId, query, columns) {
      await delay(700)
      if (columns.length === 0) throw new Error('出力する列を 1 つ以上選択してください')
      const matched = filterMockUsers(users, query)
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return {
        path: '(モック)保存ダイアログは Wails 実行時のみ表示されます',
        rows: matched.length,
        unverifiable: 0,
      }
    },

    async exportBulkTemplate(_profileId, projectId, query) {
      await delay(700)
      const all = issuesByProject.get(projectId) ?? []
      // テンプレートは検索条件で絞り込める(条件なし = 全件)。
      // 課題抽出の Excel 出力と同じ絞り込みを通し、件数が条件に追従することを
      // Wails 外の画面確認でも再現する
      const { rows: matched } = filterMockIssues(all, query)
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return {
        path: '(モック)保存ダイアログは Wails 実行時のみ表示されます',
        rows: matched.length,
        unverifiable: 0,
      }
    },

    async importBulkFile(_profileId, projectId, defaultPriorityId) {
      await delay(900)
      const all = issuesByProject.get(projectId) ?? []
      // 既存課題の先頭数件を「更新」、末尾に「新規追加」と「変更なし」を足した固定シナリオ。
      // 検証エラー行はプレビューに載せない(実バックエンドはエラー行を除いた行だけを
      // 集計・プレビューへ載せる。internal/bulk/import.go)
      const targets = all.slice(0, 4)
      const previews: BulkPreviewRow[] = targets.map((r, i) => ({
        rowNo: i + 2,
        action: 'update',
        actionLabel: ACTION_LABELS.update,
        issueKey: r.issueKey,
        summary: r.summary,
        changes:
          i % 2 === 0
            ? ['状態: 未対応 → 処理中', '担当者: (未設定) → モック 太郎']
            : ['期限: (未設定) → 2026-12-31'],
        // 1 件だけ競合警告を出して、警告の見え方を確認できるようにする
        conflictWarning: i === 1,
      }))
      previews.push({
        rowNo: targets.length + 2,
        action: 'create',
        actionLabel: ACTION_LABELS.create,
        issueKey: '',
        summary: '(モック)新規追加する課題',
        changes: [
          `種別: ${MOCK_MASTER.issueTypes[0].name}`,
          `優先度: ${
            MOCK_MASTER.priorities.find((p) => p.id === defaultPriorityId)?.name ?? '(既定値)'
          }`,
        ],
        conflictWarning: false,
      })
      // 変更が 1 つも無い行(skip)。集計とバッジの「変更なし」を確認できるようにする
      previews.push({
        rowNo: targets.length + 3,
        action: 'skip',
        actionLabel: ACTION_LABELS.skip,
        issueKey: all[4]?.issueKey ?? '',
        summary: all[4]?.summary ?? '(モック)変更なしの課題',
        changes: [],
        conflictWarning: false,
      })
      importSeq += 1
      // 1 回目だけ検証エラーを出す(実行できない状態の確認用)。
      // エラー行はプレビューにも集計にも含まれない
      const errors: BulkValidationError[] = importSeq === 1
        ? [{ rowNo: targets.length + 4, message: '(モック)種別ID が未入力です(新規追加行の必須項目)' }]
        : []

      // 取り込んだデータ行数は検証エラー行も含む(Go 側 import.go の TotalRows と同じ)
      const totalRows = previews.length + errors.length
      const creates = previews.filter((p) => p.action === 'create').length
      const updates = previews.filter((p) => p.action === 'update').length
      const skips = previews.filter((p) => p.action === 'skip').length
      // 検証エラーがある場合はジョブを作らない(Go 側 import.go と同じ。
      // 作ってしまうと不正な取り込みが履歴に残り「再開」から実行できてしまう)
      if (errors.length > 0) {
        return {
          jobId: 0,
          projectId,
          totalRows,
          creates,
          updates,
          skips,
          valid: false,
          warnings: [],
          errors,
          previews,
        }
      }
      jobSeq += 1
      const job: BulkJobRow = {
        jobId: jobSeq,
        projectId,
        kind: 'bulk_update',
        createdAt: new Date().toISOString(),
        status: 'pending',
        // ジョブに記録されるのは検証エラー行を除いた行(jobRows と同数)
        total: previews.length,
        done: 0,
        failed: 0,
        pending: creates + updates,
        sending: 0,
        conflict: 0,
        skipped: skips,
      }
      jobs.unshift(job)
      jobRows.set(
        job.jobId,
        previews.map((p) => ({
          rowNo: p.rowNo,
          issueKey: p.issueKey,
          status: p.action === 'skip' ? 'skip' : 'pending',
          resultIssueId: 0,
          error: '',
        })),
      )
      return {
        jobId: job.jobId,
        projectId,
        totalRows,
        creates,
        updates,
        skips,
        valid: true,
        warnings: [],
        errors,
        previews,
      }
    },

    async runBulkJob(_profileId, jobId, force, resendSending) {
      const started = Date.now()
      const job = jobs.find((j) => j.jobId === jobId)
      if (!job) throw new Error('ジョブが見つかりません')
      canceledJobs.delete(jobId)
      job.status = 'running'

      const rows = jobRows.get(jobId) ?? []
      // 処理対象: 未処理 + 競合(force のときのみ)+ 送信中(再送を選んだときのみ)
      const targets = rows.filter(
        (r) =>
          r.status === 'pending' ||
          (force && r.status === 'conflict') ||
          (resendSending && r.status === 'sending'),
      )
      const total = targets.length
      let done = 0
      let conflict = 0
      for (let i = 0; i < total; i += 1) {
        await delay(400)
        const row = targets[i]
        if (canceledJobs.has(jobId)) {
          // 中断時は「送信したが結果を確認できなかった行」を 1 件だけ再現し、
          // 履歴の sending 表示・再送確認の導線を手動確認できるようにする
          row.status = 'sending'
          break
        }
        // 2 件目は競合させる(force 指定時のみ書き込めることを確認できる)
        if (i === 1 && !force) {
          conflict += 1
          row.status = 'conflict'
          row.error = '(モック)リモートが更新されているため上書きしませんでした'
        } else {
          done += 1
          row.status = 'done'
          row.error = ''
          if (!row.issueKey) {
            // 新規追加行には、作成された課題の ID(再送時の二重作成防止の突合に使う)と
            // 課題キー(結果レポート・行明細の表示に使う)が付く。
            // Go 側も完了(done)へ遷移するときにだけ課題キーを記録する
            row.resultIssueId = 900000 + row.rowNo
            const projectKey =
              projects.find((p) => p.id === job.projectId)?.projectKey ?? 'MOCK'
            row.issueKey = `${projectKey}-${row.resultIssueId}`
          }
        }
        emitMockProgress({ jobId, processed: i + 1, total })
      }
      const canceled = canceledJobs.has(jobId)
      canceledJobs.delete(jobId)

      job.done = rows.filter((r) => r.status === 'done').length
      job.failed = rows.filter((r) => r.status === 'error').length
      job.pending = rows.filter((r) => r.status === 'pending').length
      job.sending = rows.filter((r) => r.status === 'sending').length
      job.conflict = rows.filter((r) => r.status === 'conflict').length
      job.skipped = rows.filter((r) => r.status === 'skip').length
      job.status = canceled
        ? 'canceled'
        : job.pending + job.sending + job.conflict > 0
          ? 'partial'
          : 'done'

      const warnings: string[] = []
      const unprocessed = job.pending + job.sending
      if (canceled) {
        // Go 側と同じく、未処理の件数を警告に載せる(結果の skipped には含めない)
        warnings.push(
          `(モック)キャンセルされました(未処理 ${unprocessed} 件: 未送信 ${job.pending} 件 / 送信中 ${job.sending} 件)。ジョブ履歴から再開できます`,
        )
      }
      if (conflict > 0) {
        warnings.push('(モック)リモートが更新されている課題をスキップしました。')
      }
      if (job.sending > 0) {
        warnings.push('(モック)送信結果を確認できなかった行があります。再開時に確認してください。')
      }
      return {
        jobId,
        done,
        failed: 0,
        conflict,
        // skipped は「取り込み時の変更なし行」だけを数える(未処理は含めない)
        skipped: job.skipped,
        durationMs: Date.now() - started,
        warnings,
      }
    },

    async cancelBulkRun(_profileId, jobId) {
      canceledJobs.add(jobId)
    },

    async listBulkJobs() {
      await delay(150)
      return jobs.map((j) => ({ ...j }))
    },

    async getBulkJobRows(_profileId, jobId) {
      await delay(200)
      const rows = jobRows.get(jobId) ?? []
      // 表示名は Wails 実行時と同じく取得時に解決する(状態は実行中に変わるため)
      return rows
        .map((r) => ({ ...r, statusLabel: rowStatusLabel(r.status) }))
        .sort((a, b) => a.rowNo - b.rowNo)
    },

    async exportBulkResultExcel(_profileId, jobId) {
      await delay(600)
      const rows = jobRows.get(jobId) ?? []
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return {
        path: '(モック)保存ダイアログは Wails 実行時のみ表示されます',
        rows: rows.length,
        unverifiable: 0,
      }
    },

    async getMasterData() {
      await delay(200)
      return {
        issueTypes: MOCK_MASTER.issueTypes.map((m) => ({ ...m })),
        priorities: MOCK_MASTER.priorities.map((m) => ({ ...m })),
        statuses: MOCK_MASTER.statuses.map((m) => ({ ...m })),
        customFields: MOCK_MASTER.customFields.map((f) => ({
          ...f,
          applicableIssueTypes: [...f.applicableIssueTypes],
          items: f.items.map((i) => ({ ...i })),
        })),
      }
    },

    async getLogInfo() {
      await delay(50)
      // モックでは実ファイルを作らないため、ダミーのパスを返す
      return { path: '(モック)logs/backlog-assistant-YYYYMMDD.log', enabled: true }
    },

    async getStorageInfo() {
      await delay(50)
      // モックでは実ファイルを作らないため、ダミーのパス・サイズを返す
      return {
        configDir: '(モック)~/.config/backlog-assistant',
        // カスタマイズしていない状態(既定)を再現する
        storageMode: 'default' as const,
        databases: profiles.map((p, i) => ({
          profileId: p.id,
          profileName: p.name,
          path: `(モック)~/.config/backlog-assistant/data/mock-${i + 1}.backlog.jp_42.db`,
          // 先頭のプロファイルだけ作成済みに見せる(未作成表示も確認できるようにする)
          sizeBytes: i === 0 ? 3_145_728 : 0,
          exists: i === 0,
          error: '',
        })),
        logPath: '(モック)logs/backlog-assistant-YYYYMMDD.log',
        logEnabled: true,
      }
    },

    async getAppVersion() {
      return { version: 'dev(モック)' }
    },

    async getRateLimitStatus() {
      await delay(50)
      const reset = Math.floor(Date.now() / 1000) + 42
      return {
        categories: [
          { name: 'read' as const, limit: 600, remaining: 587, resetUnix: reset, observed: true },
          { name: 'update' as const, limit: 150, remaining: 150, resetUnix: 0, observed: false },
          { name: 'search' as const, limit: 150, remaining: 143, resetUnix: reset, observed: true },
          { name: 'icon' as const, limit: 60, remaining: 60, resetUnix: 0, observed: false },
        ],
      }
    },
  }
}
