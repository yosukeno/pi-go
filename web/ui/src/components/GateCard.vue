<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import type { PendingGate } from "@/api/types";

const { t } = useI18n();

const props = defineProps<{
  gate: PendingGate;
  /** live is false when replaying history, where an undecided card is stale. */
  live: boolean;
}>();

const emit = defineEmits<{
  decide: [{ allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [];
  thaw: [];
}>();

// The countdown is display only; the server's clock decides. It is computed from
// an absolute deadline rather than counted down from a duration, which is why a
// page reload still shows the right number.
const now = ref(Date.now());
const timer = window.setInterval(() => (now.value = Date.now()), 200);
onBeforeUnmount(() => window.clearInterval(timer));

const remaining = computed(() => Math.max(0, props.gate.deadline - now.value));
const seconds = computed(() => Math.ceil(remaining.value / 1000));
const expired = computed(() => !props.live || remaining.value === 0);
const dangerous = computed(() => (props.gate.danger?.length ?? 0) > 0);

const args = computed(() => (props.gate.args ?? {}) as Record<string, unknown>);
const command = computed(() => String(args.value.command ?? ""));
const isBash = computed(() => props.gate.tool === "bash");

const rewriting = ref(false);
const draft = ref("");
const remember = ref(false);
const deciding = ref(false);

watch(
  () => props.gate.gate_id,
  () => {
    rewriting.value = false;
    remember.value = false;
    deciding.value = false;
  },
);

function startRewrite() {
  draft.value = isBash.value ? command.value : JSON.stringify(args.value, null, 2);
  rewriting.value = true;
  // Editing takes time, and being judged as "did not answer" while typing would
  // be wrong. The server freezes its clock and reports the new deadline on thaw.
  emit("freeze");
}

function cancelRewrite() {
  rewriting.value = false;
  emit("thaw");
}

function submitRewrite() {
  let next: unknown;
  if (isBash.value) {
    next = { ...args.value, command: draft.value };
  } else {
    try {
      next = JSON.parse(draft.value);
    } catch {
      parseError.value = t("gateCard.invalidJson");
      return;
    }
  }
  deciding.value = true;
  emit("decide", { allow: true, args: next });
}

const parseError = ref("");

function approve() {
  deciding.value = true;
  // A danger match cannot be remembered; the server enforces that too, so the
  // checkbox being disabled is a hint rather than the control.
  emit("decide", {
    allow: true,
    remember: remember.value && !dangerous.value ? (isBash.value ? "command" : "tool") : undefined,
  });
}

function reject() {
  deciding.value = true;
  emit("decide", { allow: false, reason: t("gateCard.rejectReason") } as never);
}
</script>

<template>
  <div class="gate" :class="{ expired, dangerous }">
    <div class="head">
      <span class="title">{{ expired ? t("gateCard.expired") : t("gateCard.confirmRequired") }}</span>
      <span class="tool">{{ gate.tool }}</span>
      <span v-if="!expired" class="clock">{{ t("gateCard.remaining", { n: seconds }) }}</span>
    </div>

    <template v-if="rewriting">
      <textarea v-model="draft" class="editor" :rows="isBash ? 3 : 8" spellcheck="false" />
      <div v-if="parseError" class="parse-error">{{ parseError }}</div>
      <div class="actions">
        <button class="primary" :disabled="deciding" @click="submitRewrite">{{ t("gateCard.runRewritten") }}</button>
        <button :disabled="deciding" @click="cancelRewrite">{{ t("gateCard.cancelRewrite") }}</button>
      </div>
    </template>

    <template v-else>
      <pre class="args">{{ isBash ? command : JSON.stringify(args, null, 2) }}</pre>

      <div v-if="dangerous" class="danger">
        {{ t("gateCard.dangerHit", { patterns: gate.danger?.join("、") }) }}
        <span class="note">{{ t("gateCard.dangerNote") }}</span>
      </div>

      <div v-if="!expired" class="actions">
        <button class="primary" :disabled="deciding" @click="approve">{{ t("gateCard.approve") }}</button>
        <button :disabled="deciding" @click="startRewrite">{{ t("gateCard.rewrite") }}</button>
        <button class="danger-btn" :disabled="deciding" @click="reject">{{ t("gateCard.reject") }}</button>
      </div>
      <label v-if="!expired" class="remember" :class="{ off: dangerous }">
        <input v-model="remember" type="checkbox" :disabled="dangerous" />
        <span v-if="isBash">{{ t("gateCard.rememberCommand") }}</span>
        <span v-else>{{ t("gateCard.rememberTool", { tool: gate.tool }) }}</span>
        <span v-if="dangerous" class="note">{{ t("gateCard.rememberDangerNote") }}</span>
      </label>
      <div v-if="expired" class="stale">{{ t("gateCard.staleNote") }}</div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.gate {
  border: 1px solid var(--el-color-warning);
  border-radius: 6px;
  padding: 10px 12px;
  background: color-mix(in srgb, var(--el-color-warning) 6%, transparent);

  &.dangerous {
    border-color: var(--el-color-danger);
    background: color-mix(in srgb, var(--el-color-danger) 6%, transparent);
  }

  &.expired {
    border-color: var(--el-border-color);
    background: var(--el-fill-color-lighter);
    opacity: 0.75;
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  margin-bottom: 8px;
}

.title {
  font-weight: 600;
}

.tool {
  font-family: ui-monospace, monospace;
  color: var(--el-color-primary);
}

.clock {
  margin-left: auto;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}

.args,
.editor {
  width: 100%;
  box-sizing: border-box;
  margin: 0;
  padding: 8px 10px;
  border-radius: 4px;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
  font: 12px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: pre-wrap;
  word-break: break-all;
}

.editor {
  resize: vertical;
}

.danger {
  margin-top: 8px;
  font-size: 12px;
  color: var(--el-color-danger);
}

.note {
  color: var(--el-text-color-secondary);
  font-size: 11px;
}

.parse-error {
  margin-top: 6px;
  font-size: 11px;
  color: var(--el-color-danger);
}

.actions {
  display: flex;
  gap: 8px;
  margin-top: 10px;

  button {
    border: 1px solid var(--el-border-color);
    background: var(--el-bg-color);
    border-radius: 4px;
    padding: 4px 12px;
    font-size: 12px;
    cursor: pointer;

    &:disabled {
      opacity: 0.5;
      cursor: default;
    }

    &.primary {
      background: var(--el-color-primary);
      border-color: var(--el-color-primary);
      color: #fff;
    }

    &.danger-btn {
      color: var(--el-color-danger);
      border-color: color-mix(in srgb, var(--el-color-danger) 40%, transparent);
    }
  }
}

.remember {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);

  &.off {
    opacity: 0.6;
  }
}

.stale {
  margin-top: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}
</style>
