/**
 * テスト用の共通マウントヘルパ(設計 §3.4)。
 *
 * i18n 導入後、`useI18n()` を使うコンポーネントは `.use(i18n)` 済みのアプリで
 * ないとマウントできない。既存のテストは @vue/test-utils を入れず createApp で
 * 直接マウントする流儀のため、その 4 行(host 作成 → body 追加 → createApp →
 * mount)をここへ集約し、i18n の登録漏れが起きないようにする。
 *
 * 既定では**テストごとに独立した i18n インスタンス**を使う(locale の持ち越しで
 * 別のテストが落ちるのを防ぐ)。言語切替そのもの(setLanguageMode)の反映を
 * 検証したいテストだけ `shared: true` を指定し、アプリ本体と同じシングルトンを使う。
 *
 * 注意: このファイルはテスト専用だがプロダクションの src 配下に置いている
 * (テストからのみ import されるため、vite のバンドルには入らない)。
 */
import { createApp, type App, type Component } from 'vue'

import { createAppI18n, i18n as sharedI18n, type Language } from '../i18n'

/** createAppI18n() が返すインスタンスの型(テストから locale を触るために公開する) */
type AppI18n = ReturnType<typeof createAppI18n>

export interface MountWithI18nOptions {
  /** 表示言語(既定 ja) */
  locale?: Language
  /**
   * アプリ本体と同じ i18n シングルトンを使う。
   * 言語切替(lib/language.ts の setLanguageMode)の反映まで検証する場合に指定する。
   */
  shared?: boolean
}

export interface MountedApp {
  app: App
  host: HTMLElement
  /**
   * この画面が使っている i18n インスタンス(shared 指定ならアプリ本体のもの)。
   * `i18n.global.locale.value = 'en'` で**マウント済みの画面の表示言語だけ**を
   * 切り替えられる。言語切替に追従しない表示(生成済みメッセージ等)の検証に使う。
   */
  i18n: AppI18n
  /** アンマウントして host を DOM から取り除く(afterEach 用) */
  unmount(): void
}

/** コンポーネントを i18n 付きでマウントする */
export function mountWithI18n(
  component: Component,
  options: MountWithI18nOptions = {},
): MountedApp {
  const host = document.createElement('div')
  document.body.appendChild(host)

  const app = createApp(component)
  let i18n: AppI18n
  if (options.shared) {
    // 共有インスタンスでは、locale を明示したときだけ上書きする。
    // 省略時は現在の表示言語(lib/language.ts が確定させた値)をそのまま使う
    // ため、「保存済みの言語で再マウントする」といった検証ができる。
    if (options.locale) sharedI18n.global.locale.value = options.locale
    i18n = sharedI18n
  } else {
    i18n = createAppI18n(options.locale ?? 'ja')
  }
  app.use(i18n)
  app.mount(host)

  return {
    app,
    host,
    i18n,
    unmount() {
      app.unmount()
      host.remove()
    },
  }
}
