<script lang="ts" setup>
// アプリ情報画面。TDD 例外(GUI): 見た目・レイアウトは手動確認で担保する
// (表示テーマ・表示言語の切替は配線を AboutView.theme.test.ts /
// AboutView.i18n.test.ts で検証している)。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getBackend,
  openExternalURL,
  type StorageInfo,
  type StorageMode,
} from '../lib/backend'
import { errorMessage } from '../lib/format'
import { LANGUAGE_MODES, useLanguage, type LanguageMode } from '../lib/language'
import { useMessage } from '../lib/message'
import { THEME_MODES, useTheme, type ThemeMode } from '../lib/theme'

const backend = getBackend()

const { t } = useI18n()

// --- 表示テーマ ---
// 状態と購読は lib/theme.ts のシングルトンが持つ(この画面は画面切替のたびに
// 破棄されるため、購読を所有すると開いている間しか OS 追従しなくなる)。
// ここは現在値の参照と切替の呼び出しだけを行う。

/** ラジオの表示名を引くカタログキー(選択肢の並びは THEME_MODES と共通) */
const THEME_LABEL_KEYS: Record<ThemeMode, string> = {
  system: 'about.theme.mode.system',
  light: 'about.theme.mode.light',
  dark: 'about.theme.mode.dark',
}

const { mode: themeMode, setMode: setThemeMode } = useTheme()

// --- 表示言語 ---
// 表示テーマと同じ作り。状態と `languagechange` の購読は lib/language.ts の
// シングルトンが持ち、この画面は参照と切替の呼び出しだけを行う。

/** ラジオの表示名を引くカタログキー(選択肢の並びは LANGUAGE_MODES と共通) */
const LANGUAGE_LABEL_KEYS: Record<LanguageMode, string> = {
  system: 'about.language.mode.system',
  ja: 'about.language.mode.ja',
  en: 'about.language.mode.en',
}

const { mode: languageMode, language, setLanguageMode } = useLanguage()

/** リポジトリ・不具合報告の窓口 */
const REPOSITORY_URL = 'https://github.com/r404r/p-backlog-assistant'
/** リポジトリ内のファイルを開く URL の前置き(ドキュメントの言語別リンクに使う) */
const REPOSITORY_BLOB_URL = `${REPOSITORY_URL}/blob/main`
/**
 * README(リポジトリ先頭の説明)。表示言語に応じて日本語版 / 英語版を開く
 * (設計 §3.5)。日本語版はリポジトリ先頭に表示されるためアンカーで開く。
 */
const README_URL = computed(() =>
  language.value === 'en' ? `${REPOSITORY_BLOB_URL}/README.en.md` : `${REPOSITORY_URL}#readme`,
)
/** ユーザガイド(操作手順の詳細)。README と同じく表示言語に応じて開き分ける */
const USER_GUIDE_URL = computed(() =>
  language.value === 'en'
    ? `${REPOSITORY_BLOB_URL}/docs/USER_GUIDE.en.md`
    : `${REPOSITORY_BLOB_URL}/docs/USER_GUIDE.md`,
)
/** 連絡先(問い合わせ) */
const CONTACT_MAIL = 'r404r.dev@gmail.com'

// アプリのバージョン。取得できるまで、また取得に失敗した場合は
// ローカル開発ビルドと同じ 'dev' 表示に縮退する(表示専用のためエラーにはしない)。
const appVersion = ref('dev')

/** 保存先モードの表示名を引くカタログキー(値の集合は StorageMode と共通) */
const STORAGE_MODE_LABEL_KEYS: Record<StorageMode, string> = {
  default: 'about.storage.storageModeValue.default',
  env: 'about.storage.storageModeValue.env',
  portable: 'about.storage.storageModeValue.portable',
}

// 保存データ(設定・ローカル DB・動作ログ)の所在。取得前は null。
const storage = ref<StorageInfo | null>(null)
const [storageError, setStorageError] = useMessage(t)

onMounted(async () => {
  try {
    const v = (await backend.getAppVersion()).version
    if (v) appVersion.value = v
  } catch {
    // 表示専用のため失敗は無視する
  }
  try {
    storage.value = await backend.getStorageInfo()
  } catch (e) {
    // バージョン表示と違い、所在が分からないままだと利用者が困るため理由を出す
    // (errorMessage が返す Go 由来の本文はフェーズ 1 では日本語のまま。設計 §3.1)
    setStorageError('about.storage.loadFailed', { message: errorMessage(e) })
  }
})

/**
 * バイト数を人間が読める単位へ整形する(小数 1 桁)。
 * 1 KB 未満はバイト単位のまま表示する(小さな DB が 0.0 KB に見えないようにする)。
 */
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return t('common.state.unknown')
  if (bytes < 1024) return t('about.storage.bytes', { count: bytes })
  const units = ['KB', 'MB', 'GB', 'TB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(1)} ${units[unit]}`
}

/** リンクは WebView 内ではなく OS の既定ブラウザ・メールソフトで開く */
function openLink(url: string): void {
  openExternalURL(url)
}
</script>

<template>
  <div class="about">
    <h1>{{ t('about.title') }}</h1>

    <section class="panel">
      <h2>{{ t('app.title') }}</h2>
      <p class="description">{{ t('about.description') }}</p>

      <dl class="info">
        <dt>{{ t('about.field.version') }}</dt>
        <dd>{{ appVersion }}</dd>

        <dt>{{ t('about.field.author') }}</dt>
        <dd>{{ t('about.author') }}</dd>

        <dt>{{ t('about.field.github') }}</dt>
        <dd>
          <a
            class="link"
            :href="REPOSITORY_URL"
            @click.prevent="openLink(REPOSITORY_URL)"
          >{{ REPOSITORY_URL }}</a>
          <span class="note">{{ t('about.github.note') }}</span>
        </dd>

        <dt>{{ t('about.field.docs') }}</dt>
        <dd>
          <span class="doc-links">
            <a
              class="link"
              :href="README_URL"
              @click.prevent="openLink(README_URL)"
            >{{ t('about.docs.readme') }}</a>
            <a
              class="link"
              :href="USER_GUIDE_URL"
              @click.prevent="openLink(USER_GUIDE_URL)"
            >{{ t('about.docs.userGuide') }}</a>
          </span>
          <span class="note">{{ t('about.docs.note') }}</span>
        </dd>

        <dt>{{ t('about.field.contact') }}</dt>
        <dd>
          <a
            class="link"
            :href="'mailto:' + CONTACT_MAIL"
            @click.prevent="openLink('mailto:' + CONTACT_MAIL)"
          >{{ CONTACT_MAIL }}</a>
        </dd>

        <dt>{{ t('about.field.license') }}</dt>
        <dd>
          {{ t('about.license.name') }}
          <span class="note">{{ t('about.license.note') }}</span>
        </dd>
      </dl>
    </section>

    <section class="panel">
      <h2>{{ t('about.theme.title') }}</h2>
      <p class="description">{{ t('about.theme.description') }}</p>

      <div class="theme-modes" role="radiogroup" :aria-label="t('about.theme.title')">
        <label v-for="m in THEME_MODES" :key="m" class="theme-mode">
          <input
            type="radio"
            name="theme-mode"
            :value="m"
            :checked="themeMode === m"
            @change="setThemeMode(m)"
          >
          <span>{{ t(THEME_LABEL_KEYS[m]) }}</span>
        </label>
      </div>

      <span class="note">{{ t('about.theme.systemNote') }}</span>
      <span class="note">{{ t('about.theme.titleBarNote') }}</span>
    </section>

    <section class="panel">
      <h2>{{ t('about.language.title') }}</h2>
      <p class="description">{{ t('about.language.description') }}</p>

      <div class="theme-modes" role="radiogroup" :aria-label="t('about.language.title')">
        <label v-for="m in LANGUAGE_MODES" :key="m" class="theme-mode">
          <input
            type="radio"
            name="language-mode"
            :value="m"
            :checked="languageMode === m"
            @change="setLanguageMode(m)"
          >
          <span>{{ t(LANGUAGE_LABEL_KEYS[m]) }}</span>
        </label>
      </div>

      <span class="note">{{ t('about.language.systemNote') }}</span>
      <span class="note">{{ t('about.language.limitationNote') }}</span>
    </section>

    <section class="panel">
      <h2>{{ t('about.storage.title') }}</h2>
      <p class="description">{{ t('about.storage.description') }}</p>

      <p v-if="storageError" class="error">{{ storageError }}</p>
      <p v-else-if="!storage">{{ t('common.state.loading') }}</p>

      <dl v-else class="info">
        <!-- 保存先(config.json・data/ の基点)をどう決めたか。既定以外で
             運用している場合に、利用者が意図どおりかを確認できるようにする -->
        <dt>{{ t('about.storage.storageMode') }}</dt>
        <dd>
          {{ t(STORAGE_MODE_LABEL_KEYS[storage.storageMode]) }}
          <span class="note">{{ t('about.storage.storageModeNote') }}</span>
        </dd>

        <dt>{{ t('about.storage.configDir') }}</dt>
        <dd>
          {{ storage.configDir }}
          <span class="note">{{ t('about.storage.configDirNote') }}</span>
        </dd>

        <dt>{{ t('about.storage.database') }}</dt>
        <dd>
          <p v-if="storage.databases.length === 0" class="db-empty">
            {{ t('about.storage.noProfile') }}
          </p>
          <ul v-else class="db-list">
            <li v-for="db in storage.databases" :key="db.profileId">
              <span class="db-name">{{ db.profileName }}</span>
              <span v-if="db.path" class="db-path">{{ db.path }}</span>
              <!-- 確認できなかった場合(URL 不正・権限不足等)は
                   「未作成」と区別して理由を出す -->
              <span v-if="db.error" class="note error">
                {{ t('about.storage.dbError', { message: db.error }) }}
              </span>
              <span v-else-if="db.exists" class="note">
                {{ t('about.storage.dbSize', { size: formatBytes(db.sizeBytes) }) }}
              </span>
              <template v-else>
                <span class="note">{{ t('about.storage.dbMissing') }}</span>
                <span v-if="db.sizeBytes > 0" class="note">
                  {{ t('about.storage.dbLeftover', { size: formatBytes(db.sizeBytes) }) }}
                </span>
              </template>
            </li>
          </ul>
          <span class="note">{{ t('about.storage.databaseNote') }}</span>
        </dd>

        <dt>{{ t('about.storage.log') }}</dt>
        <dd>
          <template v-if="storage.logEnabled && storage.logPath">
            {{ storage.logPath }}
            <span class="note">{{ t('about.storage.logNote') }}</span>
          </template>
          <template v-else>
            {{ t('about.storage.logDisabled') }}
            <span class="note">{{ t('about.storage.logDisabledNote') }}</span>
          </template>
        </dd>
      </dl>
    </section>
  </div>
</template>

<style scoped>
/* ウインドウ幅に追従させる(右側に空白を作らない) */
.about {
  max-width: none;
  width: 100%;
  box-sizing: border-box;
}

.description {
  font-size: 0.9rem;
  margin: 0 0 1rem;
}

/* 項目名と値の 2 列。狭いウインドウでは値が折り返る */
.info {
  display: grid;
  grid-template-columns: 8rem 1fr;
  gap: 0.5rem 1rem;
  margin: 0;
  font-size: 0.9rem;
}

.info dt {
  font-weight: 600;
  color: var(--text-muted);
}

.info dd {
  margin: 0;
  word-break: break-all;
}

.link {
  color: var(--accent-fg);
  cursor: pointer;
}

/* 選択肢(表示テーマ・表示言語のラジオ)を横並びにする。狭いウインドウでは折り返す。
   同じ見た目のため 2 つの節で共用している */
.theme-modes {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem 1.5rem;
  margin: 0 0 0.5rem;
  font-size: 0.9rem;
}

.theme-mode {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  cursor: pointer;
}

/* 複数リンクを横並びにする。狭いウインドウでは折り返す */
.doc-links {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem 1rem;
}

.note {
  display: block;
  font-size: 0.8rem;
  color: var(--text-muted);
  word-break: normal;
}

/* エラー文にはパスや URL が入りうるため、必ず折り返す(.info dd の外でも同様) */
.error {
  color: var(--danger-text);
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
  overflow-wrap: anywhere;
}

/* .note の色指定に負けないよう、エラー扱いの note は色を上書きする */
.note.error {
  margin: 0;
  font-size: 0.8rem;
}

/* プロファイルごとの DB。長いパスは折り返す(.info dd の word-break を継承) */
.db-list {
  list-style: none;
  margin: 0;
  padding: 0;
}

.db-list li + li {
  margin-top: 0.5rem;
}

.db-name {
  display: block;
  font-weight: 600;
}

.db-path {
  display: block;
}

.db-empty {
  margin: 0;
  color: var(--text-muted);
}
</style>
