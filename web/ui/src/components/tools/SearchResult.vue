<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import type { FindDetails, GrepDetails, ToolResult } from "@/api/types";

const props = defineProps<{
  result: ToolResult;
  details: FindDetails | GrepDetails;
  /** grep rows are path:line:text; find rows are bare paths. */
  kind: "find" | "grep";
}>();

const emit = defineEmits<{ suggest: [string] }>();

const { t } = useI18n();

const grep = computed(() => (props.details as GrepDetails).files !== undefined);

// The rows are parsed back out of the text rather than shipped as a second
// structure. The text is what the model sees, so rendering the same bytes keeps
// the two views from ever disagreeing.
const rows = computed(() =>
  props.result.text
    .split("\n")
    .filter((line) => line !== "" && !line.startsWith("["))
    .map((line) => {
      if (props.kind === "find") return { path: line, line: null as number | null, text: "" };
      // Split on the first two colons only: the matched text may contain more,
      // and a Windows-style path may contain one before them.
      const first = line.indexOf(":");
      const second = line.indexOf(":", first + 1);
      if (first < 0 || second < 0) return { path: line, line: null, text: "" };
      const n = Number(line.slice(first + 1, second));
      if (!Number.isFinite(n)) return { path: line, line: null, text: "" };
      return { path: line.slice(0, first), line: n, text: line.slice(second + 1) };
    }),
);

const missed = computed(() => props.details.matches === 0);
</script>

<template>
  <div class="search">
    <div class="meta">
      <span>{{ t("searchResult.matches", { n: details.matches }) }}</span>
      <span v-if="grep && (details as GrepDetails).files">
        {{ t("searchResult.inFiles", { n: (details as GrepDetails).files }) }}
      </span>
      <span class="dim">{{ t("searchResult.scanned", { n: details.scanned }) }}</span>
      <span v-if="grep && (details as GrepDetails).skipped_binary" class="dim">
        {{ t("searchResult.skippedBinary", { n: (details as GrepDetails).skipped_binary }) }}
      </span>
      <span v-if="details.limit_hit" class="warn">{{ t("searchResult.limitHit") }}</span>
      <span v-else-if="details.truncated" class="warn">{{ t("searchResult.truncated") }}</span>
    </div>

    <!-- A miss is a result, not an empty state: the scan count above says
         whether the search looked in the right place. -->
    <i18n-t v-if="missed" keypath="searchResult.noMatch" tag="div" class="miss" scope="global">
      <template #pattern>
        <code>{{ details.pattern }}</code>
      </template>
    </i18n-t>

    <ul v-else class="rows" :class="kind">
      <li v-for="(r, i) in rows" :key="i">
        <span class="path">{{ r.path }}</span>
        <span v-if="r.line !== null" class="lineno">:{{ r.line }}</span>
        <code v-if="r.text" class="text">{{ r.text }}</code>
      </li>
    </ul>

    <button
      v-if="details.limit_hit"
      class="more"
      @click="emit('suggest', t('searchResult.suggestMore', { pattern: details.pattern }))"
    >
      {{ t("searchResult.showMore") }}
    </button>
  </div>
</template>

<style scoped lang="scss">
.meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.dim {
  color: var(--el-text-color-placeholder);
}

.warn {
  color: var(--el-color-warning);
}

.miss {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-lighter);
  border-radius: 4px;
  padding: 6px 8px;

  code {
    font-family: ui-monospace, monospace;
    color: var(--el-text-color-regular);
  }
}

.rows {
  list-style: none;
  margin: 0;
  padding: 6px 8px;
  background: var(--el-fill-color-lighter);
  border-radius: 4px;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  line-height: 1.6;
}

/* A path list is short per row, so columns keep it compact the way ls does.
   grep rows carry a whole line of source and must stay full width. */
.rows.find {
  columns: 2 240px;
  column-gap: 16px;
}

.rows li {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.path {
  color: var(--el-color-primary);
}

.lineno {
  color: var(--el-text-color-placeholder);
}

.text {
  margin-left: 8px;
  color: var(--el-text-color-regular);
  white-space: pre;
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
