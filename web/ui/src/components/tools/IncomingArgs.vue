<script setup lang="ts">
import { computed } from "vue";
import { useI18n } from "vue-i18n";
import { incomingPath, previewLines } from "@/agent/argsPreview";
import type { IncomingArgs } from "@/api/types";

const props = defineProps<{ incoming: IncomingArgs }>();

const { t } = useI18n();

// The path resolves from the head when the model emits path first, from the
// tail when it doesn't — see incomingPath.
const path = computed(() => incomingPath(props.incoming) ?? "…");
const kb = computed(() => (props.incoming.bytes / 1024).toFixed(1));
// Plain text on purpose: syntax-highlighting a partial document on every
// fragment is the O(n²) trap the raw tail window exists to avoid.
const preview = computed(() => previewLines(props.incoming.tail ?? "", 10).join("\n"));
</script>

<template>
  <div class="incoming">
    <div class="line">
      <span class="dot" />
      <span class="name">{{ incoming.name || t("incomingArgs.toolFallback") }}</span>
      <span class="path" :title="path">{{ path }}</span>
      <span class="badge">{{ t("incomingArgs.received", { kb }) }}</span>
    </div>
    <pre v-if="preview" class="preview">{{ preview }}</pre>
  </div>
</template>

<style scoped lang="scss">
/* Quieter than a running tool card on purpose: the call has not started yet,
   its arguments are still being generated, so everything here is muted. */
.incoming {
  margin: 6px 0;
}

.line {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
}

.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--el-text-color-secondary);
  flex: 0 0 auto;
  animation: pulse 1.6s ease-in-out infinite;
}

@keyframes pulse {
  50% {
    opacity: 0.25;
  }
}

.name {
  font-family: ui-monospace, monospace;
  font-weight: 600;
  color: var(--el-text-color-regular);
}

.path {
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.badge {
  margin-left: auto;
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);
  flex: 0 0 auto;
}

.preview {
  margin: 6px 0 10px 14px;
  max-height: 220px;
  overflow: auto;
  font: 12px/1.55 ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  white-space: pre-wrap;
  word-break: break-all;
  border-left: 1px dashed var(--el-border-color);
  padding-left: 10px;
}
</style>
