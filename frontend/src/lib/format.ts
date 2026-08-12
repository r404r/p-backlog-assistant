/**
 * 画面をまたいで使う整形ヘルパ(R16)。
 *
 * errorMessage / formatDateTime / formatElapsed / syncModeLabel は
 * 各 view に同一実装がコピーされていたため、ここへ集約した。
 * 表示の揺れ(同じ値が画面ごとに違う形式で出る)を防ぐのが目的なので、
 * 画面固有の整形(AboutView の formatBytes、SyncStatusView の
 * formatResetTime 等、1 画面でしか使わないもの)はここへは持ち込まない。
 *
 * すべて副作用のない純粋関数(formatElapsed のみ現在時刻に依存する)。
 * TDD 例外(GUI): フロントエンドにテスト基盤が無い(R15)ため、
 * 既存実装からの移動としてテストは後追いとする。テスト基盤の導入時は
 * 副作用が無いこのモジュールから着手できる。
 */

/** 例外オブジェクトから表示用のメッセージを取り出す(Error 以外は文字列化する) */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

/**
 * RFC3339 を「YYYY-MM-DD HH:mm」(ローカル時刻)に整形する。
 * 空文字はそのまま返し、解釈できない値は元の文字列を返す
 * (握りつぶして空欄にすると、値が無いのか壊れているのか区別できないため)。
 */
export function formatDateTime(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/** 指定時刻から現在までの経過を日本語で表す(解釈できない値は空文字) */
export function formatElapsed(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const min = Math.floor((Date.now() - d.getTime()) / 60000)
  if (min < 1) return 'たった今'
  if (min < 60) return `${min} 分前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 時間前`
  return `${Math.floor(hour / 24)} 日前`
}

/**
 * 同期結果の実行モードの表示名。
 * 対象は同期結果(sync.Result.Mode)であり、Go 側が auto を full / incremental の
 * どちらかへ解決してから返すため、full 以外は差分同期として扱ってよい。
 */
export function syncModeLabel(mode: string): string {
  return mode === 'full' ? 'フル同期' : '差分同期'
}
