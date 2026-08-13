<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import MarkdownView from "./MarkdownView.vue";
import ToolCall from "./ToolCall.vue";
import IncomingArgs from "./tools/IncomingArgs.vue";
import type { TurnItem } from "@/agent/timeline";

const { t } = useI18n();

const props = defineProps<{
  turn: TurnItem;
  runActive: boolean;
  // Passed through to ToolCall for the skill badge. Threaded as props rather than
  // read from a module-level store so the components stay testable in isolation.
  skills?: { name: string; path: string }[];
  cwd?: string;
}>();

const emit = defineEmits<{
  suggest: [string];
  decide: [{ gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }];
  freeze: [string];
  thaw: [string];
}>();

const thinkingOpen = ref(false);
</script>

<template>
  <div class="turn" :class="{ streaming: turn.streaming }">
    <div class="head">
      <span class="index">{{ t("turnCard.turnIndex", { n: turn.index }) }}</span>
      <span v-if="turn.streaming" class="live">{{ t("turnCard.streaming") }}</span>
    </div>

    <div v-if="turn.thinking" class="thinking">
      <button class="toggle" @click="thinkingOpen = !thinkingOpen">
        {{ thinkingOpen ? "▾" : "▸" }} {{ t("turnCard.thinking") }}
      </button>
      <div v-if="thinkingOpen || turn.streaming" class="thinking-body">{{ turn.thinking }}</div>
    </div>

    <ToolCall
      v-for="call in turn.calls"
      :key="call.callId"
      :call="call"
      :run-active="runActive"
      :skills="skills"
      :cwd="cwd"
      @suggest="emit('suggest', $event)"
      @decide="emit('decide', $event)"
      @freeze="emit('freeze', $event)"
      @thaw="emit('thaw', $event)"
    />

    <!-- Arguments still streaming in, shown where the pending-tool card will
         appear; tool_start removes the entry, so the two never overlap. -->
    <IncomingArgs v-for="inc in turn.incoming ?? []" :key="inc.call_id" :incoming="inc" />

    <!-- Markdown is parsed live while streaming: markdown-it is cheap and the token
         stream is already throttled to ~12fps, so formatting appears as the answer
         grows instead of snapping in at the end. The one heavy, error-prone piece on
         an incomplete source is Mermaid, which MarkdownView defers via `streaming`. -->
    <MarkdownView v-if="turn.text" class="answer" :source="turn.text" :streaming="turn.streaming" />
  </div>
</template>

<style scoped lang="scss">
.turn {
  border-left: 2px solid var(--el-border-color-lighter);
  padding: 6px 0 6px 12px;
  margin: 10px 0;

  &.streaming {
    border-left-color: var(--el-color-primary);
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.live {
  color: var(--el-color-primary);
}

.thinking {
  margin: 4px 0;
}

.toggle {
  border: 0;
  background: transparent;
  color: var(--el-text-color-secondary);
  font-size: 11px;
  cursor: pointer;
  padding: 0;
}

.thinking-body {
  margin: 4px 0 6px 12px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--el-text-color-secondary);
  white-space: pre-wrap;
  border-left: 1px dashed var(--el-border-color);
  padding-left: 10px;
}

.answer {
  margin-top: 6px;
  font-size: 14px;
  line-height: 1.7;

  :deep(p) {
    margin: 0.4em 0;
  }

  :deep(pre) {
    background: var(--el-fill-color-light);
    padding: 8px 10px;
    border-radius: 6px;
    overflow-x: auto;
    font: 12px/1.55 ui-monospace, monospace;
  }

  :deep(code) {
    font-family: ui-monospace, monospace;
    font-size: 0.92em;
  }

  :deep(ul),
  :deep(ol) {
    padding-left: 1.4em;
    margin: 0.4em 0;
  }

  /* markdown-it renders GFM tables but the UA stylesheet draws no borders on
     them, so without these rules a table reads as plain aligned text. */
  :deep(table) {
    border-collapse: collapse;
    margin: 0.5em 0;
    font-size: 13px;
    /* GitHub's pattern: let a wide table scroll instead of blowing out the
       column, without stretching narrow ones. */
    display: block;
    width: max-content;
    max-width: 100%;
    overflow-x: auto;
  }

  :deep(th),
  :deep(td) {
    border: 1px solid var(--el-border-color);
    padding: 4px 10px;
    text-align: left;
  }

  :deep(th) {
    background: var(--el-fill-color-light);
  }
}
</style>
