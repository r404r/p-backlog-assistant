<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { openExternalURL, type IssueDetail } from '../lib/backend'
import { loadDetailMaximized, saveDetailMaximized } from '../lib/detailMaximized'
import { formatDateTime } from '../lib/format'
import {
  isMarkdownRule,
  loadDetailMarkdown,
  markdownLinkHref,
  renderMarkdown,
  saveDetailMarkdown,
} from '../lib/markdown'
import { useModalFocus } from '../lib/modalFocus'

const props = defineProps<{
  open: boolean
  issueKey: string
  detail: IssueDetail | null
  loading: boolean
  error: string
  copyError: string
  refreshError: string
  refreshing: boolean
  syncing: boolean
  canCopy: boolean
  returnFocus?: HTMLElement | null
}>()

const emit = defineEmits<{
  refresh: []
  copy: []
  openBrowser: []
  close: []
}>()

const { t } = useI18n()
const modal = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)

useModalFocus(modal, toRef(props, 'open'), {
  initialFocus: () => closeButton.value,
  returnFocus: () => props.returnFocus,
  onEscape: () => emit('close'),
})

const detailNote = computed(() => {
  const at = props.detail?.fetchedAt ? formatDateTime(props.detail.fetchedAt) : ''
  return at ? t('issues.detail.note.fetched', { at }) : t('issues.detail.note.unknown')
})

const commentNote = computed(() => {
  const at = props.detail?.commentsFetchedAt
    ? formatDateTime(props.detail.commentsFetchedAt)
    : ''
  return at
    ? t('issues.detail.commentNote.fetched', { at })
    : t('issues.detail.commentNote.notFetched')
})

const commentsFetched = computed(() => (props.detail?.commentsFetchedAt ?? '') !== '')

// ---------------------------------------------------------------------------
// 最大化 / 復元(設計 §3)
// ---------------------------------------------------------------------------

/** 最大化中か(true の間だけ .modal に maximized クラスが付く) */
const maximized = ref(false)

/** 最大化トグルボタン(切替後もフォーカスをここへ留める) */
const maximizeButton = ref<HTMLButtonElement | null>(null)

/**
 * 保存済みの状態は**ダイアログを開いた時点**で読む。
 *
 * このコンポーネントは閉じている間もマウントされたまま(内側を v-if で出し分ける)
 * なので、開くたびに読み直さないと別ウィンドウ・別画面での変更を取りこぼす。
 * マウント時点で既に開いている場合にも効くよう immediate で 1 度走らせる。
 */
watch(
  () => props.open,
  (open) => {
    if (open) maximized.value = loadDetailMaximized()
  },
  { immediate: true },
)

/** 現在の状態に対する操作の名前(ボタンのラベル・ツールチップに使う) */
const maximizeLabel = computed(() =>
  maximized.value ? t('issues.detail.restore') : t('issues.detail.maximize'),
)

/**
 * 最大化 / 復元を切り替える(選択は次回以降にも引き継ぐ)。
 *
 * ダブルクリック経由で呼ばれたときもフォーカスをトグルボタンへ移し、
 * キーボード操作の起点をダイアログ内に保つ(フォーカストラップの外へ出さない)。
 */
function toggleMaximized(): void {
  maximized.value = !maximized.value
  saveDetailMaximized(maximized.value)
  maximizeButton.value?.focus()
}

// ---------------------------------------------------------------------------
// Markdown の整形表示(設計 §3.3)
// ---------------------------------------------------------------------------

/**
 * このプロジェクトが Markdown 記法か。
 *
 * 判定に使うのは**課題詳細に載って届いた記法設定だけ**で、選択中プロジェクトの
 * 状態は見ない(詳細の取得中にプロジェクトを切り替えても判定がずれないため)。
 * Backlog 記法・判定不能は従来のプレーン表示のままにする。
 */
const markdownAvailable = computed(() => isMarkdownRule(props.detail?.textFormattingRule))

/** 「整形表示 / 原文」の選択(既定は整形表示。localStorage で記憶する) */
const markdownView = ref(loadDetailMarkdown())

/** 実際に整形表示するか(Markdown 記法のプロジェクトで、整形表示を選んでいるとき) */
const showMarkdown = computed(() => markdownAvailable.value && markdownView.value)

/** 表示を切り替える(選択は次回以降にも引き継ぐ) */
function setMarkdownView(on: boolean): void {
  markdownView.value = on
  saveDetailMarkdown(on)
}

/** 整形表示した詳細本文(サニタイズ済み HTML) */
const renderedDescription = computed(() =>
  showMarkdown.value ? renderMarkdown(props.detail?.description ?? '') : '',
)

/** 整形表示した各コメント(サニタイズ済み HTML。並びは detail.comments と同じ) */
const renderedComments = computed(() =>
  showMarkdown.value ? (props.detail?.comments ?? []).map((c) => renderMarkdown(c.content)) : [],
)

/**
 * 整形表示のリンクのクリックを OS の既定ブラウザへ回す。
 *
 * 整形表示のリンクは href を持たない(data-href に検証済み URL が入る)ため、
 * ここで解決しないと何も起こらない。WebView 内で遷移させないための作りで、
 * 開く前にもう一度 http / https を検証している(lib/markdown.ts)。
 */
function openMarkdownLink(event: MouseEvent): void {
  const url = markdownLinkHref(event.target)
  if (!url) return
  event.preventDefault()
  openExternalURL(url)
}
</script>

<template>
  <div v-if="open" class="modal-overlay" @click.self="$emit('close')">
    <div
      ref="modal"
      class="modal"
      :class="{ maximized }"
      role="dialog"
      aria-modal="true"
      aria-labelledby="issue-detail-title"
    >
      <!-- ヘッダ。最大化中はここを固定し、下の detail-body だけをスクロールさせる -->
      <div class="detail-header">
        <p v-if="detail" class="notice comment-note">{{ commentNote }}</p>
        <p
          v-for="(warning, index) in detail?.warnings ?? []"
          :key="index"
          class="notice warn comment-note"
        >
          {{ warning }}
        </p>

        <div class="detail-title-row">
          <!-- ダブルクリックでも最大化を切り替える。`.self` により、タイトルの文字
               (span)やボタンから上がってきたイベントでは発火しない
               = 文字を選択するためのダブルクリックで誤爆しない -->
          <h2 id="issue-detail-title" class="detail-title" @dblclick.self="toggleMaximized">
            <span class="detail-key">{{ issueKey }}</span>
            <span v-if="detail" class="detail-summary">{{ detail.summary }}</span>
          </h2>
          <!-- アクセシブル名は「今できる操作」(最大化 / 元のサイズに戻す)にする。
               WAI-ARIA APG のボタンパターンでは、aria-pressed のトグルは名前を固定し、
               名前を操作名へ切り替える場合は aria-pressed を使わない。併用すると
               「『元のサイズに戻す』が押されている」と伝わって矛盾するため付けない。 -->
          <button
            ref="maximizeButton"
            type="button"
            class="maximize-toggle"
            :aria-label="maximizeLabel"
            :title="maximizeLabel"
            @click="toggleMaximized"
          >
            <!-- 記号は装飾。意味は aria-label / title(最大化 / 元のサイズに戻す)が担う -->
            <span aria-hidden="true">{{ maximized ? '❐' : '⛶' }}</span>
          </button>
        </div>
      </div>

      <div class="detail-body">
        <p v-if="loading" class="notice">{{ t('common.state.loading') }}</p>
        <p v-else-if="error" class="error">{{ error }}</p>

        <template v-else-if="detail">
          <dl class="detail-grid">
            <dt>{{ t('issues.detail.field.status') }}</dt>
            <dd>{{ detail.statusName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.issueType') }}</dt>
            <dd>{{ detail.issueTypeName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.priority') }}</dt>
            <dd>{{ detail.priorityName || '-' }}</dd>
            <dt>{{ t('issues.detail.field.assignee') }}</dt>
            <dd>{{ detail.assigneeName || t('issues.value.unset') }}</dd>
            <dt>{{ t('issues.detail.field.dueDate') }}</dt>
            <dd>{{ detail.dueDate || '-' }}</dd>
            <dt>{{ t('issues.detail.field.created') }}</dt>
            <dd>{{ formatDateTime(detail.created) || '-' }}</dd>
            <dt>{{ t('issues.detail.field.updated') }}</dt>
            <dd>{{ formatDateTime(detail.updated) || '-' }}</dd>
            <dt>{{ t('issues.detail.field.parentIssue') }}</dt>
            <dd>{{ detail.parentIssueKey || t('issues.value.none') }}</dd>
          </dl>

          <template v-if="detail.customFields.length > 0">
            <h3 class="detail-section">{{ t('issues.detail.customFields') }}</h3>
            <dl class="detail-grid">
              <template v-for="(field, index) in detail.customFields" :key="index">
                <dt>{{ field.name }}</dt>
                <dd>{{ field.value || t('issues.value.unset') }}</dd>
              </template>
            </dl>
          </template>

          <div class="detail-section-head">
            <h3 class="detail-section">{{ t('issues.detail.description') }}</h3>
            <!-- 記法設定が Markdown のときだけ「整形表示 / 原文」を選べる
                 (Backlog 記法・判定不能では切替そのものを出さない) -->
            <div
              v-if="markdownAvailable"
              class="view-toggle"
              role="group"
              :aria-label="t('issues.detail.view.label')"
            >
              <button type="button" :aria-pressed="markdownView" @click="setMarkdownView(true)">
                {{ t('issues.detail.view.formatted') }}
              </button>
              <button type="button" :aria-pressed="!markdownView" @click="setMarkdownView(false)">
                {{ t('issues.detail.view.source') }}
              </button>
            </div>
          </div>
          <template v-if="detail.description">
            <!-- v-html に渡すのは lib/markdown.ts で変換 + DOMPurify のサニタイズを
                 必ず通した HTML だけ(設計 §3.2)。原文をそのまま渡してはならない -->
            <!-- eslint-disable-next-line vue/no-v-html -->
            <div v-if="showMarkdown" class="detail-description markdown-body" @click="openMarkdownLink" v-html="renderedDescription"></div>
            <pre v-else class="detail-description">{{ detail.description }}</pre>
          </template>
          <p v-else class="hint">{{ t('issues.detail.noDescription') }}</p>

          <h3 class="detail-section">{{ t('issues.detail.comments') }}</h3>
          <p v-if="!commentsFetched" class="hint">{{ t('issues.detail.commentsNotFetched') }}</p>
          <p v-else-if="detail.comments.length === 0" class="hint">
            {{ t('issues.detail.noComments') }}
          </p>
          <ol v-else class="comment-list">
            <li v-for="(comment, index) in detail.comments" :key="index" class="comment">
              <p class="comment-meta">
                <span class="comment-author">
                  {{ comment.authorName || t('issues.detail.unknownAuthor') }}
                </span>
                <span class="comment-date">{{ formatDateTime(comment.created) }}</span>
              </p>
              <!-- 詳細本文と同じく lib/markdown.ts のサニタイズ済み HTML のみを渡す -->
              <!-- eslint-disable-next-line vue/no-v-html -->
              <div v-if="showMarkdown" class="comment-body markdown-body" @click="openMarkdownLink" v-html="renderedComments[index]"></div>
              <pre v-else class="comment-body">{{ comment.content }}</pre>
            </li>
          </ol>
          <p v-if="commentsFetched && detail.commentsHistoryOnly > 0" class="hint">
            {{ t('issues.detail.historyOnly', { count: detail.commentsHistoryOnly }) }}
          </p>
          <p v-if="detail.commentsTruncated" class="hint warn">
            {{ t('issues.detail.commentsTruncated') }}
          </p>
          <p class="hint detail-note">{{ detailNote }}</p>
        </template>
      </div>

      <!-- フッタ。エラーと操作ボタンは最大化中もスクロールさせず常に見える位置に置く -->
      <div class="detail-footer">
        <p v-if="copyError" class="error detail-error">{{ copyError }}</p>
        <p v-if="refreshError" class="error detail-error">{{ refreshError }}</p>

        <div class="row buttons detail-buttons">
          <button
            type="button"
            :disabled="refreshing || loading || syncing"
            @click="$emit('refresh')"
          >
            {{ refreshing ? t('issues.detail.refreshing') : t('issues.detail.refresh') }}
          </button>
          <span v-if="refreshing" class="spinner" aria-hidden="true"></span>
          <button v-if="canCopy" type="button" @click="$emit('copy')">
            {{ t('issues.detail.copyUrl') }}
          </button>
          <button v-if="canCopy" type="button" @click="$emit('openBrowser')">
            {{ t('issues.detail.openInBrowser') }}
          </button>
          <button ref="closeButton" type="button" @click="$emit('close')">
            {{ t('common.action.close') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  padding: 1rem;
  box-sizing: border-box;
}

.modal {
  background: var(--surface);
  border-radius: 6px;
  padding: 1.25rem 1.5rem;
  width: min(720px, 92vw);
  max-height: 85vh;
  overflow: auto;
  box-shadow: 0 8px 24px var(--shadow);
  font-size: 0.9rem;
  transition:
    width 0.15s ease,
    height 0.15s ease;
}

/*
 * 最大化(設計 §3)。
 *
 * padding・border 込みで寸法を決めるため box-sizing: border-box にし、
 * ヘッダ・フッタ固定 + 可変領域だけスクロールの flex 縦積みにする。
 * max-height は 100%(= オーバーレイの内容ボックス)で上書きする。85vh の解除が
 * 目的だが、none にするとオーバーレイの padding(1rem)の分だけはみ出し得るため、
 * 幅の max-width: 100% と対にして「必ず画面内に収まる」ことを保証する。
 */
.modal.maximized {
  box-sizing: border-box;
  width: 96vw;
  height: 96vh;
  max-width: 100%;
  max-height: 100%;
  /* スクロールは detail-body だけが担当する(モーダル自身は動かさない) */
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

/*
 * ヘッダ・フッタは「基本は中身なりの高さ、上限を超えたら自分の中でスクロール」。
 *
 * ヘッダには件数の決まっていない警告、フッタには折り返し得るエラー文とボタン群が
 * 入る。両方を縮小不可(flex: 0 0 auto)にすると、狭小ウィンドウ・200% ズームで
 * 合計高さが画面を超えたときに可変領域が高さ 0 まで潰れ、しかもモーダル自身は
 * overflow: hidden のため固定領域も読めなくなる(レビュー 1 回目 指摘 1)。
 * 上限を 40% ずつに切り、超えた分は各領域内のスクロールへ退避させることで、
 * 可変領域には必ず 20% 以上が残る。
 */
.modal.maximized .detail-header {
  flex: 0 1 auto;
  min-height: 0;
  max-height: 40%;
  overflow-y: auto;
}

.modal.maximized .detail-footer {
  flex: 0 1 auto;
  min-height: 0;
  max-height: 40%;
  overflow-y: auto;
}

/* 可変領域。min-height: 0 が無いと flex 項目が中身の高さまで伸びてスクロールしない */
.modal.maximized .detail-body {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

/*
 * 最大化中は内側の高さ制限を外す。
 * detail-body のスクロールと本文・コメントのスクロールが重なると
 * 二重スクロールになり、広げた意味も無くなるため。
 */
.modal.maximized .detail-description,
.modal.maximized .comment-list {
  max-height: none;
  overflow: visible;
}

@media (prefers-reduced-motion: reduce) {
  .modal {
    transition: none;
  }
}

.notice {
  background: var(--bg-muted);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.notice.warn,
.hint.warn {
  color: var(--warning-text);
}

.notice.warn {
  background: var(--warning-bg);
  border-color: var(--warning-border);
}

.error {
  color: var(--danger-text);
  font-size: 0.9rem;
  margin: 0.5rem 0 0;
}

.hint {
  font-size: 0.8rem;
  color: var(--text-muted);
  margin: 0.5rem 0 0.75rem;
}

/* タイトルと最大化トグルを 1 行に並べる(トグルは常に右端) */
.detail-title-row {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
}

.detail-title {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 0.5rem;
  font-size: 1.05rem;
  margin: 0 0 0.75rem;
  /* 余白部分がダブルクリックの当たり判定になるため、行いっぱいまで広げる */
  flex: 1 1 auto;
  min-width: 0;
}

/* 最大化 / 復元のトグル。押されている(最大化中)ときは塗りで示す */
.maximize-toggle {
  flex: 0 0 auto;
  margin: 0;
  padding: 0.1rem 0.45rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--surface);
  color: var(--text-muted);
  font-size: 0.95rem;
  line-height: 1.2;
  cursor: pointer;
}

.maximize-toggle:hover {
  background: var(--bg-hover);
  color: var(--text);
}

/* 最大化中であることを見た目でも示す(状態は aria ではなくラベルの文言が伝える) */
.modal.maximized .maximize-toggle {
  background: var(--accent-emphasis);
  border-color: var(--accent-emphasis);
  color: var(--on-accent);
}

.modal.maximized .maximize-toggle:hover {
  background: var(--accent-emphasis-hover);
  border-color: var(--accent-emphasis-hover);
  color: var(--on-accent);
}

.detail-key {
  font-family: monospace;
  color: var(--text-muted);
}

.detail-summary {
  font-size: 1.05rem;
}

.detail-section {
  font-size: 0.9rem;
  margin: 1rem 0 0.4rem;
}

.detail-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  column-gap: 0.75rem;
  row-gap: 0.3rem;
  margin: 0;
}

.detail-grid dt {
  font-weight: 600;
  color: var(--text-muted);
}

.detail-grid dd {
  margin: 0;
  word-break: break-word;
}

.detail-description {
  margin: 0;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-muted);
  font-family: inherit;
  font-size: 0.85rem;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}

.detail-section-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 0.5rem;
}

/* 「整形表示 / 原文」の切替。押されている側を塗りで示す */
.view-toggle {
  display: flex;
  gap: 0;
}

.view-toggle button {
  font-size: 0.75rem;
  padding: 0.15rem 0.6rem;
  margin: 0;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text);
  cursor: pointer;
}

.view-toggle button + button {
  border-left: none;
}

.view-toggle button:first-child {
  border-radius: 4px 0 0 4px;
}

.view-toggle button:last-child {
  border-radius: 0 4px 4px 0;
}

.view-toggle button[aria-pressed='true'] {
  background: var(--accent-emphasis);
  border-color: var(--accent-emphasis);
  color: var(--on-accent);
}

/* 整形表示した本文。色はすべてトークン経由でダークモードに追従させる */
.markdown-body {
  white-space: normal;
}

.markdown-body :deep(> *:first-child) {
  margin-top: 0;
}

.markdown-body :deep(> *:last-child) {
  margin-bottom: 0;
}

.markdown-body :deep(h1),
.markdown-body :deep(h2),
.markdown-body :deep(h3),
.markdown-body :deep(h4),
.markdown-body :deep(h5),
.markdown-body :deep(h6) {
  margin: 0.8rem 0 0.4rem;
  font-size: 0.95rem;
  line-height: 1.4;
}

.markdown-body :deep(p) {
  margin: 0.4rem 0;
}

.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  margin: 0.4rem 0;
  padding-left: 1.4rem;
}

.markdown-body :deep(blockquote) {
  margin: 0.4rem 0;
  padding: 0.1rem 0.75rem;
  border-left: 3px solid var(--border);
  color: var(--text-muted);
}

.markdown-body :deep(hr) {
  border: none;
  border-top: 1px solid var(--border);
  margin: 0.8rem 0;
}

.markdown-body :deep(code) {
  font-family: monospace;
  font-size: 0.85em;
  background: var(--bg-hover);
  border-radius: 3px;
  padding: 0.05rem 0.25rem;
}

.markdown-body :deep(pre) {
  margin: 0.4rem 0;
  padding: 0.5rem 0.6rem;
  background: var(--bg-hover);
  border-radius: 4px;
  overflow-x: auto;
}

.markdown-body :deep(pre code) {
  background: none;
  padding: 0;
}

.markdown-body :deep(table) {
  border-collapse: collapse;
  margin: 0.4rem 0;
}

.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid var(--border);
  padding: 0.2rem 0.5rem;
  text-align: left;
}

.markdown-body :deep(th) {
  background: var(--bg-hover);
}

/* リンクは href を持たない(クリックで既定ブラウザへ渡す)ためカーソルを明示する */
.markdown-body :deep(a[data-href]) {
  color: var(--accent-fg);
  text-decoration: underline;
  cursor: pointer;
}

.detail-note {
  margin: 0.75rem 0 0;
}

.comment-note {
  margin: 0 0 0.75rem;
}

.comment-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 16rem;
  overflow-y: auto;
  border: 1px solid var(--border);
  border-radius: 4px;
}

.comment {
  padding: 0.5rem 0.75rem;
}

.comment + .comment {
  border-top: 1px solid var(--border);
}

.comment-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin: 0 0 0.25rem;
  font-size: 0.8rem;
  color: var(--text-muted);
}

.comment-author {
  font-weight: 600;
}

.comment-body {
  margin: 0;
  font-family: inherit;
  font-size: 0.85rem;
  white-space: pre-wrap;
  word-break: break-word;
}

.detail-error {
  margin: 0.75rem 0 0;
}

.detail-buttons {
  margin-top: 1rem;
  margin-bottom: 0;
}

.spinner {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid var(--border);
  border-top-color: var(--accent-fg);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
