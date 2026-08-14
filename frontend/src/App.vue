<script lang="ts" setup>
// アプリ外枠(サイドバー + コンテンツ)。TDD 例外(GUI): 描画・ポインタ操作は
// 手動確認で担保し、切り出せる純ロジック(サイドバー幅など)は lib/ 側でテストする。
import { computed, onBeforeUnmount, onErrorCaptured, onMounted, ref, type Component } from 'vue'
import { getBackend } from './lib/backend'
import {
  DEFAULT_SIDEBAR_WIDTH,
  loadSidebarWidth,
  resolveDragWidth,
  saveSidebarWidth,
} from './lib/sidebarWidth'
import SettingsView from './views/SettingsView.vue'
import IssuesView from './views/IssuesView.vue'
import BulkUpdateView from './views/BulkUpdateView.vue'
import UsersView from './views/UsersView.vue'
import SyncStatusView from './views/SyncStatusView.vue'
import AboutView from './views/AboutView.vue'

type ScreenId = 'settings' | 'issues' | 'bulkUpdate' | 'users' | 'syncStatus' | 'about'

interface Screen {
  id: ScreenId
  label: string
  /** サイドバー折りたたみ時に表示する 1〜2 文字の短縮ラベル */
  short: string
  component: Component
}

const screens: Screen[] = [
  { id: 'settings', label: '接続設定', short: '接', component: SettingsView },
  { id: 'issues', label: '課題抽出', short: '課', component: IssuesView },
  { id: 'bulkUpdate', label: '一括更新・追加', short: '一', component: BulkUpdateView },
  { id: 'users', label: 'ユーザ抽出', short: 'ユ', component: UsersView },
  { id: 'syncStatus', label: '同期状態', short: '同', component: SyncStatusView },
  { id: 'about', label: 'アプリ情報', short: '情', component: AboutView },
]

/** サイドバーの折りたたみ状態の保存先(次回起動時も維持する) */
const SIDEBAR_COLLAPSED_KEY = 'ba.sidebarCollapsed'

function loadCollapsed(): boolean {
  // localStorage は WebView の設定によっては参照時に例外になり得るため、
  // 失敗しても既定値(展開)で起動を継続する。
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}

const collapsed = ref(loadCollapsed())

/** 折りたたみ状態を保存する(ドラッグ・トグルの両方から使う) */
function saveCollapsed(): void {
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed.value ? '1' : '0')
  } catch {
    // 保存できなくても表示の切り替え自体は成立するため無視する
  }
}

const toggleTitle = computed(() =>
  collapsed.value ? 'メニューを展開する' : 'メニューを折りたたむ',
)

function toggleSidebar(): void {
  collapsed.value = !collapsed.value
  saveCollapsed()
}

// --- サイドバー幅のドラッグ調整 ---
// 幅の決定ロジック(クランプ・折りたたみ判定・保存)は lib/sidebarWidth.ts 側でテストする。
// ここのポインタ操作は TDD 例外(GUI)として手動確認で担保する。

/** 展開時のサイドバー幅(px)。折りたたみ中も次に展開したときの幅として保持する */
const sidebarWidth = ref(loadSidebarWidth())

/** ドラッグ中か(幅アニメーションの抑止とハイライトに使う) */
const resizing = ref(false)

/** サイドバー本体(ドラッグ中の幅をサイドバー左端からの距離として求めるために参照する) */
const sidebarEl = ref<HTMLElement | null>(null)

/** 幅調整ハンドル(ポインタキャプチャの解放に使う) */
const handleEl = ref<HTMLElement | null>(null)

/**
 * ドラッグ中のポインタ ID(未ドラッグは null)。
 * マウス・タッチ・ペンが同時に触れた場合に、開始したポインタ以外の
 * move / up を取り違えないよう ID で照合する。
 */
let activePointerId: number | null = null

// 折りたたみ中は CSS(.sidebar.collapsed)の 48px を使うため、インラインの幅は付けない
const sidebarStyle = computed(() =>
  collapsed.value ? {} : { width: `${sidebarWidth.value}px` },
)

function onHandlePointerDown(e: PointerEvent): void {
  if (resizing.value) return // 既にドラッグ中なら別ポインタでの開始は受け付けない
  if (e.button !== 0) return // 左ボタン以外(右クリック等)では開始しない
  // ポインタキャプチャにより、カーソルがサイドバー外・ウインドウ外へ出ても
  // pointermove / pointerup をハンドルが受け取り続ける
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  activePointerId = e.pointerId
  resizing.value = true
  e.preventDefault() // ドラッグ開始時のテキスト選択を抑止する
}

function onHandlePointerMove(e: PointerEvent): void {
  if (!resizing.value || e.pointerId !== activePointerId) return
  const left = sidebarEl.value?.getBoundingClientRect().left ?? 0
  const result = resolveDragWidth(e.clientX - left)
  collapsed.value = result.collapsed
  sidebarWidth.value = result.width
}

/**
 * ドラッグ終了。その時点の幅・折りたたみ状態を確定して保存する。
 *
 * pointerup / pointercancel を受け取れないまま終わる経路
 * (ポインタキャプチャの喪失、ウインドウのフォーカス喪失)があるため、
 * どこから呼ばれても安全なように冪等にしてある。
 * 呼ばれないと resizing が残り、user-select: none とカーソルが戻らなくなる。
 */
function finishResize(): void {
  if (!resizing.value) return
  resizing.value = false
  const handle = handleEl.value
  if (handle && activePointerId !== null && handle.hasPointerCapture(activePointerId)) {
    handle.releasePointerCapture(activePointerId)
  }
  activePointerId = null
  // 保存はドラッグ中(pointermove ごと)ではなく確定時にまとめて行う
  saveSidebarWidth(sidebarWidth.value)
  saveCollapsed()
}

/** pointerup / pointercancel / lostpointercapture(開始したポインタのみ処理する) */
function onHandlePointerEnd(e: PointerEvent): void {
  if (e.pointerId !== activePointerId) return
  finishResize()
}

// ウインドウがフォーカスを失うと(別アプリへの切替、OS のダイアログ等)
// pointerup が届かないことがあるため、その時点の幅で確定させる。
onMounted(() => {
  window.addEventListener('blur', finishResize)
})
onBeforeUnmount(() => {
  window.removeEventListener('blur', finishResize)
})

/** ハンドルのダブルクリックで既定幅へ戻す(折りたたみ中なら展開する) */
function resetSidebarWidth(): void {
  sidebarWidth.value = DEFAULT_SIDEBAR_WIDTH
  collapsed.value = false
  saveSidebarWidth(sidebarWidth.value)
  saveCollapsed()
}

// 初期表示は接続設定。初回起動(プロファイル 0 件)時は SettingsView 側が
// ウィザード表示になる。他画面は接続先プロファイルが無いと何も表示できないため、
// プロファイル有無に関わらず接続設定を初期表示とする。
const current = ref<ScreenId>('settings')

// アプリのバージョン(サイドバーのフッタ表示。取得失敗時は非表示のまま)
const appVersion = ref('')
onMounted(async () => {
  try {
    appVersion.value = (await getBackend().getAppVersion()).version
  } catch {
    // 表示専用のため失敗は無視する
  }
})

// 子コンポーネントの描画・ライフサイクル中の例外で画面全体が真っ白になるのを防ぐ
// 最後の防衛線(初回起動時の JSON null クラッシュが白画面として現れた実績あり)。
const fatalError = ref('')
onErrorCaptured((err) => {
  fatalError.value = err instanceof Error ? `${err.name}: ${err.message}` : String(err)
  return false // これ以上伝播させない
})
</script>

<template>
  <div class="layout" :class="{ resizing }">
    <nav ref="sidebarEl" class="sidebar" :class="{ collapsed, resizing }" :style="sidebarStyle">
      <button
        class="sidebar-toggle"
        type="button"
        :title="toggleTitle"
        :aria-label="toggleTitle"
        :aria-expanded="!collapsed"
        @click="toggleSidebar"
      >
        ≡
      </button>
      <div class="app-title" title="Backlog Assistant">
        {{ collapsed ? 'BA' : 'Backlog Assistant' }}
      </div>
      <ul>
        <li v-for="s in screens" :key="s.id">
          <button
            :class="{ active: current === s.id }"
            :title="s.label"
            :aria-label="s.label"
            @click="((current = s.id), (fatalError = ''))"
          >
            {{ collapsed ? s.short : s.label }}
          </button>
        </li>
      </ul>
      <div v-if="appVersion && !collapsed" class="app-version" :title="'バージョン ' + appVersion">
        {{ appVersion }}
      </div>
      <!-- 幅調整ハンドル(サイドバー右端に重ねる)。マウス専用のため支援技術からは隠す。
           キーボード操作では ≡ トグル(折りたたみ/展開)で代替できる。 -->
      <div
        ref="handleEl"
        class="resize-handle"
        aria-hidden="true"
        title="ドラッグで幅を調整(ダブルクリックで既定幅に戻す)"
        @pointerdown="onHandlePointerDown"
        @pointermove="onHandlePointerMove"
        @pointerup="onHandlePointerEnd"
        @pointercancel="onHandlePointerEnd"
        @lostpointercapture="onHandlePointerEnd"
        @dblclick="resetSidebarWidth"
      ></div>
    </nav>
    <main class="content">
      <div v-if="fatalError" class="fatal-error">
        <h2>画面の表示中にエラーが発生しました</h2>
        <p class="detail">{{ fatalError }}</p>
        <p>他の画面に切り替えるか、アプリを再起動してください。解決しない場合はこのメッセージを開発者に連絡してください。</p>
        <button @click="fatalError = ''">閉じる</button>
      </div>
      <component :is="screens.find((s) => s.id === current)!.component" v-else />
    </main>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  height: 100vh;
  width: 100%;
  overflow: hidden;
}

.sidebar {
  /* 既定幅。展開中は :style のインライン幅(lib/sidebarWidth.ts の既定・保存値)で上書きする */
  width: 200px;
  position: relative; /* 右端の幅調整ハンドルの基準 */
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: var(--bg-muted);
  border-right: 1px solid var(--border);
  padding: 1rem 0;
  box-sizing: border-box;
  overflow: hidden;
  transition: width 0.2s ease;
}

/* バージョン表示(フッタ)。メニューとの間の余白を吸収して最下部に置く */
.app-version {
  margin-top: auto;
  padding: 0.5rem 1rem 0;
  font-size: 0.75rem;
  color: var(--text-muted);
  white-space: nowrap;
}

.sidebar.collapsed {
  width: 48px;
}

/* ドラッグ中は幅アニメーションを止める(カーソルに 0.2s 遅れて追従するのを防ぐ) */
.sidebar.resizing {
  transition: none;
}

/* 幅調整ハンドル。サイドバー右端に重ねて置くのでレイアウト(幅)には影響しない */
.resize-handle {
  position: absolute;
  top: 0;
  right: 0;
  bottom: 0;
  width: 5px;
  cursor: col-resize;
  background: transparent;
  /* ドラッグをスクロール等のブラウザ既定ジェスチャに奪われないようにする */
  touch-action: none;
}

.resize-handle:hover {
  background: var(--handle-hover-bg);
}

/* ドラッグ中はカーソルがハンドルから外れてもハイライトを維持する */
.layout.resizing .resize-handle {
  background: var(--accent-emphasis);
}

/* ドラッグ中の文字列選択・カーソルのちらつきを抑える */
.layout.resizing {
  user-select: none;
  cursor: col-resize;
}

/* 折りたたみトグル。折りたたみ時は中央寄せにして 48px 幅に収める */
.sidebar-toggle {
  display: block;
  width: 100%;
  text-align: left;
  padding: 0 1rem 0.5rem;
  border: none;
  background: transparent;
  font-size: 1.15rem;
  line-height: 1.2;
  color: var(--text-muted);
  cursor: pointer;
}

.sidebar-toggle:hover {
  color: var(--text);
}

.sidebar.collapsed .sidebar-toggle {
  text-align: center;
  padding: 0 0 0.5rem;
}

.app-title {
  font-weight: 700;
  font-size: 1rem;
  padding: 0 1rem 1rem;
  color: var(--text);
  white-space: nowrap;
}

.sidebar.collapsed .app-title {
  padding: 0 0 1rem;
  text-align: center;
}

.sidebar ul {
  list-style: none;
  margin: 0;
  padding: 0;
}

.sidebar li button {
  display: block;
  width: 100%;
  box-sizing: border-box; /* アクティブ罫線がサイドバー外へはみ出さないように */
  text-align: left;
  padding: 0.55rem 1rem;
  border: none;
  background: transparent;
  font-size: 0.95rem;
  color: var(--text);
  cursor: pointer;
  white-space: nowrap;
  overflow: hidden;
}

.sidebar.collapsed li button {
  text-align: center;
  padding: 0.55rem 0;
}

.sidebar li button:hover {
  background: var(--bg-hover);
}

.sidebar li button.active {
  background: var(--nav-active-bg);
  font-weight: 600;
  border-right: 3px solid var(--accent-emphasis);
}

/* 残り幅を全て使う。min-width: 0 が無いと中身の広いテーブルで
   フレックス項目が縮まずウインドウ外にはみ出す。 */
.content {
  flex: 1 1 auto;
  min-width: 0;
  overflow-y: auto;
  /* 固定幅の入力欄などが狭いウインドウで操作不能にならないよう、
     クリップではなく横スクロールで逃がす */
  overflow-x: auto;
  padding: 1.5rem 2rem;
  box-sizing: border-box;
  background: var(--bg);
}

@media (prefers-reduced-motion: reduce) {
  .sidebar {
    transition: none;
  }
}
</style>

<style scoped>
.fatal-error {
  margin: 24px;
  padding: 16px 20px;
  /* 罫線にも文字と同じ強調色を使う(変換前からこの色だったため踏襲する) */
  border: 1px solid var(--danger-emphasis-text);
  border-radius: 6px;
  background: var(--danger-bg-subtle);
  color: var(--text);
}
.fatal-error h2 {
  margin-top: 0;
  color: var(--danger-emphasis-text);
  font-size: 16px;
}
.fatal-error .detail {
  font-family: monospace;
  white-space: pre-wrap;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 8px;
}
</style>
