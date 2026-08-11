/**
 * プロジェクト選択の共有状態。
 *
 * 課題抽出・一括更新・同期状態の 3 画面はサイドバー(App.vue の `<component :is>`)で
 * 切り替えるたびに破棄・再生成されるため、各画面がローカルに選択を持つと
 * 切り替えのたびに一覧の先頭へ戻ってしまう。ここでモジュールレベルの ref を共有し、
 * さらに接続先プロファイルごとに localStorage へ保存して次回起動時も維持する。
 *
 * TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
 * 将来テストできるよう、キー生成・保存値の解釈・一覧に対する解決は
 * 副作用のない純粋関数として切り出してある。
 */
import { onUnmounted, ref, watch } from 'vue'

/** 保存キーの接頭辞(App.vue の `ba.sidebarCollapsed` と同じ流儀) */
const STORAGE_PREFIX = 'ba.selectedProjectId.'

/** プロファイルごとの保存キーを作る(選択はプロファイル横断で共有しない) */
export function projectSelectionKey(profileId: string): string {
  return `${STORAGE_PREFIX}${profileId}`
}

/** localStorage の生値をプロジェクト ID へ変換する(未保存・不正値は 0 = 未選択) */
export function parseStoredProjectId(raw: string | null): number {
  if (!raw) return 0
  const id = Number(raw)
  if (!Number.isSafeInteger(id) || id <= 0) return 0
  return id
}

/**
 * プロジェクト一覧に対して選択を解決する。
 * 選択中(または復元した)プロジェクトが一覧に無ければ先頭へフォールバックし、
 * 一覧が空なら 0(未選択)にする。
 */
export function resolveProjectSelection(
  projects: readonly { id: number }[],
  current: number,
): number {
  if (projects.some((p) => p.id === current)) return current
  return projects.length > 0 ? projects[0].id : 0
}

/** 3 画面で共有するプロジェクト選択(0 = 未選択) */
export const selectedProjectId = ref(0)

/** selectedProjectId が現在どのプロファイルの選択を保持しているか(未復元は '') */
let loadedProfileId = ''

/**
 * 選択の世代。復元先のプロファイルが変わるたびに進める。
 * 古いプロファイルに対して発行した非同期処理の応答が後から届いても、
 * 共有状態へ反映しないよう判定するために使う。
 */
let selectionGeneration = 0

// 選択が変わるたびに保存する(画面切替・再起動後も維持するため)。
// 一覧に無いプロジェクトから先頭へフォールバックした場合も、
// 画面の表示と保存値を常に一致させるためそのまま上書き保存する
// (存在しない ID を保存し続けると、次回もフォールバックが走って挙動が読みにくくなるため)。
watch(selectedProjectId, (id) => {
  // 復元前(プロファイル未確定)は保存先キーが決まらないため保存しない
  if (!loadedProfileId) return
  // 未選択(0)は保存しない。プロジェクト一覧の取得失敗等で一時的に 0 になった際に、
  // 保存済みの選択まで消してしまわないようにするため。
  if (id <= 0) return
  try {
    localStorage.setItem(projectSelectionKey(loadedProfileId), String(id))
  } catch {
    // localStorage は WebView の設定によっては例外になり得る。
    // 保存できなくてもセッション中の共有は成立するため無視する。
  }
})

/**
 * 指定プロファイルの保存値を共有状態へ復元する(各画面の onMounted で呼ぶ)。
 *
 * - プロファイルが変わった場合: そのプロファイルの保存値へ切り替える
 *   (前のプロファイルの選択を持ち越さない)
 * - 同じプロファイルで既に選択済みの場合: 何もしない
 *   (画面切替のたびに保存値へ戻して、直前の選択操作を打ち消さないため)
 * - 同じプロファイルだが未選択(0)の場合: 保存値を読み直す
 *
 * 復元した ID が一覧に存在するかは呼び出し側で resolveProjectSelection により確認する。
 */
export function restoreProjectSelection(profileId: string): void {
  if (!profileId) return
  if (profileId === loadedProfileId && selectedProjectId.value > 0) return
  // プロファイルが変わる場合は世代を進め、旧プロファイル向けに発行済みの
  // 非同期処理の応答を無効化する(useProjectSelectionGuard 参照)
  if (profileId !== loadedProfileId) selectionGeneration++
  // 保存先キーを先に確定させてから ref を更新する(watch が正しいキーへ保存するため)
  loadedProfileId = profileId
  let raw: string | null = null
  try {
    raw = localStorage.getItem(projectSelectionKey(profileId))
  } catch {
    raw = null
  }
  selectedProjectId.value = parseStoredProjectId(raw)
}

/** 非同期処理の開始時点を表す控え(反映してよいかの判定に使う) */
export interface ProjectSelectionToken {
  generation: number
  profileId: string
}

/**
 * 非同期処理の応答を共有状態へ反映してよいかを判定するガード(各画面の setup で呼ぶ)。
 *
 * プロジェクト一覧の取得中に画面が破棄されたり、接続先プロファイルが切り替わったりすると、
 * 後から届いた古い応答が共有の selectedProjectId を書き換えてしまう
 * (例: プロファイル A の一覧取得中に B へ切替 → A の応答が A の先頭 ID を書き込み →
 *  保存 watch がその値を B のキーへ保存する)。
 * 処理の開始時に begin() で世代とプロファイルを控え、応答到着時に isCurrent() が
 * true の場合だけ共有状態・画面へ反映する。
 */
export function useProjectSelectionGuard() {
  let alive = true
  onUnmounted(() => {
    alive = false
  })
  return {
    /** 非同期処理の開始時点を控える */
    begin(): ProjectSelectionToken {
      return { generation: selectionGeneration, profileId: loadedProfileId }
    },
    /** 控えた時点から画面が生存し、かつ同じプロファイル・世代のままか */
    isCurrent(token: ProjectSelectionToken): boolean {
      return (
        alive && token.generation === selectionGeneration && token.profileId === loadedProfileId
      )
    },
    /**
     * 画面がまだ生存しているか。
     *
     * 接続先プロファイルの取得(getActiveProfile)を待っている間はプロファイルが
     * 未確定でトークン照合ができないため、復元前の確認にはこちらを使う。
     * 画面は同時に 1 つしか表示されない(App.vue の `<component :is>`)ので、
     * 生存していれば共有状態を触ってよいのはこの画面だけである。
     */
    isAlive(): boolean {
      return alive
    },
  }
}
