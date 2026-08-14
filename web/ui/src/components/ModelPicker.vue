<script setup lang="ts">
import { computed, ref } from "vue";
import { ArrowRight, Check, Cpu } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";
import type { ModelInfo } from "@/api/types";

// Button + popover list sitting in the composer bar (after the agent-qa
// reference page): the button names only the current model; provider, context
// window and key hints live on the rows, where they help choose instead of
// cluttering the bar.
const props = defineProps<{
  models: ModelInfo[];
  current?: string;
  /** disabled while a run is in flight — switching models mid-run returns 409. */
  disabled?: boolean;
}>();

const emit = defineEmits<{ change: [string] }>();

const { t } = useI18n();

const open = ref(false);

function pick(m: ModelInfo) {
  // A model whose key is missing is listed but unselectable: hiding it would
  // leave you wondering where it went. Naming the variable is the difference
  // between knowing it is unusable and knowing what to do — the terminal prints
  // its own hint here, and a browser has no way to.
  if (!m.configured) return;
  open.value = false;
  if (m.id !== props.current) emit("change", m.id);
}

const ctx = (m: ModelInfo) =>
  m.context_window >= 1_000_000
    ? `${Math.round(m.context_window / 1_000_000)}M`
    : `${Math.round(m.context_window / 1000)}K`;

const desc = (m: ModelInfo) =>
  `${m.provider} · ${ctx(m)}${m.configured ? "" : ` · ${t("modelPicker.needsKey", { key: m.key_env ?? "key" })}`}`;

// Shown beside the button rather than on every row, because it is a property of
// what is in effect rather than a reason to choose one entry over another.
const subagentModel = computed(
  () => props.models.find((m) => m.id === props.current)?.subagent_model,
);
</script>

<template>
  <el-popover
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="264"
    :teleported="false"
    popper-class="picker-pop"
  >
    <template #reference>
      <button
        class="picker-btn tip"
        :disabled="disabled"
        :data-tip="open ? undefined : disabled ? t('modelPicker.tipDisabled') : t('modelPicker.tip')"
      >
        <el-icon><Cpu /></el-icon>
        <span class="name">{{ current || t("modelPicker.selectModel") }}</span>
        <el-icon class="caret" :class="{ open }"><ArrowRight /></el-icon>
      </button>
    </template>
    <div class="list">
      <div
        v-for="m in models"
        :key="m.id"
        class="item"
        :class="{ active: m.id === current, off: !m.configured }"
        @click="pick(m)"
      >
        <div class="item-text">
          <div class="item-title">{{ m.id }}</div>
          <div class="item-desc">{{ desc(m) }}</div>
        </div>
        <el-icon v-if="m.id === current" class="item-check"><Check /></el-icon>
      </div>
    </div>
  </el-popover>
  <!-- A sibling rather than part of the popover, so the bar is unchanged for
       anyone with no mapping configured. -->
  <span
    v-if="subagentModel"
    class="sub"
    :title="t('modelPicker.subagentTip', { model: subagentModel, current })"
    >{{ t("modelPicker.subagentLabel", { model: subagentModel }) }}</span
  >
</template>

<style scoped lang="scss">
.picker-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 11px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  font: 600 13px var(--pg-mono);
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 16px;
  }

  &:hover:not(:disabled) {
    background: var(--el-fill-color-light);
  }

  &:disabled {
    opacity: 0.6;
    cursor: default;
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.caret {
  color: var(--el-text-color-secondary);
  transition: transform 0.2s;

  &.open {
    transform: rotate(90deg);
  }
}

/* teleported=false, so the popper is reachable from scoped styles via :deep. */
:deep(.picker-pop) {
  padding: 6px !important;
  border-radius: 10px !important;
}

.list {
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 320px;
  overflow-y: auto;
}

.item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s;

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.active {
    background: var(--el-fill-color);
  }

  &.off {
    opacity: 0.55;
    cursor: not-allowed;
  }
}

.item-text {
  flex: 1;
  min-width: 0;
}

.item-title {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.3;
}

.item-desc {
  margin-top: 1px;
  font: 11px ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--el-text-color-secondary);
}

.item-check {
  flex: none;
  color: var(--el-color-primary);
}

/* Quiet on purpose: it is a fact about the configuration, not a control and not
   a warning. */
.sub {
  font: 11px ui-monospace, SFMono-Regular, Menlo, monospace;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  cursor: help;
}
</style>
