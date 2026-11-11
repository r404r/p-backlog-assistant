<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { WikiTreeNode } from '../lib/wikiTree'
defineProps<{ nodes: WikiTreeNode[]; selectedId: number; disabled: boolean }>()
const emit = defineEmits<{ select: [path: string, id: number] }>()
const { t } = useI18n()
</script>
<template>
  <ul class="wiki-tree">
    <li v-for="node in nodes" :key="node.path">
      <button
        v-if="node.children.length && !node.pageIds.length"
        type="button"
        :disabled="disabled"
        :title="t('wiki.selectSubtree')"
        @click="emit('select', node.path, node.pageIds[0] ?? 0)"
      >
        {{ node.label || t('wiki.unnamed') }}
      </button>
      <button
        v-for="id in node.pageIds"
        :key="id"
        type="button"
        :disabled="disabled"
        :aria-pressed="selectedId === id"
        @click="emit('select', node.path, id)"
      >
        {{ node.label || t('wiki.unnamed') }}
      </button>
      <WikiTree
        v-if="node.children.length && !node.pageIds.length"
        :nodes="node.children"
        :selected-id="selectedId"
        :disabled="disabled"
        @select="(path, id) => emit('select', path, id)"
      />
    </li>
  </ul>
</template>
<style scoped>
.wiki-tree {
  list-style: none;
  padding-left: 1rem;
  margin: 0.25rem 0;
}
.wiki-tree button {
  display: block;
  text-align: left;
  max-width: 100%;
  overflow-wrap: anywhere;
  margin: 0.2rem 0;
}
.wiki-tree button[aria-pressed='true'] {
  font-weight: bold;
  border-color: var(--accent-fg);
}
</style>
