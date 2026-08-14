<script setup lang="ts">
import { computed, nextTick, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import AnsiText from "./AnsiText.vue";
import BashResult from "./tools/BashResult.vue";
import EditResult from "./tools/EditResult.vue";
import LsResult from "./tools/LsResult.vue";
import ReadManyResult from "./tools/ReadManyResult.vue";
import ReadResult from "./tools/ReadResult.vue";
import SearchResult from "./tools/SearchResult.vue";
import SubagentResult from "./tools/SubagentResult.vue";
import TodoResult from "./tools/TodoResult.vue";
import WriteResult from "./tools/WriteResult.vue";
import CodeBlock from "./CodeBlock.vue";
import GateCard from "./GateCard.vue";
import {
  isBashDetails,
  isEditDetails,
  isLsDetails,
  isReadDetails,
  isReadManyDetails,
  isSearchDetails,
  isSubagentDetails,
  isTodoDetails,
  isWriteDetails,
  matchSkillRead,
  summarizeArgs,
  type TimelineCall,
} from "@/agent/timeline";

const props = defineProps<{
  call: TimelineCall;
  runActive: boolean;
  skills?: { name: string; path: string }[];
  cwd?: string;
  /** See TurnCard's prop of the same name; passed straight through to TodoResult. */
  todoPinned?: boolean;
}>();

const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string];
  thaw: [string];
}>();

const { t } = useI18n();

const label = computed(() => summarizeArgs(props.call.name, props.call.args));
const failed = computed(() => props.call.result?.is_error === true);
const isSubagent = computed(() => props.call.name === "subagent");

const state = computed(() => {
  if (props.call.gate) return "gate";
  if (props.call.running) return "running";
  if (props.call.orphaned) return "orphaned";
  return failed.value ? "error" : "done";
});

const details = computed(() => props.call.result?.details);

// A read that lands on a SKILL.md is the agent loading a skill. Labelling the
// read itself, rather than emitting a separate event, keeps one fact in one place
// on the timeline.
const skillName = computed(() =>
  matchSkillRead(props.call.name, props.call.args, props.skills ?? [], props.cwd ?? ""),
);

// Live output is a terminal: new lines belong at the bottom and the view should
// follow them. Only while it is the newest content, though — scrolling up to read
// something must not be undone on the next fragment, which is the usual way an
// auto-scrolling log becomes unusable.
const liveEl = ref<HTMLElement | null>(null);
watch(
  () => props.call.liveOutput,
  async () => {
    const el = liveEl.value;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    if (!atBottom) return;
    await nextTick();
    el.scrollTop = el.scrollHeight;
  },
);
</script>

<template>
  <div class="call" :class="state">
    <div class="line">
      <span class="dot" />
      <span class="name">{{ call.name }}</span>
      <span class="args">{{ label }}</span>
      <span v-if="skillName" class="badge skill">skill {{ skillName }}</span>
      <span v-if="state === 'running'" class="badge">{{ t("toolCall.running") }}</span>
      <span v-else-if="state === 'gate'" class="badge warn">{{ t("toolCall.waitingApproval") }}</span>
      <span v-else-if="state === 'orphaned'" class="badge">{{ t("toolCall.orphaned") }}</span>
      <span v-else-if="failed" class="badge bad">{{ t("toolCall.failed") }}</span>
    </div>

    <!-- The correction hint is a heuristic (same tool, same target, succeeded
         after a failure), so it stays a soft note rather than a claim. -->
    <div v-if="call.corrects" class="corrects">↳ {{ t("toolCall.correctsHint") }}</div>

    <!-- A subagent owns the whole body, running or settled: it is another agent's
         run, not output, so it does not switch representation halfway through the
         way a command does. -->
    <div v-if="isSubagent" class="body">
      <SubagentResult
        :frames="call.liveFrames"
        :result="call.result"
        :args="call.args"
        :details="isSubagentDetails(details) ? details : undefined"
        :running="call.running"
      />
    </div>

    <!-- Output from a command that is still running. Replaced by the settled
         result on tool_end, so nothing is rendered twice. AnsiText honours the
         command's own colours, same as the settled BashResult does. -->
    <div v-else-if="call.liveOutput" class="body">
      <div class="live" ref="liveEl"><AnsiText :text="call.liveOutput" wrap /></div>
    </div>

    <!-- The gate still shows for a subagent: delegating is itself a call that may
         need approval, and hiding the card would leave nothing to approve with. -->
    <div v-if="call.gate" class="body">
      <GateCard
        :gate="call.gate"
        :live="runActive"
        @decide="emit('decide', { gateId: call.gate!.gate_id, ...$event })"
        @freeze="emit('freeze', call.gate!.gate_id)"
        @thaw="emit('thaw', call.gate!.gate_id)"
      />
    </div>

    <!-- A subagent's own card renders its result, including the failure, so these
         two branches skip it rather than showing it a second time. -->
    <div v-else-if="failed && !isSubagent" class="body">
      <div class="error-text">{{ call.result?.text }}</div>
      <!-- A failure can still carry details: a non-zero exit code comes with the
           command's output, which is exactly what you need to see. -->
      <BashResult v-if="isBashDetails(details)" :result="call.result!" :details="details" />
    </div>

    <div v-else-if="call.result && !isSubagent" class="body">
      <ReadResult
        v-if="call.name === 'read' && isReadDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <!-- After the single-file branch, matching the guards' own discrimination:
           ReadManyDetails deliberately has no total_lines, so the two cannot both
           match and the order is a statement of intent rather than a dependency. -->
      <ReadManyResult
        v-else-if="call.name === 'read' && isReadManyDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <LsResult
        v-else-if="call.name === 'ls' && isLsDetails(details)"
        :result="call.result"
        :details="details"
        @suggest="emit('suggest', $event)"
      />
      <SearchResult
        v-else-if="(call.name === 'find' || call.name === 'grep') && isSearchDetails(details)"
        :result="call.result"
        :details="details"
        :kind="call.name"
        @suggest="emit('suggest', $event)"
      />
      <EditResult v-else-if="call.name === 'edit' && isEditDetails(details)" :details="details" />
      <WriteResult v-else-if="call.name === 'write' && isWriteDetails(details)" :result="call.result" :details="details" />
      <BashResult v-else-if="call.name === 'bash' && isBashDetails(details)" :result="call.result" :details="details" />
      <TodoResult
        v-else-if="call.name === 'todo' && isTodoDetails(details)"
        :details="details"
        :superseded="call.superseded"
        :pinned="todoPinned"
      />
      <CodeBlock v-else :code="call.result.text" />
    </div>
  </div>
</template>

<style scoped lang="scss">
.call {
  margin: 7px 0;
}

/* flex-start so a wrapped multi-line command keeps the dot, name and badges on
   its first line instead of centring against the whole block. */
.line {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12px;
  line-height: 1.5;
}

.dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--el-color-success);
  flex: 0 0 auto;
  /* Optically centred on the first text line (12px × 1.5 ≈ 18px). */
  margin-top: 6px;

  .running & {
    background: var(--el-color-primary);
  }
}

.running .dot {
  animation: pulse 1s ease-in-out infinite;
}

.gate .dot {
  background: var(--el-color-warning);
}

.error .dot,
.orphaned .dot {
  background: var(--el-color-danger);
}

@keyframes pulse {
  50% {
    opacity: 0.25;
  }
}

.name {
  font-family: var(--pg-mono);
  font-weight: 600;
  color: var(--el-text-color-primary);
}

/* Full text, wrapped, never elided: a truncated command hides what will run
   (or what ran), and the one line it saves is not worth that. */
.args {
  font-family: var(--pg-mono);
  color: var(--el-text-color-secondary);
  white-space: pre-wrap;
  word-break: break-all;
  min-width: 0;
}

.badge {
  margin-left: auto;
  font-size: 10px;
  padding: 1px 7px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  color: var(--el-text-color-secondary);
  flex: 0 0 auto;

  &.warn {
    background: var(--el-color-warning-light-9);
    color: var(--el-color-warning);
  }

  &.bad {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }

  /* The skill badge sits next to the arguments rather than being pushed right,
     so it reads as part of the call instead of as its status. */
  &.skill {
    margin-left: 0;
    background: var(--el-color-success-light-9);
    color: var(--el-color-success);
  }
}

.corrects {
  margin: 2px 0 4px 14px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  border-left: 1px dashed var(--el-border-color);
  padding-left: 8px;
}

.body {
  margin: 6px 0 10px 14px;
}

/* Terminal styling, and deliberately not syntax highlighted: command output is
   not code, and colouring keywords in a stack trace is noise. Font and padding
   live in AnsiText; this box provides the dark shell. Colours match the settled
   CodeBlock terminal so the view does not change appearance when the call
   settles. */
/* The terminal surface is its own token, not a fill: every skin keeps command
   output on a dark block — that is the convention output is written for — and each
   one borrows its own family's dark surface for it. See theme/build.ts. */
.live {
  margin: 0;
  max-height: 300px;
  overflow: auto;
  border-radius: 10px;
  background: var(--pg-term-bg);
  color: var(--pg-term-fg);
}

.error-text {
  font-size: 12px;
  color: var(--el-color-danger);
  background: var(--el-color-danger-light-9);
  border: 1px solid color-mix(in srgb, var(--el-color-danger) 18%, transparent);
  border-radius: 8px;
  padding: 7px 10px;
  margin-bottom: 6px;
  white-space: pre-wrap;
}
</style>
