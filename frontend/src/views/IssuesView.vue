<script lang="ts" setup>
// 課題抽出画面(プレースホルダ)。検索条件フォームの骨格のみ。機能は未実装。
import { reactive } from 'vue'

const cond = reactive({
  project: '',
  dateKind: 'updated',
  dateFrom: '',
  dateTo: '',
  keyword: '',
  status: '',
  assignee: '',
})
</script>

<template>
  <div class="issues">
    <h1>課題抽出</h1>
    <p class="placeholder-note">この画面は未実装です(マイルストーン 2 で実装予定)。以下は検索条件フォームの骨格のみで、動作しません。</p>

    <section class="cond-form">
      <div class="row">
        <label for="i-project">プロジェクト</label>
        <select id="i-project" v-model="cond.project" disabled>
          <option value="">(未実装)</option>
        </select>
      </div>

      <div class="row">
        <label for="i-date-kind">期間</label>
        <select id="i-date-kind" v-model="cond.dateKind" disabled>
          <option value="updated">更新日</option>
          <option value="created">作成日</option>
        </select>
        <input v-model="cond.dateFrom" type="date" disabled />
        <span>〜</span>
        <input v-model="cond.dateTo" type="date" disabled />
      </div>

      <div class="row">
        <label for="i-keyword">キーワード</label>
        <input id="i-keyword" v-model="cond.keyword" type="text" placeholder="件名 + 詳細の部分一致" disabled />
      </div>
      <p class="hint">検索対象は件名と詳細のみです(コメント等は対象外。Backlog サイト上の検索とは範囲が異なります)。</p>

      <div class="row">
        <label for="i-status">状態</label>
        <select id="i-status" v-model="cond.status" disabled>
          <option value="">すべて</option>
        </select>
        <label for="i-assignee">担当者</label>
        <select id="i-assignee" v-model="cond.assignee" disabled>
          <option value="">すべて</option>
        </select>
      </div>

      <div class="row buttons">
        <button disabled>検索(未実装)</button>
        <button disabled>Excel 出力(未実装)</button>
      </div>
    </section>
  </div>
</template>

<style scoped>
h1 {
  font-size: 1.4rem;
  margin: 0 0 1rem;
}

.placeholder-note {
  background: #f6f8fa;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  padding: 0.5rem 0.75rem;
  font-size: 0.85rem;
  color: #57606a;
}

.cond-form {
  max-width: 640px;
}

.row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 0.75rem;
  flex-wrap: wrap;
}

.row label {
  font-weight: 600;
  font-size: 0.9rem;
  min-width: 6rem;
}

input,
select {
  padding: 0.4rem 0.5rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  font-size: 0.9rem;
  background: #fff;
  color: #1f2328;
}

input:disabled,
select:disabled {
  background: #f6f8fa;
  color: #8c959f;
}

.hint {
  font-size: 0.8rem;
  color: #57606a;
  margin: -0.25rem 0 0.75rem 6.5rem;
}

button {
  padding: 0.4rem 0.9rem;
  border: 1px solid #d0d7de;
  border-radius: 4px;
  background: #f6f8fa;
  font-size: 0.9rem;
}

button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
