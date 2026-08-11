<script lang="ts" setup>
// アプリ情報画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { onMounted, ref } from 'vue'
import { getBackend, openExternalURL, type StorageInfo } from '../lib/backend'

const backend = getBackend()

/** リポジトリ・不具合報告の窓口 */
const REPOSITORY_URL = 'https://github.com/r404r/p-backlog-assistant'
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

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}

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
  border: 1px solid #d0d7de;
  border-radius: 6px;
  padding: 1rem 1.25rem;
  margin-bottom: 1.25rem;
  background: #fff;
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
  color: #57606a;
}

.info dd {
  margin: 0;
  word-break: break-all;
}

.link {
  color: #0b5cad;
  cursor: pointer;
}

.note {
  display: block;
  font-size: 0.8rem;
  color: #57606a;
  word-break: normal;
}

/* エラー文にはパスや URL が入りうるため、必ず折り返す(.info dd の外でも同様) */
.error {
  color: #b52a2a;
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
  color: #57606a;
}
</style>
