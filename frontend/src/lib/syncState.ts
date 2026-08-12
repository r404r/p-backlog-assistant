/**
 * 実行中の課題同期の共有状態(R10)。
 *
 * 課題同期は Go 側で走り続けるため、開始した画面が破棄されても同期は止まらない。
 * 実行中フラグを各 view のローカル ref だけで持つと、サイドバーで画面を移動した
 * 時点でフラグが失われ(App.vue の `<component :is>` は画面を破棄・再生成する)、
 * 移動先の画面から共有のプロジェクト選択(projectSelection.ts)を切り替えられて
 * しまう。切り替わると、同期の完了処理(鮮度の再読込・候補の再取得・結果表示)が
 * 切替先のプロジェクトに作用し得る。
 *
 * そこで projectSelection と同じ流儀で、進行中の同期をモジュールレベルの状態として
 * 共有し、課題抽出・同期状態・一括更新の 3 画面が同じ値を見て抑止を判断する。
 *
 * 解除の設計(2 経路):
 *  1. 開始した呼び出し自身(runIssueSync の finally)。async 関数の継続は画面が
 *     破棄されても走り続けるため、成功・失敗のどちらでも確実に解除できる。
 *     こちらが主経路。
 *  2. 完了フェーズ(phase === 'done')の進捗イベント。Wails イベントは購読側の
 *     画面に依存しないため、1 の応答を受け取れない事態(呼び出し元の例外・
 *     予期しない中断)でも解除できる保険。ただし Go 側は成功時にしか done を
 *     出さない(sync.SyncIssues はエラー時に PhaseDone を report しない)ため、
 *     失敗の解除は 1 に依存する。1 と 2 のどちらが先でも良いように、解除は
 *     runId 一致を条件とする冪等な操作にしてある。
 *
 * done で解除しても、開始した画面自身は自分のローカルフラグ(syncing)が
 * 下りるまで固定されたままなので、応答到着前に自画面の選択が動くことはない。
 *
 * Vue コンポーネントに依存しない純粋なモジュール状態(begin / end と参照用の computed)
 * として切り出してあり、syncState.test.ts で検証する(R15)。
 */
import { computed, readonly, ref } from 'vue'
import { onSyncProgress } from './backend'

/** 実行中の課題同期の識別情報 */
export interface ActiveIssueSync {
  /** 同期対象の接続先プロファイル */
  profileId: string
  /** 同期対象のプロジェクト */
  projectId: number
  /** 実行 ID(進捗イベント・解除の突き合わせに使う) */
  runId: string
}

const active = ref<ActiveIssueSync | null>(null)

/** 実行中の課題同期(非実行中は null)。表示用の参照専用ハンドル */
export const activeIssueSync = readonly(active)

/** 課題同期が実行中か(画面をまたいで共有する) */
export const issueSyncRunning = computed(() => active.value !== null)

/**
 * done イベントによる解除(保険経路)の購読解除関数。
 *
 * 購読は同期の開始時に行う。モジュールの読み込み時に購読すると、Wails ランタイムの
 * 注入前に評価された場合にモック側のエミッタへ繋がってしまうため
 * (backend.ts の onSyncProgress は購読時にランタイムの有無を判定する)、
 * バックエンド呼び出しが成立している「同期の開始時点」まで遅らせる。
 */
let unsubscribeDone: (() => void) | null = null

/**
 * 課題同期の開始を記録する。
 *
 * 既に別の同期が記録されている場合も新しい実行で上書きする。Go 側は
 * 同期を直列化する(service の syncMu)ため同時実行にはならないが、
 * 状態が食い違ったときに「実行中のまま残る」より「最新の実行を指す」方が安全。
 */
export function beginIssueSync(profileId: string, projectId: number, runId: string): void {
  active.value = { profileId, projectId, runId }
  if (unsubscribeDone) return
  unsubscribeDone = onSyncProgress((p) => {
    if (p.phase !== 'done') return
    endIssueSync(p.runId)
  })
}

/**
 * 課題同期の終了を記録する(runId が一致する場合のみ解除する)。
 *
 * 一致を条件にするのは、古い実行の遅れた解除が、後から始まった新しい実行の
 * 記録を消してしまわないようにするため。
 */
export function endIssueSync(runId: string): void {
  if (!active.value || active.value.runId !== runId) return
  active.value = null
  if (unsubscribeDone) {
    unsubscribeDone()
    unsubscribeDone = null
  }
}
