/**
 * AboutView の「言語 / Language」切替の統合テスト(設計 §3.4)。
 *
 * language.ts 単体のテスト(lib/language.test.ts)では、画面のラジオが
 * setLanguageMode へ配線されているか・実際に表示文言が変わるかまでは分からない。
 * ここでは **ja / en の実表示文字列**で検証する(実装と同じキーを参照する
 * アサートはトートロジーになるため避ける。設計 §3.4)。
 *
 * 言語切替はアプリ本体の i18n シングルトンを更新するため、マウントは
 * `shared: true`(= 本体と同じインスタンス)で行う。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import { i18n } from '../lib/i18n'
import { LANGUAGE_MODE_KEY, setLanguageMode } from '../lib/language'
import { mountWithI18n, type MountedApp } from '../lib/testing/mountWithI18n'
import AboutView from './AboutView.vue'

// 画面が触れるバックエンドは最小限のスタブにする(言語切替以外は本テストの対象外)。
vi.mock('../lib/backend', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/backend')>()
  return {
    ...actual,
    getBackend: () => ({
      getAppVersion: async () => ({ version: 'test' }),
      getStorageInfo: async () => {
        if (storageFails) throw new Error('disk error')
        return {
          configDir: '/tmp/config',
          storageMode,
          databases: [],
          logEnabled: false,
          logPath: '',
        }
      },
    }),
    openExternalURL: (...args: unknown[]) => opened.push(String(args[0])),
  }
})

/** openExternalURL に渡された URL(ドキュメントリンクの検証に使う) */
const opened: string[] = []

/** 保存データの取得を失敗させるか(生成済みメッセージの言語追従の検証に使う) */
let storageFails = false

/** バックエンドが返す保存先モード(表示の検証に使う) */
let storageMode: 'default' | 'env' | 'portable' = 'default'

/** 保留中の Promise の継続と Vue の再描画をすべて進める */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
  await nextTick()
}

let mounted: MountedApp | null = null

async function mountAboutView(): Promise<MountedApp> {
  const app = mountWithI18n(AboutView, { shared: true })
  await flush()
  return app
}

/** 表示言語のラジオをモード値で探す(表示テーマのラジオと value が重なるため name で絞る) */
function languageRadio(host: HTMLElement, mode: string): HTMLInputElement {
  const el = host.querySelector<HTMLInputElement>(`input[name="language-mode"][value="${mode}"]`)
  if (!el) throw new Error(`言語のラジオが見つかりません: ${mode}`)
  return el
}

/** ラジオを選択する(利用者のクリックと同じく change を発火させる) */
async function chooseLanguage(host: HTMLElement, mode: string): Promise<void> {
  const el = languageRadio(host, mode)
  el.checked = true
  el.dispatchEvent(new Event('change'))
  await nextTick()
}

/** リンク文字列でアンカーを探す */
function linkByText(host: HTMLElement, text: string): HTMLAnchorElement {
  const found = Array.from(host.querySelectorAll('a')).find(
    (a) => (a.textContent ?? '').trim() === text,
  )
  if (!found) throw new Error(`リンクが見つかりません: ${text}`)
  return found as HTMLAnchorElement
}

beforeEach(() => {
  localStorage.clear()
  opened.length = 0
  storageFails = false
  storageMode = 'default'
  // 明示モードから始める(happy-dom の navigator.language に依存させない)
  setLanguageMode('ja')
})

afterEach(() => {
  mounted?.unmount()
  mounted = null
  // 表示言語はモジュールレベルの共有状態のため、次のテストへ持ち越さない
  setLanguageMode('ja')
  localStorage.clear()
})

describe('AboutView の表示言語', () => {
  it('3 択のラジオを表示する', async () => {
    const { host } = (mounted = await mountAboutView())

    expect(languageRadio(host, 'system')).toBeTruthy()
    expect(languageRadio(host, 'ja')).toBeTruthy()
    expect(languageRadio(host, 'en')).toBeTruthy()
    expect(host.textContent).toContain('言語 / Language')
    expect(host.textContent).toContain('日本語')
    expect(host.textContent).toContain('English')
  })

  it('現在のモードが選択済みとして表示される', async () => {
    setLanguageMode('en')
    const { host } = (mounted = await mountAboutView())

    expect(languageRadio(host, 'en').checked).toBe(true)
    expect(languageRadio(host, 'system').checked).toBe(false)
  })

  it('English を選ぶと表示が英語に切り替わり、保存される', async () => {
    const { host } = (mounted = await mountAboutView())
    expect(host.textContent).toContain('アプリ情報')
    expect(host.textContent).toContain('保存データ')

    await chooseLanguage(host, 'en')

    expect(i18n.global.locale.value).toBe('en')
    expect(host.textContent).toContain('About')
    expect(host.textContent).toContain('Stored Data')
    expect(host.textContent).not.toContain('保存データ')
    expect(document.documentElement.lang).toBe('en')
    expect(localStorage.getItem(LANGUAGE_MODE_KEY)).toBe('en')
  })

  it('日本語へ戻すと表示も日本語に戻る', async () => {
    const { host } = (mounted = await mountAboutView())
    await chooseLanguage(host, 'en')

    await chooseLanguage(host, 'ja')

    expect(host.textContent).toContain('アプリ情報')
    expect(host.textContent).not.toContain('Stored Data')
    expect(document.documentElement.lang).toBe('ja')
    expect(localStorage.getItem(LANGUAGE_MODE_KEY)).toBe('ja')
  })

  it('ドキュメントのリンクは表示言語に応じて日本語版 / 英語版を開く', async () => {
    const { host } = (mounted = await mountAboutView())

    linkByText(host, 'ユーザガイド').dispatchEvent(new Event('click', { cancelable: true }))
    expect(opened).toEqual([
      'https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md',
    ])

    opened.length = 0
    await chooseLanguage(host, 'en')

    linkByText(host, 'README').dispatchEvent(new Event('click', { cancelable: true }))
    linkByText(host, 'User Guide').dispatchEvent(new Event('click', { cancelable: true }))
    expect(opened).toEqual([
      'https://github.com/r404r/p-backlog-assistant/blob/main/README.en.md',
      'https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.en.md',
    ])
  })

  it('英語表示でも表示テーマの節は英語で表示される', async () => {
    const { host } = (mounted = await mountAboutView())

    await chooseLanguage(host, 'en')

    expect(host.textContent).toContain('Appearance')
    expect(host.textContent).not.toContain('表示テーマ')
  })

  it('切替前に生成されたエラーメッセージも、切替後の言語で表示される', async () => {
    // t() の結果を ref に保存していると、言語を切り替えても旧言語のまま残る
    // (Codex レビュー指摘)。キー + 補間値で保持していることをここで固定する。
    storageFails = true
    const { host } = (mounted = await mountAboutView())
    expect(host.textContent).toContain('保存データの情報を取得できませんでした: disk error')

    await chooseLanguage(host, 'en')

    expect(host.textContent).toContain('Could not load the stored data information: disk error')
    expect(host.textContent).not.toContain('保存データの情報を取得できませんでした')
  })

  it('保存データの節に保存先モードを表示する(既定)', async () => {
    const { host } = (mounted = await mountAboutView())

    expect(host.textContent).toContain('保存先モード')
    expect(host.textContent).toContain('既定(ユーザ設定フォルダ)')

    await chooseLanguage(host, 'en')

    expect(host.textContent).toContain('Storage location mode')
    expect(host.textContent).toContain('Default (user config folder)')
    expect(host.textContent).not.toContain('保存先モード')
  })

  it('ポータブル・環境変数で運用中はその旨を表示する', async () => {
    storageMode = 'portable'
    const { host } = (mounted = await mountAboutView())
    expect(host.textContent).toContain('ポータブル(portable.txt)')

    await chooseLanguage(host, 'en')
    expect(host.textContent).toContain('Portable (portable.txt)')

    mounted.unmount()
    storageMode = 'env'
    setLanguageMode('ja')
    const second = (mounted = await mountAboutView())
    expect(second.host.textContent).toContain('環境変数(BACKLOG_ASSISTANT_HOME)')

    await chooseLanguage(second.host, 'en')
    expect(second.host.textContent).toContain('Environment variable (BACKLOG_ASSISTANT_HOME)')
  })

  it('再マウントしても保存済みの言語で表示される', async () => {
    const first = await mountAboutView()
    await chooseLanguage(first.host, 'en')
    first.unmount()

    const { host } = (mounted = await mountAboutView())

    expect(host.textContent).toContain('About')
    expect(languageRadio(host, 'en').checked).toBe(true)
  })
})
