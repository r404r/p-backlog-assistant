/**
 * 画面をまたいで使う整形ヘルパ(R16)。
 *
 * errorMessage / formatDateTime / formatElapsed は
 * 各 view に同一実装がコピーされていたため、ここへ集約した。
 * (かつてあった syncModeLabel は、多言語対応で機械値のフロント翻訳
 *  lib/enumLabels.ts の translateSyncMode へ移したため削除した。)
 * 表示の揺れ(同じ値が画面ごとに違う形式で出る)を防ぐのが目的なので、
 * 画面固有の整形(AboutView の formatBytes、SyncStatusView の
 * formatResetTime 等、1 画面でしか使わないもの)はここへは持ち込まない。
 *
 * すべて副作用のない純粋関数(formatElapsed のみ現在時刻に依存する)。
 * テストは format.test.ts(R15 で導入した Vitest)。
 */
import { i18n, type Language } from './i18n'
import type { TranslateFn } from './columnLabels'

export type { TranslateFn }

/**
 * グローバル Composer の翻訳関数(設計 §3.2)。
 *
 * `useI18n()` は setup の中でしか使えないため、lib の純関数から翻訳したい場合は
 * この関数を既定値として使う。**アプリ本体では i18n インスタンスは 1 つだけ**
 * なので、これはそのまま画面と同じ表示言語になる。
 * (テストが `mountWithI18n` で独立インスタンスを使う場合だけ食い違うため、
 *  画面から呼ぶ関数は `t` を引数で受け取れるようにしてある。)
 */
export const globalTranslate: TranslateFn = (key, named) =>
  named ? i18n.global.t(key, named) : i18n.global.t(key)

/**
 * 現在の表示言語(解決済み)。
 * `Intl` / `toLocaleString` に**明示的に**渡すために使う(実行環境のロケールで
 * 整形すると、表示言語を英語にしても日付・数値だけ日本語形式のままになる)。
 */
export function currentLanguage(): Language {
  return i18n.global.locale.value as Language
}

/** 例外オブジェクトから表示用のメッセージを取り出す(Error 以外は文字列化する) */
export function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

/**
 * RFC3339 を「YYYY-MM-DD HH:mm」(ローカル時刻)に整形する。
 * 空文字はそのまま返し、解釈できない値は元の文字列を返す
 * (握りつぶして空欄にすると、値が無いのか壊れているのか区別できないため)。
 *
 * 表示形式は言語に依存させない(ISO 8601 に近い並びは両言語で誤読が無く、
 * 画面間・Excel 出力との突き合わせもしやすいため)。
 */
export function formatDateTime(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

/**
 * 指定時刻から現在までの経過を表す(解釈できない値は空文字)。
 *
 * 文言はカタログ(`sync.elapsed.*`)から引く。呼び出し元が画面(setup)なら
 * `useI18n()` の `t` を渡すこと。省略時はグローバル Composer を使う。
 */
export function formatElapsed(iso: string, t: TranslateFn = globalTranslate): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const min = Math.floor((Date.now() - d.getTime()) / 60000)
  if (min < 1) return t('sync.elapsed.justNow')
  if (min < 60) return t('sync.elapsed.minutes', { count: min })
  const hour = Math.floor(min / 60)
  if (hour < 24) return t('sync.elapsed.hours', { count: hour })
  return t('sync.elapsed.days', { count: Math.floor(hour / 24) })
}
