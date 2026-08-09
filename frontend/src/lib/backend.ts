// backend.ts
// Wails バインディング呼び出しの型付きラッパー。
// Wails ランタイム上では window.go.main.App のメソッドを呼び出し、
// Wails 外(vite dev / ビルド検証)ではモック実装にフォールバックする。
//
// 注意: シグネチャはマイルストーン 2 時点の契約。バックエンドとの最終結合時に調整する。
//
// TDD 例外(GUI): フロントエンドにテスト基盤が無いため、この層はテスト先行では実装していない
// (契約は Go 側のユニットテストで担保する)。

// ---------------------------------------------------------------------------
// 型定義
// ---------------------------------------------------------------------------

/** 接続プロファイル(API キーは含まない。キーは OS キーチェーンにのみ保存される) */
export interface Profile {
  /** プロファイル ID(バックエンドが採番) */
  id: string
  /** 表示名 */
  name: string
  /** スペース URL(例: https://example.backlog.jp) */
  spaceUrl: string
  /** 直近の接続テストで確認したユーザ名(未接続なら空) */
  lastUserName: string
  /** 直近の接続テストで確認したユーザ ID(未接続なら 0。DB ファイルの特定に使用) */
  lastUserId: number
}

/** プロファイル保存の入力 */
export interface ProfileInput {
  /** 既存プロファイルの変更時は ID を指定。新規登録時は空文字 */
  id: string
  name: string
  spaceUrl: string
  /**
   * API キー。新規登録時は必須。
   * 変更時に空文字を渡すと「キーは変更しない」(キーチェーンの既存値を維持)。
   */
  apiKey: string
}

/** 接続テスト(GET /users/myself)の結果 */
export interface ConnectionTestResult {
  ok: boolean
  /** 認証ユーザの ID(DB ファイル名の決定に使用) */
  userId: number
  /** 認証ユーザの表示名 */
  userName: string
  /** RoleType(自前定数: 1=管理者, 2=一般ユーザ, ...) */
  roleType: number
  /** users/teams API にアクセス可能か(false なら管理者機能は縮退) */
  adminAvailable: boolean
  /** エラー時の説明(日本語)。ok=true なら空 */
  message: string
}

/** 権限状態(Go 側 service.PermissionStatus と対) */
export interface PermissionStatus {
  /** users/teams API にアクセス可能か */
  adminAvailable: boolean
  /** 縮退状態(プロジェクト単位取得へフォールバック)かどうか */
  degraded: boolean
  /** UI 表示用の説明(日本語) */
  message: string
}

/** プロジェクト(ローカル DB の projects テーブル由来) */
export interface Project {
  /** Backlog のプロジェクト ID */
  id: number
  /** プロジェクトキー(例: SAMPLE) */
  projectKey: string
  /** プロジェクト名 */
  name: string
  /** このプロジェクトの課題の最終同期時刻(RFC3339。未同期なら空文字) */
  lastSyncedAt: string
}

/**
 * 同期モード。
 * auto = バックエンドが同期状態から判定(未同期・長期未同期ならフル同期)/
 * full = フル同期 / incremental = 差分同期。
 * 未同期プロジェクトで incremental を指定すると必ず失敗するため、UI の既定は auto。
 */
export type SyncMode = 'auto' | 'full' | 'incremental'

/** 同期実行の結果サマリ */
export interface SyncResult {
  /** 実行された同期モード(フォールバックで full になる場合がある) */
  mode: string
  /** API から取得した件数 */
  fetched: number
  /** ローカル DB へ登録・更新した件数 */
  upserted: number
  /** 論理削除した件数 */
  deleted: number
  /** 警告(権限縮退・削除候補の未確定など。日本語) */
  warnings: string[]
  /** 所要時間(ミリ秒) */
  durationMs: number
}

/** 課題検索条件(ローカル DB に対する検索) */
export interface IssueQuery {
  /** 対象プロジェクト ID(必須) */
  projectId: number
  /** キーワード(件名 + 詳細の部分一致。Go 側で NFKC + ケースフォールド正規化) */
  keyword?: string
  /** 更新日の下限(YYYY-MM-DD) */
  updatedFrom?: string
  /** 更新日の上限(YYYY-MM-DD) */
  updatedTo?: string
  /** 作成日の下限(YYYY-MM-DD) */
  createdFrom?: string
  /** 作成日の上限(YYYY-MM-DD) */
  createdTo?: string
  /** 状態名(完全一致。空ならすべて) */
  statusName?: string
  /** 担当者名(完全一致。空ならすべて) */
  assigneeName?: string
  /** 画面プレビューの取得上限。未指定ならバックエンドの既定値。Excel 出力時は無視される */
  limit?: number
}

/** 課題 1 件(検索結果の表示用) */
export interface IssueRow {
  issueKey: string
  summary: string
  statusName: string
  assigneeName: string
  issueTypeName: string
  priorityName: string
  /** 作成日時(RFC3339) */
  created: string
  /** 更新日時(RFC3339) */
  updated: string
  /** 期限(YYYY-MM-DD。未設定なら空文字) */
  dueDate: string
}

/** 課題検索の結果。rows は limit で切り詰められるが total は条件に一致する全件数 */
export interface IssueSearchResult {
  rows: IssueRow[]
  total: number
}

/** 条件フォームのセレクト候補(ローカル DB の実データから抽出) */
export interface FilterOptions {
  statuses: string[]
  assignees: string[]
}

/** 同期状態(sync_state テーブル 1 行) */
export interface SyncStateRow {
  /** データ種別(issues / users / teams / projects) */
  dataKind: string
  /** プロジェクト ID(プロジェクト単位でない種別は 0) */
  projectId: number
  /** 最終同期完了時刻(RFC3339。未同期なら空文字) */
  lastSyncedAt: string
}

/** Excel 出力の結果。ユーザが保存ダイアログをキャンセルした場合は path が空文字 */
export interface ExportResult {
  path: string
  rows: number
}

// ---------------------------------------------------------------------------
// バックエンドインターフェース
// ---------------------------------------------------------------------------

export interface Backend {
  /** 保存済みプロファイル一覧を返す */
  listProfiles(): Promise<Profile[]>
  /**
   * プロファイルを保存する(新規登録 / 変更)。
   * 呼び出し前に接続テスト成功が前提(バックエンド側でも検証される想定)。
   * 保存後のプロファイルを返す。
   */
  saveProfile(input: ProfileInput): Promise<Profile>
  /**
   * プロファイルを削除する。OS キーチェーンの API キーも必ず削除される。
   * @param deleteLocalData true ならローカル DB(取得データ)も削除する
   */
  deleteProfile(id: string, deleteLocalData: boolean): Promise<void>
  /**
   * 接続テスト(GET /users/myself)。
   * @param profileId 既存プロファイルの ID(新規登録時は空文字)
   * @param spaceUrl  スペース URL
   * @param apiKey    API キー。空文字かつ profileId 指定時はキーチェーンの既存キーでテストする
   */
  testConnection(profileId: string, spaceUrl: string, apiKey: string): Promise<ConnectionTestResult>
  /** 指定プロファイルの権限状態(実権限)を返す */
  getPermissionStatus(profileId: string): Promise<PermissionStatus>
  /** 保存済みの接続先プロファイル ID を返す(未選択なら空文字) */
  getActiveProfile(): Promise<string>
  /** 接続先プロファイル ID を保存する(空文字 = 選択解除) */
  setActiveProfile(id: string): Promise<void>

  // --- 課題抽出 / 同期 ------------------------------------------------------

  /** ローカル DB に保存済みのプロジェクト一覧を返す(API は呼ばない) */
  listProjects(profileId: string): Promise<Project[]>
  /** Backlog からプロジェクト一覧を取得してローカル DB を更新する */
  syncProjects(profileId: string): Promise<void>
  /** 指定プロジェクトの課題を同期する(差分 / フル) */
  syncIssues(profileId: string, projectId: number, mode: SyncMode): Promise<SyncResult>
  /** ローカル DB から課題を検索する(API は呼ばない) */
  searchIssues(profileId: string, query: IssueQuery): Promise<IssueSearchResult>
  /** 条件フォームの状態・担当者候補を返す */
  listFilterOptions(profileId: string, projectId: number): Promise<FilterOptions>
  /** データ種別ごとの同期状態一覧を返す */
  getSyncState(profileId: string): Promise<SyncStateRow[]>
  /**
   * 検索条件に一致する課題を Excel 出力する(表示上限に関わらず全件)。
   * 保存先は Go 側の保存ダイアログで選択する。キャンセル時は path が空文字。
   * @param columns 出力する列キー(IssueRow のキー)を表示順で指定する
   */
  exportIssuesExcel(profileId: string, query: IssueQuery, columns: string[]): Promise<ExportResult>
}

// ---------------------------------------------------------------------------
// Wails バインディング実装
// ---------------------------------------------------------------------------

/** Wails が window に注入する Go 側 App メソッド群(実行時参照) */
interface WailsApp {
  ListProfiles(): Promise<Profile[]>
  SaveProfile(input: ProfileInput): Promise<Profile>
  DeleteProfile(id: string, deleteLocalData: boolean): Promise<void>
  TestConnection(profileId: string, spaceUrl: string, apiKey: string): Promise<ConnectionTestResult>
  GetPermissionStatus(profileId: string): Promise<PermissionStatus>
  GetActiveProfile(): Promise<string>
  SetActiveProfile(id: string): Promise<void>
  ListProjects(profileId: string): Promise<Project[]>
  SyncProjects(profileId: string): Promise<void>
  SyncIssues(profileId: string, projectId: number, mode: SyncMode): Promise<SyncResult>
  SearchIssues(profileId: string, query: IssueQuery): Promise<IssueSearchResult>
  ListFilterOptions(profileId: string, projectId: number): Promise<FilterOptions>
  GetSyncState(profileId: string): Promise<SyncStateRow[]>
  ExportIssuesExcel(profileId: string, query: IssueQuery, columns: string[]): Promise<ExportResult>
}

function findWailsApp(): WailsApp | null {
  const w = window as unknown as Record<string, unknown>
  const go = w['go'] as Record<string, unknown> | undefined
  const main = go?.['main'] as Record<string, unknown> | undefined
  const app = main?.['App'] as Partial<WailsApp> | undefined
  if (app && typeof app.ListProfiles === 'function') {
    return app as WailsApp
  }
  return null
}

function createWailsBackend(app: WailsApp): Backend {
  return {
    listProfiles: () => app.ListProfiles(),
    saveProfile: (input) => app.SaveProfile(input),
    deleteProfile: (id, deleteLocalData) => app.DeleteProfile(id, deleteLocalData),
    testConnection: (profileId, spaceUrl, apiKey) => app.TestConnection(profileId, spaceUrl, apiKey),
    getPermissionStatus: (profileId) => app.GetPermissionStatus(profileId),
    getActiveProfile: () => app.GetActiveProfile(),
    setActiveProfile: (id) => app.SetActiveProfile(id),
    listProjects: (profileId) => app.ListProjects(profileId),
    syncProjects: (profileId) => app.SyncProjects(profileId),
    syncIssues: (profileId, projectId, mode) => app.SyncIssues(profileId, projectId, mode),
    searchIssues: (profileId, query) => app.SearchIssues(profileId, query),
    listFilterOptions: (profileId, projectId) => app.ListFilterOptions(profileId, projectId),
    getSyncState: (profileId) => app.GetSyncState(profileId),
    exportIssuesExcel: (profileId, query, columns) =>
      app.ExportIssuesExcel(profileId, query, columns),
  }
}

// ---------------------------------------------------------------------------
// モック実装(vite dev / ビルド検証用)
// ---------------------------------------------------------------------------

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

const MOCK_PROJECTS: Project[] = [
  { id: 101, projectKey: 'SAMPLE', name: 'サンプル開発プロジェクト', lastSyncedAt: '' },
  { id: 102, projectKey: 'DEMO', name: 'デモ運用プロジェクト', lastSyncedAt: '' },
  { id: 103, projectKey: 'TRIAL', name: '検証用プロジェクト', lastSyncedAt: '' },
]

/** 日付を YYYY-MM-DD 形式で返す */
function ymd(d: Date): string {
  return d.toISOString().slice(0, 10)
}

/** 決定的にダミー課題を生成する(モック専用) */
function buildMockIssues(project: Project, count: number): IssueRow[] {
  const rows: IssueRow[] = []
  const base = Date.now()
  for (let i = 0; i < count; i += 1) {
    const created = new Date(base - (count - i) * 6 * 3600 * 1000)
    const updated = new Date(created.getTime() + ((i % 7) + 1) * 3600 * 1000)
    const due = new Date(updated.getTime() + ((i % 11) + 1) * 24 * 3600 * 1000)
    rows.push({
      issueKey: `${project.projectKey}-${i + 1}`,
      summary: `${MOCK_SUMMARY_WORDS[i % MOCK_SUMMARY_WORDS.length]}の対応 #${i + 1}`,
      statusName: MOCK_STATUSES[i % MOCK_STATUSES.length],
      assigneeName: MOCK_ASSIGNEES[i % MOCK_ASSIGNEES.length],
      issueTypeName: MOCK_ISSUE_TYPES[i % MOCK_ISSUE_TYPES.length],
      priorityName: MOCK_PRIORITIES[i % MOCK_PRIORITIES.length],
      created: created.toISOString(),
      updated: updated.toISOString(),
      dueDate: i % 3 === 0 ? '' : ymd(due),
    })
  }
  return rows
}

/** モックのローカル検索(Go 側の LIKE 部分一致に相当する簡易版) */
function filterMockIssues(rows: IssueRow[], query: IssueQuery): IssueRow[] {
  const keyword = (query.keyword ?? '').trim().toLowerCase()
  return rows.filter((r) => {
    if (keyword && !r.summary.toLowerCase().includes(keyword)) return false
    if (query.updatedFrom && r.updated.slice(0, 10) < query.updatedFrom) return false
    if (query.updatedTo && r.updated.slice(0, 10) > query.updatedTo) return false
    if (query.createdFrom && r.created.slice(0, 10) < query.createdFrom) return false
    if (query.createdTo && r.created.slice(0, 10) > query.createdTo) return false
    if (query.statusName && r.statusName !== query.statusName) return false
    if (query.assigneeName && r.assigneeName !== query.assigneeName) return false
    return true
  })
}

function createMockBackend(): Backend {
  // メモリ上のみ。リロードで消える。
  const profiles: Profile[] = []
  const keys = new Map<string, string>() // profileId -> apiKey(モック用)
  let seq = 0
  let activeId = ''

  // 課題抽出・同期のモック状態。プロジェクト 101 のみ「同期済み」の初期状態とし、
  // 他は未同期にして「未同期プロジェクトの導線」も確認できるようにする。
  const projects: Project[] = MOCK_PROJECTS.map((p) => ({ ...p }))
  const issuesByProject = new Map<number, IssueRow[]>()
  const syncState: SyncStateRow[] = []

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

    async syncIssues(_profileId, projectId, mode) {
      await delay(1200)
      const project = projects.find((p) => p.id === projectId)
      if (!project) throw new Error('プロジェクトが見つかりません')
      const started = Date.now()
      const existing = issuesByProject.get(projectId) ?? []
      // auto は Go 側と同じく「同期実績が無ければフル同期」に解決する
      const effectiveMode: SyncMode =
        mode === 'auto' ? (existing.length === 0 ? 'full' : 'incremental') : mode
      let fetched: number
      let upserted: number
      if (effectiveMode === 'full' || existing.length === 0) {
        const count = existing.length > 0 ? existing.length : 120 + (projectId % 7) * 13
        issuesByProject.set(projectId, buildMockIssues(project, count))
        fetched = count
        upserted = count
      } else {
        // 差分同期: 先頭数件だけ更新されたことにする
        fetched = Math.min(existing.length, 8)
        upserted = fetched
        const now = new Date().toISOString()
        for (let i = 0; i < fetched; i += 1) existing[i].updated = now
      }
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

    async searchIssues(_profileId, query) {
      await delay(300)
      const all = issuesByProject.get(query.projectId) ?? []
      const matched = filterMockIssues(all, query)
      const limit = query.limit && query.limit > 0 ? query.limit : matched.length
      return { rows: matched.slice(0, limit).map((r) => ({ ...r })), total: matched.length }
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

    async exportIssuesExcel(_profileId, query, columns) {
      await delay(800)
      if (columns.length === 0) throw new Error('出力する列を 1 つ以上選択してください')
      const all = issuesByProject.get(query.projectId) ?? []
      const matched = filterMockIssues(all, query)
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return { path: '(モック)保存ダイアログは Wails 実行時のみ表示されます', rows: matched.length }
    },
  }
}

// ---------------------------------------------------------------------------
// エクスポート
// ---------------------------------------------------------------------------

let cached: Backend | null = null

/** バックエンドを取得する。Wails 上ならバインディング、そうでなければモック */
export function getBackend(): Backend {
  if (!cached) {
    const app = findWailsApp()
    cached = app ? createWailsBackend(app) : createMockBackend()
  }
  return cached
}

/** モック動作中かどうか(UI での注記表示用) */
export function isMockBackend(): boolean {
  return findWailsApp() === null
}
