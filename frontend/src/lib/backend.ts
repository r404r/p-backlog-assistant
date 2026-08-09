// backend.ts
// Wails バインディング呼び出しの型付きラッパー。
// Wails ランタイム上では window.go.main.App のメソッドを呼び出し、
// Wails 外(vite dev / ビルド検証)ではモック実装にフォールバックする。
//
// 注意: シグネチャはマイルストーン 1 時点の想定。バックエンドとの最終結合時に調整する。

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
  }
}

// ---------------------------------------------------------------------------
// モック実装(vite dev / ビルド検証用)
// ---------------------------------------------------------------------------

const URL_PATTERN = /^https:\/\/[a-z0-9][a-z0-9-]*\.backlog\.(jp|com)\/?$/i

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function createMockBackend(): Backend {
  // メモリ上のみ。リロードで消える。
  const profiles: Profile[] = []
  const keys = new Map<string, string>() // profileId -> apiKey(モック用)
  let seq = 0
  let activeId = ''

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
