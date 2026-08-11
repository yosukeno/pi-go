<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import type { Usage } from "@/api/types";

// Session-cumulative billing tokens, as opposed to the ContextMeter next to it
// which tracks the latest turn's window occupancy. Same two-layer pattern: the
// button answers "how much have I burned" at a glance; the popover breaks the
// input side into cached vs actually-billed, since cache_read is a slice of
// input rather than an addition to it (see llm.Usage on the Go side).
const props = defineProps<{ usage: Usage }>();

const { t } = useI18n();

const open = ref(false);

const cachePct = computed(() => {
  const u = props.usage;
  if (!u.cache_read || !u.input) return 0;
  return Math.round((u.cache_read / u.input) * 100);
});

const fresh = computed(() =>
  Math.max(props.usage.input - (props.usage.cache_read ?? 0), 0),
);

const total = computed(() => props.usage.input + props.usage.output);

const visible = computed(() => total.value > 0);

// short() for the compact button — K/M keeps it from jittering the bar as
// numbers grow; the popover carries the exact grouped figures.
const short = (n: number) => {
  if (n >= 1_000_000)
    return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K`;
  return String(Math.round(n));
};

const grouped = (n: number) => n.toLocaleString("en-US");

const hint = computed(() =>
  t("usageMeter.hint", {
    input: grouped(props.usage.input),
    cachePct: cachePct.value,
    output: grouped(props.usage.output),
  }),
);
</script>

<template>
  <el-popover
    v-if="visible"
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="300"
    :teleported="false"
    popper-class="usage-pop"
  >
    <template #reference>
      <button class="usage-btn tip" :data-tip="open ? undefined : hint">
        <el-icon>
          <!-- ¥ coin with an outgoing arrow (花费金额-copy.svg); currentColor
               keeps it in step with the numbers beside it. -->
          <svg viewBox="0 0 1024 1024" fill="currentColor" aria-hidden="true">
            <path
              d="M504.8 956.9c-59.5-0.8-117.2-13.1-171.5-36.4-52.7-22.6-100-54.7-140.6-95.3-83.6-83.6-129.6-194.1-129.6-311.3 0-244.3 201-443 448-443 13.9 0 25.3 11.2 25.3 24.9s-11.3 24.9-25.3 24.9c-219.1 0-397.3 176.3-397.3 393 0 216.8 178.2 393.2 397.3 393.2 219 0 397.3-176.3 397.3-393 0-13.7 11.3-24.9 25.3-24.9 13.9 0 25.3 11.2 25.3 24.9 0 59.7-11.8 117.6-35.2 172.2-22.6 52.7-54.9 100.1-96.1 140.8-41.2 40.7-89.1 72.7-142.4 95.1-55.2 23.1-113.8 34.9-174.2 34.9h-6.3z"
            />
            <path
              d="M373.9 603.7c-13.8 0-25-11.2-25.1-25 0-13.7 11.3-24.9 25.3-24.9h274.1c13.9 0 25.3 11.2 25.3 24.9s-11.3 24.9-25.2 24.9H373.9z m0-131c-13.8 0-25.1-11.2-25.1-25 0-13.7 11.3-24.9 25.3-24.9h274.1c13.9 0 25.3 11.2 25.3 24.9s-11.3 24.9-25.2 24.9H373.9z"
            />
            <path
              d="M510.9 741.6c-15 0-27.3-12.2-27.4-27.2V443.2c0-15 12.3-27.2 27.5-27.2s27.5 12.2 27.5 27.2v271.1c0 15-12.3 27.3-27.5 27.3h-0.1z"
            />
            <path
              d="M511.1 450.1c-6.8 0-13.1-2.6-17.8-7.3L380 331c-4.7-4.6-7.4-11-7.4-17.6 0-6.6 2.7-13 7.4-17.6 4.8-4.7 11.2-7.3 18-7.3 6.8 0 13.2 2.6 18 7.4l113 111.8c4.7 4.6 7.4 11 7.4 17.6 0 6.6-2.7 13-7.4 17.6-4.4 4.4-10.2 7-16.3 7.3l-1.6-0.1z"
            />
            <path
              d="M511 450.1c-6.7 0-13-2.6-17.7-7.3-4.7-4.6-7.4-11.1-7.4-17.6 0-6.6 2.7-13 7.4-17.6l113-111.8c4.8-4.7 11.2-7.3 18-7.3 6.8 0 13.2 2.6 18 7.4 4.7 4.6 7.4 11 7.4 17.6 0 6.6-2.7 13-7.4 17.6L529 442.7c-4.8 4.7-11.1 7.3-17.7 7.3l-0.3 0.1zM933.6 232.6c-6.8 0-13.2-2.6-17.9-7.3l-113-111.9c-4.7-4.6-7.4-11-7.4-17.6 0-6.6 2.7-13 7.4-17.6 4.8-4.7 11.2-7.3 18-7.3 6.8 0 13.1 2.6 17.9 7.3l112.9 111.9c4.7 4.6 7.4 11 7.4 17.6 0 6.6-2.7 13-7.4 17.6-4.8 4.7-11.1 7.3-17.8 7.3h-0.1z"
            />
            <path
              d="M820.6 344.5c-6.8 0-13.2-2.6-17.9-7.3-4.7-4.6-7.4-11.1-7.4-17.6 0-6.6 2.7-13 7.4-17.6l113-111.8c4.8-4.7 11.2-7.4 18-7.4 6.8 0 13.2 2.6 18 7.4 4.7 4.6 7.4 11 7.4 17.6 0 6.6-2.7 13-7.4 17.6l-113 111.8c-5 4.7-11.4 7.3-18.1 7.3z"
            />
            <path
              d="M698.7 232.6c-13.8 0-25-11.2-25.1-24.9 0.1-13.7 11.4-24.9 25.1-24.9h228.7c13.8 0 25.1 11.2 25.1 24.9s-11.3 24.9-25.3 24.9H698.7z"
            />
          </svg>
        </el-icon>
        <span class="pair up">↑{{ short(usage.input) }}</span>
        <span class="pair down">↓{{ short(usage.output) }}</span>
      </button>
    </template>

    <div class="head">
      <span class="big">{{ short(total) }}</span>
      <span class="dim">{{ t("usageMeter.totalLabel") }}</span>
      <span v-if="cachePct" class="cache-tag">{{ t("usageMeter.cacheTag", { pct: cachePct }) }}</span>
    </div>

    <div class="rows">
      <div class="row">
        <span class="name">{{ t("usageMeter.rows.inputTotal") }}</span>
        <span class="val">{{ grouped(usage.input) }}</span>
      </div>
      <div v-if="usage.cache_read" class="row sub">
        <span class="name">{{ t("usageMeter.rows.cacheHit") }}</span>
        <span class="val cache">{{ grouped(usage.cache_read) }} <em>{{ cachePct }}%</em></span>
      </div>
      <div class="row" :class="{ sub: usage.cache_read }">
        <span class="name">{{ t("usageMeter.rows.billedInput") }}</span>
        <span class="val">{{ grouped(fresh) }}</span>
      </div>
      <div class="row">
        <span class="name">{{ t("usageMeter.rows.output") }}</span>
        <span class="val">{{ grouped(usage.output) }}</span>
      </div>
      <div v-if="usage.reasoning" class="row sub">
        <span class="name">{{ t("usageMeter.rows.reasoning") }}</span>
        <span class="val">{{ grouped(usage.reasoning) }}</span>
      </div>
    </div>

    <div class="foot">
      {{ t("usageMeter.foot") }}
    </div>
  </el-popover>
</template>

<style scoped lang="scss">
.usage-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 10px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  color: #000;
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 16px;
    color: #000;
  }

  &:hover {
    background: var(--el-fill-color-light);
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
  }
}

.pair {
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  color: #000;
}

/* teleported=false, so the popper is reachable from scoped styles via :deep. */
:deep(.usage-pop) {
  padding: 12px !important;
  border-radius: 10px !important;
}

.head {
  display: flex;
  align-items: baseline;
  gap: 4px;
  margin-bottom: 8px;

  .big {
    font-size: 18px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
  }

  .dim {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .cache-tag {
    margin-left: auto;
    font-size: 11px;
    padding: 1px 7px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    color: var(--el-color-success);
  }
}

.rows {
  display: flex;
  flex-direction: column;
}

.row {
  display: flex;
  align-items: center;
  padding: 5px 6px;
  font-size: 12px;

  .name {
    color: var(--el-text-color-primary);
  }

  &.sub .name {
    padding-left: 14px;
    color: var(--el-text-color-secondary);
  }

  .val {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);

    &.cache {
      color: var(--el-color-success);
    }

    em {
      font-style: normal;
      color: var(--el-text-color-secondary);
      margin-left: 4px;
    }
  }
}

.foot {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
  font-size: 11px;
  line-height: 1.5;
  color: var(--el-text-color-secondary);
}
</style>
