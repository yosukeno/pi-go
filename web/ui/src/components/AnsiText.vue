<script setup lang="ts">
import { computed } from "vue";
import { ansiToLines, colorizeLongListing } from "./ansi";

// Command output with its colour escapes honoured, plus a fallback that
// colours `ls -l`-format lines the way ls itself would have (a piped ls
// prints none). The parser drops everything but SGR and the spans render as
// text nodes, so this is as injection-proof as the syntax-highlighted code
// viewer. Owns its own font and padding because a parent's scoped styles do
// not reach a child component's root.
const props = defineProps<{
  text: string;
  /** wrap long lines instead of scrolling horizontally (live output does). */
  wrap?: boolean;
}>();

const lines = computed(() => colorizeLongListing(ansiToLines(props.text)));
</script>

<template>
  <pre class="ansi" :class="{ wrap }"><span v-for="(line, i) in lines" :key="i"><span
      v-for="(t, j) in line"
      :key="j"
      :style="t.style"
    >{{ t.text }}</span><br v-if="i < lines.length - 1" /></span></pre>
</template>

<style scoped lang="scss">
.ansi {
  margin: 0;
  padding: 8px 10px;
  font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: pre;
  color: inherit;
  background: transparent;

  &.wrap {
    white-space: pre-wrap;
    word-break: break-word;
  }
}
</style>
