<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { LsDetails, ToolResult } from "@/api/types";

const props = defineProps<{ result: ToolResult; details: LsDetails }>();

const emit = defineEmits<{ suggest: [string] }>();

const { t } = useI18n();

// The trailing '/' the tool writes is the only type information in the text, so
// the renderer reads it back rather than asking the server for a second shape.
const entries = computed(() =>
  props.result.text
    .split("\n")
    .filter((line) => line !== "" && !line.startsWith("["))
    .map((name) => ({ name, dir: name.endsWith("/") })),
);
</script>

<template>
  <div class="ls">
    <div class="meta">
      <span v-if="details.dirs">{{ t("lsResult.dirs", { n: details.dirs }) }}</span>
      <span v-if="details.files">{{ t("lsResult.files", { n: details.files }) }}</span>
      <span v-if="details.entry_limited" class="warn">{{ t("lsResult.entryLimited") }}</span>
      <span v-else-if="details.truncated" class="warn">{{ t("lsResult.truncated") }}</span>
    </div>
    <ul class="grid">
      <li v-for="e in entries" :key="e.name" :class="{ dir: e.dir }">{{ e.name }}</li>
    </ul>
    <button
      v-if="details.entry_limited"
      class="more"
      @click="emit('suggest', t('lsResult.suggestMore', { path: details.path }))"
    >
      {{ t("lsResult.listMore") }}
    </button>
  </div>
</template>

<style scoped lang="scss">
.meta {
  display: flex;
  gap: 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.warn {
  color: var(--el-color-warning);
}

.grid {
  list-style: none;
  margin: 0;
  padding: 6px 8px;
  background: var(--el-fill-color-lighter);
  border-radius: 4px;
  /* A directory listing is a set of short names; columns keep a 40-entry
     directory from pushing the rest of the turn off the screen. */
  columns: 3 160px;
  column-gap: 16px;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  line-height: 1.55;
}

.grid li {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-regular);
}

.grid li.dir {
  /* A fixed sky blue, not --el-color-primary: the UI's primary is black by
     decision, but a directory wants the colour a terminal's ls gives it. */
  color: #0ea5e9;
  font-weight: 600;
}

.more {
  margin-top: 6px;
  border: 1px solid var(--el-border-color);
  background: transparent;
  color: var(--el-color-primary);
  border-radius: 4px;
  font-size: 11px;
  padding: 3px 8px;
  cursor: pointer;
}
</style>
