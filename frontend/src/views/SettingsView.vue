<script lang="ts" setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getBackend,
  isMockBackend,
  type ConnectionTestResult,
  type PermissionStatus,
  type Profile,
} from '../lib/backend'
import { errorMessage } from '../lib/format'
import { useMessage } from '../lib/message'
import { useModalFocus } from '../lib/modalFocus'
import { invalidateProjectRefresh } from '../lib/projectRefresh'

const backend = getBackend()
const mock = isMockBackend()

const { t } = useI18n()

// ---------------------------------------------------------------------------
// プロファイル一覧・接続先セレクタ
// ---------------------------------------------------------------------------

const profiles = ref<Profile[]>([])
const activeProfileId = ref<string>('')
const loading = ref(true)
const [globalError, setGlobalError] = useMessage(t)

const isFirstRun = computed(() => !loading.value && profiles.value.length === 0)

// バックエンドへ保存済み(と分かっている)接続先。保存失敗時のロールバック先。
let persistedActiveId = ''
// 世代カウンタ。古い setActiveProfile の非同期完了が、後から行われた
// 新しい選択の状態(エラー表示・ロールバック)を上書きしないようにする。
let activeSelectGen = 0

async function reloadProfiles() {
  loading.value = true
  setGlobalError(null)
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
    setGlobalError('settings.error.loadProfiles', { message: errorMessage(e) })
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
    setGlobalError('settings.error.saveActiveProfile', { message: errorMessage(e) })
    activeProfileId.value = persistedActiveId // UI の選択を元へ戻す
  }
})

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
const [formError, setFormError] = useMessage(t)

// 接続テスト状態
const testing = ref(false)
const testResult = ref<ConnectionTestResult | null>(null)

const formTitle = computed(() =>
  formMode.value === 'edit' ? t('settings.form.titleEdit') : t('settings.form.titleCreate'),
)

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
  setFormError(null)
  testResult.value = null
}

function openEditForm(p: Profile) {
  formMode.value = 'edit'
  form.id = p.id
  form.name = p.name
  form.spaceUrl = p.spaceUrl
  form.apiKey = '' // 再表示機能は無し。空のままなら「キーは変更しない」
  setFormError(null)
  testResult.value = null
}

function closeForm() {
  formMode.value = 'closed'
  form.apiKey = ''
  setFormError(null)
  testResult.value = null
}

async function runTest() {
  setFormError(null)
  if (!form.spaceUrl.trim()) {
    setFormError('settings.error.spaceUrlRequired')
    return
  }
  if (formMode.value === 'create' && !form.apiKey) {
    setFormError('settings.error.apiKeyRequired')
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
    setFormError('settings.error.test', { message: errorMessage(e) })
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
    setGlobalError('settings.error.permission', { message: errorMessage(e) })
  } finally {
    permLoading.value = false
  }
}

async function save() {
  if (!canSave.value) return
  setFormError(null)
  if (!form.name.trim()) {
    setFormError('settings.error.nameRequired')
    return
  }
  saving.value = true
  try {
    // 変更保存では ID が変わらないため、プロジェクト一覧の突合記録を捨てる。
    // 残したままだと、接続先 URL・API キーを変えても 10 分間は初回の突合が
    // 省略され、前の接続先の一覧を表示し続けてしまう。
    //
    // 捨てるのは保存の**開始前**。完了後にすると、保存の待機中に課題抽出・
    // 同期状態へ移動した画面が古い記録を見て突合を省略してしまい、記録の
    // 無効化はリアクティブでないため、その画面は表示したまま再突合しない。
    // 保存が失敗しても無効化したままになるが、次の画面表示で余分に 1 回
    // 突合するだけなので安全側に倒す。新規登録は ID が未確定(空文字)で
    // 記録も無いため、対象外(invalidateProjectRefresh は空 ID を無視する)。
    invalidateProjectRefresh(form.id)
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
    setFormError('settings.error.save', { message: errorMessage(e) })
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
const [deleteError, setDeleteError] = useMessage(t)

function openDeleteDialog(p: Profile) {
  deleteTarget.value = p
  deleteLocalData.value = true
  setDeleteError(null)
}

function closeDeleteDialog() {
  if (deleting.value) return
  deleteTarget.value = null
}

/** 確認ダイアログ本体(フォーカスをこの中へ閉じ込める範囲) */
const deleteModal = ref<HTMLElement | null>(null)

/** 確認ダイアログを開いているか */
const deleteDialogOpen = computed(() => deleteTarget.value !== null)

// 開いている間はフォーカスをダイアログ内に閉じ込め、ESC で閉じる。
// 初期フォーカスは指定せず先頭のフォーカス可能要素(「ローカルデータも削除する」の
// チェックボックス)に任せる。破壊的な「削除する」に最初からフォーカスを当てると、
// 開いた直後の Enter で削除できてしまうため。
useModalFocus(deleteModal, deleteDialogOpen, { onEscape: () => closeDeleteDialog() })

async function confirmDelete() {
  if (!deleteTarget.value) return
  deleting.value = true
  setDeleteError(null)
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
    setDeleteError('settings.error.delete', { message: errorMessage(e) })
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="settings">
    <h1>{{ t('settings.title') }}</h1>

    <p v-if="mock" class="mock-note">{{ t('settings.mockNote') }}</p>

    <p v-if="globalError" class="error">{{ globalError }}</p>

    <p v-if="loading">{{ t('common.state.loading') }}</p>

    <!-- 初回起動(プロファイル 0 件)ウィザード -->
    <div v-else-if="isFirstRun && formMode === 'closed'" class="first-run">
      <h2>{{ t('settings.firstRun.title') }}</h2>
      <p>
        {{ t('settings.firstRun.line1') }}<br />
        {{ t('settings.firstRun.line2') }}
      </p>
      <button class="primary" @click="openCreateForm">{{ t('settings.firstRun.button') }}</button>
    </div>

    <template v-else>
      <!-- 接続先セレクタ -->
      <section v-if="profiles.length > 0" class="selector-row">
        <label for="active-profile">{{ t('settings.active.label') }}</label>
        <select id="active-profile" v-model="activeProfileId">
          <option v-for="p in profiles" :key="p.id" :value="p.id">
            {{ t('settings.active.option', { name: p.name, url: p.spaceUrl }) }}
          </option>
        </select>
      </section>

      <!-- プロファイル一覧 -->
      <section v-if="profiles.length > 0" class="profile-list">
        <h2>{{ t('settings.list.title') }}</h2>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>{{ t('settings.list.colName') }}</th>
                <th>{{ t('settings.list.colSpaceUrl') }}</th>
                <th>{{ t('settings.list.colUser') }}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in profiles" :key="p.id" :class="{ active: p.id === activeProfileId }">
                <td>
                  {{ p.name }}
                  <span v-if="p.id === activeProfileId" class="badge">
                    {{ t('settings.list.activeBadge') }}
                  </span>
                </td>
                <td>{{ p.spaceUrl }}</td>
                <td>{{ p.lastUserName || t('settings.list.notConnected') }}</td>
                <td class="actions">
                  <button @click="openEditForm(p)">{{ t('settings.action.edit') }}</button>
                  <button class="danger" @click="openDeleteDialog(p)">
                    {{ t('settings.action.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="list-footer">
          <button @click="openCreateForm">{{ t('settings.action.create') }}</button>
        </div>
      </section>

      <!-- 権限状態(保存後に GetPermissionStatus で確認した実権限) -->
      <section v-if="permLoading || permStatus" class="perm-status">
        <h2>{{ t('settings.perm.title') }}</h2>
        <p v-if="permLoading">
          {{ t('settings.perm.checking', { name: permStatusProfileName }) }}
        </p>
        <div
          v-else-if="permStatus"
          class="test-result"
          :class="permStatus.adminAvailable ? 'ok' : 'ng'"
        >
          <p class="result-title">
            {{ t('settings.perm.confirmed', { name: permStatusProfileName }) }}
          </p>
          <p>{{ permStatus.message }}</p>
        </div>
      </section>
    </template>

    <!-- 登録・変更フォーム -->
    <section v-if="formMode !== 'closed'" class="profile-form">
      <h2>{{ formTitle }}</h2>

      <div class="field">
        <label for="f-name">{{ t('settings.form.name') }}</label>
        <input
          id="f-name"
          v-model="form.name"
          type="text"
          :placeholder="t('settings.form.namePlaceholder')"
        />
      </div>

      <div class="field">
        <label for="f-url">{{ t('settings.form.spaceUrl') }}</label>
        <input
          id="f-url"
          v-model="form.spaceUrl"
          type="text"
          :placeholder="t('settings.form.spaceUrlPlaceholder')"
        />
        <p class="hint">{{ t('settings.form.spaceUrlHint') }}</p>
      </div>

      <div class="field">
        <label for="f-key">{{ t('settings.form.apiKey') }}</label>
        <input
          id="f-key"
          v-model="form.apiKey"
          type="password"
          autocomplete="off"
          :placeholder="
            formMode === 'edit'
              ? t('settings.form.apiKeyPlaceholderEdit')
              : t('settings.form.apiKeyPlaceholder')
          "
        />
        <p class="hint">
          {{ t('settings.form.apiKeyHint')
          }}<span v-if="formMode === 'edit'">{{ t('settings.form.apiKeyHintEdit') }}</span>
        </p>
      </div>

      <div class="form-buttons">
        <button :disabled="testing || saving" @click="runTest">
          {{ testing ? t('settings.action.testing') : t('settings.action.test') }}
        </button>
        <button class="primary" :disabled="!canSave" @click="save">
          {{ saving ? t('settings.action.saving') : t('settings.action.save') }}
        </button>
        <button :disabled="saving" @click="closeForm">{{ t('common.action.cancel') }}</button>
      </div>

      <p v-if="formError" class="error">{{ formError }}</p>

      <!-- 接続テスト結果 -->
      <div v-if="testResult" class="test-result" :class="testResult.ok ? 'ok' : 'ng'">
        <template v-if="testResult.ok">
          <p class="result-title">{{ t('settings.test.okTitle') }}</p>
          <ul>
            <li>{{ t('settings.test.userName', { name: testResult.userName }) }}</li>
            <li>
              {{ t('settings.test.permProvisional') }}
              <template v-if="testResult.adminAvailable">
                {{ t('settings.test.adminAvailable') }}
              </template>
              <template v-else>{{ t('settings.test.adminUnavailable') }}</template>
            </li>
          </ul>
          <p class="hint">{{ t('settings.test.provisionalNote') }}</p>
          <p class="hint">{{ t('settings.test.saveNote') }}</p>
        </template>
        <template v-else>
          <p class="result-title">{{ t('settings.test.ngTitle') }}</p>
          <p>{{ testResult.message }}</p>
        </template>
      </div>
      <p v-else-if="!testing" class="hint">{{ t('settings.test.beforeTestNote') }}</p>
    </section>

    <!-- 削除確認ダイアログ -->
    <div v-if="deleteTarget" class="modal-overlay" @click.self="closeDeleteDialog">
      <div
        ref="deleteModal"
        class="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="del-title"
      >
        <h2 id="del-title">{{ t('settings.delete.title') }}</h2>
        <p>
          {{ t('settings.delete.confirm', { name: deleteTarget.name, url: deleteTarget.spaceUrl })
          }}<br />
          {{ t('settings.delete.confirmNote') }}
        </p>
        <label class="checkbox">
          <input v-model="deleteLocalData" type="checkbox" />
          {{ t('settings.delete.alsoLocalData') }}
        </label>
        <p v-if="!deleteLocalData" class="hint warn">
          {{ t('settings.delete.keepLocalWarning') }}
        </p>
        <p v-if="deleteError" class="error">{{ deleteError }}</p>
        <div class="form-buttons">
          <button class="danger" :disabled="deleting" @click="confirmDelete">
            {{ deleting ? t('settings.delete.deleting') : t('settings.delete.button') }}
          </button>
          <button :disabled="deleting" @click="closeDeleteDialog">
            {{ t('common.action.cancel') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.settings {
  max-width: none;
  width: 100%;
  box-sizing: border-box;
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
  background: var(--warning-bg);
  border: 1px solid var(--warning-border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
}

.first-run {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1.5rem;
  background: var(--bg-muted);
}

.selector-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.selector-row select {
  min-width: 280px;
}

/* 幅が足りないときだけ横スクロールさせる(パネル自体は全幅を保つ) */
.table-wrap {
  width: 100%;
  overflow-x: auto;
}

.profile-list table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.9rem;
}

.profile-list th,
.profile-list td {
  border: 1px solid var(--border);
  padding: 0.4rem 0.6rem;
  text-align: left;
}

.profile-list th {
  background: var(--bg-muted);
  font-weight: 600;
}

.profile-list tr.active td {
  background: var(--selection-bg);
}

.badge {
  display: inline-block;
  margin-left: 0.4rem;
  padding: 0 0.4rem;
  font-size: 0.75rem;
  color: var(--accent-fg);
  border: 1px solid var(--accent-fg);
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
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  background: var(--surface);
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
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.9rem;
  background: var(--bg);
  color: var(--text);
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.25rem 0 0;
}

.hint.warn {
  color: var(--warning-text);
}

.form-buttons {
  display: flex;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

button.danger {
  color: var(--danger-text);
  border-color: var(--danger-border-muted);
}

button.danger:hover:not(:disabled) {
  background: var(--danger-bg-hover);
}

.error {
  color: var(--danger-text);
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
  background: var(--success-bg);
  border: 1px solid var(--success-border);
}

.test-result.ng {
  background: var(--danger-bg-hover);
  border: 1px solid var(--danger-border-muted);
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
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}

.modal {
  background: var(--surface);
  border-radius: 6px;
  padding: 1.25rem 1.5rem;
  width: min(480px, 90vw);
  box-shadow: 0 8px 24px var(--shadow);
  font-size: 0.9rem;
}

.checkbox {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin: 0.75rem 0 0;
}
</style>
