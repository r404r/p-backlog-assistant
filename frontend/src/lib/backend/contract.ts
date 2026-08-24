
// Go/Wailsとフロントエンド間の型契約、および契約値の純粋helper。
// 実行環境への接続はwails.ts、開発用データと挙動はmock.tsが所有する。

import { currentLanguage, globalTranslate, type TranslateFn } from '../format'
import type { Language } from '../i18n'

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
 *
 * 多言語対応(設計 §3.1 / §3.2): 文言はカタログ(`sync.progress.*`)から引く。
 * 翻訳関数と表示言語は**呼び出し元(画面)から明示的に渡す**こと。省略時は
 * グローバル Composer(lib/i18n.ts のシングルトン)を使うが、テストが
 * `mountWithI18n` で画面ごとの独立インスタンスを使う場合は食い違うため、
 * 画面からは `formatSyncProgress(p, t, language)` の形で呼ぶ。
 * 桁区切りは実行環境ロケールではなく**解決済みの表示言語**を明示指定する。
 */
export function formatSyncProgress(
  p: SyncProgress,
  t: TranslateFn = globalTranslate,
  language: Language = currentLanguage(),
): string {
  const num = (n: number) => n.toLocaleString(language)
  switch (p.phase) {
    case 'count':
      return t('sync.progress.count')
    case 'fetch':
      return p.total > 0
        ? t('sync.progress.fetch', { fetched: num(p.fetched), total: num(p.total) })
        : t('sync.progress.fetchUnknownTotal', { fetched: num(p.fetched) })
    case 'deleteScan':
      return t('sync.progress.deleteScan', { fetched: num(p.fetched) })
    case 'done':
      return t('sync.progress.done', { fetched: num(p.fetched) })
    default:
      return ''
  }
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
 *
 * **多言語対応(設計 §3.1)による方針変更 — 移行済み**: 表示経路はこの日本語の
 * 写しではなく、生の機械値(action / status)を lib/enumLabels.ts でフロント翻訳
 * する方式へ移行した。フェーズ 1 の画面変換が済んだ時点で、**ここと
 * actionLabel() / rowStatusLabel() を参照する画面は 1 つも無い**
 * (BulkUpdateView は action / status から translateAction / translateRowStatus で
 * 翻訳する)。残しているのは上記 1(旧バインディングのフォールバック)と
 * 2(モックバックエンド)の**契約フィールドを埋める用途**だけで、
 * ユーザに見える文字列としては使わない。Go 側が旧バインディングを切ったときに
 * まとめて削除する。
 */
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

/**
 * 保存先の決定方法(Go 側 storagepath.Mode と対)。
 *
 * 明示指定(portable.txt / 環境変数)が使えない場合は起動時にエラーとするため、
 * 「フォールバック中」という状態は存在しない(この 3 値のみ)。
 */
export type StorageMode = 'default' | 'env' | 'portable'

/** 保存先モードの一覧(表示の選択肢と正規化に使う) */
export const STORAGE_MODES: readonly StorageMode[] = ['default', 'env', 'portable']

/** 保存データの所在(Go 側 main.StorageInfo と対) */
export interface StorageInfo {
  /** 設定ファイル(config.json)の置き場所 */
  configDir: string
  /** 保存先(config.json・data/ の基点)の決定方法 */
  storageMode: StorageMode
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
