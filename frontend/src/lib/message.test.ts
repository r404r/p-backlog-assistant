/**
 * 表示メッセージ(翻訳キー + 補間値)の保持ヘルパのテスト。
 *
 * 言語切替の追従が要件そのものなので、`t` の結果を保存した場合との差
 * (切替後に文言が変わる)を明示的に検証する。
 */
import { describe, expect, it } from 'vitest'
import { computed, ref } from 'vue'

import { useMessage } from './message'

/** 表示言語を持つ簡易翻訳関数(カタログの代わり) */
function translatorFor(language: { value: 'ja' | 'en' }) {
  const catalog = {
    ja: {
      'test.plain': '失敗しました',
      'test.withParams': '取得に失敗しました: {message}',
    },
    en: {
      'test.plain': 'It failed',
      'test.withParams': 'Failed to fetch: {message}',
    },
  }
  return (key: string, named?: Record<string, unknown>): string => {
    const text = catalog[language.value][key as 'test.plain' | 'test.withParams'] ?? key
    if (!named) return text
    return text.replace(/\{(\w+)\}/g, (_m, name: string) => String(named[name]))
  }
}

describe('useMessage', () => {
  it('未設定のときは空文字を返す', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text] = useMessage(translatorFor(language))

    expect(text.value).toBe('')
  })

  it('キーと補間値から表示文字列を組み立てる', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))

    set('test.withParams', { message: 'offline' })

    expect(text.value).toBe('取得に失敗しました: offline')
  })

  it('補間値が無いキーも扱える', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))

    set('test.plain')

    expect(text.value).toBe('失敗しました')
  })

  it('表示言語を切り替えると、設定済みのメッセージも新しい言語で表示される', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))
    set('test.withParams', { message: 'offline' })
    expect(text.value).toBe('取得に失敗しました: offline')

    language.value = 'en'

    // Go 由来の自由文(offline)は翻訳せず、そのまま連結される
    expect(text.value).toBe('Failed to fetch: offline')
  })

  it('null を渡すと消える', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))
    set('test.plain')

    set(null)

    expect(text.value).toBe('')
  })

  it('後から設定したメッセージで置き換わる', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))
    set('test.plain')

    set('test.withParams', { message: 'timeout' })

    expect(text.value).toBe('取得に失敗しました: timeout')
  })

  it('リアクティブに参照できる(computed から追跡できる)', () => {
    const language = ref<'ja' | 'en'>('ja')
    const [text, set] = useMessage(translatorFor(language))
    const shown = computed(() => (text.value ? `[${text.value}]` : '-'))
    expect(shown.value).toBe('-')

    set('test.plain')

    expect(shown.value).toBe('[失敗しました]')
  })
})
