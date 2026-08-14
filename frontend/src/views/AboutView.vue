<script lang="ts" setup>
// アプリ情報画面。TDD 例外(GUI): 見た目・レイアウトは手動確認で担保する
// (表示テーマの切替は配線を AboutView.theme.test.ts で検証している)。
import { onMounted, ref } from 'vue'
import { getBackend, openExternalURL, type StorageInfo } from '../lib/backend'
import { errorMessage } from '../lib/format'
import { THEME_MODES, useTheme, type ThemeMode } from '../lib/theme'

const backend = getBackend()

// --- 表示テーマ ---
// 状態と購読は lib/theme.ts のシングルトンが持つ(この画面は画面切替のたびに
// 破棄されるため、購読を所有すると開いている間しか OS 追従しなくなる)。
// ここは現在値の参照と切替の呼び出しだけを行う。

/** ラジオの表示名(選択肢の並びは THEME_MODES と共通) */
const THEME_LABELS: Record<ThemeMode, string> = {
  system: 'システムに合わせる(既定)',
  light: 'ライト',
  dark: 'ダーク',
}

const { mode: themeMode, setMode: setThemeMode } = useTheme()

/** リポジトリ・不具合報告の窓口 */
const REPOSITORY_URL = 'https://github.com/r404r/p-backlog-assistant'
/** README(リポジトリ先頭の説明) */
const README_URL = 'https://github.com/r404r/p-backlog-assistant#readme'
/** ユーザガイド(操作手順の詳細) */
const USER_GUIDE_URL =
  'https://github.com/r404r/p-backlog-assistant/blob/main/docs/USER_GUIDE.md'
/** 連絡先(問い合わせ) */
const CONTACT_MAIL = 'r404r.dev@gmail.com'

// アプリのバージョン。取得できるまで、また取得に失敗した場合は
// ローカル開発ビルドと同じ 'dev' 表示に縮退する(表示専用のためエラーにはしない)。
const appVersion = ref('dev')

// 保存データ(設定・ローカル DB・動作ログ)の所在。取得前は null。
const storage = ref<StorageInfo | null>(null)
const storageError = ref('')

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
    storageError.value = `保存データの情報を取得できませんでした: ${errorMessage(e)}`
  }
})

/**
 * バイト数を人間が読める単位へ整形する(小数 1 桁)。
 * 1 KB 未満はバイト単位のまま表示する(小さな DB が 0.0 KB に見えないようにする)。
 */
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '不明'
  if (bytes < 1024) return `${bytes} バイト`
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
    <h1>アプリ情報</h1>

    <section class="panel">
      <h2>Backlog Assistant</h2>
      <p class="description">
        Nulab Backlog
        の課題とユーザ情報を、ローカルにキャッシュしながら安全に抽出・一括更新するデスクトップアプリです。
      </p>

      <dl class="info">
        <dt>バージョン</dt>
        <dd>{{ appVersion }}</dd>

        <dt>作者</dt>
        <dd>r404r</dd>

        <dt>GitHub</dt>
        <dd>
          <a
            class="link"
            :href="REPOSITORY_URL"
            @click.prevent="openLink(REPOSITORY_URL)"
          >{{ REPOSITORY_URL }}</a>
          <span class="note">不具合の報告・要望はこちらの Issues へお寄せください。</span>
        </dd>

        <dt>ドキュメント</dt>
        <dd>
          <span class="doc-links">
            <a
              class="link"
              :href="README_URL"
              @click.prevent="openLink(README_URL)"
            >README</a>
            <a
              class="link"
              :href="USER_GUIDE_URL"
              @click.prevent="openLink(USER_GUIDE_URL)"
            >ユーザガイド</a>
          </span>
          <span class="note">既定のブラウザで開きます(概要は README、操作手順はユーザガイド)。</span>
        </dd>

        <dt>連絡先</dt>
        <dd>
          <a
            class="link"
            :href="'mailto:' + CONTACT_MAIL"
            @click.prevent="openLink('mailto:' + CONTACT_MAIL)"
          >{{ CONTACT_MAIL }}</a>
        </dd>

        <dt>ライセンス</dt>
        <dd>
          MIT License
          <span class="note">配布物およびリポジトリの LICENSE ファイルをご確認ください。</span>
        </dd>
      </dl>
    </section>

    <section class="panel">
      <h2>表示テーマ</h2>
      <p class="description">
        画面の配色を切り替えます。選ぶとすぐに反映され、設定は自動で保存されます。
      </p>

      <div class="theme-modes" role="radiogroup" aria-label="表示テーマ">
        <label v-for="m in THEME_MODES" :key="m" class="theme-mode">
          <input
            type="radio"
            name="theme-mode"
            :value="m"
            :checked="themeMode === m"
            @change="setThemeMode(m)"
          >
          <span>{{ THEME_LABELS[m] }}</span>
        </label>
      </div>

      <span class="note">「システムに合わせる」は OS の外観設定に追従します。</span>
      <span class="note">
        ウィンドウ枠(タイトルバー)の配色は、Windows ではテーマに追従し、macOS では OS
        の外観設定に従います。
      </span>
    </section>

    <section class="panel">
      <h2>保存データ</h2>
      <p class="description">
        このアプリがこのパソコンに保存しているファイルの場所です。API
        キーは下記のファイルではなく OS のキーチェーンに保存されます。
      </p>

      <p v-if="storageError" class="error">{{ storageError }}</p>
      <p v-else-if="!storage">読み込み中...</p>

      <dl v-else class="info">
        <dt>設定フォルダ</dt>
        <dd>
          {{ storage.configDir }}
          <span class="note">接続プロファイル(config.json)の保存先です。</span>
        </dd>

        <dt>ローカル DB</dt>
        <dd>
          <p v-if="storage.databases.length === 0" class="db-empty">
            プロファイルが登録されていません。
          </p>
          <ul v-else class="db-list">
            <li v-for="db in storage.databases" :key="db.profileId">
              <span class="db-name">{{ db.profileName }}</span>
              <span v-if="db.path" class="db-path">{{ db.path }}</span>
              <!-- 確認できなかった場合(URL 不正・権限不足等)は
                   「未作成」と区別して理由を出す -->
              <span v-if="db.error" class="note error">取得エラー: {{ db.error }}</span>
              <span v-else-if="db.exists" class="note">
                {{ formatBytes(db.sizeBytes) }}(WAL・SHM を含む合計)
              </span>
              <template v-else>
                <span class="note">未作成(接続テスト後に作成されます)</span>
                <span v-if="db.sizeBytes > 0" class="note">
                  WAL・SHM のみ残っています(合計 {{ formatBytes(db.sizeBytes) }})
                </span>
              </template>
            </li>
          </ul>
          <span class="note">取得した課題・ユーザのキャッシュです。削除してもアプリは再取得できます。</span>
        </dd>

        <dt>動作ログ</dt>
        <dd>
          <template v-if="storage.logEnabled && storage.logPath">
            {{ storage.logPath }}
            <span class="note">不具合の報告時に添付いただくと調査が早くなります。</span>
          </template>
          <template v-else>
            無効
            <span class="note">ログ出力が無効なため、ファイルは作成されません。</span>
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

h1 {
  font-size: 1.4rem;
  margin: 0 0 1rem;
}

h2 {
  font-size: 1.05rem;
  margin: 0 0 0.75rem;
}

.panel {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: var(--surface);
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

/* テーマの選択肢を横並びにする。狭いウインドウでは折り返す */
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
