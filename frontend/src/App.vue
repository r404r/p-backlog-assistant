<script lang="ts" setup>
// アプリ外枠(サイドバー + コンテンツ)。TDD 例外(GUI): フロントエンドにテスト基盤が
// 無いため手動確認で担保する。
import { computed, onErrorCaptured, onMounted, ref, type Component } from 'vue'
import { getBackend } from './lib/backend'
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

const toggleTitle = computed(() =>
  collapsed.value ? 'メニューを展開する' : 'メニューを折りたたむ',
)

function toggleSidebar(): void {
  collapsed.value = !collapsed.value
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed.value ? '1' : '0')
  } catch {
    // 保存できなくても表示の切り替え自体は成立するため無視する
  }
}

// 初期表示は接続設定。初回起動(プロファイル 0 件)時は SettingsView 側が
// ウィザード表示になる。他画面が未実装のため、プロファイル有無に関わらず
// 接続設定を初期表示とする(マイルストーン 2 で見直す)。
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
  <div class="layout">
    <nav class="sidebar" :class="{ collapsed }">
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
  width: 200px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  background: #f6f8fa;
  border-right: 1px solid #d0d7de;
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
  color: #57606a;
  white-space: nowrap;
}

.sidebar.collapsed {
  width: 48px;
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
  color: #57606a;
  cursor: pointer;
}

.sidebar-toggle:hover {
  color: #1f2328;
}

.sidebar.collapsed .sidebar-toggle {
  text-align: center;
  padding: 0 0 0.5rem;
}

.app-title {
  font-weight: 700;
  font-size: 1rem;
  padding: 0 1rem 1rem;
  color: #1f2328;
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
  color: #1f2328;
  cursor: pointer;
  white-space: nowrap;
  overflow: hidden;
}

.sidebar.collapsed li button {
  text-align: center;
  padding: 0.55rem 0;
}

.sidebar li button:hover {
  background: #eaeef2;
}

.sidebar li button.active {
  background: #dbe9f6;
  font-weight: 600;
  border-right: 3px solid #0b5cad;
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
  background: #fff;
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
  border: 1px solid #d1242f;
  border-radius: 6px;
  background: #fff5f5;
  color: #24292f;
}
.fatal-error h2 {
  margin-top: 0;
  color: #d1242f;
  font-size: 16px;
}
.fatal-error .detail {
  font-family: monospace;
  white-space: pre-wrap;
  background: #fff;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  padding: 8px;
}
</style>
