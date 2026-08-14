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
  /**
   * todoPinned says the plan is already on screen in the bar above the composer,
   * so the inline task-list card should collapse to a line like a superseded one.
   * Threaded down rather than read from a store for the same reason the two above
   * are: the components stay testable in isolation.
   */
  todoPinned?: boolean;
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
      :todo-pinned="todoPinned"
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
/* The rule down the left is a guide, not a divider: it groups a turn's tool calls
   with the answer they produced, so it only has to be visible enough to be
   followed. The streaming turn gets the accent, which makes "where is it working"
   answerable from the shape of the page rather than from reading it. */
.turn {
  border-left: 2px solid var(--el-border-color-lighter);
  padding: 6px 0 8px 14px;
  margin: 14px 0;
  transition: border-color var(--pg-transition);

  &.streaming {
    border-left-color: var(--el-color-primary);
  }
}

.head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-placeholder);
}

.live {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--el-color-primary);

  /* The dot is the part that says "now": a word alone reads as a label that could
     have been left behind by a finished turn. */
  &::before {
    content: "";
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: currentcolor;
    animation: turn-live 1.2s ease-in-out infinite;
  }
}

@keyframes turn-live {
  0%,
  100% {
    opacity: 1;
  }

  50% {
    opacity: 0.25;
  }
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
  padding: 2px 7px 2px 4px;
  border-radius: 6px;
  transition: background var(--pg-transition);

  &:hover {
    background: var(--el-fill-color-light);
    color: var(--el-text-color-primary);
  }
}

.thinking-body {
  margin: 4px 0 8px 6px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--el-text-color-secondary);
  white-space: pre-wrap;
  border-left: 1px dashed var(--el-border-color);
  padding-left: 12px;
}

.answer {
  margin-top: 8px;
  font-size: 14px;
  line-height: 1.75;
  color: var(--el-text-color-primary);

  :deep(p) {
    margin: 0.55em 0;
  }

  /* Headings inside an answer are section markers in a paragraph of chat, not page
     titles: they get weight and space above, not size. Left at the framework's
     default they came out larger than the composer's own labels, which made a
     three-heading answer look like a document someone pasted in. */
  :deep(h1),
  :deep(h2),
  :deep(h3),
  :deep(h4) {
    margin: 1.1em 0 0.4em;
    font-size: 1em;
    font-weight: 650;
    line-height: 1.5;
  }

  :deep(h1) {
    font-size: 1.12em;
  }

  :deep(h2) {
    font-size: 1.06em;
  }

  :deep(pre) {
    background: var(--el-fill-color-lighter);
    border: 1px solid var(--el-border-color-extra-light);
    padding: 10px 12px;
    border-radius: 10px;
    overflow-x: auto;
    font: 12px/1.6 var(--pg-mono);
  }

  :deep(code) {
    font-family: var(--pg-mono);
    font-size: 0.9em;
  }

  /* Inline code only: a tinted tile inside a code block would draw a second box
     around every line. */
  :deep(:not(pre) > code) {
    padding: 1px 5px;
    border-radius: 5px;
    background: var(--el-fill-color-light);
    color: var(--el-color-primary-dark-2);
  }

  :deep(a) {
    color: var(--el-color-primary-dark-2);
    text-decoration-color: var(--pg-accent-line);
    text-underline-offset: 2px;
  }

  :deep(blockquote) {
    margin: 0.6em 0;
    padding: 2px 0 2px 12px;
    border-left: 2px solid var(--el-border-color-light);
    color: var(--el-text-color-regular);
  }

  :deep(hr) {
    margin: 1.2em 0;
    border: 0;
    border-top: 1px solid var(--el-border-color-extra-light);
  }

  :deep(ul),
  :deep(ol) {
    padding-left: 1.4em;
    margin: 0.5em 0;
  }

  :deep(li) {
    margin: 0.2em 0;
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
    border: 1px solid var(--el-border-color-light);
    padding: 5px 11px;
    text-align: left;
  }

  :deep(th) {
    background: var(--el-fill-color-lighter);
    font-weight: 600;
  }
}
</style>
