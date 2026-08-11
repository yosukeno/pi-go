<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import { parsePatch } from "@/agent/patch";

// Side-by-side rendering of a unified patch. The diff itself is computed in
// Go; this component only re-aligns what diff.Unified already decided — the
// "never re-implement diff in JS" rule from DiffView still holds.
const props = withDefaults(
  defineProps<{
    patch: string;
    /** collapseOver is the row count past which the view starts folded. */
    collapseOver?: number;
  }>(),
  { collapseOver: 40 },
);

const parsed = computed(() => parsePatch(props.patch));

interface FlatRow {
  key: string;
  kind: "hunk" | "line";
  header?: string;
  left?: { kind: string; no?: number; text: string };
  right?: { kind: string; no?: number; text: string };
}

const rows = computed<FlatRow[]>(() => {
  const p = parsed.value;
  if (!p) return [];
  const out: FlatRow[] = [];
  p.hunks.forEach((h, hi) => {
    out.push({ key: `h${hi}`, kind: "hunk", header: h.header });
    h.rows.forEach((r, ri) => out.push({ key: `${hi}:${ri}`, kind: "line", left: r.left, right: r.right }));
  });
  return out;
});

const { t } = useI18n();

const expanded = ref(false);
const collapsible = computed(() => rows.value.length > props.collapseOver);
const shown = computed(() => (collapsible.value && !expanded.value ? rows.value.slice(0, props.collapseOver) : rows.value));
const hidden = computed(() => rows.value.length - shown.value.length);
</script>

<template>
  <div class="sbs">
    <div v-if="!parsed" class="bad">{{ t("sideBySideDiff.parseError") }}</div>
    <template v-else>
      <div class="rows">
        <div v-for="r in shown" :key="r.key" class="row" :class="r.kind">
          <template v-if="r.kind === 'hunk'">
            <span class="hunk">{{ r.header }}</span>
          </template>
          <template v-else>
            <span class="cell" :class="r.left!.kind">
              <span class="no">{{ r.left!.no ?? "" }}</span>
              <span class="txt">{{ r.left!.text }}</span>
            </span>
            <span class="cell" :class="r.right!.kind">
              <span class="no">{{ r.right!.no ?? "" }}</span>
              <span class="txt">{{ r.right!.text }}</span>
            </span>
          </template>
        </div>
      </div>
      <button v-if="collapsible" class="more" @click="expanded = !expanded">
        {{ expanded ? t("common.collapse") : t("sideBySideDiff.moreLines", { n: hidden }) }}
      </button>
    </template>
  </div>
</template>

<style scoped lang="scss">
.sbs {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  overflow: hidden;
}

.rows {
  overflow-x: auto;
}

.row {
  &.line {
    display: grid;
    grid-template-columns: 1fr 1fr;
    min-width: max-content;
  }
}

.cell {
  display: flex;
  min-width: 0;
  font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;

  &.del {
    background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
    .txt {
      color: color-mix(in srgb, var(--el-color-danger) 80%, black);
    }
  }

  &.add {
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    .txt {
      color: color-mix(in srgb, var(--el-color-success) 80%, black);
    }
  }

  &.ctx .txt {
    color: var(--el-text-color-regular);
  }

  &.empty {
    background: var(--el-fill-color-lighter);
  }
}

.no {
  flex: 0 0 3em;
  padding: 0 6px;
  text-align: right;
  color: var(--el-text-color-placeholder);
  user-select: none;
}

.txt {
  white-space: pre;
  padding-right: 10px;
}

.hunk {
  display: block;
  padding: 2px 10px;
  font: 11px/1.6 ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-top: 1px solid var(--el-border-color-lighter);

  .row:first-child > & {
    border-top: 0;
  }
}

.bad {
  padding: 8px 10px;
  font-size: 12px;
  color: var(--el-color-danger);
}

.more {
  width: 100%;
  border: 0;
  border-top: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
  color: var(--el-text-color-secondary);
  font-size: 11px;
  padding: 4px;
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }
}
</style>
