<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from "vue";
import { Icon } from "@iconify/vue";
import { ArrowLeft, HomeFilled, Search } from "@element-plus/icons-vue";
import { useI18n } from "vue-i18n";
import { api } from "@/api/client";
import type { FileEntry } from "@/api/types";
import { fuzzyFilter } from "@/agent/fuzzy";
import { folderIcon, folderOpenIcon, folderPlusIcon } from "./fileIcons";
import { invalidateTree } from "./fileTreeStore";

// Workspace picker for "new session". The model is Claude-Code-style "the
// folder you are looking at is the selection": rows navigate into a directory,
// the footer always names what will be used, and the root is the default one
// Enter away. VSCode's quick pick contributes the filter box and keyboard
// confirm; Finder contributes the inline "new folder" row.
defineProps<{
  /** server root, absolute — shown muted so the boundary is unambiguous. */
  cwd: string;
}>();
const emit = defineEmits<{ create: [workspace: string]; close: [] }>();

const { t } = useI18n();

const visible = ref(true);
/** Browsing location, root-relative and slash-separated; "" is the root. */
const current = ref("");
const dirs = ref<FileEntry[] | null>(null);
const error = ref("");
const query = ref("");

const segments = computed(() => current.value.split("/").filter(Boolean));

const shown = computed(() => {
  const all = dirs.value ?? [];
  if (!query.value) return all;
  const order = new Map(fuzzyFilter(query.value, all.map((d) => d.name), 100).map((n, i) => [n, i]));
  return all.filter((d) => order.has(d.name)).sort((a, b) => order.get(a.name)! - order.get(b.name)!);
});

async function load() {
  error.value = "";
  dirs.value = null;
  try {
    const res = await api.files(current.value);
    dirs.value = res.entries.filter((e) => e.dir);
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e);
  }
}

onMounted(load);

function go(rel: string) {
  if (rel === current.value) return;
  current.value = rel;
  query.value = "";
  cancelCreate();
  load();
}

function into(name: string) {
  go(current.value ? `${current.value}/${name}` : name);
}

function up() {
  go(segments.value.slice(0, -1).join("/"));
}

function jump(i: number) {
  go(segments.value.slice(0, i + 1).join("/"));
}

function confirm() {
  emit("create", current.value);
}

// --- inline new folder (Finder: a row appears with its name in edit) -------

const creating = ref(false);
const newName = ref("");
const createErr = ref("");
const newInput = ref<HTMLInputElement | null>(null);

async function startCreate() {
  creating.value = true;
  createErr.value = "";
  newName.value = t("workspacePicker.newFolder");
  await nextTick();
  newInput.value?.focus();
  newInput.value?.select();
}

function cancelCreate() {
  creating.value = false;
  createErr.value = "";
}

async function commitCreate() {
  if (!creating.value) return;
  const name = newName.value.trim();
  if (!name) {
    cancelCreate();
    return;
  }
  if (/[/\\]/.test(name)) {
    createErr.value = t("workspacePicker.invalidName");
    return;
  }
  try {
    await api.mkdir(current.value ? `${current.value}/${name}` : name);
  } catch (e) {
    createErr.value = e instanceof Error ? e.message : String(e);
    return;
  }
  cancelCreate();
  // The file panel caches listings; the new folder only shows up there if the
  // tree is told to drop them.
  invalidateTree();
  // A folder made in a workspace picker is almost always the one the user
  // wants to be in, so the dialog follows Finder's "create and select" one
  // step further: create and enter.
  into(name);
}

// Same compact convention as the file tree: today keeps the time only, this
// year drops the year.
function fmtTime(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const now = new Date();
  const p = (n: number) => String(n).padStart(2, "0");
  const hm = `${p(d.getHours())}:${p(d.getMinutes())}`;
  if (d.toDateString() === now.toDateString()) return hm;
  if (d.getFullYear() === now.getFullYear()) return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${hm}`;
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}
</script>

<template>
  <el-dialog v-model="visible" width="640px" class="ws-dialog" :show-close="false" @close="emit('close')">
    <template #header>
      <div class="head">
        <Icon class="hicon" :icon="folderOpenIcon" />
        <div>
          <div class="title">{{ t("workspacePicker.title") }}</div>
          <div class="sub">{{ t("workspacePicker.subtitle") }}</div>
        </div>
      </div>
    </template>

    <div class="crumbs">
      <button class="up" :disabled="!current" :title="t('workspacePicker.up')" @click="up">
        <el-icon><ArrowLeft /></el-icon>
      </button>
      <button class="crumb" :class="{ on: !current }" @click="go('')">
        <el-icon class="home"><HomeFilled /></el-icon>
        {{ t("workspacePicker.root") }}
      </button>
      <template v-for="(seg, i) in segments" :key="i">
        <span class="sep">/</span>
        <button class="crumb" :class="{ on: i === segments.length - 1 }" @click="jump(i)">{{ seg }}</button>
      </template>
      <button class="newdir" :title="t('workspacePicker.newFolderTip')" @click="startCreate">
        <Icon class="ndicon" :icon="folderPlusIcon" />
        {{ t("workspacePicker.newFolder") }}
      </button>
    </div>

    <div class="filter">
      <el-icon class="ficon"><Search /></el-icon>
      <input
        v-model="query"
        class="finput"
        :placeholder="t('workspacePicker.filterPlaceholder')"
        spellcheck="false"
        @keydown.enter="confirm"
      />
    </div>

    <div class="list">
      <div v-if="creating" class="row editing">
        <Icon class="fdir" :icon="folderIcon" />
        <input
          ref="newInput"
          v-model="newName"
          class="nameinput"
          spellcheck="false"
          @keydown.enter.prevent="commitCreate"
          @keydown.esc.stop="cancelCreate"
          @blur="commitCreate"
        />
      </div>
      <div v-if="createErr" class="create-err">{{ createErr }}</div>

      <div v-if="!dirs && !error" class="hint">{{ t("common.loading") }}</div>
      <button v-else-if="error" class="hint err" @click="load">{{ t("workspacePicker.loadFailed") }}</button>
      <template v-else>
        <button v-for="d in shown" :key="d.name" class="row" @click="into(d.name)">
          <Icon class="fdir" :icon="folderIcon" />
          <span class="dname">{{ d.name }}</span>
          <span class="dtime">{{ fmtTime(d.mtime_ms) }}</span>
        </button>
        <div v-if="!shown.length && !creating" class="hint">
          {{ query ? t("workspacePicker.noMatch") : t("workspacePicker.noSubdirs") }}
        </div>
      </template>
    </div>

    <template #footer>
      <div class="foot">
        <div class="picked" :title="cwd + (current ? '/' + current : '')">
          <span class="plabel">{{ t("workspacePicker.workspaceLabel") }}</span>
          <span class="pvalue">{{ current ? `/${current}` : t("workspacePicker.root") }}</span>
        </div>
        <div class="ops">
          <button class="btn" @click="visible = false">{{ t("common.cancel") }}</button>
          <button class="btn primary" @click="confirm">{{ t("workspacePicker.createSession") }}</button>
        </div>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped lang="scss">
.head {
  display: flex;
  align-items: center;
  gap: 10px;

  .hicon {
    width: 26px;
    height: 26px;
    flex: 0 0 auto;
  }

  .title {
    font-size: 15px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  .sub {
    margin-top: 2px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }
}

.crumbs {
  display: flex;
  align-items: center;
  gap: 2px;
  min-width: 0;
  margin-bottom: 10px;

  .up {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    margin-right: 4px;
    border: 1px solid var(--el-border-color);
    border-radius: 5px;
    background: var(--el-bg-color);
    color: var(--el-text-color-regular);
    cursor: pointer;

    &:disabled {
      opacity: 0.35;
      cursor: default;
    }

    &:not(:disabled):hover {
      border-color: var(--el-text-color-primary);
    }
  }

  .crumb {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    border: 0;
    background: transparent;
    padding: 2px 5px;
    border-radius: 4px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
    cursor: pointer;
    max-width: 140px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;

    .home {
      font-size: 12px;
    }

    &:hover {
      background: var(--el-fill-color-light);
    }

    &.on {
      color: var(--el-text-color-primary);
      font-weight: 600;
    }
  }

  .sep {
    color: var(--el-text-color-placeholder);
    font-size: 12px;
  }

  .newdir {
    margin-left: auto;
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: 1px solid var(--el-border-color);
    background: var(--el-bg-color);
    padding: 3px 8px;
    border-radius: 5px;
    font-size: 12px;
    color: var(--el-text-color-regular);
    cursor: pointer;

    .ndicon {
      width: 15px;
      height: 15px;
    }

    &:hover {
      border-color: var(--el-text-color-primary);
    }
  }
}

.filter {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 10px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  margin-bottom: 8px;

  &:focus-within {
    border-color: var(--el-text-color-primary);
  }

  .ficon {
    color: var(--el-text-color-secondary);
    font-size: 13px;
  }

  .finput {
    flex: 1;
    min-width: 0;
    border: 0;
    outline: none;
    padding: 7px 0;
    font-size: 13px;
    background: transparent;
    color: var(--el-text-color-primary);
  }
}

.list {
  height: 208px;
  overflow: auto;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 6px;
  padding: 4px;
}

.row {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 6px 8px;
  border-radius: 5px;
  cursor: pointer;
  text-align: left;

  &:hover {
    background: var(--el-fill-color-light);
  }

  &.editing {
    cursor: default;

    &:hover {
      background: transparent;
    }
  }

  .fdir {
    flex: 0 0 auto;
    width: 16px;
    height: 16px;
  }

  .dname {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 13px;
    color: var(--el-text-color-regular);
  }

  .dtime {
    flex: 0 0 auto;
    font-size: 11px;
    color: var(--el-text-color-secondary);
    font-variant-numeric: tabular-nums;
  }
}

.nameinput {
  flex: 1;
  min-width: 0;
  border: 1px solid var(--el-color-primary);
  border-radius: 4px;
  outline: none;
  padding: 2px 6px;
  font-size: 13px;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
}

.create-err {
  padding: 2px 8px 6px 31px;
  font-size: 11px;
  color: var(--el-color-danger);
}

.hint {
  display: block;
  width: 100%;
  border: 0;
  background: transparent;
  padding: 18px 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  text-align: center;

  &.err {
    color: var(--el-color-danger);
    cursor: pointer;
  }
}

.foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;

  .picked {
    display: flex;
    align-items: baseline;
    gap: 6px;
    min-width: 0;
    overflow: hidden;

    .plabel {
      flex: 0 0 auto;
      font-size: 11px;
      color: var(--el-text-color-secondary);
    }

    .pvalue {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-size: 12px;
      font-family: ui-monospace, monospace;
      color: var(--el-text-color-primary);
    }
  }

  .ops {
    flex: 0 0 auto;
    display: flex;
    gap: 8px;
  }

  .btn {
    padding: 6px 14px;
    border: 1px solid var(--el-border-color);
    border-radius: 6px;
    background: var(--el-bg-color);
    color: var(--el-text-color-regular);
    font-size: 13px;
    cursor: pointer;

    &:hover {
      border-color: var(--el-text-color-primary);
    }

    &.primary {
      background: var(--el-text-color-primary);
      border-color: var(--el-text-color-primary);
      color: var(--el-bg-color);

      &:hover {
        opacity: 0.85;
      }
    }
  }
}
</style>

<style lang="scss">
// Unscoped: el-dialog renders its own chrome, these just tighten it up. The
// 640px width against ~400px of content makes the panel 8:5-ish (≈1.6, the
// golden-rectangle neighbourhood) — wide and low, the shape file pickers
// have settled on since the macOS open panel.
.ws-dialog {
  border-radius: 10px;

  .el-dialog__header {
    padding: 16px 20px 12px;
    margin-right: 0;
  }

  .el-dialog__body {
    padding: 0 20px 12px;
  }

  .el-dialog__footer {
    padding: 12px 20px 14px;
    border-top: 1px solid var(--el-border-color-lighter);
  }
}
</style>
