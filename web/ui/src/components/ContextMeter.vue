<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowRight } from "@element-plus/icons-vue";
import { chooseLever, contextLevel, createContextEstimator } from "@/agent/contextEstimate";
import type { Message, ToolResult } from "@/api/types";

// Three layers now. The button answers "how much runway is left" at a glance; the
// popover answers "what is eating it"; and the footer answers "so what do I do",
// which the breakdown is the only thing in the app able to decide.
//
// That last layer is why the breakdown earns its complexity. Old tool output is
// dropped from the prompt automatically and can be fetched again, so a
// tools-dominated context is already being managed and compaction would buy
// little. Conversation text is the shape nothing handles by itself, and the one
// compaction exists for. Same numbers, opposite advice — see `lever`.
//
// The absolute number is always the server's measured prompt size; character
// estimates only split it up.
const props = defineProps<{
  /** used is the latest turn's prompt size, not the session total. */
  used: number;
  /** window is 0 for a model that is not in the catalog; the meter hides itself. */
  window: number;
  /** overhead is the server's fixed-cost estimate (system prompt + tool schemas). */
  overhead: number;
  messages: Message[];
  results: Record<string, ToolResult>;
  /**
   * clearTrigger is the prompt size at which the server starts dropping old tool
   * results, 0 when clearing is off. The warning bands are measured against it — see
   * contextLevel for why fractions of the window are the wrong thing to use.
   */
  clearTrigger?: number;
  /** disabled while a run is in flight — compacting mid-run returns 409. */
  disabled?: boolean;
  /** compacting is true for the length of the summarising call, which is a turn. */
  compacting?: boolean;
}>();

const emit = defineEmits<{ compact: [] }>();

const { t } = useI18n();

const open = ref(false);
const expanded = reactive(new Set<string>());

// Settled messages and results are immutable (the stream appends, never edits
// in place), so the per-object cache turns each event's transcript rescan into
// lookups for everything but the newcomer.
const estimator = createContextEstimator();

interface Item {
  name: string;
  tokens: number;
}
interface Cat {
  key: string;
  label: string;
  color: string;
  tokens: number;
  note?: string;
  items?: Item[];
  moreCount?: number;
}

const COLORS = {
  overhead: "#7c6cf0",
  user: "#3b82f6",
  assistant: "#10b981",
  tools: "#f59e0b",
};

function preview(text: string): string {
  const oneLine = text.replace(/\s+/g, " ").trim();
  return oneLine.length > 40 ? oneLine.slice(0, 40) + "…" : oneLine;
}

function top5(items: Item[]): { items: Item[]; more: number } {
  items.sort((a, b) => b.tokens - a.tokens);
  return { items: items.slice(0, 5), more: Math.max(0, items.length - 5) };
}

// Raw character estimates per category. Thinking blocks are excluded on
// purpose: they are not replayed to the model, so they occupy nothing.
const raw = computed(() => {
  let user = 0;
  let assistant = 0;
  let tools = 0;
  const userItems: Item[] = [];
  const asstItems: Item[] = [];
  const toolItems: Item[] = [];

  for (const m of props.messages) {
    const { tokens, first } = estimator.ofMessage(m);
    if (!tokens) continue;
    const item: Item = { name: preview(first) || t("contextMeter.toolCallFallback"), tokens };
    if (m.role === "user") {
      user += tokens;
      userItems.push(item);
    } else {
      assistant += tokens;
      asstItems.push(item);
    }
  }

  for (const r of Object.values(props.results)) {
    const tokens = estimator.ofResult(r);
    if (!tokens) continue;
    tools += tokens;
    toolItems.push({ name: r.name ?? "tool", tokens });
  }

  return { user, assistant, tools, userItems, asstItems, toolItems };
});

// The measured total is the authority. Estimates are normalised so the slices
// sum to it: proportions from characters, magnitude from the server.
const cats = computed<Cat[]>(() => {
  const overhead = Math.max(0, props.overhead);
  const r = raw.value;
  const convEst = r.user + r.assistant + r.tools;
  // No measurement yet (fresh session, or a reload before the first run):
  // fall back to raw estimates for everything.
  const convMeasured =
    props.used > 0 ? Math.max(props.used - overhead, 0) : convEst;
  const scale = convEst > 0 ? convMeasured / convEst : 0;
  const at = (n: number) => Math.round(n * scale);

  return [
    {
      key: "overhead",
      label: t("contextMeter.cats.overhead"),
      color: COLORS.overhead,
      tokens: overhead,
      note: t("contextMeter.overheadNote"),
    },
    {
      key: "user",
      label: t("contextMeter.cats.user"),
      color: COLORS.user,
      tokens: at(r.user),
      ...itemsOf(r.userItems),
    },
    {
      key: "assistant",
      label: t("contextMeter.cats.assistant"),
      color: COLORS.assistant,
      tokens: at(r.assistant),
      ...itemsOf(r.asstItems),
    },
    {
      key: "tools",
      label: t("contextMeter.cats.tools"),
      color: COLORS.tools,
      tokens: at(r.tools),
      ...itemsOf(r.toolItems),
    },
  ];
});

function itemsOf(items: Item[]): { items?: Item[]; moreCount?: number } {
  if (!items.length) return {};
  const { items: top, more } = top5(items);
  return { items: top, moreCount: more };
}

const total = computed(() =>
  props.used > 0
    ? Math.max(props.used, props.overhead)
    : cats.value.reduce((a, c) => a + c.tokens, 0),
);

const percent = computed(() => {
  if (!props.window || !total.value) return 0;
  return Math.min(100, (total.value / props.window) * 100);
});

const free = computed(() => Math.max(props.window - total.value, 0));

const level = computed(() => contextLevel(total.value, props.window, props.clearTrigger));

// The button shows only the icon and the bar, so the numbers live in the
// tooltip; the breakdown is a click away.
// With clearing on, amber means the mechanism has engaged and is holding — not that
// something is wrong. Saying otherwise would train people to ignore it, since that is
// where a busy session normally sits.
const hint = computed(() => {
  const nums = `${total.value ? short(total.value) : "—"}/${short(props.window)} · ${Math.round(percent.value)}%`;
  const clearing = (props.clearTrigger ?? 0) > 0;
  if (level.value === "high")
    return clearing
      ? t("contextMeter.hint.highClearing", { nums })
      : t("contextMeter.hint.high", { nums });
  if (level.value === "mid")
    return clearing
      ? t("contextMeter.hint.midClearing", { nums })
      : t("contextMeter.hint.mid", { nums });
  return t("contextMeter.hint.low", { nums });
});

// Which mechanism this shape calls for. Extracted rather than inlined because it is
// the one piece of judgement on this panel, and advice that is wrong is worse than
// no advice — see chooseLever for the asymmetry it encodes.
const lever = computed(() => {
  const r = raw.value;
  return chooseLever(r.user, r.assistant, r.tools);
});

// An empty session has nothing a summary could replace: the button says so
// instead of offering a no-op that would still spend a model call.
const empty = computed(() => props.messages.length === 0);

function askCompact() {
  if (props.disabled || props.compacting || empty.value) return;
  open.value = false;
  emit("compact");
}

// Segment widths are shares of the window, not of the used part: the free
// space is the point of the bar.
const segments = computed(() =>
  cats.value
    .filter((c) => c.tokens > 0)
    .map((c) => ({
      ...c,
      w: props.window ? Math.max((c.tokens / props.window) * 100, 0.8) : 0,
    })),
);

const freeW = computed(() =>
  props.window ? Math.max((free.value / props.window) * 100, 0) : 0,
);

function toggle(key: string, hasItems: boolean) {
  if (!hasItems) return;
  if (expanded.has(key)) expanded.delete(key);
  else expanded.add(key);
}

const pctOf = (tokens: number) => {
  if (!props.window || !tokens) return "0%";
  const p = (tokens / props.window) * 100;
  return (p < 10 ? p.toFixed(1) : p.toFixed(0)) + "%";
};

const short = (n: number) => {
  if (n >= 1_000_000)
    return `${(n / 1_000_000).toFixed(n % 1_000_000 === 0 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 100_000 ? 0 : 1)}K`;
  return String(Math.round(n));
};
</script>

<template>
  <el-popover
    v-if="window"
    v-model:visible="open"
    placement="top-start"
    trigger="click"
    :width="340"
    :teleported="false"
    popper-class="ctx-pop"
  >
    <template #reference>
      <button class="meter-btn tip" :class="level" :data-tip="open ? undefined : hint">
        <el-icon>
          <!-- Stacked-layers context icon (~/Downloads/上下文.svg);
               currentColor keeps it in step with the bar beside it. -->
          <svg viewBox="0 0 1024 1024" fill="currentColor" aria-hidden="true">
            <path
              d="M909.0048 797.2352a32 32 0 1 0-26.0096-58.4704l26.0096 58.4704z m-768-58.4704a32 32 0 1 0-26.0096 58.4704l26.0096-58.4704z m768-154.88a32 32 0 0 0-26.0096-58.4704l26.0096 58.4704z m-768-58.4704a32 32 0 1 0-26.0096 58.4704l26.0096-58.4704z m6.3488-159.9488l312.1664 156.0576 28.672-57.2416L175.9232 308.224l-28.672 57.2416z m417.1264 156.0576l312.1664-156.0576-28.6208-57.2416-312.1664 156.0576 28.6208 57.2416z m312.1664-289.6384L564.48 75.776l-28.672 57.2416 312.2176 156.1088 28.672-57.2416zM459.52 75.776L147.3536 231.936l28.6208 57.2416 312.1664-156.1088-28.6208-57.2416z m104.96 0a117.3504 117.3504 0 0 0-104.96 0l28.672 57.2416c14.9504-7.4752 32.6656-7.4752 47.616 0l28.672-57.2416z m312.1664 289.6896c55.04-27.5456 55.04-106.0864 0-133.632l-28.6208 57.2928a10.6496 10.6496 0 0 1 0 19.0976l28.672 57.2416z m-417.1264 156.0576a117.2992 117.2992 0 0 0 104.96 0l-28.672-57.2416a53.3504 53.3504 0 0 1-47.616 0l-28.672 57.2416zM175.9744 308.224a10.6496 10.6496 0 0 1 0-19.0976l-28.672-57.2416c-54.9888 27.4944-54.9888 106.0352 0 133.5808l28.672-57.2416z m383.6928 644.3008l349.3376-155.2896-26.0096-58.4704-349.3376 155.2384 26.0096 58.5216zM114.9952 797.184l349.3376 155.2896 26.0096-58.5216-349.3376-155.2384-26.0096 58.4704z m418.6624 96.768c-13.824 6.144-29.5424 6.144-43.3152 0l-26.0096 58.5216a117.2992 117.2992 0 0 0 95.3344 0l-26.0096-58.5216z m26.0096-154.8288l349.3376-155.2896-26.0096-58.4704-349.3376 155.2896 26.0096 58.4704z m-444.672-155.2896l349.3376 155.2896 26.0096-58.4704-349.3376-155.2896-26.0096 58.4704z m418.6624 96.768c-13.824 6.144-29.5424 6.144-43.3152 0l-26.0096 58.5216a117.3504 117.3504 0 0 0 95.3344 0l-26.0096-58.4704z"
            />
          </svg>
        </el-icon>
        <span class="mini">
          <span
            class="seg oh"
            :style="{ width: `${window ? Math.min((overhead / window) * 100, 100) : 0}%` }"
          />
          <span class="seg conv" :style="{ width: `${Math.max(percent - (overhead / window) * 100, 0)}%` }" />
        </span>
      </button>
    </template>

    <div class="head">
      <span class="big">{{ short(total) }}</span>
      <span class="dim">/ {{ short(window) }}</span>
      <span class="pct-tag" :class="level">{{ Math.round(percent) }}%</span>
    </div>

    <div class="stack">
      <div
        v-for="seg in segments"
        :key="seg.key"
        class="seg"
        :style="{ width: `${seg.w}%`, background: seg.color }"
        :title="t('contextMeter.segTitle', { label: seg.label, tokens: short(seg.tokens), pct: pctOf(seg.tokens) })"
      />
      <div
        v-if="freeW > 0"
        class="seg free"
        :style="{ width: `${freeW}%` }"
        :title="t('contextMeter.freeSegTitle', { tokens: short(free), pct: pctOf(free) })"
      />
    </div>

    <div class="rows">
      <template v-for="c in cats" :key="c.key">
        <div
          class="row"
          :class="{ clickable: c.items?.length }"
          @click="toggle(c.key, !!c.items?.length)"
        >
          <span class="dot" :style="{ background: c.color }" />
          <span class="name">{{ c.label }}</span>
          <span class="val">~{{ short(c.tokens) }} <em>{{ pctOf(c.tokens) }}</em></span>
          <el-icon v-if="c.items?.length" class="chev" :class="{ down: expanded.has(c.key) }">
            <ArrowRight />
          </el-icon>
        </div>
        <div v-if="c.note" class="note">{{ c.note }}</div>
        <div v-if="c.items?.length && expanded.has(c.key)" class="items">
          <div v-for="(it, i) in c.items" :key="i" class="item">
            <span class="iname" :title="it.name">{{ it.name }}</span>
            <span class="itok">~{{ short(it.tokens) }}</span>
          </div>
          <div v-if="c.moreCount" class="more">{{ t("contextMeter.moreItems", { n: c.moreCount }) }}</div>
        </div>
      </template>
      <div class="row">
        <span class="dot hollow" />
        <span class="name">{{ t("contextMeter.freeSpace") }}</span>
        <span class="val">{{ short(free) }} <em>{{ pctOf(free) }}</em></span>
      </div>
    </div>

    <div class="act">
      <!-- The advice comes before the button, because which lever to pull is the
           question and the button is only one of the two answers. -->
      <p v-if="lever" class="advice" :class="lever.kind">
        <template v-if="lever.kind === 'tools'">
          {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.toolWord") }}</strong>{{ t("contextMeter.advice.toolsPost", { share: lever.share }) }}
        </template>
        <template v-else>
          {{ t("contextMeter.advice.pre") }}<strong>{{ t("contextMeter.advice.convWord") }}</strong>{{ t("contextMeter.advice.convPost", { share: lever.share }) }}
        </template>
      </p>
      <button
        class="compact-btn"
        :class="{ suggested: !empty && lever?.kind === 'conversation' }"
        :disabled="disabled || compacting || empty"
        :title="
          disabled
            ? t('contextMeter.compact.disabledTitle')
            : empty
              ? t('contextMeter.compact.emptyTitle')
              : t('contextMeter.compact.title')
        "
        @click="askCompact"
      >
        {{
          compacting
            ? t("contextMeter.compact.compacting")
            : empty
              ? t("contextMeter.compact.emptyLabel")
              : t("contextMeter.compact.label")
        }}
      </button>
    </div>

    <div class="foot">
      {{ t("contextMeter.foot") }}
    </div>
  </el-popover>
</template>

<style scoped lang="scss">
.meter-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 30px;
  padding: 0 10px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  color: var(--el-text-color-primary);
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

.mini {
  position: relative;
  width: 48px;
  height: 4px;
  border-radius: 2px;
  background: var(--el-fill-color-dark);
  overflow: hidden;
  display: inline-flex;

  .seg {
    height: 100%;

    &.oh {
      background: var(--el-text-color-placeholder);
    }

    &.conv {
      background: var(--el-color-success);
    }
  }
}

.mid .mini .seg.conv {
  background: var(--el-color-warning);
}

.high .mini .seg.conv {
  background: var(--el-color-danger);
}

/* teleported=false, so the popper is reachable from scoped styles via :deep. */
:deep(.ctx-pop) {
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
    font-variant-numeric: tabular-nums;
  }

  .pct-tag {
    margin-left: auto;
    font-size: 11px;
    padding: 1px 7px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    color: var(--el-color-success);

    &.mid {
      background: color-mix(in srgb, var(--el-color-warning) 14%, transparent);
      color: var(--el-color-warning);
    }

    &.high {
      background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
      color: var(--el-color-danger);
    }
  }
}

.stack {
  display: flex;
  height: 10px;
  border-radius: 5px;
  overflow: hidden;
  background: var(--el-fill-color-light);
  margin-bottom: 10px;

  .seg {
    height: 100%;

    &.free {
      background: repeating-linear-gradient(
        -45deg,
        transparent 0 3px,
        var(--el-fill-color) 3px 6px
      );
    }
  }
}

.rows {
  display: flex;
  flex-direction: column;
}

.row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 6px;
  border-radius: 5px;
  font-size: 12px;

  &.clickable {
    cursor: pointer;

    &:hover {
      background: var(--el-fill-color-light);
    }
  }

  .dot {
    flex: none;
    width: 8px;
    height: 8px;
    border-radius: 50%;

    &.hollow {
      border: 1.5px solid var(--el-text-color-placeholder);
      background: transparent;
    }
  }

  .name {
    color: var(--el-text-color-primary);
  }

  .val {
    margin-left: auto;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-primary);

    em {
      font-style: normal;
      color: var(--el-text-color-secondary);
      margin-left: 4px;
    }
  }

  .chev {
    flex: none;
    color: var(--el-text-color-secondary);
    transition: transform 0.2s;

    &.down {
      transform: rotate(90deg);
    }
  }
}

.note {
  padding: 0 6px 4px 22px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.items {
  padding: 0 6px 4px 22px;

  .item {
    display: flex;
    align-items: baseline;
    gap: 8px;
    padding: 2px 0;
    font-size: 11px;
  }

  .iname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-text-color-regular);
  }

  .itok {
    flex: none;
    font-variant-numeric: tabular-nums;
    color: var(--el-text-color-secondary);
  }

  .more {
    padding-top: 2px;
    font-size: 11px;
    color: var(--el-text-color-placeholder);
  }
}

.act {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.advice {
  margin: 0 0 8px;
  font-size: 11px;
  line-height: 1.6;
  color: var(--el-text-color-regular);

  strong {
    font-weight: 600;
    color: var(--el-text-color-primary);
  }
}

.compact-btn {
  width: 100%;
  height: 28px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: transparent;
  font-size: 12px;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s;

  &:hover:not(:disabled) {
    background: var(--el-fill-color-light);
  }

  /* Suggested only when the breakdown says so: a button that always looks like
     the recommended action stops carrying information. */
  &.suggested {
    border-color: var(--el-color-primary);
    color: var(--el-color-primary);
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }

  &:focus-visible {
    outline: 2px solid var(--el-color-primary);
    outline-offset: 1px;
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
