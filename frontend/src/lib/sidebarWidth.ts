/**
 * サイドバー幅の管理。
 *
 * ドラッグで変えた幅のクランプ・折りたたみ判定・localStorage への保存/復元をまとめる。
 * ポインタ操作(App.vue のドラッグハンドル)は DOM に依存するためコンポーネント側に置き、
 * ここには副作用のない純粋関数と localStorage の入出力だけを置いて
 * sidebarWidth.test.ts で検証する。
 */

/** 既定のサイドバー幅(px)。ダブルクリックでこの幅へ戻す */
export const DEFAULT_SIDEBAR_WIDTH = 200

/** ドラッグで縮められる下限(px)。これ以上狭くしたい場合は折りたたみを使う */
export const MIN_SIDEBAR_WIDTH = 140

/** ドラッグで広げられる上限(px)。コンテンツ領域を潰さないための制限 */
export const MAX_SIDEBAR_WIDTH = 360

/** この幅より狭くドラッグしたら折りたたみへスナップする(px) */
export const COLLAPSE_SIDEBAR_WIDTH = 120

/** サイドバー幅の保存先(次回起動時も維持する。App.vue の `ba.sidebarCollapsed` と同じ流儀) */
export const SIDEBAR_WIDTH_KEY = 'ba.sidebarWidth'

/**
 * 幅を最小〜最大の範囲へ丸める(整数 px)。
 * 数値でない値(NaN)は既定幅にフォールバックする。
 */
export function clampSidebarWidth(width: number): number {
  if (Number.isNaN(width)) return DEFAULT_SIDEBAR_WIDTH
  if (width < MIN_SIDEBAR_WIDTH) return MIN_SIDEBAR_WIDTH
  if (width > MAX_SIDEBAR_WIDTH) return MAX_SIDEBAR_WIDTH
  return Math.round(width)
}

/** ドラッグ結果として反映すべきサイドバーの状態 */
export interface SidebarDragResult {
  /** 折りたたむか(既存の ≡ トグルと同じ折りたたみ状態) */
  collapsed: boolean
  /** 展開時に使う幅(px)。折りたたみ中も次に展開したときの幅として保持する */
  width: number
}

/**
 * ドラッグ中のポインタ位置から求めた幅を、実際の表示状態へ変換する。
 *
 * しきい値未満なら折りたたみへスナップする。そのときの幅は最小幅(クランプ結果)とし、
 * 折りたたみ中に ≡ トグルで展開した場合は最小幅から再開する
 * (ドラッグで最小幅まで縮めてから折りたたんだ流れと一致させるため)。
 */
export function resolveDragWidth(width: number): SidebarDragResult {
  const clamped = clampSidebarWidth(width)
  // NaN は既定幅へフォールバック済みなので、折りたたみ判定からは除外する
  const collapsed = !Number.isNaN(width) && width < COLLAPSE_SIDEBAR_WIDTH
  return { collapsed, width: clamped }
}

/** localStorage の生値を幅へ変換する(未保存・不正値は既定幅、範囲外はクランプ) */
export function parseStoredSidebarWidth(raw: string | null): number {
  if (!raw) return DEFAULT_SIDEBAR_WIDTH
  const width = Number(raw)
  if (!Number.isFinite(width)) return DEFAULT_SIDEBAR_WIDTH
  return clampSidebarWidth(width)
}

/** 保存済みの幅を読み出す(参照に失敗しても既定幅で起動を継続する) */
export function loadSidebarWidth(): number {
  // localStorage は WebView の設定によっては参照時に例外になり得る
  try {
    return parseStoredSidebarWidth(localStorage.getItem(SIDEBAR_WIDTH_KEY))
  } catch {
    return DEFAULT_SIDEBAR_WIDTH
  }
}

/** 幅を保存する(保存できなくても表示自体は成立するため失敗は無視する) */
export function saveSidebarWidth(width: number): void {
  try {
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(clampSidebarWidth(width)))
  } catch {
    // 保存できなくてもセッション中の幅調整は成立するため無視する
  }
}
