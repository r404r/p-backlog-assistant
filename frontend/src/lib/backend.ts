// backend.ts
// Wails バインディング呼び出しの型付きラッパー。
// Wails ランタイム上では window.go.main.App のメソッドを呼び出し、
// Wails 外(vite dev / ビルド検証)ではモック実装にフォールバックする。
//
// 注意: シグネチャは app.go の公開メソッド(Wails バインディング)と 1 対 1 の手書き契約。
// Go 側を変更したら、このファイルの型・引数・モック実装も併せて更新すること
// (契約の二重管理は改善課題 R14 として別途整理する)。
//
// TDD 例外(Wails 結合): バインディング呼び出し・モック実装はテスト先行では実装していない
// (契約は Go 側のユニットテストで担保する)。ランタイムに依存しない純ヘルパ
// (formatSyncProgress / actionLabel / rowStatusLabel / customColumnKey / newSyncRunId)は
// backend.test.ts で検証する(R15)。

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

/**
 * 課題同期の進捗の段階(Go 側 internal/sync の Phase と対)。
 * count = 総件数の確認 / fetch = 取得・保存 / deleteScan = 削除検知 / done = 完了。
 */
export type SyncPhase = 'count' | 'fetch' | 'deleteScan' | 'done'

/** 課題同期の進捗(Wails イベント 'sync:progress' のペイロード) */
export interface SyncProgress {
  /**
   * 実行 ID。syncIssues の呼び出し側が採番して渡した値がそのまま返る。
   * 画面はこれが一致するイベントだけを受理する(同じプロファイル・
   * 同じプロジェクトを続けて同期し直した場合や、画面切替で失効した
   * 実行がまだ動いている場合に、新旧を取り違えないため)。
   */
  runId: string
  /** 進捗の発生元プロファイル(補助情報) */
  profileId: string
  /** 進捗の発生元プロジェクト(補助情報) */
  projectId: number
  phase: SyncPhase
  /** 取得済み件数 */
  fetched: number
  /** 総件数(不明な場合は 0。差分同期では総件数が分からない) */
  total: number
}

/** newSyncRunId の連番(同一プロセス内で重複しないようにするため) */
let syncRunSeq = 0

/**
 * 課題同期の実行 ID を採番する。
 *
 * 進捗イベントは syncIssues の応答より先に届くため、実行 ID は
 * 「呼び出す側が呼び出す前に決める」必要がある(応答で受け取る方式では
 * 最初の進捗を取りこぼす)。アプリは 1 プロセス・1 ウィンドウで動くため、
 * 連番 + 時刻 + 乱数で十分に一意になる。
 */
export function newSyncRunId(): string {
  syncRunSeq += 1
  return `sync-${Date.now()}-${syncRunSeq}-${Math.random().toString(36).slice(2, 8)}`
}

/**
 * 同期の進捗を画面表示用の文字列にする(課題抽出画面・同期状態画面で共用)。
 * 総件数が分からない段階では分母を出さない(0 件中と誤解させないため)。
 */
export function formatSyncProgress(p: SyncProgress): string {
  switch (p.phase) {
    case 'count':
      return '総件数を確認中...'
    case 'fetch':
      return p.total > 0
        ? `取得中 ${p.fetched.toLocaleString()} / ${p.total.toLocaleString()} 件`
        : `取得中 ${p.fetched.toLocaleString()} 件`
    case 'deleteScan':
      return `削除された課題を確認中(${p.fetched.toLocaleString()} 件取得済み)`
    case 'done':
      return `取得完了 ${p.fetched.toLocaleString()} 件(仕上げ中...)`
    default:
      return ''
  }
}

/** カスタム属性列の列キーの接頭辞(Go 側 export パッケージの規約 cf_{定義ID} と対) */
export const CUSTOM_COLUMN_PREFIX = 'cf_'

/**
 * カスタム属性の定義 ID から列キーを作る。
 * Excel 出力の列選択・一覧の表示列・IssueRow.customFields のキーで共通に使う。
 */
export function customColumnKey(defId: number): string {
  return `${CUSTOM_COLUMN_PREFIX}${defId}`
}

/**
 * 一括更新の処理区分・行状態の表示名(Go 側 bulk.ActionLabel /
 * main.bulkRowStatusLabels の写し。R14)。
 *
 * **正は Go 側**で、通常の経路では Go が解決した actionLabel / statusLabel を
 * そのまま表示する。この写しを置いているのは次の 3 用途に限る:
 *   1. 旧バージョンのバインディング(表示名を返さない)に当たったときの
 *      フォールバック。内部値(create / pending)を素で見せないため。
 *   2. Wails 外で動くモックバックエンド。
 *   3. 行データを伴わない集計見出し(取り込み結果の「新規追加 / 更新 / 変更なし」)。
 * Go 側でラベルを変えたらここも合わせること(検査は R14 の残課題)。
 */
const ACTION_LABELS: Record<string, string> = {
  create: '新規追加',
  update: '更新',
  skip: '変更なし',
}

const ROW_STATUS_LABELS: Record<string, string> = {
  pending: '未処理',
  sending: '送信中(結果未確認)',
  done: '完了',
  error: '失敗',
  conflict: '競合',
  skip: '変更なし',
}

/** 処理区分の表示名(未知の値はそのまま返す)。ACTION_LABELS の注記を参照 */
export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

/** 行状態の表示名(未知の値はそのまま返す)。ACTION_LABELS の注記を参照 */
export function rowStatusLabel(status: string): string {
  return ROW_STATUS_LABELS[status] ?? status
}

/**
 * カスタム属性 1 定義に対する絞り込み条件(Go 側 customfield.Filter と対)。
 *
 * 定義ごとに 1 条件で、型ごとに使うフィールドが決まっている:
 * - 文字列 / 文章 …………………… text(部分一致)
 * - 数値 / 日付 …………………… min / max(範囲。境界を含む)
 * - リスト系 ……………………… itemIds(選択肢 ID のいずれか一致)
 *
 * 複数の条件は AND で連結される。すべて未指定の条件は無視される。
 */
export interface CustomFieldFilter {
  /** 対象のカスタム属性定義 ID */
  defId: number
  /** 定義の型 ID(比較方法の判断に使う。CustomFieldDef.typeId と同じ値) */
  typeId: number
  /** テキスト系の部分一致(空・空白のみは条件なし) */
  text?: string
  /** 数値・日付の下限(数値は数値文字列、日付は YYYY-MM-DD) */
  min?: string
  /** 数値・日付の上限 */
  max?: string
  /** リスト系で選択された選択肢 ID(いずれか 1 つに一致すれば真) */
  itemIds?: number[]
}

/** 課題検索条件(ローカル DB に対する検索) */
export interface IssueQuery {
  /** 対象プロジェクト ID(必須) */
  projectId: number
  /**
   * キーワード(課題キー + 件名 + 詳細の部分一致。Go 側で NFKC + ケースフォールド正規化)。
   * 半角・全角スペース区切りで複数語を指定できる(連結方法は keywordMode)
   */
  keyword?: string
  /**
   * 複数キーワードの連結方法('and' = すべて含む / 'or' = いずれかを含む)。
   * 未指定・未知の値は 'and'(既定)として扱われる
   */
  keywordMode?: 'and' | 'or'
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
  /**
   * カスタム属性の絞り込み条件(定義ごとに 1 条件・AND)。
   * カスタム属性の値はローカル DB の生 JSON にしか無いため、
   * Go 側では SQL 条件で絞った後に適用される(2 段階検索)
   */
  customFieldFilters?: CustomFieldFilter[]
  /** 画面プレビューの取得上限。未指定ならバックエンドの既定値。Excel 出力時は無視される */
  limit?: number
  /**
   * 画面プレビューの取得開始位置(0 始まり)。検索結果の改ページに使い、
   * 一致した全件のうち先頭 offset 件を読み飛ばして limit 件を返す
   * (total は読み飛ばす前の一致件数のまま)。
   * 未指定・負値は 0(先頭)として扱われる。Excel 出力・一括更新テンプレートの
   * 全件走査では limit と同様に無視される
   */
  offset?: number
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
  /**
   * カスタム属性の表示文字列(キー = Excel 出力と同じ列キー `cf_{定義ID}`)。
   * searchIssues に渡した columns で要求した列だけが入る(整形は Go 側で済み)
   */
  customFields: Record<string, string>
}

/** 課題検索の結果。rows は limit で切り詰められるが total は条件に一致する全件数 */
export interface IssueSearchResult {
  rows: IssueRow[]
  total: number
  /**
   * カスタム属性条件を判定できなかった課題の件数
   * (ローカルに保存された課題データが古い・壊れている行)。
   * 0 でなければ結果は「条件に合う全件」ではないため、画面で警告すること。
   * カスタム属性条件を指定しない検索では常に 0
   */
  unverifiable: number
}

/** 課題詳細に表示するカスタム属性 1 件(表示用の名前と値。整形は Go 側で済み) */
export interface IssueCustomField {
  name: string
  value: string
}

/**
 * 課題コメント 1 件(表示用)。
 *
 * 本文を持つコメントだけが入る。状態変更等の「変更履歴のみの項目」は
 * 本文が無いため件数(IssueDetail.commentsHistoryOnly)だけで伝える。
 */
export interface IssueComment {
  /** 投稿者の表示名(不明なら空文字) */
  authorName: string
  /** 本文(改行を含む生テキスト) */
  content: string
  /** 投稿日時(RFC3339) */
  created: string
}

/**
 * 課題 1 件の詳細(ローカル DB へ取り込んだ時点の内容)。
 *
 * 検索結果の課題キーをクリックしたときのポップアップ表示に使う。
 * 通常は最終同期時点の内容だが、refreshIssueDetail でこの課題だけを
 * 取り込み直した場合はその時点になる(いずれも fetchedAt がその時刻を指す)。
 * Backlog 側の最新とは限らないため、画面は fetchedAt を添えて注記すること。
 */
export interface IssueDetail {
  issueKey: string
  summary: string
  /** 詳細本文(改行を含む生テキスト) */
  description: string
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
  /**
   * 親課題キー(Excel 出力の親課題キー列と同じ規約)。
   * 親なし・判定不能は空文字、ローカルに無い親は `ID:<数値>`
   */
  parentIssueKey: string
  /**
   * カスタム属性(課題が持つ全件)。
   * 並びは定義順ではなく、同期時の課題レスポンスに現れた順
   * (定義取得の API 往復を避けるため、Go 側は生 JSON の名前だけを使う)
   */
  customFields: IssueCustomField[]
  /** この課題をローカルへ取り込んだ時刻(RFC3339。不明なら空文字) */
  fetchedAt: string
  /**
   * コメント(新しい順。本文を持つものだけ)。
   *
   * コメントは**同期では取得されない**。refreshIssueDetail を実行した課題にだけ
   * 入るため、未取得の課題では空配列になる(空配列 = 「コメントが無い」ではない。
   * 未取得かどうかは commentsFetchedAt で判定すること)。
   */
  comments: IssueComment[]
  /** コメントを取得した時刻(RFC3339)。空文字 = 未取得 */
  commentsFetchedAt: string
  /** 本文が無い(状態変更等の変更履歴のみの)項目の件数 */
  commentsHistoryOnly: number
  /** 取得上限に達し、古いコメントを取得しきれていない */
  commentsTruncated: boolean
  /**
   * 部分的な失敗の警告(コメントだけ取得できなかった等)。
   * 課題本体の内容は有効なので、画面は詳細を表示したうえでこの警告を添える。
   * getIssueDetail(ローカル参照のみ)では常に空配列。
   */
  warnings: string[]
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
  /**
   * カスタム属性条件を判定できず、出力から外れた課題の件数(課題抽出の出力のみ)。
   * 0 でなければ出力ファイルは「条件に合う全件」ではない
   */
  unverifiable: number
}

/**
 * Excel 出力の列(Go 側 export.ColumnMeta と対。R14)。
 *
 * 列キー・ラベル・既定選択はすべて Go 側の列定義から供給される。
 * 画面は受け取った内容を並べるだけにして、ラベルが Excel のヘッダと
 * ずれないようにする(以前は画面「作成日」/ Excel「作成日時」のずれがあった)。
 */
export interface ExportColumn {
  /** 出力時に指定する列キー */
  key: string
  /** 画面に表示するラベル(Excel のヘッダと同じ文字列) */
  label: string
  /** 既定で選択する列かどうか */
  byDefault: boolean
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
  /** この行の処理区分。skip は「変更が 1 つも無い行」(送信しない) */
  action: 'create' | 'update' | 'skip'
  /**
   * 処理区分の表示名(Go 側 bulk.ActionLabel で解決済み。結果 Excel と同じ文言)。
   * 表示名の対応表を画面に持たないための項目(R14)。
   */
  actionLabel: string
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
  /** 変更が 1 つも無く、送信しない行数(skip 行) */
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
  /**
   * 行の状態の表示名(Go 側 bulkRowStatusLabel で解決済み。結果 Excel と同じ文言)。
   * 表示名の対応表を画面に持たないための項目(R14)。
   */
  statusLabel: string
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
  /**
   * 課題を同期する。runId は進捗イベント(onSyncProgress)と突き合わせるための
   * 実行 ID(newSyncRunId で採番する。進捗を使わない呼び出しは空文字でよい)。
   */
  syncIssues(
    profileId: string,
    projectId: number,
    mode: SyncMode,
    runId: string,
  ): Promise<SyncResult>
  /**
   * ローカル DB から課題を検索する(API は呼ばない)。
   * @param columns 一覧に表示する列キー(Excel 出力と同じ形式)。
   *                `cf_{定義ID}` を含めると、その属性の値が IssueRow.customFields に入る
   */
  searchIssues(
    profileId: string,
    query: IssueQuery,
    columns?: string[],
  ): Promise<IssueSearchResult>
  /**
   * ローカル DB から課題 1 件の詳細を返す(API は呼ばない)。
   * 課題がローカルに無い場合はエラーになる(空の詳細は返らない)。
   */
  getIssueDetail(profileId: string, projectId: number, issueKey: string): Promise<IssueDetail>
  /**
   * 課題 1 件を Backlog から取得し直してローカル DB へ反映し、反映後の詳細を返す
   * (詳細ポップアップの「最新の状態を取得」)。getIssueDetail と違い API を呼ぶ。
   *
   * 反映は同期と同じ変換を通るため、検索索引もこの課題ぶんだけ最新になる
   * (プロジェクトの同期状態・最終同期時刻は変わらない)。
   * Backlog 上で削除されている場合はエラーになり、ローカルの内容は変わらない。
   */
  refreshIssueDetail(profileId: string, projectId: number, issueKey: string): Promise<IssueDetail>
  /** 条件フォームの状態・担当者候補を返す */
  listFilterOptions(profileId: string, projectId: number): Promise<FilterOptions>
  /** データ種別ごとの同期状態一覧を返す */
  getSyncState(profileId: string): Promise<SyncStateRow[]>
  /**
   * 課題抽出の列選択に出す固定列(列キー・ラベル・既定選択)を返す。
   * カスタム属性列は含まない(getMasterData で取得した定義から画面が組み立てる)。
   */
  getIssueExportColumns(): Promise<ExportColumn[]>
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
  /** ユーザ抽出の列選択に出す列(列キー・ラベル・既定選択)を返す */
  getUserExportColumns(): Promise<ExportColumn[]>
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
   * @param query     テンプレートに含める課題の条件(条件が空なら対象プロジェクトの全件。
   *                  カスタム属性での絞り込みは未対応)
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
    throw new Error('出力できる列の情報を取得できませんでした(アプリを更新してください)')
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
  }
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
        throw new Error(
          '課題の詳細はこのバージョンのアプリでは表示できません(アプリを更新してください)',
        )
      }
      return normalizeIssueDetail(await app.GetIssueDetail(profileId, projectId, issueKey), issueKey)
    },
    refreshIssueDetail: async (profileId, projectId, issueKey) => {
      // 旧バージョンのバインディング(メソッド未実装)では、取得できていないのに
      // 「最新の状態」を表示してしまわないようエラーにする(getIssueDetail と同じ流儀)
      if (typeof app.RefreshIssueDetail !== 'function') {
        throw new Error(
          '最新の状態の取得はこのバージョンのアプリでは利用できません(アプリを更新してください)',
        )
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

function emitMockProgress(p: BulkProgress): void {
  for (const cb of mockProgressListeners) cb(p)
}

type SyncProgressCallback = (p: SyncProgress) => void

const mockSyncProgressListeners = new Set<SyncProgressCallback>()

function emitMockSyncProgress(p: SyncProgress): void {
  for (const cb of mockSyncProgressListeners) cb(p)
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
 * クリップボードへ文字列をコピーする。
 *
 * Wails ランタイムの ClipboardSetText を第一の経路にする。WebView 内の
 * navigator.clipboard は権限・フォーカスの条件に左右されるため、デスクトップ
 * アプリとして確実に動く OS 側の API を優先する。ランタイムが無い環境
 * (vite dev / テスト)や古いランタイム(ClipboardSetText 未実装)では
 * navigator.clipboard.writeText へフォールバックする。
 *
 * どちらも使えない場合は例外を投げる(コピーできていないのに成功したように
 * 見せると、利用者が空のクリップボードを貼り付けてしまうため)。
 */
export async function copyToClipboard(text: string): Promise<void> {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.ClipboardSetText === 'function') {
    // 失敗は reject で届くが、真偽値で失敗を返す実装に備えて false も失敗として扱う
    const ok = await rt.ClipboardSetText(text)
    if (ok === false) throw new Error('クリップボードへの書き込みが拒否されました')
    return
  }
  const clipboard = navigator.clipboard as Clipboard | undefined
  if (clipboard && typeof clipboard.writeText === 'function') {
    await clipboard.writeText(text)
    return
  }
  throw new Error('この環境ではクリップボードを利用できません')
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

/**
 * 課題同期の進捗イベント('sync:progress')を購読する。戻り値を呼ぶと購読を解除する。
 *
 * 経路の扱いは onBulkProgress と同じ(Wails ランタイムが無ければモックの
 * 簡易エミッタを購読する)。イベントはプロファイル・プロジェクトを問わず
 * 届くため、画面側で「自分が開始した同期か」を必ず確認すること。
 */
export function onSyncProgress(cb: (p: SyncProgress) => void): () => void {
  const rt = findWailsRuntime()
  if (rt) {
    const off = rt.EventsOn('sync:progress', (...data: unknown[]) => {
      const p = data[0] as Partial<SyncProgress> | undefined
      if (!p) return
      cb({
        runId: p.runId ?? '',
        profileId: p.profileId ?? '',
        projectId: p.projectId ?? 0,
        phase: p.phase ?? 'fetch',
        fetched: p.fetched ?? 0,
        total: p.total ?? 0,
      })
    })
    // Wails の EventsOn は解除関数を返すが、バージョンにより undefined の場合がある
    return typeof off === 'function' ? off : () => {}
  }
  mockSyncProgressListeners.add(cb)
  return () => {
    mockSyncProgressListeners.delete(cb)
  }
}
