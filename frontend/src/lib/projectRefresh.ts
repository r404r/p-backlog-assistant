/**
 * 画面表示時のプロジェクト一覧の自動突合(API 同期)のスロットリング。
 *
 * 課題抽出・同期状態の 2 画面は、参加解除されたプロジェクトのキャッシュを
 * 表示し続けないよう、表示のたびに API と突合してから一覧を読み込む(高 1)。
 * ただしサイドバーで画面を行き来するたびに API 通信が走ると、体感が重く
 * レート制限も消費するため、**前回の成功から一定時間(10 分)以内は突合を
 * 省略**し、ローカルキャッシュの読み込みだけを行う。
 *
 * 省略は正常動作なので警告等は出さない。参加解除の検出は最大 10 分遅れるが、
 * 手動の「プロジェクト一覧を同期」ボタンと課題同期では従来どおり即時に反映される。
 *
 * 最終成功時刻は接続先プロファイルごとに保持する。プロファイルを切り替えると
 * 対象スペースが変わり、突合の要否も別勘定になるため。
 *
 * 保持先は syncState.ts と同じモジュールレベルの共有状態(localStorage 等へは
 * 永続化しない)。画面は App.vue の `<component :is>` で破棄・再生成されるため
 * 画面ローカルに持つと機能せず、逆に永続化するとアプリ再起動直後でも省略され
 * 得る。「起動後の初回表示では必ず突合する」を保つため、あえて揮発させる。
 *
 * Vue に依存しない素のモジュール状態として切り出してあり、
 * projectRefresh.test.ts で検証する(R15)。
 */

/** 自動突合を省略する間隔(ミリ秒)。前回成功からこの時間内なら突合しない */
export const PROJECT_REFRESH_INTERVAL_MS = 10 * 60 * 1000

/** プロファイル ID → 自動・手動を問わず最後に突合へ成功した時刻(ミリ秒) */
const lastRefreshedAt = new Map<string, number>()

/**
 * プロファイル ID → 実行中の突合。
 *
 * 画面をまたいで共有する(syncState.ts と同じ理由)。課題抽出で突合を始めた
 * 直後に同期状態へ移動すると、移動先も同じ突合を始めてしまい、Go 側の直列化
 * (service の syncMu)で待たされたうえで同じ API 突合をやり直すことになる。
 * 実行中のものがあればその Promise へ合流させ、突合は 1 回だけにする。
 *
 * 値は entry オブジェクトで持ち、記録・削除の前に「今も自分が実行中の突合か」を
 * 同一性で確かめる。無効化(invalidateProjectRefresh)や reset の後に古い突合が
 * 成功しても、その結果で記録し直さないため。
 */
interface InFlightRefresh {
  promise: Promise<void>
}
const inFlight = new Map<string, InFlightRefresh>()

/**
 * 前回の成功時刻から、今回の自動突合を省略してよいかを判定する純関数。
 *
 * 未記録(undefined)は省略しない。境界(経過がちょうど interval)も
 * 省略しない側に倒し、「10 分以内なら省略」を厳密に満たす。
 * 経過が負(時計の巻き戻し・未来の記録)になる場合も、判断材料が壊れている
 * ため安全側(突合する)に倒す。
 */
export function shouldSkipProjectRefresh(
  lastSyncedAt: number | undefined,
  now: number,
  intervalMs: number,
): boolean {
  if (lastSyncedAt === undefined) return false
  const elapsed = now - lastSyncedAt
  if (elapsed < 0) return false
  return elapsed < intervalMs
}

/**
 * 突合の成功を記録する(自動・手動とも成功時のみ呼ぶ)。
 *
 * 失敗時に記録しないのは、次の画面表示で再試行させるため。
 * プロファイルが未確定(空文字)の場合は、別プロファイルの記録と
 * 混同しないよう記録しない。
 */
export function markProjectsRefreshed(profileId: string, now: number = Date.now()): void {
  if (!profileId) return
  lastRefreshedAt.set(profileId, now)
}

/** 指定プロファイルの最終成功時刻(未記録は undefined) */
export function projectsRefreshedAt(profileId: string): number | undefined {
  return lastRefreshedAt.get(profileId)
}

/**
 * 指定プロファイルの自動突合を省略してよいか(既定の間隔で判定する)。
 * 画面側はこの関数だけを使えばよい。
 */
export function shouldSkipProjectRefreshFor(profileId: string, now: number = Date.now()): boolean {
  if (!profileId) return false
  return shouldSkipProjectRefresh(projectsRefreshedAt(profileId), now, PROJECT_REFRESH_INTERVAL_MS)
}

/**
 * プロジェクト一覧の突合を実行する。同じプロファイルの突合が実行中なら、
 * 新たに始めず実行中のものへ合流する(戻り値の Promise を共有する)。
 *
 * 成功時にだけ最終成功時刻を記録するため、呼び出し側は記録を意識しなくてよい。
 * 失敗は合流した全員へ伝わる(各画面がそれぞれの流儀でエラー表示する)。
 *
 * 手動の「プロジェクト一覧を同期」もこの関数を通す。手動で外したいのは
 * 「10 分以内なら省略する」という時間の間引き(画面側で判定する)であって、
 * 今まさに走っている同一の突合をもう 1 回叩き直すことではないため。
 *
 * プロファイルが未確定(空文字)の場合は共有も記録もせず、そのまま実行する。
 */
export function runSharedProjectRefresh(
  profileId: string,
  sync: () => Promise<void>,
): Promise<void> {
  if (!profileId) return sync()
  const running = inFlight.get(profileId)
  if (running) return running.promise

  const entry: InFlightRefresh = { promise: Promise.resolve() }
  entry.promise = (async () => {
    await sync()
    // 突合の途中で無効化・リセットされていたら、その結果では記録しない
    if (inFlight.get(profileId) !== entry) return
    markProjectsRefreshed(profileId)
  })().finally(() => {
    if (inFlight.get(profileId) === entry) inFlight.delete(profileId)
  })
  inFlight.set(profileId, entry)
  return entry.promise
}

/**
 * 指定プロファイルの記録を無効化する(次の画面表示で必ず突合させる)。
 *
 * 接続先 URL・API キーを変更しても プロファイル ID は変わらないため、
 * 記録を残したままだと 10 分間は新しい接続先に対する初回突合が省略され、
 * 前の接続先のプロジェクト一覧を表示し続けてしまう。
 * 実行中の突合も切り離し(古い接続先の結果で記録し直さない)、
 * 以降の呼び出しは新しい突合を開始する。
 */
export function invalidateProjectRefresh(profileId: string): void {
  if (!profileId) return
  lastRefreshedAt.delete(profileId)
  inFlight.delete(profileId)
}

/** 記録をすべて消す(テスト用。共有状態を次のテストへ持ち越さないため) */
export function resetProjectRefreshState(): void {
  lastRefreshedAt.clear()
  inFlight.clear()
}
