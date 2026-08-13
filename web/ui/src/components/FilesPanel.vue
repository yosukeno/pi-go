<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Close, Refresh, Search } from "@element-plus/icons-vue";
import FileTree from "./FileTree.vue";
import FilePreview from "./FilePreview.vue";
import ChangesView from "./ChangesView.vue";
import GitBar from "./GitBar.vue";
import QuickOpen from "./QuickOpen.vue";
import { clearTreeCache, indexEpoch, treeEpoch } from "./fileTreeStore";
import { collectChanges, changedPathCount } from "@/agent/changes";
import { api } from "@/api/client";
import type { TimelineItem } from "@/agent/timeline";

// The workspace file panel: two tabs — the file tree and this session's
// changes. Sizing, dragging and the dock edge all live in DockArea; this
// component fills whatever slot it is given and keeps the tabs and tools.
// The preview takes the whole panel rather than splitting it, so a narrow
// panel still gives file content full width.
const props = defineProps<{ items: TimelineItem[]; layout: "right" | "bottom"; workspace?: string }>();
const emit = defineEmits<{ close: []; "update:layout": ["right" | "bottom"] }>();

const { t } = useI18n();

type View = "tree" | "file" | "changes";
const view = ref<View>("tree");
// backTo remembers which tab the preview was opened from.
const backTo = ref<"tree" | "changes">("tree");
const selected = ref("");

// Switching sessions can switch the workspace the panel is scoped to; a
// preview left over from the previous one would show a file this tree does
// not even list.
watch(
  () => props.workspace,
  () => {
    if (view.value === "file") view.value = "tree";
    selected.value = "";
  },
);
// epoch forces a tree/preview remount after a refresh: every still-expanded
// directory refetches, which is exactly what the refresh button promises.
const epoch = ref(0);

const changeCount = computed(() => changedPathCount(collectChanges(props.items)));

function openFile(path: string) {
  if (view.value !== "file") backTo.value = view.value === "changes" ? "changes" : "tree";
  selected.value = path;
  view.value = "file";
}

function refresh() {
  clearTreeCache();
  indexPaths.value = null;
  epoch.value++;
}

// --- quick open (⌘P / Ctrl+P) ----------------------------------------------
const searching = ref(false);
const indexPaths = ref<string[] | null>(null);

// The stream busts the index when a run ends (the agent's writes may have
// added files); dropping the cached paths means the next ⌘P rewalks lazily.
watch(indexEpoch, () => {
  indexPaths.value = null;
});

// Quick-open follows the same scoping as the tree: with a session workspace
// set, only paths under it are offered.
const scopedIndex = computed(() => {
  const all = indexPaths.value ?? [];
  const ws = props.workspace ?? "";
  return ws ? all.filter((p) => p.startsWith(ws + "/")) : all;
});

async function openSearch() {
  searching.value = true;
  if (indexPaths.value === null) {
    try {
      indexPaths.value = (await api.fileIndex()).paths;
    } catch {
      indexPaths.value = [];
    }
  }
}

function openFromSearch(path: string) {
  searching.value = false;
  openFile(path);
}

// ⌘P is the browser's print shortcut: intercept it only while the panel is
// open, which is exactly when the user means "open a file".
function onKeydown(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "p") {
    e.preventDefault();
    if (searching.value) searching.value = false;
    else openSearch();
  }
}
onMounted(() => window.addEventListener("keydown", onKeydown));
onBeforeUnmount(() => window.removeEventListener("keydown", onKeydown));
</script>

<template>
  <section class="panel">
    <header class="head">
      <div class="tabs">
        <button class="tab" :class="{ on: view !== 'changes' }" @click="view = 'tree'">{{ t("filesPanel.tabFiles") }}</button>
        <button class="tab" :class="{ on: view === 'changes' }" @click="view = 'changes'">
          {{ t("filesPanel.tabChanges") }}<span v-if="changeCount" class="count">{{ changeCount }}</span>
        </button>
      </div>
      <!-- With a session workspace set the whole panel is scoped to it; say
           so, or the tree reads as "the root, mysteriously emptier". -->
      <span v-if="workspace" class="ws-root" :title="t('filesPanel.workspaceTitle', { ws: workspace })">/{{ workspace }}</span>
      <!-- Chrome DevTools style dock toggle: the icon shows the current
           layout (a window with the docked side filled), click flips it. -->
      <button
        class="ghost layout-btn"
        :title="layout === 'right' ? t('filesPanel.toBottomLayout') : t('filesPanel.toRightLayout')"
        @click="emit('update:layout', layout === 'right' ? 'bottom' : 'right')"
      >
        <svg v-if="layout === 'right'" width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
          <rect x="1.5" y="2.5" width="12" height="10" rx="2" stroke="currentColor" />
          <path d="M9.5 2.5H11.5A2 2 0 0 1 13.5 4.5V10.5A2 2 0 0 1 11.5 12.5H9.5Z" fill="currentColor" />
        </svg>
        <svg v-else width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
          <rect x="1.5" y="2.5" width="12" height="10" rx="2" stroke="currentColor" />
          <path d="M1.5 8.5V10.5A2 2 0 0 0 3.5 12.5H11.5A2 2 0 0 0 13.5 10.5V8.5Z" fill="currentColor" />
        </svg>
      </button>
      <button class="ghost" :title="t('filesPanel.quickOpen')" @click="openSearch">
        <el-icon><Search /></el-icon>
      </button>
      <button class="ghost" :title="t('filesPanel.refresh')" @click="refresh">
        <el-icon><Refresh /></el-icon>
      </button>
      <button class="ghost" :title="t('common.close')" @click="emit('close')">
        <el-icon><Close /></el-icon>
      </button>
    </header>
    <!-- A second row rather than another chip in the header: the header is
         already tabs plus four buttons, and this is the one thing here that is
         a property of the workspace rather than of the panel. It renders
         nothing at all when the state cannot be fetched. -->
    <GitBar :epoch="epoch" />
    <div class="content">
      <QuickOpen v-if="searching" :paths="scopedIndex" @open="openFromSearch" @close="searching = false" />
      <div v-else-if="view === 'tree'" class="scroll">
        <!-- The tree roots at the session's workspace and re-roots itself
             smoothly on a switch (hold-dim-swap inside FileTree), so the
             key deliberately does NOT include the workspace. Emitted paths
             stay root-relative, so preview and save need no mapping. -->
        <FileTree
          :key="`${epoch}-${treeEpoch}`"
          :path="workspace ?? ''"
          :depth="0"
          :selected="selected"
          @open="openFile"
        />
      </div>
      <FilePreview v-else-if="view === 'file'" :key="`${selected}#${epoch}`" :path="selected" @back="view = backTo" />
      <div v-else class="scroll" :key="`changes-${epoch}`">
        <ChangesView :items="items" :workspace="workspace ?? ''" @preview="openFile" />
      </div>
    </div>
  </section>
</template>

<style scoped lang="scss">
.panel {
  height: 100%;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  background: var(--el-bg-color);
}

.head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.ws-root {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  font-family: ui-monospace, monospace;
  color: var(--el-text-color-secondary);
}

.tabs {
  flex: 1;
  display: flex;
  gap: 2px;
  padding-left: 6px;
}

.tab {
  border: 0;
  background: transparent;
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
    font-weight: 600;
  }

  .count {
    font-size: 10px;
    padding: 0 5px;
    border-radius: 8px;
    background: var(--el-fill-color-darker);
    color: var(--el-text-color-primary);
    font-variant-numeric: tabular-nums;
  }
}

.ghost {
  display: inline-flex;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }
}

.layout-btn svg {
  display: block;
}

.content {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.scroll {
  flex: 1;
  overflow: auto;
  padding: 6px 4px 12px;
}
</style>
