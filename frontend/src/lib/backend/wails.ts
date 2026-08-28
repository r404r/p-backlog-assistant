
// windowへ注入されたWails runtime / Go AppをBackend契約へ適合させるadapter。

import { globalTranslate } from '../format'
import {
  STORAGE_MODES,
  type AppVersion,
  type Backend,
  type BulkImportResult,
  type BulkJobRow,
  type BulkJobRowDetail,
  type BulkRunResult,
  type ConnectionTestResult,
  type ExportColumn,
  type ExportResult,
  type FilterOptions,
  type IssueDetail,
  type IssueQuery,
  type IssueSearchResult,
  type LogInfo,
  type MasterData,
  type PermissionStatus,
  type Profile,
  type ProfileInput,
  type Project,
  type RateLimitStatus,
  type StorageInfo,
  type StorageMode,
  type SyncMode,
  type SyncResult,
  type SyncStateRow,
  type UserQuery,
  type UserSearchResult,
} from './contract'
import { actionLabel, rowStatusLabel } from './shared'

function normalizeStorageMode(value: unknown): StorageMode {
  return STORAGE_MODES.includes(value as StorageMode) ? (value as StorageMode) : 'default'
}

/** Wails が window に注入する Go 側 App メソッド群(実行時参照) */
export interface WailsApp {
  ListProfiles(): Promise<Profile[]>
  SaveProfile(input: ProfileInput): Promise<Profile>
  DeleteProfile(id: string, deleteLocalData: boolean): Promise<void>
  TestConnection(profileId: string, spaceUrl: string, apiKey: string): Promise<ConnectionTestResult>
  GetPermissionStatus(profileId: string): Promise<PermissionStatus>
  GetActiveProfile(): Promise<string>
  SetActiveProfile(id: string): Promise<void>
  ListProjects(profileId: string): Promise<Project[]>
  SyncProjects(profileId: string): Promise<void>
  SyncIssues(
    profileId: string,
    projectId: number,
    mode: SyncMode,
    runId: string,
  ): Promise<SyncResult>
  SearchIssues(
    profileId: string,
    query: IssueQuery,
    columns: string[],
  ): Promise<IssueSearchResult>
  GetIssueDetail(profileId: string, projectId: number, issueKey: string): Promise<IssueDetail>
  RefreshIssueDetail(profileId: string, projectId: number, issueKey: string): Promise<IssueDetail>
  ListFilterOptions(profileId: string, projectId: number): Promise<FilterOptions>
  GetSyncState(profileId: string): Promise<SyncStateRow[]>
  GetIssueExportColumns(): Promise<ExportColumn[]>
  ExportIssuesExcel(profileId: string, query: IssueQuery, columns: string[]): Promise<ExportResult>
  SyncUsers(profileId: string): Promise<SyncResult>
  ListUsers(profileId: string, query: UserQuery): Promise<UserSearchResult>
  GetUserExportColumns(): Promise<ExportColumn[]>
  ExportUsersExcel(profileId: string, query: UserQuery, columns: string[]): Promise<ExportResult>
  ExportBulkTemplate(profileId: string, projectId: number, query: IssueQuery): Promise<ExportResult>
  ImportBulkFile(
    profileId: string,
    projectId: number,
    defaultPriorityId: number,
  ): Promise<BulkImportResult>
  RunBulkJob(
    profileId: string,
    jobId: number,
    force: boolean,
    resendSending: boolean,
  ): Promise<BulkRunResult>
  CancelBulkRun(profileId: string, jobId: number): Promise<void>
  ListBulkJobs(profileId: string): Promise<BulkJobRow[]>
  GetBulkJobRows(profileId: string, jobId: number): Promise<BulkJobRowDetail[]>
  ExportBulkResultExcel(profileId: string, jobId: number): Promise<ExportResult>
  GetMasterData(profileId: string, projectId: number): Promise<MasterData>
  GetLogInfo(): Promise<LogInfo>
  GetStorageInfo(): Promise<StorageInfo>
  GetRateLimitStatus(profileId: string): Promise<RateLimitStatus>
  GetAppVersion(): Promise<AppVersion>
}

/** Wails ランタイム(window.runtime)のうち本アプリが使う API */
interface WailsRuntime {
  EventsOn(name: string, callback: (...data: unknown[]) => void): () => void
  /** 既定のブラウザで URL を開く */
  BrowserOpenURL(url: string): void
  /**
   * クリップボードへ文字列を書き込む(Wails v2.13 の公式 API)。
   * 失敗時は reject する(解決値は成功を表す真偽値)。
   */
  ClipboardSetText(text: string): Promise<boolean>
}

/**
 * window.runtime をそのまま返す(Wails 外では null)。
 * 個々の API の有無は呼び出し側で確認する(ランタイムのバージョン差を吸収するため)。
 */
export function findWailsRuntimeObject(): Partial<WailsRuntime> | null {
  const w = window as unknown as Record<string, unknown>
  const rt = w['runtime'] as Partial<WailsRuntime> | undefined
  return rt ?? null
}

export function findWailsRuntime(): WailsRuntime | null {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.EventsOn === 'function') {
    return rt as WailsRuntime
  }
  return null
}

export function findWailsApp(): WailsApp | null {
  const w = window as unknown as Record<string, unknown>
  const go = w['go'] as Record<string, unknown> | undefined
  const main = go?.['main'] as Record<string, unknown> | undefined
  const app = main?.['App'] as Partial<WailsApp> | undefined
  if (app && typeof app.ListProfiles === 'function') {
    return app as WailsApp
  }
  return null
}

/**
 * Go から届いた列メタデータを正規化する(R14)。
 * 列が 1 つも無いと出力できなくなるため、空で握り潰さずエラーにする
 * (旧バージョンのバインディングでメソッドが無い場合も同様)。
 */
function normalizeExportColumns(cols: ExportColumn[] | null | undefined): ExportColumn[] {
  const out = (cols ?? []).map((c) => ({
    key: c?.key ?? '',
    label: c?.label ?? '',
    byDefault: c?.byDefault ?? false,
  }))
  if (out.length === 0) {
    throw new Error(globalTranslate('common.backend.exportColumnsUnavailable'))
  }
  return out
}

/**
 * Go から届いた課題詳細を正規化する(getIssueDetail / refreshIssueDetail 共通)。
 * Go の nil スライスは JSON null で届くため、配列・文字列は必ず埋める。
 */
function normalizeIssueDetail(r: IssueDetail | null | undefined, issueKey: string): IssueDetail {
  return {
    issueKey: r?.issueKey ?? issueKey,
    summary: r?.summary ?? '',
    description: r?.description ?? '',
    statusName: r?.statusName ?? '',
    assigneeName: r?.assigneeName ?? '',
    issueTypeName: r?.issueTypeName ?? '',
    priorityName: r?.priorityName ?? '',
    created: r?.created ?? '',
    updated: r?.updated ?? '',
    dueDate: r?.dueDate ?? '',
    parentIssueKey: r?.parentIssueKey ?? '',
    customFields: (r?.customFields ?? []).map((c) => ({
      name: c?.name ?? '',
      value: c?.value ?? '',
    })),
    fetchedAt: r?.fetchedAt ?? '',
    // コメント関連は旧バージョンのバインディングでは届かない(undefined)。
    // 未取得(空文字・空配列)へ寄せ、画面が「取得を促す」表示に縮退できるようにする
    comments: (r?.comments ?? []).map((c) => ({
      authorName: c?.authorName ?? '',
      content: c?.content ?? '',
      created: c?.created ?? '',
    })),
    commentsFetchedAt: r?.commentsFetchedAt ?? '',
    commentsHistoryOnly: r?.commentsHistoryOnly ?? 0,
    commentsTruncated: r?.commentsTruncated ?? false,
    warnings: r?.warnings ?? [],
    // 記法設定も旧バージョンのバインディングでは届かない(undefined)。
    // 空文字へ寄せると画面は従来のプレーン表示に縮退する(誤レンダリングしない)
    textFormattingRule: r?.textFormattingRule ?? '',
  }
}

export function createWailsBackend(app: WailsApp): Backend {
  // Go の nil スライス / nil マップは JSON null として届くため、配列・オブジェクトは
  // ここで必ず正規化する(初回起動時の ListProfiles null で画面が真っ白になった実績あり)
  return {
    listProfiles: async () => (await app.ListProfiles()) ?? [],
    saveProfile: (input) => app.SaveProfile(input),
    deleteProfile: (id, deleteLocalData) => app.DeleteProfile(id, deleteLocalData),
    testConnection: (profileId, spaceUrl, apiKey) => app.TestConnection(profileId, spaceUrl, apiKey),
    getPermissionStatus: (profileId) => app.GetPermissionStatus(profileId),
    getActiveProfile: async () => (await app.GetActiveProfile()) ?? '',
    setActiveProfile: (id) => app.SetActiveProfile(id),
    listProjects: async (profileId) =>
      ((await app.ListProjects(profileId)) ?? []).map((p) => ({
        ...p,
        // 旧バージョンのバインディング(フィールド未実装)では「不明ではない」扱いにする
        syncStateUnknown: p.syncStateUnknown ?? false,
      })),
    syncProjects: (profileId) => app.SyncProjects(profileId),
    syncIssues: async (profileId, projectId, mode, runId) => {
      const r = await app.SyncIssues(profileId, projectId, mode, runId)
      return {
        mode: r?.mode ?? mode,
        fetched: r?.fetched ?? 0,
        upserted: r?.upserted ?? 0,
        deleted: r?.deleted ?? 0,
        warnings: r?.warnings ?? [],
        durationMs: r?.durationMs ?? 0,
      }
    },
    searchIssues: async (profileId, query, columns) => {
      const r = await app.SearchIssues(profileId, query, columns ?? [])
      // Go の nil マップは null で届くため、常にオブジェクトへ正規化する
      // (画面が r.customFields[key] を条件分岐なしで参照できるように)
      const rows = (r?.rows ?? []).map((row) => ({ ...row, customFields: row.customFields ?? {} }))
      return { rows, total: r?.total ?? 0, unverifiable: r?.unverifiable ?? 0 }
    },
    getIssueDetail: async (profileId, projectId, issueKey) => {
      // 旧バージョンのバインディング(メソッド未実装)では空の詳細を返さない
      // (すべての項目が空のポップアップは「値が無い課題」と誤読させるため)
      if (typeof app.GetIssueDetail !== 'function') {
        throw new Error(globalTranslate('common.backend.issueDetailUnavailable'))
      }
      return normalizeIssueDetail(await app.GetIssueDetail(profileId, projectId, issueKey), issueKey)
    },
    refreshIssueDetail: async (profileId, projectId, issueKey) => {
      // 旧バージョンのバインディング(メソッド未実装)では、取得できていないのに
      // 「最新の状態」を表示してしまわないようエラーにする(getIssueDetail と同じ流儀)
      if (typeof app.RefreshIssueDetail !== 'function') {
        throw new Error(globalTranslate('common.backend.issueRefreshUnavailable'))
      }
      return normalizeIssueDetail(
        await app.RefreshIssueDetail(profileId, projectId, issueKey),
        issueKey,
      )
    },
    listFilterOptions: async (profileId, projectId) => {
      const r = await app.ListFilterOptions(profileId, projectId)
      return { statuses: r?.statuses ?? [], assignees: r?.assignees ?? [] }
    },
    getSyncState: async (profileId) => (await app.GetSyncState(profileId)) ?? [],
    // 旧バージョンのバインディング(メソッド未実装)では normalizeExportColumns が
    // 「アプリを更新してください」のエラーにする(列が無いまま出力させない)
    getIssueExportColumns: async () =>
      normalizeExportColumns(
        typeof app.GetIssueExportColumns === 'function' ? await app.GetIssueExportColumns() : [],
      ),
    exportIssuesExcel: async (profileId, query, columns) => {
      const r = await app.ExportIssuesExcel(profileId, query, columns)
      return { path: r?.path ?? '', rows: r?.rows ?? 0, unverifiable: r?.unverifiable ?? 0 }
    },
    syncUsers: async (profileId) => {
      const r = await app.SyncUsers(profileId)
      return {
        mode: r?.mode ?? 'full',
        fetched: r?.fetched ?? 0,
        upserted: r?.upserted ?? 0,
        deleted: r?.deleted ?? 0,
        warnings: r?.warnings ?? [],
        durationMs: r?.durationMs ?? 0,
      }
    },
    listUsers: async (profileId, query) => {
      const r = await app.ListUsers(profileId, query)
      // 所属チーム・プロジェクトが空の場合、Go の nil スライスは null で届く
      const rows = (r?.rows ?? []).map((u) => ({
        ...u,
        teamNames: u.teamNames ?? [],
        projectKeys: u.projectKeys ?? [],
        adminProjectKeys: u.adminProjectKeys ?? [],
      }))
      return { rows, total: r?.total ?? 0 }
    },
    getUserExportColumns: async () =>
      normalizeExportColumns(
        typeof app.GetUserExportColumns === 'function' ? await app.GetUserExportColumns() : [],
      ),
    exportUsersExcel: async (profileId, query, columns) => {
      const r = await app.ExportUsersExcel(profileId, query, columns)
      // ユーザ抽出にカスタム属性条件は無いため判定不能は常に 0
      return { path: r?.path ?? '', rows: r?.rows ?? 0, unverifiable: 0 }
    },
    exportBulkTemplate: async (profileId, projectId, query) => {
      const r = await app.ExportBulkTemplate(profileId, projectId, query)
      return { path: r?.path ?? '', rows: r?.rows ?? 0, unverifiable: 0 }
    },
    importBulkFile: async (profileId, projectId, defaultPriorityId) => {
      const r = await app.ImportBulkFile(profileId, projectId, defaultPriorityId)
      // Go の nil スライスは null で届くため、配列は必ず正規化する
      return {
        jobId: r?.jobId ?? 0,
        projectId: r?.projectId ?? projectId,
        totalRows: r?.totalRows ?? 0,
        creates: r?.creates ?? 0,
        updates: r?.updates ?? 0,
        skips: r?.skips ?? 0,
        valid: r?.valid ?? false,
        warnings: r?.warnings ?? [],
        errors: r?.errors ?? [],
        previews: (r?.previews ?? []).map((p) => ({
          ...p,
          changes: p.changes ?? [],
          conflictWarning: p.conflictWarning ?? false,
          // 旧バージョンのバインディング(表示名未実装)では写しの対応表で補う
          // (内部値 create / update / skip を素で見せない)
          actionLabel: p.actionLabel || actionLabel(p.action),
        })),
      }
    },
    runBulkJob: async (profileId, jobId, force, resendSending) => {
      const r = await app.RunBulkJob(profileId, jobId, force, resendSending)
      return {
        jobId: r?.jobId ?? jobId,
        done: r?.done ?? 0,
        failed: r?.failed ?? 0,
        conflict: r?.conflict ?? 0,
        skipped: r?.skipped ?? 0,
        durationMs: r?.durationMs ?? 0,
        warnings: r?.warnings ?? [],
      }
    },
    cancelBulkRun: (profileId, jobId) => app.CancelBulkRun(profileId, jobId),
    listBulkJobs: async (profileId) =>
      ((await app.ListBulkJobs(profileId)) ?? []).map((j) => ({
        ...j,
        // 旧バージョンのバインディング(集計フィールド未実装)でも 0 として扱う
        conflict: j.conflict ?? 0,
        skipped: j.skipped ?? 0,
      })),
    getBulkJobRows: async (profileId, jobId) =>
      ((await app.GetBulkJobRows(profileId, jobId)) ?? []).map((r) => ({
        rowNo: r?.rowNo ?? 0,
        issueKey: r?.issueKey ?? '',
        status: r?.status ?? '',
        // 旧バージョンのバインディング(表示名未実装)では写しの対応表で補う
        // (内部値 pending / sending 等を素で見せない)
        statusLabel: r?.statusLabel || rowStatusLabel(r?.status ?? ''),
        resultIssueId: r?.resultIssueId ?? 0,
        error: r?.error ?? '',
      })),
    exportBulkResultExcel: async (profileId, jobId) => {
      const r = await app.ExportBulkResultExcel(profileId, jobId)
      return { path: r?.path ?? '', rows: r?.rows ?? 0, unverifiable: 0 }
    },
    getMasterData: async (profileId, projectId) => {
      const r = await app.GetMasterData(profileId, projectId)
      return {
        issueTypes: r?.issueTypes ?? [],
        priorities: r?.priorities ?? [],
        statuses: r?.statuses ?? [],
        // 旧バージョンのバインディング(カスタム属性未実装)でも画面を壊さない
        customFields: r?.customFields ?? [],
      }
    },
    getLogInfo: async () => {
      // 旧バージョンのバインディング(GetLogInfo 未実装)でも画面を壊さない
      if (typeof app.GetLogInfo !== 'function') return { path: '', enabled: false }
      const r = await app.GetLogInfo()
      return { path: r?.path ?? '', enabled: r?.enabled ?? false }
    },
    getStorageInfo: async () => {
      // 旧バージョンのバインディング(GetStorageInfo 未実装)では空データを
      // 返さない(「プロファイルなし・ログ無効」と誤読させるため)。
      // 呼び出し側のエラー表示に載せる。
      if (typeof app.GetStorageInfo !== 'function') {
        throw new Error(globalTranslate('common.backend.storageInfoUnavailable'))
      }
      const r = await app.GetStorageInfo()
      return {
        configDir: r?.configDir ?? '',
        storageMode: normalizeStorageMode(r?.storageMode),
        databases: (r?.databases ?? []).map((d) => ({
          profileId: d?.profileId ?? '',
          profileName: d?.profileName ?? '',
          path: d?.path ?? '',
          sizeBytes: d?.sizeBytes ?? 0,
          exists: d?.exists ?? false,
          error: d?.error ?? '',
        })),
        logPath: r?.logPath ?? '',
        logEnabled: r?.logEnabled ?? false,
      }
    },
    getRateLimitStatus: async (profileId) => {
      const r = await app.GetRateLimitStatus(profileId)
      return { categories: r?.categories ?? [] }
    },
    getAppVersion: async () => {
      // 旧バージョンのバインディングでも画面を壊さない
      if (typeof app.GetAppVersion !== 'function') return { version: '' }
      const r = await app.GetAppVersion()
      return { version: r?.version ?? '' }
    },
  }
}
