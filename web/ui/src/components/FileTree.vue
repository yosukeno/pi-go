<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { ArrowDown, ArrowUp, CaretRight } from "@element-plus/icons-vue";
import { api } from "@/api/client";
import type { FileEntry } from "@/api/types";
import { fileIcon, folderIcon, folderOpenIcon } from "./fileIcons";
import { sortEntries, toggleSort, treeCache, treeExpanded, treeLoadError, treeSort } from "./fileTreeStore";

// The workspace file tree: lazy and recursive. Listings, expansion and errors
// live in fileTreeStore (module level), so collapsing a directory neither
// refetches nor forgets its inner expansion on the way back down.

const props = defineProps<{
  /** workspace-relative directory this node lists; "" is the root. */
  path: string;
  depth: number;
  /** currently previewed file, threaded down for the active-row highlight. */
  selected?: string;
}>();
const emit = defineEmits<{ open: [string] }>();

const { t } = useI18n();

const entries = ref<FileEntry[] | null>(null);
// loading is the first fetch (nothing to show yet); refreshing is a path
// switch — the old listing stays on screen, dimmed, until the new one lands,
// which is what keeps a workspace change from blanking the panel.
const loading = ref(false);
const refreshing = ref(false);

// The listing as the column header ordered it. treeSort is shared, so one
// click re-orders every expanded directory at once.
const sorted = computed(() => (entries.value ? sortEntries(entries.value, treeSort.key, treeSort.asc) : null));

async function load() {
  const cached = treeCache.get(props.path);
  if (cached) {
    entries.value = cached;
    treeLoadError.delete(props.path);
    return;
  }
  if (entries.value === null) loading.value = true;
  else refreshing.value = true;
  treeLoadError.delete(props.path);
  try {
    const res = await api.files(props.path);
    treeCache.set(props.path, res.entries);
    entries.value = res.entries;
  } catch (e) {
    treeLoadError.set(props.path, e instanceof Error ? e.message : String(e));
  } finally {
    loading.value = false;
    refreshing.value = false;
  }
}

onMounted(load);
// Only the root node's path ever changes (the panel re-roots it to the
// session's workspace); child nodes are keyed to their own fixed path.
watch(() => props.path, load);

function childPath(name: string): string {
  return props.path === "" || props.path === "." ? name : `${props.path}/${name}`;
}

function click(e: FileEntry) {
  const p = childPath(e.name);
  if (!e.dir) {
    emit("open", p);
    return;
  }
  if (treeExpanded.has(p)) treeExpanded.delete(p);
  else treeExpanded.add(p);
}

const indent = (extra = 0) => ({ paddingLeft: `${8 + props.depth * 14 + extra}px` });

// Largest fitting English unit, two decimals — same convention as WriteResult.
function fmtSize(e: FileEntry): string {
  if (e.dir) return "";
  const b = e.size;
  if (b < 1024) return `${b} B`;
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(2)} KB`;
  return `${(b / 1024 / 1024).toFixed(2)} MB`;
}

// Full local timestamp, one shape for everything: "2026-08-08 13:05".
function fmtTime(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
</script>

<template>
  <div class="tree" :class="{ refreshing }">
    <div v-if="!entries && loading" class="hint" :style="indent()">{{ t("common.loading") }}</div>
    <button v-else-if="!entries && treeLoadError.has(path)" class="hint err" :style="indent()" @click="load">
      {{ t("fileTree.loadFailed") }}
    </button>
    <template v-else-if="entries">
      <!-- Column headers sit at the root only; nested levels indent, so their
           rows cannot line up under a header anyway. Sticky so a long listing
           keeps them visible. -->
      <div v-if="depth === 0" class="colhead">
        <span class="lead" />
        <button class="cell hname" :class="{ on: treeSort.key === 'name' }" @click="toggleSort('name')">
          {{ t("fileTree.colName") }}
          <el-icon v-if="treeSort.key === 'name'" class="arrow">
            <ArrowUp v-if="treeSort.asc" />
            <ArrowDown v-else />
          </el-icon>
        </button>
        <button class="cell hsize" :class="{ on: treeSort.key === 'size' }" @click="toggleSort('size')">
          {{ t("fileTree.colSize") }}
          <el-icon v-if="treeSort.key === 'size'" class="arrow">
            <ArrowUp v-if="treeSort.asc" />
            <ArrowDown v-else />
          </el-icon>
        </button>
        <button class="cell htime" :class="{ on: treeSort.key === 'time' }" @click="toggleSort('time')">
          {{ t("fileTree.colTime") }}
          <el-icon v-if="treeSort.key === 'time'" class="arrow">
            <ArrowUp v-if="treeSort.asc" />
            <ArrowDown v-else />
          </el-icon>
        </button>
      </div>
      <template v-for="e in sorted ?? []" :key="e.name">
        <button
          class="node"
          :class="{ active: !e.dir && selected === childPath(e.name) }"
          :style="indent()"
          @click="click(e)"
        >
          <el-icon v-if="e.dir" class="chev" :class="{ open: treeExpanded.has(childPath(e.name)) }">
            <CaretRight />
          </el-icon>
          <span v-else class="chev" />
          <Icon
            class="kind"
            :icon="e.dir ? (treeExpanded.has(childPath(e.name)) ? folderOpenIcon : folderIcon) : fileIcon(e.name)"
          />
          <span class="name">{{ e.name }}</span>
          <span class="size">{{ fmtSize(e) }}</span>
          <span class="time">{{ fmtTime(e.mtime_ms) }}</span>
        </button>
        <FileTree
          v-if="e.dir && treeExpanded.has(childPath(e.name))"
          :path="childPath(e.name)"
          :depth="depth + 1"
          :selected="selected"
          @open="emit('open', $event)"
        />
      </template>
      <div v-if="!sorted?.length" class="hint" :style="indent()">{{ t("fileTree.emptyDir") }}</div>
    </template>
  </div>
</template>

<style scoped lang="scss">
.tree {
  font-size: 13px;
  transition: opacity 0.15s ease;

  /* A path switch (workspace change) keeps the old listing, dimmed, until
     the new one arrives — no blank "加载中" between workspaces. */
  &.refreshing {
    opacity: 0.35;
  }
}

/* Column header row. The lead spacer is the width of a root row's chevron +
   icon (+ their gaps), so 名称 starts exactly above the file names; the size
   and time cells reuse the row columns' fixed widths. */
.colhead {
  position: sticky;
  top: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  gap: 5px;
  padding: 0 8px;
  background: var(--el-bg-color);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.lead {
  flex: 0 0 35px;
}

.cell {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: 0;
  background: transparent;
  padding: 4px 0;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover,
  &.on {
    color: var(--el-text-color-primary);
  }

  &.on {
    font-weight: 600;
  }
}

.hname {
  flex: 1;
  min-width: 0;
}

.hsize,
.htime {
  justify-content: flex-end;
  font-variant-numeric: tabular-nums;
}

.hsize {
  flex: 0 0 64px;
}

.htime {
  flex: 0 0 96px;
}

.arrow {
  font-size: 10px;
}

.node {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 100%;
  border: 0;
  background: transparent;
  padding-top: 3px;
  padding-bottom: 3px;
  padding-right: 8px;
  cursor: pointer;
  text-align: left;
  border-radius: 4px;

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.active {
    background: var(--el-fill-color);
  }
}

.chev {
  flex: 0 0 14px;
  width: 14px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  transition: transform 0.12s;

  &.open {
    transform: rotate(90deg);
  }
}

span.chev {
  /* Files get a spacer so their names align with directories'. */
  display: inline-block;
}

.kind {
  flex: 0 0 auto;
  width: 16px;
  height: 16px;
}

.name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--el-text-color-regular);
}

.size,
.time {
  flex: 0 0 auto;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.size {
  width: 64px;
}

.time {
  width: 96px;
}

.hint {
  display: block;
  width: 100%;
  border: 0;
  background: transparent;
  padding-top: 3px;
  padding-bottom: 3px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: left;

  &.err {
    color: var(--el-color-danger);
    cursor: pointer;
  }
}
</style>
