<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import AnsiText from "./AnsiText.vue";
import { detectLanguage, highlightLines } from "./highlight";
import { languageIcon, terminalIcon } from "./fileIcons";

// A code viewer with syntax colouring, a collapse and a copy button.
//
// Every token is rendered as a text node, never through v-html, so nothing a
// tool prints can inject markup — a coding agent displays file contents it did
// not write all day long. That is also why the highlighter produces tokens
// instead of HTML.
const props = withDefaults(
  defineProps<{
    code: string;
    /** lang is a file extension or language name; unknown ones render plain. */
    lang?: string;
    /** collapsedHeight in px; content taller than this starts folded. */
    collapsedHeight?: number;
    expandedHeight?: number;
    lineNumbers?: boolean;
    /**
     * terminal styling for command output. Output is not code, so it is never
     * highlighted: colouring a stack trace's keywords is noise. The colours a
     * command itself prints (ANSI SGR) are honoured, though — a red error
     * line is information.
     */
    terminal?: boolean;
    startLine?: number;
  }>(),
  { collapsedHeight: 140, expandedHeight: 460, lineNumbers: false, terminal: false, startLine: 1 },
);

const { t } = useI18n();

const expanded = ref(false);
const copied = ref(false);

const plain = computed(() => props.code.replace(/\n$/, ""));
const language = computed(() => detectLanguage(props.lang));
const lines = computed(() => highlightLines(plain.value, language.value));
// In terminal mode the rows come from AnsiText, but escapes never change the
// newline count, so the label can count lines without parsing them.
const rowCount = computed(() => (props.terminal ? plain.value.split("\n").length : lines.value.length));
const collapsible = computed(() => rowCount.value > 8);
const maxHeight = computed(() => `${expanded.value ? props.expandedHeight : props.collapsedHeight}px`);

const label = computed(() => {
  if (props.terminal) return t("codeBlock.output");
  return props.lang || t("codeBlock.text");
});

// The badge carries a file-type icon so the language reads as a tag rather than
// as one more button next to copy/collapse.
const badgeIcon = computed(() => (props.terminal ? terminalIcon : languageIcon(props.lang)));

async function copy() {
  try {
    await navigator.clipboard.writeText(props.code);
    copied.value = true;
    window.setTimeout(() => (copied.value = false), 1200);
  } catch {
    // Clipboard access can be denied; failing quietly is better than an alert.
  }
}
</script>

<template>
  <div class="code-block" :class="{ terminal }">
    <div class="bar">
      <span class="lang"><Icon class="kind" :icon="badgeIcon" />{{ label }}</span>
      <span class="count">{{ t("codeBlock.lineCount", { n: rowCount }) }}</span>
      <button class="ghost" @click="copy">{{ copied ? t("common.copied") : t("common.copy") }}</button>
      <button v-if="collapsible" class="ghost" @click="expanded = !expanded">
        {{ expanded ? t("common.collapse") : t("common.expand") }}
      </button>
    </div>
    <div class="body" :style="{ maxHeight }">
      <AnsiText v-if="terminal" :text="plain" />
      <pre v-else><code><span v-for="(line, i) in lines" :key="i" class="row"><span
        v-if="lineNumbers" class="ln">{{ startLine + i }}</span><span class="txt"><span
        v-for="(tok, j) in line" :key="j" :class="tok.kind">{{ tok.text }}</span></span></span></code></pre>
    </div>
    <div v-if="collapsible && !expanded" class="fade" />
  </div>
</template>

<style scoped lang="scss">
.code-block {
  position: relative;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  overflow: hidden;
  background: var(--el-fill-color-lighter);

  &.terminal {
    background: #1e1f22;
    border-color: #2c2e33;

    .body,
    .lang,
    .count {
      color: #d6d7db;
    }

    /* The label pill and the collapsed fade are otherwise driven by light-theme
       CSS vars (--el-fill-color*), which on this dark surface read as a white
       pill and a white band washing out the last lines of output. Pull both
       onto the terminal's own dark ramp. */
    .lang {
      background: #2c2e33;
    }

    .fade {
      background: linear-gradient(transparent, #1e1f22);
    }
  }
}

.bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

/* A tag, not a control: icon + name on a filled pill, so the eye separates it
   from the copy/collapse buttons on the other end of the bar. */
.lang {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 7px 1px 5px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 10px;
  background: var(--el-fill-color);
  color: var(--el-text-color-regular);
  font-weight: 600;
  line-height: 1.7;

  .kind {
    width: 13px;
    height: 13px;
    flex: 0 0 auto;
  }
}

.count {
  margin-right: auto;
}

.ghost {
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font-size: 11px;
  padding: 2px 4px;
  border-radius: 4px;

  &:hover {
    background: var(--el-fill-color);
  }
}

.body {
  overflow: auto;

  pre {
    margin: 0;
    padding: 8px 10px;
    font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
    white-space: pre;
  }

  code {
    display: block;
  }
}

.row {
  display: flex;
}

.ln {
  flex: 0 0 3.2em;
  text-align: right;
  padding-right: 10px;
  color: var(--el-text-color-placeholder);
  user-select: none;
}

.txt {
  white-space: pre;
}

// A restrained palette: the point is to find a string or a comment at a glance,
// not to turn the file into a rainbow.
.comment {
  color: #6a737d;
  font-style: italic;
}

.string {
  color: #0a6640;
}

.number {
  color: #005cc5;
}

.keyword {
  color: #b31d28;
}

.type {
  color: #6f42c1;
}

.func {
  color: #795e26;
}

.fade {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 28px;
  pointer-events: none;
  background: linear-gradient(transparent, var(--el-fill-color-lighter));
}
</style>
