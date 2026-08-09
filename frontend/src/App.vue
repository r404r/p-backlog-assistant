<script lang="ts" setup>
import { onErrorCaptured, ref, type Component } from 'vue'
import SettingsView from './views/SettingsView.vue'
import IssuesView from './views/IssuesView.vue'
import BulkUpdateView from './views/BulkUpdateView.vue'
import UsersView from './views/UsersView.vue'
import SyncStatusView from './views/SyncStatusView.vue'

type ScreenId = 'settings' | 'issues' | 'bulkUpdate' | 'users' | 'syncStatus'

interface Screen {
  id: ScreenId
  label: string
  component: Component
}

const screens: Screen[] = [
  { id: 'settings', label: '接続設定', component: SettingsView },
  { id: 'issues', label: '課題抽出', component: IssuesView },
  { id: 'bulkUpdate', label: '一括更新・追加', component: BulkUpdateView },
  { id: 'users', label: 'ユーザ抽出', component: UsersView },
  { id: 'syncStatus', label: '同期状態', component: SyncStatusView },
]

// 初期表示は接続設定。初回起動(プロファイル 0 件)時は SettingsView 側が
// ウィザード表示になる。他画面が未実装のため、プロファイル有無に関わらず
// 接続設定を初期表示とする(マイルストーン 2 で見直す)。
const current = ref<ScreenId>('settings')

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
    <nav class="sidebar">
      <div class="app-title">Backlog Assistant</div>
      <ul>
        <li v-for="s in screens" :key="s.id">
          <button
            :class="{ active: current === s.id }"
            @click="((current = s.id), (fatalError = ''))"
          >
            {{ s.label }}
          </button>
        </li>
      </ul>
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
  overflow: hidden;
}

.sidebar {
  width: 200px;
  flex-shrink: 0;
  background: #f6f8fa;
  border-right: 1px solid #d0d7de;
  padding: 1rem 0;
  box-sizing: border-box;
}

.app-title {
  font-weight: 700;
  font-size: 1rem;
  padding: 0 1rem 1rem;
  color: #1f2328;
}

.sidebar ul {
  list-style: none;
  margin: 0;
  padding: 0;
}

.sidebar li button {
  display: block;
  width: 100%;
  text-align: left;
  padding: 0.55rem 1rem;
  border: none;
  background: transparent;
  font-size: 0.95rem;
  color: #1f2328;
  cursor: pointer;
}

.sidebar li button:hover {
  background: #eaeef2;
}

.sidebar li button.active {
  background: #dbe9f6;
  font-weight: 600;
  border-right: 3px solid #0b5cad;
}

.content {
  flex: 1;
  overflow-y: auto;
  padding: 1.5rem 2rem;
  box-sizing: border-box;
  background: #fff;
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
