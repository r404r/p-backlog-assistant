<script lang="ts" setup>
// 同期状態画面(プレースホルダ)。機能は未実装。
</script>

<template>
  <div>
    <h1>同期状態</h1>
    <p class="placeholder-note">
      この画面は未実装です(マイルストーン 2 で実装予定)。<br />
      予定機能: データ種別ごとの最終同期時刻・件数の表示、手動での差分同期 / フル同期の実行、
      レート制限の残量表示。
    </p>
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
  max-width: 640px;
}
</style>
