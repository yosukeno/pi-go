<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import CodeBlock from "../CodeBlock.vue";
import { readBodies } from "@/agent/timeline";
import type { ReadManyDetails, ToolResult } from "@/api/types";

/**
 * A read of several files in one call.
 *
 * This is the component `ReadManyDetails` was written without: until now a
 * multi-file read fell through to `ToolCall.vue`'s plain-text fallback, which shows
 * the concatenated blob with `==> path <==` headers in it and no syntax colouring,
 * no per-file line counts, and — the part that actually loses information — no
 * visible distinction between a file that came back and one that could not be read.
 * That mattered more here than for any other tool, because one unreadable path is a
 * normal outcome for this call rather than a failure of it.
 *
 * The file list is the card. Bodies are collapsed by default and open one at a time:
 * five files' contents stacked open is the context pollution the batching was meant
 * to reduce, only moved into the page.
 */
const props = defineProps<{ result: ToolResult; details: ReadManyDetails }>();

// Same contract as ReadResult: a truncated read is the model's business, and the
// button fills the composer rather than sending.
const emit = defineEmits<{ suggest: [string] }>();

const { t } = useI18n();

const open = ref<Set<number>>(new Set());
function toggle(i: number) {
  const next = new Set(open.value);
  if (next.has(i)) next.delete(i);
  else next.add(i);
  open.value = next;
}

// Sliced once per result rather than per row, so opening a file costs nothing.
const bodies = computed(() => readBodies(props.result.text, props.details.files));

const failed = computed(() => props.details.files.filter((f) => f.error).length);
const truncated = computed(() => props.details.files.filter((f) => f.truncated).length);

function langOf(path: string): string {
  return path.split(".").pop() ?? "";
}

function nameOf(path: string): string {
  const parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}
</script>

<template>
  <div class="read-many">
    <div class="meta">
      <span>{{ t("readManyResult.files", { n: details.files.length }) }}</span>
      <!-- Both counts are stated rather than left to be spotted in the list: the
           whole point of the summary line is that it is read instead of the list. -->
      <span v-if="failed" class="bad">{{ t("readManyResult.failed", { n: failed }) }}</span>
      <span v-if="truncated" class="warn">
        {{ t("readManyResult.truncated", { n: truncated, ways: details.files.length }) }}
      </span>
    </div>

    <ul class="files">
      <li v-for="(f, i) in details.files" :key="f.path" :class="{ bad: !!f.error }">
        <button class="row" :aria-expanded="open.has(i)" @click="toggle(i)">
          <span class="caret">{{ open.has(i) ? "▾" : "▸" }}</span>
          <span class="name" :title="f.path">{{ nameOf(f.path) }}</span>
          <span class="dir">{{ f.path }}</span>
          <span v-if="f.error" class="tag bad">{{ t("readManyResult.error") }}</span>
          <template v-else>
            <span v-if="f.truncated" class="tag warn">
              {{ t("readManyResult.shown", { shown: f.shown_lines, total: f.total_lines }) }}
            </span>
            <span v-else class="tag">{{ t("readManyResult.lines", { n: f.total_lines }) }}</span>
          </template>
        </button>

        <div v-if="open.has(i)" class="body">
          <div v-if="f.error" class="error-text">{{ f.error }}</div>
          <template v-else>
            <!-- Line numbers start at 1: readMany reads from the top of each file,
                 which is why offset is refused alongside paths. -->
            <CodeBlock v-if="bodies[i] !== undefined" :code="bodies[i]!" :lang="langOf(f.path)" line-numbers />
            <div v-else class="note">{{ t("readManyResult.bodyMissing") }}</div>
            <!-- Naming path rather than paths, because offset only pairs with the
                 single-file form; the tool's own note says the same thing. -->
            <button
              v-if="f.truncated"
              class="more"
              @click="emit('suggest', t('readManyResult.suggestContinue', { path: f.path, line: (f.shown_lines ?? 0) + 1 }))"
            >
              {{ t("readManyResult.continueFrom", { line: (f.shown_lines ?? 0) + 1 }) }}
            </button>
          </template>
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped lang="scss">
.meta {
  display: flex;
  gap: 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  margin-bottom: 4px;
}

.warn {
  color: var(--el-color-warning);
}

.bad {
  color: var(--el-color-danger);
}

.files {
  list-style: none;
  margin: 0;
  padding: 0;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 4px;
  overflow: hidden;
}

.files > li + li {
  border-top: 1px solid var(--el-border-color-lighter);
}

.row {
  display: flex;
  align-items: baseline;
  gap: 8px;
  width: 100%;
  padding: 4px 8px;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color-light);
  }
}

.caret {
  flex: 0 0 auto;
  color: var(--el-text-color-secondary);
}

.name {
  flex: 0 0 auto;
  font-family: ui-monospace, monospace;
}

/* The full path, dimmed and allowed to be clipped: the file name identifies the
   row, the path disambiguates it, and neither should push the counts off the end. */
.dir {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  direction: rtl;
  text-align: left;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.tag {
  flex: 0 0 auto;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--el-text-color-secondary);
}

.body {
  padding: 4px 8px 8px;
}

.error-text {
  font-size: 12px;
  color: var(--el-color-danger);
  overflow-wrap: anywhere;
}

.note {
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.more {
  margin-top: 6px;
  border: 1px solid var(--el-border-color);
  background: transparent;
  color: var(--el-color-primary);
  border-radius: 4px;
  font-size: 11px;
  padding: 3px 8px;
  cursor: pointer;
}
</style>
