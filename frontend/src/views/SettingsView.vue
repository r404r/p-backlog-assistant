<script lang="ts" setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  getBackend,
  isMockBackend,
  type ConnectionTestResult,
  type PermissionStatus,
  type Profile,
} from '../lib/backend'

const backend = getBackend()
const mock = isMockBackend()

// ---------------------------------------------------------------------------
// プロファイル一覧・接続先セレクタ
// ---------------------------------------------------------------------------

const profiles = ref<Profile[]>([])
const activeProfileId = ref<string>('')
const loading = ref(true)
const globalError = ref('')

const isFirstRun = computed(() => !loading.value && profiles.value.length === 0)

// バックエンドへ保存済み(と分かっている)接続先。保存失敗時のロールバック先。
let persistedActiveId = ''
// 世代カウンタ。古い setActiveProfile の非同期完了が、後から行われた
// 新しい選択の状態(エラー表示・ロールバック)を上書きしないようにする。
let activeSelectGen = 0

async function reloadProfiles() {
  loading.value = true
  globalError.value = ''
  try {
    profiles.value = await backend.listProfiles()
    // バックエンドに永続化された接続先を復元する
    try {
      const saved = await backend.getActiveProfile()
      persistedActiveId = saved
      if (saved) activeProfileId.value = saved
    } catch {
      // 取得失敗時はフォールバック(先頭プロファイル)に任せる
    }
    if (!profiles.value.some((p) => p.id === activeProfileId.value)) {
      // フォールバック選択は watch 経由でバックエンドへ永続化される
      activeProfileId.value = profiles.value.length > 0 ? profiles.value[0].id : ''
    }
  } catch (e) {
    globalError.value = `プロファイル一覧の取得に失敗しました: ${errorMessage(e)}`
  } finally {
    loading.value = false
  }
}

onMounted(reloadProfiles)

// 接続先セレクタの変更をバックエンドへ永続化する。
// 失敗時は UI の選択を保存済みの値へロールバックし、エラーを表示する。
watch(activeProfileId, async (id) => {
  if (id === persistedActiveId) return // ロールバックによる書き戻し等は再保存しない
  const gen = ++activeSelectGen
  try {
    await backend.setActiveProfile(id)
    if (gen !== activeSelectGen) return // より新しい選択が進行中なら何もしない
    persistedActiveId = id
  } catch (e) {
    if (gen !== activeSelectGen) return // 古い失敗で新しい選択を巻き戻さない
    globalError.value = `接続先の保存に失敗しました: ${errorMessage(e)}`
    activeProfileId.value = persistedActiveId // UI の選択を元へ戻す
  }
})

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

// ---------------------------------------------------------------------------
// フォーム(新規登録 / 変更)
// ---------------------------------------------------------------------------

type FormMode = 'closed' | 'create' | 'edit'

const formMode = ref<FormMode>('closed')
const form = reactive({
  id: '',
  name: '',
  spaceUrl: '',
  apiKey: '',
})
const saving = ref(false)
const formError = ref('')

// 接続テスト状態
const testing = ref(false)
const testResult = ref<ConnectionTestResult | null>(null)

const formTitle = computed(() => (formMode.value === 'edit' ? 'プロファイルの変更' : 'プロファイルの新規登録'))

/** 保存はテスト成功時のみ有効 */
const canSave = computed(
  () => testResult.value !== null && testResult.value.ok && !saving.value && !testing.value,
)

// フォーム内容が変わったらテスト結果を無効化(再テストを必須にする)
watch(
  () => [form.spaceUrl, form.apiKey],
  () => {
    testResult.value = null
  },
)

function openCreateForm() {
  formMode.value = 'create'
  form.id = ''
  form.name = ''
  form.spaceUrl = ''
  form.apiKey = ''
  formError.value = ''
  testResult.value = null
}

function openEditForm(p: Profile) {
  formMode.value = 'edit'
  form.id = p.id
  form.name = p.name
  form.spaceUrl = p.spaceUrl
  form.apiKey = '' // 再表示機能は無し。空のままなら「キーは変更しない」
  formError.value = ''
  testResult.value = null
}

function closeForm() {
  formMode.value = 'closed'
  form.apiKey = ''
  formError.value = ''
  testResult.value = null
}

async function runTest() {
  formError.value = ''
  if (!form.spaceUrl.trim()) {
    formError.value = 'スペース URL を入力してください'
    return
  }
  if (formMode.value === 'create' && !form.apiKey) {
    formError.value = 'API キーを入力してください'
    return
  }
  testing.value = true
  testResult.value = null
  try {
    // apiKey もトリムする(コピー&ペーストの改行・空白混入で 401 になるため。Go 側でも同様にトリム)
    testResult.value = await backend.testConnection(
      form.id,
      form.spaceUrl.trim(),
      form.apiKey.trim(),
    )
  } catch (e) {
    formError.value = `接続テストに失敗しました: ${errorMessage(e)}`
  } finally {
    testing.value = false
  }
}

// ---------------------------------------------------------------------------
// 権限状態(保存後に GetPermissionStatus で実権限を確認する。
// 接続テストの roleType 判定はあくまで「暫定」表示)
// ---------------------------------------------------------------------------

const permStatus = ref<PermissionStatus | null>(null)
const permStatusProfileName = ref('')
const permLoading = ref(false)

async function refreshPermissionStatus(p: Profile) {
  permLoading.value = true
  permStatus.value = null
  permStatusProfileName.value = p.name
  try {
    permStatus.value = await backend.getPermissionStatus(p.id)
  } catch (e) {
    globalError.value = `権限状態の確認に失敗しました: ${errorMessage(e)}`
  } finally {
    permLoading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  formError.value = ''
  if (!form.name.trim()) {
    formError.value = 'プロファイル名を入力してください'
    return
  }
  saving.value = true
  try {
    const saved = await backend.saveProfile({
      id: form.id,
      name: form.name.trim(),
      spaceUrl: form.spaceUrl.trim(),
      apiKey: form.apiKey.trim(),
    })
    await reloadProfiles()
    activeProfileId.value = saved.id
    closeForm()
    // 保存成功後に実権限を確認し、暫定表示を確定値へ置き換える
    await refreshPermissionStatus(saved)
  } catch (e) {
    formError.value = `保存に失敗しました: ${errorMessage(e)}`
  } finally {
    saving.value = false
  }
}

// ---------------------------------------------------------------------------
// 削除(確認ダイアログ)
// ---------------------------------------------------------------------------

const deleteTarget = ref<Profile | null>(null)
const deleteLocalData = ref(true) // 既定 ON(設計書 4.1)
const deleting = ref(false)
const deleteError = ref('')

function openDeleteDialog(p: Profile) {
  deleteTarget.value = p
  deleteLocalData.value = true
  deleteError.value = ''
}

function closeDeleteDialog() {
  if (deleting.value) return
  deleteTarget.value = null
}

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleting.value = true
  deleteError.value = ''
  try {
    await backend.deleteProfile(deleteTarget.value.id, deleteLocalData.value)
    if (form.id === deleteTarget.value.id) closeForm()
    if (permStatusProfileName.value === deleteTarget.value.name) {
      permStatus.value = null
      permStatusProfileName.value = ''
    }
    deleteTarget.value = null
    await reloadProfiles()
  } catch (e) {
    deleteError.value = `削除に失敗しました: ${errorMessage(e)}`
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="settings">
    <h1>接続設定</h1>

    <p v-if="mock" class="mock-note">
      Wails ランタイム外で動作中のため、モックデータを表示しています(保存内容はリロードで消えます)。
    </p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="loading">読み込み中...</p>

    <!-- 初回起動(プロファイル 0 件)ウィザード -->
    <div v-else-if="isFirstRun && formMode === 'closed'" class="first-run">
      <h2>ようこそ</h2>
      <p>
        Backlog に接続するための設定がまだありません。<br />
        最初の接続プロファイルを登録してください。
      </p>
      <button class="primary" @click="openCreateForm">プロファイルを登録する</button>
    </div>

    <template v-else>
      <!-- 接続先セレクタ -->
      <section v-if="profiles.length > 0" class="selector-row">
        <label for="active-profile">接続先:</label>
        <select id="active-profile" v-model="activeProfileId">
          <option v-for="p in profiles" :key="p.id" :value="p.id">
            {{ p.name }}({{ p.spaceUrl }})
          </option>
        </select>
      </section>

      <!-- プロファイル一覧 -->
      <section v-if="profiles.length > 0" class="profile-list">
        <h2>プロファイル一覧</h2>
        <table>
          <thead>
            <tr>
              <th>プロファイル名</th>
              <th>スペース URL</th>
              <th>接続ユーザ</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in profiles" :key="p.id" :class="{ active: p.id === activeProfileId }">
              <td>
                {{ p.name }}
                <span v-if="p.id === activeProfileId" class="badge">接続中</span>
              </td>
              <td>{{ p.spaceUrl }}</td>
              <td>{{ p.lastUserName || '未接続' }}</td>
              <td class="actions">
                <button @click="openEditForm(p)">変更</button>
                <button class="danger" @click="openDeleteDialog(p)">削除</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div class="list-footer">
          <button @click="openCreateForm">新規登録</button>
        </div>
      </section>

      <!-- 権限状態(保存後に GetPermissionStatus で確認した実権限) -->
      <section v-if="permLoading || permStatus" class="perm-status">
        <h2>権限状態</h2>
        <p v-if="permLoading">「{{ permStatusProfileName }}」の権限状態を確認中...</p>
        <div
          v-else-if="permStatus"
          class="test-result"
          :class="permStatus.adminAvailable ? 'ok' : 'ng'"
        >
          <p class="result-title">{{ permStatusProfileName }}(実権限で確認済み)</p>
          <p>{{ permStatus.message }}</p>
        </div>
      </section>
    </template>

    <!-- 登録・変更フォーム -->
    <section v-if="formMode !== 'closed'" class="profile-form">
      <h2>{{ formTitle }}</h2>

      <div class="field">
        <label for="f-name">プロファイル名</label>
        <input id="f-name" v-model="form.name" type="text" placeholder="例: 開発チーム用" />
      </div>

      <div class="field">
        <label for="f-url">スペース URL</label>
        <input
          id="f-url"
          v-model="form.spaceUrl"
          type="text"
          placeholder="https://example.backlog.jp"
        />
        <p class="hint">
          https:// で始まる *.backlog.jp / *.backlog.com の URL のみ使用できます。
        </p>
      </div>

      <div class="field">
        <label for="f-key">API キー</label>
        <input
          id="f-key"
          v-model="form.apiKey"
          type="password"
          autocomplete="off"
          :placeholder="formMode === 'edit' ? '(変更する場合のみ入力)' : 'API キーを入力'"
        />
        <p class="hint">
          保存後は再表示できません。<span v-if="formMode === 'edit'">空欄のまま保存すると現在のキーを維持します。</span>
        </p>
      </div>

      <div class="form-buttons">
        <button :disabled="testing || saving" @click="runTest">
          {{ testing ? '接続テスト中...' : '接続テスト' }}
        </button>
        <button class="primary" :disabled="!canSave" @click="save">
          {{ saving ? '保存中...' : '保存' }}
        </button>
        <button :disabled="saving" @click="closeForm">キャンセル</button>
      </div>

      <p v-if="formError" class="error">{{ formError }}</p>

      <!-- 接続テスト結果 -->
      <div v-if="testResult" class="test-result" :class="testResult.ok ? 'ok' : 'ng'">
        <template v-if="testResult.ok">
          <p class="result-title">接続テスト成功</p>
          <ul>
            <li>ユーザ名: {{ testResult.userName }}</li>
            <li>
              権限状態(暫定):
              <template v-if="testResult.adminAvailable">管理者機能を利用できる見込みです</template>
              <template v-else>管理者機能は利用不可の見込み(ユーザ・チーム取得はプロジェクト単位に縮退します)</template>
            </li>
          </ul>
          <p class="hint">
            この権限状態はロール(roleType)による暫定判定です。確定した権限状態は保存後に実際の API 呼び出しで確認します。
          </p>
          <p class="hint">「保存」を押すとこのプロファイルを保存します。</p>
        </template>
        <template v-else>
          <p class="result-title">接続テスト失敗</p>
          <p>{{ testResult.message }}</p>
        </template>
      </div>
      <p v-else-if="!testing" class="hint">
        「接続テスト」が成功すると「保存」できるようになります。
      </p>
    </section>

    <!-- 削除確認ダイアログ -->
    <div v-if="deleteTarget" class="modal-overlay" @click.self="closeDeleteDialog">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="del-title">
        <h2 id="del-title">プロファイルの削除</h2>
        <p>
          プロファイル「{{ deleteTarget.name }}」({{ deleteTarget.spaceUrl }})を削除します。<br />
          OS キーチェーンに保存された API キーも削除されます。よろしいですか?
        </p>
        <label class="checkbox">
          <input v-model="deleteLocalData" type="checkbox" />
          ローカルデータ(DB)も削除する(推奨)
        </label>
        <p v-if="!deleteLocalData" class="hint warn">
          チェックを外すと、取得済みデータ(個人情報を含む可能性があります)がローカルに残ります。
        </p>
        <p v-if="deleteError" class="error">{{ deleteError }}</p>
        <div class="form-buttons">
          <button class="danger" :disabled="deleting" @click="confirmDelete">
            {{ deleting ? '削除中...' : '削除する' }}
          </button>
          <button :disabled="deleting" @click="closeDeleteDialog">キャンセル</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.settings {
  max-width: 760px;
}

h1 {
  font-size: 1.4rem;
  margin: 0 0 1rem;
}

h2 {
  font-size: 1.05rem;
  margin: 0 0 0.75rem;
}

section {
  margin-bottom: 1.5rem;
}

.mock-note {
  background: #fff8e1;
  border: 1px solid #e6c96a;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}

.first-run {
  border: 1px solid #d0d7de;
  border-radius: 6px;
  padding: 1.5rem;
  background: #f6f8fa;
}

.selector-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.selector-row select {
  min-width: 280px;
}

.profile-list table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.9rem;
}

.profile-list th,
.profile-list td {
  border: 1px solid #d0d7de;
  padding: 0.4rem 0.6rem;
  text-align: left;
}

.profile-list th {
  background: #f6f8fa;
  font-weight: 600;
}

.profile-list tr.active td {
  background: #eef6ff;
}

.badge {
  display: inline-block;
  margin-left: 0.4rem;
  padding: 0 0.4rem;
  font-size: 0.75rem;
  color: #0b5cad;
  border: 1px solid #0b5cad;
  border-radius: 8px;
}

.actions {
  white-space: nowrap;
  width: 1%;
}

.actions button + button {
  margin-left: 0.4rem;
}

.list-footer {
  margin-top: 0.75rem;
}

.profile-form {
  border: 1px solid #d0d7de;
  border-radius: 6px;
  padding: 1rem 1.25rem;
  background: #fff;
}

.field {
  margin-bottom: 0.9rem;
}

.field label {
  display: block;
  font-weight: 600;
  margin-bottom: 0.25rem;
  font-size: 0.9rem;
}

.field input {
  width: 100%;
  max-width: 420px;
  box-sizing: border-box;
}

input[type='text'],
input[type='password'],
select {
  padding: 0.4rem 0.5rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  font-size: 0.9rem;
  background: #fff;
  color: #1f2328;
}

.hint {
  font-size: 0.8rem;
  color: #57606a;
  margin: 0.25rem 0 0;
}

.hint.warn {
  color: #9a6700;
}

.form-buttons {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

button {
  padding: 0.4rem 0.9rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  background: #f6f8fa;
  color: #1f2328;
  font-size: 0.9rem;
  cursor: pointer;
}

button:hover:not(:disabled) {
  background: #eaeef2;
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

button.primary {
  background: #0b5cad;
  border-color: #0b5cad;
  color: #fff;
}

button.primary:hover:not(:disabled) {
  background: #094c8f;
}

button.danger {
  color: #b52a2a;
  border-color: #d0a0a0;
}

button.danger:hover:not(:disabled) {
  background: #fbeaea;
}

.error {
  color: #b52a2a;
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.test-result {
  margin-top: 0.9rem;
  border-radius: 4px;
  padding: 0.6rem 0.9rem;
  font-size: 0.9rem;
}

.test-result.ok {
  background: #e9f5ec;
  border: 1px solid #7fbf90;
}

.test-result.ng {
  background: #fbeaea;
  border: 1px solid #d0a0a0;
}

.result-title {
  font-weight: 600;
  margin: 0 0 0.3rem;
}

.test-result ul {
  margin: 0;
  padding-left: 1.2rem;
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.modal {
  background: #fff;
  border-radius: 6px;
  padding: 1.25rem 1.5rem;
  width: min(480px, 90vw);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.2);
  font-size: 0.9rem;
}

.checkbox {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin: 0.75rem 0 0;
}
</style>
