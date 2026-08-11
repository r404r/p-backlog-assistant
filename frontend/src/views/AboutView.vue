<script lang="ts" setup>
// アプリ情報画面。TDD 例外(GUI): フロントエンドにテスト基盤が無いため手動確認で担保する。
import { onMounted, ref } from 'vue'
import { getBackend, openExternalURL } from '../lib/backend'

const backend = getBackend()

/** リポジトリ・不具合報告の窓口 */
const REPOSITORY_URL = 'https://github.com/r404r/p-backlog-assistant'
/** 連絡先(問い合わせ) */
const CONTACT_MAIL = 'r404r.dev@gmail.com'

// アプリのバージョン。取得できるまで、また取得に失敗した場合は
// ローカル開発ビルドと同じ 'dev' 表示に縮退する(表示専用のためエラーにはしない)。
const appVersion = ref('dev')

onMounted(async () => {
  try {
    const v = (await backend.getAppVersion()).version
    if (v) appVersion.value = v
  } catch {
    // 表示専用のため失敗は無視する
  }
})

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
</style>
