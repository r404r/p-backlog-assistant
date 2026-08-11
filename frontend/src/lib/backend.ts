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
  /**
   * 同期状態(鮮度)の取得に失敗したかどうか。
   * true のとき lastSyncedAt は「未同期」ではなく「不明」を意味するため、
   * UI は未同期の警告を出さず、取得できなかった旨を表示する。
   */
  syncStateUnknown: boolean
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

/** ユーザ検索条件(ローカル DB に対する検索) */
export interface UserQuery {
  /** キーワード(名前・ユーザ ID・メールアドレスの部分一致) */
  keyword?: string
  /**
   * ロール種別(API 実値。
   * 1=管理者 / 2=一般ユーザ / 3=レポーター / 4=閲覧者 /
   * 5=ゲストレポーター / 6=ゲスト閲覧者。未指定・0 ならすべて)
   */
  roleType?: number
  /** 画面プレビューの取得上限。未指定ならバックエンドの既定値。Excel 出力時は無視される */
  limit?: number
}

/** ユーザ 1 件(検索結果の表示・Excel 出力用) */
export interface UserRow {
  /** Backlog のユーザ ID(数値) */
  id: number
  /** ログイン用のユーザ ID(Backlog API の userId) */
  userCode: string
  /** 表示名 */
  name: string
  /** メールアドレス */
  mailAddress: string
  /**
   * スペース全体のロール種別(API 実値。
   * 1=管理者 / 2=一般ユーザ / 3=レポーター / 4=閲覧者 /
   * 5=ゲストレポーター / 6=ゲスト閲覧者)
   */
  roleType: number
  /**
   * roleType の日本語表記(Go 側で解決済み)。
   * 既知の値は名称、未知の値は「不明(N)」形式で数値を含む。
   */
  roleName: string
  /** 所属チーム名 */
  teamNames: string[]
  /** 参加プロジェクトのキー */
  projectKeys: string[]
  /** 管理者として登録されているプロジェクトのキー */
  adminProjectKeys: string[]
}

/** ユーザ検索の結果。rows は limit で切り詰められるが total は条件に一致する全件数 */
export interface UserSearchResult {
  rows: UserRow[]
  total: number
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

// --- 一括更新・追加(画面 3) ---------------------------------------------

/** 取り込み時の検証エラー(1 行 1 件) */
export interface BulkValidationError {
  /** Excel の行番号(ヘッダを除く 1 始まりではなく、シート上の行番号) */
  rowNo: number
  /** エラー内容(日本語) */
  message: string
}

/** dry-run プレビューの 1 行 */
export interface BulkPreviewRow {
  /** Excel の行番号 */
  rowNo: number
  /** この行の処理区分。skip は検証エラー等で実行対象外になる行 */
  action: 'create' | 'update' | 'skip'
  /** 課題キー(新規追加行は空文字) */
  issueKey: string
  /** 件名 */
  summary: string
  /** 変更内容(「状態: 未対応 → 処理中」等の human readable な差分。Go 側で生成) */
  changes: string[]
  /**
   * 取り込み時点でリモートの更新日時が base_updated と異なっていたか。
   * true でも実行はできるが、実行直前の再チェックで conflict として skip される可能性が高い。
   */
  conflictWarning: boolean
}

/** Excel 取り込み(検証 + dry-run)の結果 */
export interface BulkImportResult {
  /** 採番されたジョブ ID(実行・再開時に指定する) */
  jobId: number
  /** 対象プロジェクト ID(テンプレートに固定されている値) */
  projectId: number
  /** 取り込んだデータ行数(ヘッダを除く) */
  totalRows: number
  /** 新規追加になる行数 */
  creates: number
  /** 更新になる行数 */
  updates: number
  /** 実行対象外(変更なし・エラー)の行数 */
  skips: number
  /** すべての検証を通過し、実行可能かどうか */
  valid: boolean
  /** 取り込み時の警告(旧テンプレートのプロジェクト ID 欠落・担当者検証のフォールバック等) */
  warnings: string[]
  /** 検証エラー一覧 */
  errors: BulkValidationError[]
  /** dry-run プレビュー */
  previews: BulkPreviewRow[]
}

/** 一括実行の結果サマリ */
export interface BulkRunResult {
  jobId: number
  /** 成功件数 */
  done: number
  /** 失敗件数 */
  failed: number
  /** 競合により書き込まなかった件数 */
  conflict: number
  /**
   * 取り込み時に「変更なし」と判定して送信対象から外した行数(skip 行)。
   * キャンセル・中断で処理しなかった行はここには含まれない。
   * 未処理の件数は warnings と、ジョブ履歴の pending / sending で確認する。
   */
  skipped: number
  /** 所要時間(ミリ秒) */
  durationMs: number
  /** 警告(キャンセル・部分実行・再送確認の必要等。日本語) */
  warnings: string[]
}

/** ジョブ履歴の 1 行 */
export interface BulkJobRow {
  jobId: number
  projectId: number
  /** ジョブ種別(bulk_update 等) */
  kind: string
  /** 作成日時(RFC3339) */
  createdAt: string
  /** ジョブ状態(pending / running / done / failed / canceled 等) */
  status: string
  /** 対象行数 */
  total: number
  /** 完了行数 */
  done: number
  /** 失敗行数 */
  failed: number
  /** 未処理行数 */
  pending: number
  /**
   * 送信中のまま残っている行数。
   * 0 より大きい場合は実行が中断された可能性があり、再開時に再送するかの確認が必要
   * (新規追加の二重作成を防ぐため、自動では再送しない)。
   */
  sending: number
  /**
   * 競合により書き込まなかった行数。
   * 0 より大きい場合は「競合を上書きして再実行(force)」で処理できる。
   */
  conflict: number
  /**
   * 取り込み時に「変更なし」と判定して送信対象から外した行数(skip 行)。
   * キャンセル・中断で処理しなかった行は pending / sending に残る。
   */
  skipped: number
}

/** ジョブ行明細の 1 行(履歴の展開表示用) */
export interface BulkJobRowDetail {
  /** Excel の行番号 */
  rowNo: number
  /** 課題キー(新規追加行は空文字) */
  issueKey: string
  /** 行の状態(pending / sending / done / error / conflict / skip) */
  status: string
  /** 新規追加で作成済みの場合の課題 ID(未作成なら 0) */
  resultIssueId: number
  /** エラー内容(日本語。エラーが無ければ空文字) */
  error: string
}

/** マスタデータの 1 項目(種別・優先度・状態に共通) */
export interface MasterItem {
  id: number
  name: string
}

/** リスト系カスタム属性の選択肢(Go 側 main.CustomFieldItemDTO と対) */
export interface CustomFieldItem {
  id: number
  name: string
}

/** カスタム属性の定義(Go 側 main.CustomFieldDefDTO と対) */
export interface CustomFieldDef {
  id: number
  /** 型 ID(1=文字列 / 2=文章 / 3=数値 / 4=日付 / 5=単一リスト / 6=複数リスト / 7=チェックボックス / 8=ラジオ) */
  typeId: number
  /** 型の表示名(Go 側で解決済み。未知の型は「不明(N)」) */
  typeName: string
  name: string
  description: string
  /** 入力必須かどうか */
  required: boolean
  /** 適用対象の課題種別 ID(空配列 = 全課題種別に適用) */
  applicableIssueTypes: number[]
  /**
   * リスト系で「その他」の直接入力を許すか。
   * API 応答に含まれない場合は false(「不明」と区別できないため、
   * 入力可否の判定には使わず表示・案内のみに使う)
   */
  allowInput: boolean
  /** リスト系で課題登録時に選択肢自体の追加を許すか(「その他」入力とは別機能) */
  allowAddItem: boolean
  /** リスト系の選択肢(リスト系以外は空配列) */
  items: CustomFieldItem[]
}

/** プロジェクトのマスタデータ(取り込み時の既定値選択・表示に使う) */
export interface MasterData {
  issueTypes: MasterItem[]
  priorities: MasterItem[]
  statuses: MasterItem[]
  /** カスタム属性の定義(未対応プラン・権限不足のスペースでは空配列) */
  customFields: CustomFieldDef[]
}

/** 一括実行の進捗(Wails イベント 'bulk:progress' のペイロード) */
export interface BulkProgress {
  jobId: number
  /** 処理済み行数 */
  processed: number
  /** 対象行数 */
  total: number
}

/** レート制限の区分別残量(Go 側 backlogclient.CategoryStatus と対) */
export interface RateLimitCategory {
  /** 区分(read=読み込み / update=更新 / search=検索 / icon=アイコン) */
  name: 'read' | 'update' | 'search' | 'icon'
  /** 毎分上限(未取得は 0) */
  limit: number
  /** 現在のウィンドウの残量(未取得は 0) */
  remaining: number
  /** 現在のウィンドウのリセット時刻(Unix 秒。未取得は 0) */
  resetUnix: number
  /** サーバ実値を観測済みか。false のとき UI は「未取得」を表示する */
  observed: boolean
}

/** レート制限の残量スナップショット(常に read/update/search/icon の 4 件・この順) */
export interface RateLimitStatus {
  categories: RateLimitCategory[]
}

/** アプリのバージョン情報(Go 側 main.AppVersionInfo と対) */
export interface AppVersion {
  /** ビルド時に埋め込まれたバージョン(例: v1.0.0。ローカル開発ビルドは dev) */
  version: string
}

/** 動作ログの状態(Go 側 main.LogInfo と対) */
export interface LogInfo {
  /** ログファイルのパス(無効な場合は空文字) */
  path: string
  /** ログ出力が有効かどうか */
  enabled: boolean
}

/** 1 プロファイル分のローカル DB の所在(Go 側 service.DatabaseInfo と対) */
export interface StorageDatabase {
  profileId: string
  profileName: string
  /** DB ファイルのパス(未接続・URL 不正で特定できない場合は空文字) */
  path: string
  /** DB 本体 + WAL + SHM のうち存在するファイルの合計バイト数(未作成なら 0) */
  sizeBytes: number
  /** DB 本体(.db)が作成済みか。WAL / SHM のみ残存の場合は false */
  exists: boolean
  /**
   * 所在・サイズを確認できなかった理由(正常時は空文字)。
   * 「未作成」(exists = false・error = 空)とは区別して表示すること。
   */
  error: string
}

/** 保存データの所在(Go 側 main.StorageInfo と対) */
export interface StorageInfo {
  /** 設定ファイル(config.json)の置き場所 */
  configDir: string
  /** プロファイルごとのローカル DB */
  databases: StorageDatabase[]
  /** 動作ログのパス(無効な場合は空文字) */
  logPath: string
  /** 動作ログが有効かどうか */
  logEnabled: boolean
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

  // --- ユーザ抽出 -----------------------------------------------------------

  /**
   * Backlog からユーザ(+ チーム・プロジェクト参加情報)を同期する。
   * 権限不足の場合はプロジェクト単位の取得へ縮退し、その旨が warnings に入る。
   */
  syncUsers(profileId: string): Promise<SyncResult>
  /** ローカル DB からユーザを検索する(API は呼ばない) */
  listUsers(profileId: string, query: UserQuery): Promise<UserSearchResult>
  /**
   * 検索条件に一致するユーザを Excel 出力する(表示上限に関わらず全件)。
   * 保存先は Go 側の保存ダイアログで選択する。キャンセル時は path が空文字。
   * @param columns 出力する列キー(UserRow のキー)を表示順で指定する
   */
  exportUsersExcel(profileId: string, query: UserQuery, columns: string[]): Promise<ExportResult>

  // --- 一括更新・追加 -------------------------------------------------------

  /**
   * 一括更新テンプレート(xlsx)を出力する。
   * 保存先は Go 側の保存ダイアログで選択する。キャンセル時は path が空文字。
   * @param projectId 対象プロジェクト ID(テンプレートに固定される)
   * @param query     テンプレートに含める課題の条件(現状は projectId のみを使う)
   */
  exportBulkTemplate(
    profileId: string,
    projectId: number,
    query: IssueQuery,
  ): Promise<ExportResult>
  /**
   * 記入済み Excel を取り込み、検証と dry-run プレビューを行う(Backlog への書き込みはしない)。
   * 取り込むファイルは Go 側のファイル選択ダイアログで選ぶ。
   * キャンセル時は jobId=0・totalRows=0 の結果が返る。
   * @param defaultPriorityId 優先度が未入力の新規行に適用する既定の優先度 ID
   */
  importBulkFile(
    profileId: string,
    projectId: number,
    defaultPriorityId: number,
  ): Promise<BulkImportResult>
  /**
   * 取り込み済みジョブを実行する(Backlog へ 1 件ずつ書き込む)。
   * @param force         true なら競合(リモートが更新済み)を無視して強制上書きする
   * @param resendSending true なら中断時に sending のまま残った行を再送する
   *                      (新規追加の二重作成の可能性があるため、既定は false)
   */
  runBulkJob(
    profileId: string,
    jobId: number,
    force: boolean,
    resendSending: boolean,
  ): Promise<BulkRunResult>
  /** 実行中のジョブを中断する(送信済みの行は取り消さない) */
  cancelBulkRun(profileId: string, jobId: number): Promise<void>
  /** ジョブ履歴を新しい順に返す */
  listBulkJobs(profileId: string): Promise<BulkJobRow[]>
  /** 指定ジョブの行明細を行番号順に返す(履歴の展開表示用) */
  getBulkJobRows(profileId: string, jobId: number): Promise<BulkJobRowDetail[]>
  /**
   * 指定ジョブの結果レポートを Excel 出力する。
   * 保存先は Go 側の保存ダイアログで選択する。キャンセル時は path が空文字。
   */
  exportBulkResultExcel(profileId: string, jobId: number): Promise<ExportResult>
  /** 種別・優先度・状態のマスタデータを返す */
  getMasterData(profileId: string, projectId: number): Promise<MasterData>

  // --- 動作ログ -------------------------------------------------------------

  /** 動作ログの出力先パスと有効・無効を返す */
  getLogInfo(): Promise<LogInfo>

  // --- 保存データ -----------------------------------------------------------

  /** 設定ディレクトリ・ローカル DB・動作ログの所在を返す(アプリ情報画面用) */
  getStorageInfo(): Promise<StorageInfo>

  // --- レート制限 -----------------------------------------------------------

  /**
   * レート制限の区分別残量を返す(サーバから観測した実測値のみ。追加の API 通信は
   * 発生しないため、画面からの定期的な参照も安全)。
   */
  getRateLimitStatus(profileId: string): Promise<RateLimitStatus>

  /** アプリのバージョンを返す(サイドバーのフッタ表示用) */
  getAppVersion(): Promise<AppVersion>
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
  SyncUsers(profileId: string): Promise<SyncResult>
  ListUsers(profileId: string, query: UserQuery): Promise<UserSearchResult>
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
}

/**
 * window.runtime をそのまま返す(Wails 外では null)。
 * 個々の API の有無は呼び出し側で確認する(ランタイムのバージョン差を吸収するため)。
 */
function findWailsRuntimeObject(): Partial<WailsRuntime> | null {
  const w = window as unknown as Record<string, unknown>
  const rt = w['runtime'] as Partial<WailsRuntime> | undefined
  return rt ?? null
}

function findWailsRuntime(): WailsRuntime | null {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.EventsOn === 'function') {
    return rt as WailsRuntime
  }
  return null
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
    syncIssues: async (profileId, projectId, mode) => {
      const r = await app.SyncIssues(profileId, projectId, mode)
      return {
        mode: r?.mode ?? mode,
        fetched: r?.fetched ?? 0,
        upserted: r?.upserted ?? 0,
        deleted: r?.deleted ?? 0,
        warnings: r?.warnings ?? [],
        durationMs: r?.durationMs ?? 0,
      }
    },
    searchIssues: async (profileId, query) => {
      const r = await app.SearchIssues(profileId, query)
      return { rows: r?.rows ?? [], total: r?.total ?? 0 }
    },
    listFilterOptions: async (profileId, projectId) => {
      const r = await app.ListFilterOptions(profileId, projectId)
      return { statuses: r?.statuses ?? [], assignees: r?.assignees ?? [] }
    },
    getSyncState: async (profileId) => (await app.GetSyncState(profileId)) ?? [],
    exportIssuesExcel: (profileId, query, columns) =>
      app.ExportIssuesExcel(profileId, query, columns),
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
    exportUsersExcel: (profileId, query, columns) =>
      app.ExportUsersExcel(profileId, query, columns),
    exportBulkTemplate: async (profileId, projectId, query) => {
      const r = await app.ExportBulkTemplate(profileId, projectId, query)
      return { path: r?.path ?? '', rows: r?.rows ?? 0 }
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
        resultIssueId: r?.resultIssueId ?? 0,
        error: r?.error ?? '',
      })),
    exportBulkResultExcel: async (profileId, jobId) => {
      const r = await app.ExportBulkResultExcel(profileId, jobId)
      return { path: r?.path ?? '', rows: r?.rows ?? 0 }
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
        throw new Error(
          '保存データの情報はこのバージョンのアプリでは取得できません(アプリを更新してください)',
        )
      }
      const r = await app.GetStorageInfo()
      return {
        configDir: r?.configDir ?? '',
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
    roleName: '一般ユーザー',
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
    roleName: '一般ユーザー',
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
// モック実行時のみ、この簡易エミッタ経由で 'bulk:progress' 相当を配信する
// (画面側は onBulkProgress を呼ぶだけで、どちらの経路かを意識しない)。

type BulkProgressCallback = (p: BulkProgress) => void

const mockProgressListeners = new Set<BulkProgressCallback>()

function emitMockProgress(p: BulkProgress): void {
  for (const cb of mockProgressListeners) cb(p)
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
  const users: UserRow[] = MOCK_USERS.map((u) => ({ ...u }))
  const issuesByProject = new Map<number, IssueRow[]>()
  const syncState: SyncStateRow[] = []

  // 一括更新のモック状態。ジョブは新しい順に保持する。
  const jobs: BulkJobRow[] = []
  /** ジョブ ID -> 行明細(履歴の展開表示・結果レポートの確認用) */
  const jobRows = new Map<number, BulkJobRowDetail[]>()
  const canceledJobs = new Set<number>()
  let jobSeq = 0
  // 1 回目の取り込みは検証エラーあり、2 回目以降はエラー無しにして、
  // 「実行できない状態」と「実行できる状態」の両方を手動確認できるようにする。
  let importSeq = 0

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

    async exportUsersExcel(_profileId, query, columns) {
      await delay(700)
      if (columns.length === 0) throw new Error('出力する列を 1 つ以上選択してください')
      const matched = filterMockUsers(users, query)
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return { path: '(モック)保存ダイアログは Wails 実行時のみ表示されます', rows: matched.length }
    },

    async exportBulkTemplate(_profileId, projectId) {
      await delay(700)
      const all = issuesByProject.get(projectId) ?? []
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return { path: '(モック)保存ダイアログは Wails 実行時のみ表示されます', rows: all.length }
    },

    async importBulkFile(_profileId, projectId, defaultPriorityId) {
      await delay(900)
      const all = issuesByProject.get(projectId) ?? []
      // 既存課題の先頭数件を「更新」、末尾に「新規追加」と「エラー行」を足した固定シナリオ
      const targets = all.slice(0, 4)
      const previews: BulkPreviewRow[] = targets.map((r, i) => ({
        rowNo: i + 2,
        action: 'update',
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
      importSeq += 1
      const withErrors = importSeq === 1
      const errors: BulkValidationError[] = withErrors
        ? [{ rowNo: targets.length + 3, message: '(モック)種別ID が未入力です(新規追加行の必須項目)' }]
        : []
      if (withErrors) {
        previews.push({
          rowNo: targets.length + 3,
          action: 'skip',
          issueKey: '',
          summary: '(モック)検証エラーの行',
          changes: [],
          conflictWarning: false,
        })
      }

      jobSeq += 1
      const totalRows = previews.length
      const creates = previews.filter((p) => p.action === 'create').length
      const updates = previews.filter((p) => p.action === 'update').length
      const skips = previews.filter((p) => p.action === 'skip').length
      const job: BulkJobRow = {
        jobId: jobSeq,
        projectId,
        kind: 'bulk_update',
        createdAt: new Date().toISOString(),
        status: 'pending',
        total: totalRows,
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
          error:
            p.action === 'skip'
              ? errors.find((e) => e.rowNo === p.rowNo)?.message ?? ''
              : '',
        })),
      )
      return {
        jobId: job.jobId,
        projectId,
        totalRows,
        creates,
        updates,
        skips,
        // エラー行があるうちは実行できない(画面の「実行」ボタン無効の確認用)
        valid: errors.length === 0,
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
            // 新規追加行は作成済みの課題 ID が付く(再送時の二重作成防止の突合に使う)
            row.resultIssueId = 900000 + row.rowNo
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
      return rows.map((r) => ({ ...r })).sort((a, b) => a.rowNo - b.rowNo)
    },

    async exportBulkResultExcel(_profileId, jobId) {
      await delay(600)
      const rows = jobRows.get(jobId) ?? []
      // モックでは保存ダイアログを出せないため、ダミーのパスを返す
      return { path: '(モック)保存ダイアログは Wails 実行時のみ表示されます', rows: rows.length }
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

/**
 * 外部リンクを OS の既定ブラウザで開く。
 *
 * デスクトップの WebView 内で通常のリンク遷移を行うとアプリの画面自体が
 * 外部サイトに置き換わってしまうため、Wails ランタイムの BrowserOpenURL を使う。
 * ランタイムが無い環境(vite dev / ビルド検証)や、古いランタイムで
 * BrowserOpenURL が存在しない場合は window.open にフォールバックする。
 */
export function openExternalURL(url: string): void {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.BrowserOpenURL === 'function') {
    rt.BrowserOpenURL(url)
    return
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

/**
 * 一括実行の進捗イベント('bulk:progress')を購読する。戻り値を呼ぶと購読を解除する。
 *
 * Wails ランタイムの EventsOn は実行時に window.runtime から参照する
 * (バインディング生成物に型が無いため)。ランタイムが無い環境(vite dev / ビルド検証)では
 * モックバックエンドの簡易エミッタを購読する。どちらも存在しない場合は
 * 解除だけを行う no-op を返し、画面側は分岐せずに使える。
 */
export function onBulkProgress(cb: (p: BulkProgress) => void): () => void {
  const rt = findWailsRuntime()
  if (rt) {
    const off = rt.EventsOn('bulk:progress', (...data: unknown[]) => {
      const p = data[0] as Partial<BulkProgress> | undefined
      if (!p) return
      cb({ jobId: p.jobId ?? 0, processed: p.processed ?? 0, total: p.total ?? 0 })
    })
    // Wails の EventsOn は解除関数を返すが、バージョンにより undefined の場合がある
    return typeof off === 'function' ? off : () => {}
  }
  mockProgressListeners.add(cb)
  return () => {
    mockProgressListeners.delete(cb)
  }
}
