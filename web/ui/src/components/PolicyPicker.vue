<script setup lang="ts">
import { computed, ref } from "vue";
import { ArrowRight, Check, CircleCheck, Lock, WarningFilled } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";
import type { PolicyMode } from "@/api/types";
import type { Component } from "vue";

// The approval-gate mode switch, living in the composer bar. Menu layout after
// ChatGPT's approval popover: icon + name + one-line rationale per option, the
// dangerous one in orange. Switching into auto disarms the bash gate — there is
// no confirmation dialog, so the orange is the whole warning.
const props = defineProps<{
  mode: PolicyMode;
  /** remaining turns of a turn-limited auto run, when there is one. */
  remaining?: number;
}>();

const emit = defineEmits<{ change: [PolicyMode] }>();

const { t } = useI18n();

const open = ref(false);

const MODES = computed<{ key: PolicyMode; label: string; desc: string; icon: Component }[]>(() => [
  { key: "strict", label: t("policyPicker.modes.strict.label"), desc: t("policyPicker.modes.strict.desc"), icon: Lock },
  { key: "standard", label: t("policyPicker.modes.standard.label"), desc: t("policyPicker.modes.standard.desc"), icon: CircleCheck },
  { key: "auto", label: t("policyPicker.modes.auto.label"), desc: t("policyPicker.modes.auto.desc"), icon: WarningFilled },
]);

function pick(m: PolicyMode) {
  open.value = false;
  if (m !== props.mode) emit("change", m);
}

function meta(m: PolicyMode) {
  return MODES.value.find((x) => x.key === m)!;
}
</script>

<template>
  <el-popover
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="300"
    :teleported="false"
    popper-class="policy-pop"
  >
    <template #reference>
      <button
        class="policy-btn tip"
        :class="mode"
        :data-tip="open ? undefined : t('policyPicker.tip')"
      >
        <el-icon><component :is="meta(mode).icon" /></el-icon>
        <span class="name">{{ meta(mode).label }}{{ remaining ? ` ·${remaining}` : "" }}</span>
        <el-icon class="caret" :class="{ open }"><ArrowRight /></el-icon>
      </button>
    </template>
    <div class="menu-title">{{ t("policyPicker.menuTitle") }}</div>
    <div class="list">
      <div
        v-for="m in MODES"
        :key="m.key"
        class="item"
        :class="{ active: m.key === mode, [m.key]: true }"
        @click="pick(m.key)"
      >
        <el-icon class="item-icon"><component :is="m.icon" /></el-icon>
        <div class="item-text">
          <div class="item-title">{{ m.label }}</div>
          <div class="item-desc">{{ m.desc }}</div>
        </div>
        <el-icon v-if="m.key === mode" class="item-check"><Check /></el-icon>
      </div>
    </div>
  </el-popover>
</template>

<style scoped lang="scss">
.policy-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 30px;
  padding: 0 10px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 16px;
  }

  &:hover {
    background: var(--el-fill-color-light);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }

  /* Only the gate-off mode shouts, same as ChatGPT's orange "完全访问". */
  &.auto {
    color: var(--el-color-warning);
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
:deep(.policy-pop) {
  padding: 6px !important;
  border-radius: 12px !important;
}

.menu-title {
  padding: 8px 12px 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s;

  &:hover {
    background: var(--el-fill-color-light);
  }

  /* auto stays orange whether or not it is the active one: the warning is
     about what the mode means, not about selection state. */
  &.auto {
    .item-icon,
    .item-title,
    .item-check {
      color: var(--el-color-warning);
    }
  }
}

.item-icon {
  flex: none;
  font-size: 17px;
  color: var(--el-text-color-regular);
}

.item-text {
  flex: 1;
  min-width: 0;
}

.item-title {
  font-size: 13px;
  font-weight: 600;
  line-height: 1.3;
  color: var(--el-text-color-primary);
}

.item-desc {
  margin-top: 2px;
  font-size: 12px;
  line-height: 1.35;
  color: var(--el-text-color-secondary);
}

.item-check {
  flex: none;
  color: var(--el-color-primary);
}
</style>
