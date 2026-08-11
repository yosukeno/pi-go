<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import CodeBlock from "../CodeBlock.vue";
import type { ReadDetails, ToolResult } from "@/api/types";

const props = defineProps<{ result: ToolResult; details: ReadDetails }>();

// Offering to continue rather than doing it: a truncated read is the model's
// business, and silently issuing the next one for it would be deciding on the
// user's behalf. Clicking fills the input box instead of sending.
const emit = defineEmits<{ suggest: [string] }>();

const { t } = useI18n();

const lang = computed(() => props.details.path.split(".").pop() ?? "");
const nextOffset = computed(() => props.details.first_line + props.details.shown_lines - 1);
</script>

<template>
  <div class="read">
    <div class="meta">
      <span>{{ t("readResult.lines", { n: details.total_lines }) }}</span>
      <span v-if="details.truncated" class="warn">
        {{ t("readResult.truncated", { first: details.first_line, last: nextOffset, by: t(details.truncated_by === "bytes" ? "readResult.byBytes" : "readResult.byLines") }) }}
      </span>
    </div>
    <CodeBlock :code="result.text" :lang="lang" line-numbers :start-line="details.first_line" />
    <button
      v-if="details.truncated"
      class="more"
      @click="emit('suggest', t('readResult.suggestContinue', { path: details.path, line: nextOffset + 1 }))"
    >
      {{ t("readResult.continueFrom", { line: nextOffset + 1 }) }}
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
