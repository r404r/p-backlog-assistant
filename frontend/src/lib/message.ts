/**
 * 画面が表示するメッセージ(エラー・警告・完了通知)の保持ヘルパ。
 *
 * 動機(Codex レビュー指摘): `ref('')` に `t()` の**結果**を入れると、
 * 表示言語を切り替えても既に生成済みのメッセージは旧言語のまま残る。
 * そこで**翻訳キーと補間値を状態として持ち、表示のたびに t() する**。
 * 表示文字列は computed なので、`locale` が変われば自動で作り直される。
 *
 * 使い方(全画面で統一):
 *
 * ```ts
 * const { t } = useI18n()
 * const [globalError, setGlobalError] = useMessage(t)
 * // 失敗時
 * setGlobalError('issues.error.loadProjects', { message: errorMessage(e) })
 * // 消すとき
 * setGlobalError(null)
 * ```
 *
 * テンプレートは従来どおり `v-if="globalError"` / `{{ globalError }}` で使える
 * (`globalError` は文字列の computed のため、置き換えでテンプレートは変わらない)。
 *
 * Go が返す自由文(フェーズ 1 では日本語のまま)は**補間値として渡す**。
 * 翻訳せずそのまま差し込まれるため、言語を切り替えても自由文だけは元のまま残る
 * (これは設計 §3.1 のフェーズ分割どおりの挙動)。
 *
 * 構造化された結果(同期結果・取り込み結果など)は文字列にせずオブジェクトのまま
 * 保持し、テンプレート側で t() すること(このヘルパは単文メッセージ専用)。
 */
import { computed, shallowRef, type ComputedRef } from 'vue'

import type { TranslateFn } from './columnLabels'

/** 表示メッセージの素材。key はカタログキー、params は補間値 */
export interface MessageSpec {
  key: string
  params?: Record<string, unknown>
}

/**
 * メッセージを設定する。`null` を渡すと消える。
 * key は静的検査の前提(設計 §3.3)に合わせて**文字列リテラル**で渡すこと。
 */
export type MessageSetter = (key: string | null, params?: Record<string, unknown>) => void

/**
 * メッセージの保持と表示文字列の組み立て。
 *
 * @param t 画面の翻訳関数(`useI18n()` の `t`)。画面ごとの i18n インスタンスに
 *          追従させるため、グローバル Composer ではなく**呼び出し側の t** を渡す。
 * @returns `[表示文字列(computed), 設定関数]`
 */
export function useMessage(t: TranslateFn): [ComputedRef<string>, MessageSetter] {
  // 中身は入れ替えるだけで内部を書き換えないため shallowRef で十分
  const spec = shallowRef<MessageSpec | null>(null)

  const text = computed(() => {
    const current = spec.value
    if (!current) return ''
    return current.params ? t(current.key, current.params) : t(current.key)
  })

  const set: MessageSetter = (key, params) => {
    spec.value = key === null ? null : { key, params }
  }

  return [text, set]
}
