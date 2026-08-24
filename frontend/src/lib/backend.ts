// backend.ts
// バックエンド公開facade。型契約・Wails adapter・開発用mockは
// lib/backend/ 配下へ責務別に分割し、画面側のimport経路だけをここで安定させる。
// Wails ランタイム上では window.go.main.App のメソッドを呼び出し、
// Wails 外(vite dev / ビルド検証)ではモック実装にフォールバックする。
//
// 注意: contract.ts のシグネチャは app.go の公開メソッドと1対1の手書き契約。
// Go側を変更したらcontract・Wails adapter・mockを併せて更新すること。
//
// 公開契約とruntime分岐はbackend.test.tsおよび各画面テストで固定する。

import type { Backend, BulkProgress, SyncProgress } from './backend/contract'
import { globalTranslate } from './format'
import {
  createMockBackend,
  onMockBulkProgress,
  onMockSyncProgress,
} from './backend/mock'
import {
  createWailsBackend,
  findWailsApp,
  findWailsRuntime,
  findWailsRuntimeObject,
} from './backend/wails'

export {
  CUSTOM_COLUMN_PREFIX,
  actionLabel,
  customColumnKey,
  rowStatusLabel,
} from './backend/shared'
export * from './backend/contract'

// ---------------------------------------------------------------------------
// エクスポート
// ---------------------------------------------------------------------------

let cached: Backend | null = null

/** バックエンドを取得する。Wails 上ならバインディング、そうでなければモック */
export function getBackend(): Backend {
  if (!cached) {
    const app = findWailsApp()
    cached = app ? createWailsBackend(app) : createMockBackend()
  }
  return cached
}

/** モック動作中かどうか(UI での注記表示用) */
export function isMockBackend(): boolean {
  return findWailsApp() === null
}

/**
 * 外部リンクを OS の既定ブラウザで開く。
 *
 * デスクトップの WebView 内で通常のリンク遷移を行うとアプリの画面自体が
 * 外部サイトに置き換わってしまうため、Wails ランタイムの BrowserOpenURL を使う。
 * ランタイムが無い環境(vite dev / ビルド検証)や、古いランタイムで
 * BrowserOpenURL が存在しない場合は window.open にフォールバックする。
 */
export function openExternalURL(url: string): void {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.BrowserOpenURL === 'function') {
    rt.BrowserOpenURL(url)
    return
  }
  window.open(url, '_blank', 'noopener,noreferrer')
}

/**
 * クリップボードへ文字列をコピーする。
 *
 * Wails ランタイムの ClipboardSetText を第一の経路にする。WebView 内の
 * navigator.clipboard は権限・フォーカスの条件に左右されるため、デスクトップ
 * アプリとして確実に動く OS 側の API を優先する。ランタイムが無い環境
 * (vite dev / テスト)や古いランタイム(ClipboardSetText 未実装)では
 * navigator.clipboard.writeText へフォールバックする。
 *
 * どちらも使えない場合は例外を投げる(コピーできていないのに成功したように
 * 見せると、利用者が空のクリップボードを貼り付けてしまうため)。
 */
export async function copyToClipboard(text: string): Promise<void> {
  const rt = findWailsRuntimeObject()
  if (rt && typeof rt.ClipboardSetText === 'function') {
    // 失敗は reject で届くが、真偽値で失敗を返す実装に備えて false も失敗として扱う
    const ok = await rt.ClipboardSetText(text)
    if (ok === false) throw new Error(globalTranslate('common.backend.clipboardRejected'))
    return
  }
  const clipboard = navigator.clipboard as Clipboard | undefined
  if (clipboard && typeof clipboard.writeText === 'function') {
    await clipboard.writeText(text)
    return
  }
  throw new Error(globalTranslate('common.backend.clipboardUnavailable'))
}

/**
 * 一括実行の進捗イベント('bulk:progress')を購読する。戻り値を呼ぶと購読を解除する。
 *
 * Wails ランタイムの EventsOn は実行時に window.runtime から参照する
 * (バインディング生成物に型が無いため)。ランタイムが無い環境(vite dev / ビルド検証)では
 * モックバックエンドの簡易エミッタを購読する。どちらも存在しない場合は
 * 解除だけを行う no-op を返し、画面側は分岐せずに使える。
 */
export function onBulkProgress(cb: (p: BulkProgress) => void): () => void {
  const rt = findWailsRuntime()
  if (rt) {
    const off = rt.EventsOn('bulk:progress', (...data: unknown[]) => {
      const p = data[0] as Partial<BulkProgress> | undefined
      if (!p) return
      cb({ jobId: p.jobId ?? 0, processed: p.processed ?? 0, total: p.total ?? 0 })
    })
    // Wails の EventsOn は解除関数を返すが、バージョンにより undefined の場合がある
    return typeof off === 'function' ? off : () => {}
  }
  return onMockBulkProgress(cb)
}

/**
 * 課題同期の進捗イベント('sync:progress')を購読する。戻り値を呼ぶと購読を解除する。
 *
 * 経路の扱いは onBulkProgress と同じ(Wails ランタイムが無ければモックの
 * 簡易エミッタを購読する)。イベントはプロファイル・プロジェクトを問わず
 * 届くため、画面側で「自分が開始した同期か」を必ず確認すること。
 */
export function onSyncProgress(cb: (p: SyncProgress) => void): () => void {
  const rt = findWailsRuntime()
  if (rt) {
    const off = rt.EventsOn('sync:progress', (...data: unknown[]) => {
      const p = data[0] as Partial<SyncProgress> | undefined
      if (!p) return
      cb({
        runId: p.runId ?? '',
        profileId: p.profileId ?? '',
        projectId: p.projectId ?? 0,
        phase: p.phase ?? 'fetch',
        fetched: p.fetched ?? 0,
        total: p.total ?? 0,
      })
    })
    // Wails の EventsOn は解除関数を返すが、バージョンにより undefined の場合がある
    return typeof off === 'function' ? off : () => {}
  }
  return onMockSyncProgress(cb)
}
