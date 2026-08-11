<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import DiffView from "../DiffView.vue";
import type { ToolResult, WriteDetails } from "@/api/types";
import { diffOf } from "@/agent/timeline";

const props = defineProps<{ result: ToolResult; details: WriteDetails }>();

const { t } = useI18n();

// A new file has nothing to diff against, so it shows a size line only. An
// overwrite carries a diff, and that is the part worth looking at.
const diff = computed(() => diffOf(props.details));

// Largest fitting English unit, two decimals — 34203 bytes reads "33.40 KB".
const size = computed(() => {
  const b = props.details.bytes;
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(2)} KB`;
  return `${(b / 1024 / 1024).toFixed(2)} MB`;
});
</script>

<template>
  <div class="write">
    <div class="meta">
      <span class="tag" :class="details.created ? 'new' : 'over'">{{ t(details.created ? "writeResult.created" : "writeResult.overwritten") }}</span>
      <span>{{ size }}</span>
    </div>
    <DiffView v-if="diff" v-bind="diff" />
  </div>
</template>

<style scoped lang="scss">
.meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 6px;
}

.tag {
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 10px;

  &.new {
    background: color-mix(in srgb, var(--el-color-success) 15%, transparent);
    color: var(--el-color-success);
  }

  &.over {
    background: color-mix(in srgb, var(--el-color-warning) 15%, transparent);
    color: var(--el-color-warning);
  }
}
</style>
