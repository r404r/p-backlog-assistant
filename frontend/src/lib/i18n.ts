/**
 * vue-i18n インスタンスとメッセージカタログの所有者(設計 §3.2 / §3.3)。
 *
 * 方針:
 *  - カタログは **画面別ファイル**(locales/{ja,en}/{common,app,issues,…}.json)に
 *    分割し、このモジュールがマージして 1 つのカタログにする。画面変換を並行で
 *    進める担当が同じファイルを同時に編集して競合するのを構造的に防ぐため。
 *  - **日本語が正**。英語に訳が無いキーは `fallbackLocale: 'ja'` で日本語が出る。
 *  - インスタンスは**この 1 つだけ**(シングルトン)。main.ts が `.use(i18n)` で
 *    登録し、各コンポーネントは `useI18n()`(= グローバルスコープ)を使う。
 *    言語切替は lib/language.ts がグローバル Composer の `locale.value` を更新する。
 *  - テストは createAppI18n() で**独立したインスタンス**を作れる
 *    (lib/testing/mountWithI18n.ts を参照)。
 *
 * 型:
 *  カタログの型は**日本語カタログから導出**(`typeof ja`)し、英語カタログに
 *  その型を課すことで「英語に無いキー」を型検査で落とす。キー集合の完全一致・
 *  プレースホルダの一致は localeCatalog.test.ts が静的に検査する。
 */
import { createI18n } from 'vue-i18n'

import jaAbout from '../locales/ja/about.json'
import jaApp from '../locales/ja/app.json'
import jaBulk from '../locales/ja/bulk.json'
import jaCommon from '../locales/ja/common.json'
import jaIssues from '../locales/ja/issues.json'
import jaSettings from '../locales/ja/settings.json'
import jaSync from '../locales/ja/sync.json'
import jaUsers from '../locales/ja/users.json'

import enAbout from '../locales/en/about.json'
import enApp from '../locales/en/app.json'
import enBulk from '../locales/en/bulk.json'
import enCommon from '../locales/en/common.json'
import enIssues from '../locales/en/issues.json'
import enSettings from '../locales/en/settings.json'
import enSync from '../locales/en/sync.json'
import enUsers from '../locales/en/users.json'

/** 実際に表示できる言語(モード 'system' はこのどちらかへ解決される) */
export type Language = 'ja' | 'en'

/** 訳が無いときに使う言語。日本語が正のため常に ja */
export const FALLBACK_LANGUAGE: Language = 'ja'

/** カタログの名前空間(= locales/{ja,en}/ のファイル名)。静的検査からも参照する */
export const CATALOG_NAMESPACES = [
  'common',
  'app',
  'about',
  'issues',
  'bulk',
  'settings',
  'sync',
  'users',
] as const

/** 日本語カタログ(正)。名前空間はファイル名と 1 対 1 で対応する */
const ja = {
  common: jaCommon,
  app: jaApp,
  about: jaAbout,
  issues: jaIssues,
  bulk: jaBulk,
  settings: jaSettings,
  sync: jaSync,
  users: jaUsers,
}

/** カタログのスキーマ(日本語カタログから導出する) */
export type LocaleCatalog = typeof ja

/** 英語カタログ。日本語カタログの型を課すことで、訳し漏れを型検査で落とす */
const en: LocaleCatalog = {
  common: enCommon,
  app: enApp,
  about: enAbout,
  issues: enIssues,
  bulk: enBulk,
  settings: enSettings,
  sync: enSync,
  users: enUsers,
}

/** 全言語のカタログ。localeCatalog.test.ts はここを読んでキー集合を突き合わせる */
export const messages: Record<Language, LocaleCatalog> = { ja, en }

/**
 * i18n インスタンスを作る。
 *
 * アプリ本体はモジュール末尾のシングルトンを使う。この関数を直接呼ぶのは、
 * 状態を共有したくないテスト(mountWithI18n)だけ。
 */
export function createAppI18n(locale: Language = FALLBACK_LANGUAGE) {
  return createI18n({
    // Composition API のみを使う(Legacy API のグローバル注入は使わない)
    legacy: false,
    locale,
    fallbackLocale: FALLBACK_LANGUAGE,
    messages,
    // 未訳キーのフォールバックは設計どおりの動作のため、警告は出さない
    // (英語カタログの欠落は localeCatalog.test.ts が検出する)。
    fallbackWarn: false,
    missingWarn: false,
  })
}

/** アプリ本体が使う唯一の i18n インスタンス(main.ts が .use する) */
export const i18n = createAppI18n()

/**
 * 表示言語を切り替える(アプリ本体のインスタンスに対して)。
 * 呼び出し元は lib/language.ts のみ。
 */
export function setI18nLanguage(language: Language): void {
  i18n.global.locale.value = language
}
