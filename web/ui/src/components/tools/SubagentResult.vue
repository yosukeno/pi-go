<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import CodeBlock from "../CodeBlock.vue";
import BashResult from "./BashResult.vue";
import EditResult from "./EditResult.vue";
import WriteResult from "./WriteResult.vue";
import {
  isBashDetails,
  isEditDetails,
  isReadDetails,
  isWriteDetails,
  subagentSteps,
  type SubagentStep,
} from "@/agent/timeline";
import type { SubagentFrame, ToolResult } from "@/api/types";

/**
 * A delegated run, shown as its own card.
 *
 * A subagent is not a tool call that happens to take a while: it is another agent
 * with its own turns, its own tool calls and its own ending. Rendering it as one
 * line of output would flatten all of that, which is why the parent forwards the
 * child's events rather than a summary of them.
 *
 * Collapsed is the default, and collapsed is not "hidden": it shows the newest
 * event, following along as the run progresses. That is the state a card spends
 * most of its life in — several delegations at once, none of which you are reading
 * closely until one goes wrong — so it has to be informative on its own.
 *
 * Expanded, each finished tool row opens into what the call actually produced,
 * rendered by the same components the parent timeline uses for its own results:
 * the child speaks the same event contract one level down, so nothing needs a
 * second implementation.
 */
const props = defineProps<{
  /** frames arrive while the call is running; the settled copy lives on the result. */
  frames?: SubagentFrame[];
  /** result is present once the delegation has finished. */
  result?: ToolResult;
  /** The delegation call's own arguments: mode is known from the first moment. */
  args?: unknown;
  details?: { mode?: string; model?: string; ref?: string; commit?: string; session?: string; turns?: number; tampered?: boolean };
  running: boolean;
}>();

const { t } = useI18n();

// Expanded is remembered per card rather than derived from `running`, so a card
// does not collapse itself out from under someone the moment the run finishes.
const expanded = ref(false);

// The projection lives in timeline.ts, which is deliberately Vue-free so it can be
// unit tested without a component. This one has already been wrong once — see the
// note on subagentSteps — so it is the last thing that should be trapped in an SFC.
const steps = computed(() => subagentSteps(props.frames));
const latest = computed<SubagentStep | undefined>(() => steps.value[steps.value.length - 1]);

const childSession = computed(
  () => props.details?.session ?? (props.frames ?? []).find((f) => f.session)?.session,
);

// Mode is in the call's arguments, so the header can say it from the first
// moment; waiting for the settled details would leave the chip missing for the
// whole run. The details value wins once it exists, as the authoritative one.
const mode = computed(() => props.details?.mode ?? (props.args as { mode?: string } | undefined)?.mode);

// The model arrives with the child's session header — the first frame it sends —
// and the settled details repeat it. A running card should say what it is
// running on, not only what it ran on.
const model = computed(() => props.details?.model ?? (props.frames ?? []).find((f) => f.model)?.model);

const failed = computed(() => props.result?.is_error === true);

// Turns and tools are the two numbers that say what a delegation did; the run's
// own count wins once it exists, because frame capping can drop early turn rows.
const stepTurns = computed(() => steps.value.filter((s) => s.kind === "turn").length);
const toolCount = computed(() => steps.value.filter((s) => s.kind === "tool").length);

// A finished row opens on click, keyed by call id rather than index so the set
// survives new rows arriving. Running rows stay shut: there is nothing to show
// yet, and a row that opened itself on tool_end would yank the list around.
const openRows = ref<Set<string>>(new Set());
const canOpen = (s: SubagentStep) =>
  s.kind === "tool" && s.done === true && s.callId != null && (s.text !== undefined || s.details !== undefined);
function toggleRow(s: SubagentStep) {
  if (!canOpen(s)) return;
  const next = new Set(openRows.value);
  if (next.has(s.callId!)) next.delete(s.callId!);
  else next.add(s.callId!);
  openRows.value = next;
}

// resultOf assembles the ToolResult a child's settled call produced, so the
// result components the parent timeline uses render it without a second shape.
function resultOf(s: SubagentStep): ToolResult {
  return { call_id: s.callId ?? "", name: s.name as ToolResult["name"], text: s.text ?? "", is_error: s.bad || undefined, details: s.details };
}

// The collapsed strip follows the newest step, and only the newest: this is the
// state where nobody is reading it closely, so it should always be showing the
// thing that just happened.
const stripEl = ref<HTMLElement | null>(null);
watch(
  () => steps.value.length,
  async () => {
    if (expanded.value) return;
    await nextTick();
    const el = stripEl.value;
    if (el) el.scrollTop = el.scrollHeight;
  },
);

// Expanded, the same rule as a terminal: follow the tail unless the reader has
// scrolled away from it, because undoing someone's scroll on every new event is how
// an auto-scrolling log becomes unusable.
const listEl = ref<HTMLElement | null>(null);
watch(
  () => steps.value.length,
  async () => {
    if (!expanded.value) return;
    const el = listEl.value;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (!atBottom) return;
    await nextTick();
    el.scrollTop = el.scrollHeight;
  },
);
</script>

<template>
  <div class="sub" :class="{ running, failed }">
    <div class="head" @click="expanded = !expanded">
      <span class="caret">{{ expanded ? "▾" : "▸" }}</span>
      <span class="title">subagent</span>
      <span v-if="mode" class="chip">{{ mode }}</span>
      <span v-if="model" class="chip dim">{{ model }}</span>
      <span v-if="details?.tampered" class="chip bad">{{ t("subagentResult.tampered") }}</span>
      <span v-else-if="details?.commit" class="chip ok">{{ t("subagentResult.committed") }}</span>
      <span class="count">{{ t("subagentResult.stats", { turns: details?.turns ?? stepTurns, tools: toolCount }) }}</span>
    </div>

    <!-- Collapsed: the newest event, scrolled to. Not a summary and not nothing —
         the one line that says where the delegation is right now. -->
    <div v-if="!expanded" class="strip" ref="stripEl">
      <div v-if="latest" class="step" :class="[latest.kind, { bad: latest.bad }]">
        <span class="kind">{{ latest.kind === "tool" ? "·" : "—" }}</span>
        <span class="label">{{ latest.label }}</span>
        <span v-if="latest.summary" class="detail">{{ latest.summary }}</span>
        <span v-else-if="latest.detail" class="detail">{{ latest.detail }}</span>
      </div>
      <div v-else-if="running" class="step muted">{{ t("subagentResult.starting") }}</div>
      <div v-else-if="result" class="step muted">{{ t("subagentResult.done") }}</div>
    </div>

    <div v-else class="body">
      <div class="list" ref="listEl">
        <template v-for="(s, i) in steps" :key="i">
          <template v-if="s.kind === 'tool'">
            <div class="step tool" :class="{ bad: s.bad, clickable: canOpen(s) }" @click="toggleRow(s)">
              <span class="kind">{{ canOpen(s) ? (openRows.has(s.callId!) ? "▾" : "▸") : "·" }}</span>
              <span class="label">{{ s.label }}</span>
              <span v-if="s.summary" class="detail" :title="s.summary">{{ s.summary }}</span>
              <span v-if="!s.done" class="badge run">{{ t("subagentResult.running") }}</span>
              <span v-else-if="s.bad" class="badge bad">{{ t("subagentResult.failed") }}</span>
            </div>
            <div v-if="openRows.has(s.callId!)" class="row-result">
              <div v-if="s.bad" class="error-text">{{ s.text }}</div>
              <template v-else-if="s.name === 'read' && isReadDetails(s.details)">
                <!-- ReadResult without its continue button: that suggestion fills
                     the parent's input, and the path belongs to the child's
                     worktree, where the parent cannot read it. -->
                <div class="meta">
                  <span>{{ t("subagentResult.lines", { n: s.details.total_lines }) }}</span>
                  <span v-if="s.details.truncated" class="warn">
                    {{
                      t("subagentResult.truncated", {
                        first: s.details.first_line,
                        last: s.details.first_line + s.details.shown_lines - 1,
                        by: t(s.details.truncated_by === "bytes" ? "subagentResult.byBytes" : "subagentResult.byLines"),
                      })
                    }}
                  </span>
                </div>
                <CodeBlock
                  :code="s.text ?? ''"
                  :lang="s.details.path.split('.').pop() ?? ''"
                  line-numbers
                  :start-line="s.details.first_line"
                />
              </template>
              <BashResult v-else-if="isBashDetails(s.details)" :result="resultOf(s)" :details="s.details" />
              <EditResult v-else-if="isEditDetails(s.details)" :details="s.details" />
              <WriteResult v-else-if="isWriteDetails(s.details)" :result="resultOf(s)" :details="s.details" />
              <CodeBlock v-else :code="s.text ?? ''" />
            </div>
          </template>
          <div v-else class="step" :class="[s.kind, { bad: s.bad }]">
            <span class="kind">—</span>
            <span class="label">{{ s.label }}</span>
            <span v-if="s.detail" class="detail">{{ s.detail }}</span>
          </div>
        </template>
        <div v-if="!steps.length" class="step muted">{{ t("subagentResult.noEvents") }}</div>
      </div>

      <!-- The answer, once there is one. This is the only part that entered the
           parent's conversation; everything above stayed in the child. -->
      <CodeBlock v-if="result && !failed" :code="result.text" />
      <div v-else-if="failed" class="error-text">{{ result?.text }}</div>

      <div class="foot">
        <span v-if="details?.turns">{{ t("subagentResult.turns", { n: details.turns }) }}</span>
        <code v-if="details?.ref" class="ref">git show {{ details.ref }}</code>
        <!-- The transcript path, because the whole point of delegation is that the
             detail did not come back here, and a person still has to be able to read it. -->
        <code v-if="childSession" class="ref">pi-go -analyze-session {{ childSession }}</code>
      </div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.sub {
  border: 1px solid var(--el-border-color);
  border-left: 2px solid var(--el-color-info);
  border-radius: 4px;
  margin: 4px 0;
  font-size: 12px;

  &.running {
    border-left-color: var(--el-color-primary);
  }

  &.failed {
    border-left-color: var(--el-color-danger);
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 8px;
  cursor: pointer;
  user-select: none;

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.caret {
  color: var(--el-text-color-secondary);
  width: 10px;
}

.title {
  font-family: ui-monospace, monospace;
  font-weight: 600;
  color: var(--el-color-info);
}

.chip {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--el-fill-color);
  color: var(--el-text-color-secondary);

  &.dim {
    opacity: 0.7;
  }

  &.ok {
    background: color-mix(in srgb, var(--el-color-success) 18%, transparent);
    color: var(--el-color-success);
  }

  &.bad {
    background: color-mix(in srgb, var(--el-color-danger) 15%, transparent);
    color: var(--el-color-danger);
  }
}

.count {
  margin-left: auto;
  color: var(--el-text-color-secondary);
  font-size: 10px;
}

/* One line tall on purpose: collapsed means "tell me where it is", not "show me
   some of it". Scrolling rather than replacing keeps the transition from one event
   to the next legible instead of making the row flicker. */
.strip {
  height: 20px;
  overflow: hidden;
  padding: 0 8px 4px 26px;
  scroll-behavior: smooth;
}

.body {
  padding: 0 8px 8px 26px;
}

/* Taller than a plain event log needs, because rows now open into file contents
   and diffs: 220px was sized for one-line rows, and a read inside it is a slit. */
.list {
  max-height: 420px;
  overflow: auto;
  border-left: 1px dashed var(--el-border-color);
  padding-left: 8px;
  margin-bottom: 6px;
}

.step {
  display: flex;
  gap: 6px;
  align-items: baseline;
  line-height: 20px;
  font-family: ui-monospace, monospace;
  white-space: nowrap;

  &.turn,
  &.end {
    color: var(--el-text-color-secondary);
  }

  &.bad .label {
    color: var(--el-color-danger);
  }

  &.clickable {
    cursor: pointer;
    border-radius: 3px;

    &:hover {
      background: var(--el-fill-color-light);
    }
  }

  &.muted {
    color: var(--el-text-color-secondary);
  }
}

.kind {
  color: var(--el-text-color-secondary);
  flex: 0 0 auto;
}

.detail {
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
}

.badge {
  margin-left: auto;
  font-size: 10px;
  padding: 0 5px;
  border-radius: 3px;
  flex: 0 0 auto;

  &.run {
    background: color-mix(in srgb, var(--el-color-primary) 14%, transparent);
    color: var(--el-color-primary);
    animation: rowpulse 1s ease-in-out infinite;
  }

  &.bad {
    background: color-mix(in srgb, var(--el-color-danger) 15%, transparent);
    color: var(--el-color-danger);
  }
}

@keyframes rowpulse {
  50% {
    opacity: 0.4;
  }
}

.row-result {
  margin: 2px 0 8px 16px;
}

.meta {
  display: flex;
  gap: 10px;
  font-size: 11px;
  font-family: inherit;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
  white-space: normal;

  .warn {
    color: var(--el-color-warning);
  }
}

.foot {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  color: var(--el-text-color-secondary);
  font-size: 10px;
}

.ref {
  font-family: ui-monospace, monospace;
  background: var(--el-fill-color);
  padding: 1px 4px;
  border-radius: 3px;
  user-select: all;
}

.error-text {
  color: var(--el-color-danger);
  background: color-mix(in srgb, var(--el-color-danger) 7%, transparent);
  border-radius: 4px;
  padding: 6px 8px;
  margin-bottom: 6px;
  white-space: pre-wrap;
}
</style>
