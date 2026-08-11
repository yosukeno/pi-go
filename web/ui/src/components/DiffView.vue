<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";

// The diff arrives already rendered by the Go side (diff/format.go): line
// numbers, context folding and all. So this component only colours it by prefix
// and folds it when long — deliberately not re-implementing the diff in JS,
// because two implementations would eventually disagree.
const props = withDefaults(
  defineProps<{
    diff: string;
    path: string;
    added: number;
    removed: number;
    /** collapseOver is the line count past which the diff starts folded. */
    collapseOver?: number;
  }>(),
  { collapseOver: 28 },
);

interface Row {
  kind: "add" | "del" | "ctx" | "meta";
  text: string;
}

const rows = computed<Row[]>(() =>
  props.diff
    .replace(/\n$/, "")
    .split("\n")
    .map((text) => {
      // The rendered form is "<sign> <lineno> <content>"; leading spaces before
      // the sign are part of the alignment.
      const sign = text.trimStart()[0];
      if (sign === "+") return { kind: "add", text };
      if (sign === "-") return { kind: "del", text };
      if (text.startsWith("@@")) return { kind: "meta", text };
      return { kind: "ctx", text };
    }),
);

const { t } = useI18n();

const expanded = ref(false);
const collapsible = computed(() => rows.value.length > props.collapseOver);
const shown = computed(() => (collapsible.value && !expanded.value ? rows.value.slice(0, props.collapseOver) : rows.value));
const hidden = computed(() => rows.value.length - shown.value.length);
</script>

<template>
  <div class="diff">
    <div class="head">
      <span class="path" :title="path">{{ path }}</span>
      <span class="stat">
        <span class="plus">+{{ added }}</span>
        <span class="minus">-{{ removed }}</span>
      </span>
    </div>
    <div class="rows">
      <div v-for="(r, i) in shown" :key="i" class="row" :class="r.kind">{{ r.text }}</div>
    </div>
    <button v-if="collapsible" class="more" @click="expanded = !expanded">
      {{ expanded ? t("common.collapse") : t("diffView.moreLines", { n: hidden }) }}
    </button>
  </div>
</template>

<style scoped lang="scss">
.diff {
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  overflow: hidden;
}

.head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 10px;
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
}

.path {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.stat {
  margin-left: auto;
  display: flex;
  gap: 6px;
  font-variant-numeric: tabular-nums;
}

.plus {
  color: var(--el-color-success);
}

.minus {
  color: var(--el-color-danger);
}

.rows {
  overflow-x: auto;
}

.row {
  padding: 0 10px;
  font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
  white-space: pre;

  &.add {
    background: color-mix(in srgb, var(--el-color-success) 12%, transparent);
    color: color-mix(in srgb, var(--el-color-success) 80%, black);
  }

  &.del {
    background: color-mix(in srgb, var(--el-color-danger) 12%, transparent);
    color: color-mix(in srgb, var(--el-color-danger) 80%, black);
  }

  &.ctx {
    color: var(--el-text-color-secondary);
  }

  &.meta {
    color: var(--el-color-primary);
    background: var(--el-fill-color-lighter);
  }
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
