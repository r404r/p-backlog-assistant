/**
 * 課題詳細ダイアログの最大化状態の保存(設計 §3)。
 *
 * 保存値は `'1'`(最大化)/ `'0'`(復元)だけを認め、未設定・不正値・
 * localStorage の参照/保存例外はすべて **既定 = 復元(非最大化)** へ縮退する。
 * ダイアログの表示そのものは状態が読めなくても成立するため、失敗を画面へ
 * 伝えることはしない(lib/sidebarWidth.ts・lib/markdown.ts と同じ流儀)。
 *
 * DOM に触れる部分(クラス切替・フォーカス)はコンポーネント側に置き、
 * ここには純関数と localStorage の入出力だけを置いて detailMaximized.test.ts で
 * 検証する。
 */

/** 最大化状態の保存先(次回ダイアログを開いたときも維持する) */
export const DETAIL_MAXIMIZED_KEY = 'ba.detailMaximized'

/** 既定は復元(非最大化)。最大化は明示的に選んだときだけの状態にする */
const DEFAULT_DETAIL_MAXIMIZED = false

/** 最大化を表す保存値(これ以外はすべて既定へ縮退する) */
const MAXIMIZED_VALUE = '1'

/** 復元(非最大化)を表す保存値 */
const RESTORED_VALUE = '0'

/**
 * 保存に失敗したか(このセッションの間だけ覚えておく)。
 *
 * 読み出しは成功するのに保存だけ失敗する環境(クォータ超過・読み取り専用の
 * ストレージ)では、`'1'` が保存された状態で復元しても古い `'1'` が残り続ける。
 * そのまま読み直すと「戻したはずの最大化」が次に開いたときへ復活してしまう
 * (レビュー 1 回目 指摘 2)。保存できなくなった時点で**記憶そのものが成立して
 * いない**とみなし、以降の読み出しは設計どおり既定(非最大化)へ縮退させる。
 */
let storageUnwritable = false

/** localStorage の生値を最大化状態へ変換する(未保存・不正値は既定) */
export function parseStoredDetailMaximized(raw: string | null): boolean {
  if (raw === MAXIMIZED_VALUE) return true
  if (raw === RESTORED_VALUE) return false
  return DEFAULT_DETAIL_MAXIMIZED
}

/** 保存済みの状態を読み出す(参照に失敗しても既定で表示を継続する) */
export function loadDetailMaximized(): boolean {
  // 保存できない環境では、残っている値は既に現在の状態と食い違い得る
  if (storageUnwritable) return DEFAULT_DETAIL_MAXIMIZED
  // localStorage は WebView の設定によっては参照時に例外になり得る
  try {
    return parseStoredDetailMaximized(localStorage.getItem(DETAIL_MAXIMIZED_KEY))
  } catch {
    return DEFAULT_DETAIL_MAXIMIZED
  }
}

/**
 * 状態を保存する。
 *
 * 保存できなくても**その場の切替は成立させる**(操作を無かったことにはしない)。
 * 縮退するのは「次に開いたときの記憶」の方で、以降 loadDetailMaximized() が
 * 既定を返すようになる。
 */
export function saveDetailMaximized(maximized: boolean): void {
  try {
    localStorage.setItem(DETAIL_MAXIMIZED_KEY, maximized ? MAXIMIZED_VALUE : RESTORED_VALUE)
  } catch {
    storageUnwritable = true
  }
}

/**
 * 「保存できない」判定を初期化する。
 *
 * 判定はモジュール内に持つセッション状態のため、テストのように 1 つのプロセスで
 * 複数のストレージ環境を切り替える場合はここで戻す(通常の実行では呼ばない)。
 */
export function resetDetailMaximizedStorageState(): void {
  storageUnwritable = false
}
