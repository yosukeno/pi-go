<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import { indexEpoch } from "./fileTreeStore";
import { gitLabel, gitTooltip, isWarn } from "./gitBar";
import type { GitStatus } from "@/api/types";

// One line under the file panel's header saying what version control thinks of
// this workspace: the branch, how far it has drifted from its upstream, and how
// much is uncommitted.
//
// It exists because nothing in this UI ever mentioned git, and the cost of that
// showed up in this project's own repository — a hundred and thirty files sat
// uncommitted for days while every surface reported a healthy workspace. The
// panel's "changes" tab could not have caught it: that tab is scoped to what
// this agent touched since its own baseline, which is a different question.
//
// Counts, never a file list. The panel below already lists files, and the
// comparable feature in Claude Code became a five-figure token cost per turn by
// embedding content instead of numbers.
const props = defineProps<{ epoch: number }>();

const { t } = useI18n();
const status = ref<GitStatus | null>(null);
// Failure shows nothing at all. This is a status line, not a task: an error row
// where the branch should be is worse than the silence it replaces.
const failed = ref(false);

async function load() {
  try {
    status.value = await api.workspaceGit();
    failed.value = false;
  } catch {
    failed.value = true;
  }
}

// Two triggers, both meaning "the files may have moved under us": the manual
// refresh button (epoch), and the end of a run (indexEpoch, which the stream
// bumps because the agent's writes may have added files). Polling is deliberately
// absent — a timer would run a git subprocess forever on an idle tab.
watch(() => props.epoch, load, { immediate: true });
watch(indexEpoch, load);

// Composition lives in ./gitBar.ts so it can be tested without mounting this
// component; t is passed in so those tests can assert structure, not copy.
const label = computed(() => (failed.value ? "" : gitLabel(status.value, t)));
const tooltip = computed(() => (failed.value ? "" : gitTooltip(status.value, t)));
const warn = computed(() => !failed.value && isWarn(status.value));
</script>

<template>
  <div v-if="label" class="git-bar" :class="{ warn }" :title="tooltip">
    <svg width="12" height="12" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <circle cx="4.5" cy="12" r="1.9" stroke="currentColor" stroke-width="1.3" />
      <circle cx="4.5" cy="4" r="1.9" stroke="currentColor" stroke-width="1.3" />
      <circle cx="11.5" cy="4" r="1.9" stroke="currentColor" stroke-width="1.3" />
      <path d="M4.5 5.9v4.2M6.4 4h3.2M11.5 5.9v1.6a2.5 2.5 0 0 1-2.5 2.5H6.4" stroke="currentColor" stroke-width="1.3" />
    </svg>
    <span class="text">{{ label }}</span>
  </div>
</template>

<style scoped lang="scss">
.git-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 12px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  cursor: default;

  &.warn {
    color: var(--el-color-warning);
  }

  svg {
    flex: none;
  }

  .text {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
}
</style>
