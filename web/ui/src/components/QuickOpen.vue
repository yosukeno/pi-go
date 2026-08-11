<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import { Icon } from "@iconify/vue";
import { useI18n } from "vue-i18n";
import { fuzzyFilter } from "@/agent/fuzzy";
import { baseName, fileIcon } from "./fileIcons";

// Quick-open (⌘P): the workspace path index plus a fuzzy box. All matching is
// client-side — the index is fetched once when the box first opens.
const props = defineProps<{ paths: string[] }>();
const emit = defineEmits<{ open: [string]; close: [] }>();

const { t } = useI18n();

const query = ref("");
const active = ref(0);
const inputEl = ref<HTMLInputElement | null>(null);
const listEl = ref<HTMLElement | null>(null);

const results = computed(() => fuzzyFilter(query.value, props.paths, 50));
watch(results, () => (active.value = 0));
watch(active, async () => {
  await nextTick();
  listEl.value?.children[active.value]?.scrollIntoView({ block: "nearest" });
});

onMounted(() => inputEl.value?.focus());

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    emit("close");
  } else if (e.key === "ArrowDown") {
    e.preventDefault();
    active.value = Math.min(active.value + 1, results.value.length - 1);
  } else if (e.key === "ArrowUp") {
    e.preventDefault();
    active.value = Math.max(active.value - 1, 0);
  } else if (e.key === "Enter" && results.value[active.value]) {
    emit("open", results.value[active.value]);
  }
}
</script>

<template>
  <div class="quickopen" @keydown="onKeydown">
    <input
      ref="inputEl"
      v-model="query"
      class="qinput"
      :placeholder="t('quickOpen.placeholder')"
      spellcheck="false"
    />
    <div class="qlist" ref="listEl">
      <button
        v-for="(p, i) in results"
        :key="p"
        class="qrow"
        :class="{ active: i === active }"
        @click="emit('open', p)"
        @mousemove="active = i"
      >
        <Icon class="qicon" :icon="fileIcon(baseName(p))" />
        <span class="qpath">{{ p }}</span>
      </button>
      <div v-if="query && !results.length" class="qempty">{{ t("quickOpen.noMatch") }}</div>
    </div>
  </div>
</template>

<style scoped lang="scss">
.quickopen {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
}

.qinput {
  margin: 8px 10px;
  padding: 7px 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  font-size: 13px;
  outline: none;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);

  &:focus {
    border-color: var(--el-text-color-primary);
  }
}

.qlist {
  flex: 1;
  min-height: 0;
  overflow: auto;
  padding-bottom: 8px;
}

.qrow {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 4px 14px;
  cursor: pointer;
  text-align: left;
  font-size: 12px;

  &.active {
    background: var(--el-fill-color);
  }
}

.qicon {
  flex: 0 0 auto;
  width: 15px;
  height: 15px;
}

.qpath {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-regular);
}

.qempty {
  padding: 14px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
</style>
