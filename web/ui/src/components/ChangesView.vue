<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import { CaretRight, Document } from "@element-plus/icons-vue";
import DiffView from "./DiffView.vue";
import SideBySideDiff from "./SideBySideDiff.vue";
import { collectChanges, changedPathCount, collectIncomingWrites } from "@/agent/changes";
import { diffOf, isWriteDetails, type TimelineCall, type TimelineItem } from "@/agent/timeline";
import { api } from "@/api/client";
import type { WorkspaceChange, WorkspaceDiff } from "@/api/types";
import { baseName, fileIcon } from "./fileIcons";

// The changes tab, in two scopes: this session (projected from the timeline,
// grouped by turn) and the whole workspace (journaled pre-images, cumulative
// diff against the first touch). Same rows, same unified/side-by-side toggle —
// the two scopes differ only in where the data comes from. When the session
// has its own workspace subdirectory, the workspace scope narrows to it.
const props = defineProps<{ items: TimelineItem[]; workspace?: string }>();
const emit = defineEmits<{ preview: [string] }>();

const { t } = useI18n();

const scope = ref<"session" | "workspace">("session");
const side = ref(false);

// --- session scope (pure timeline projection) ------------------------------
const groups = computed(() => collectChanges(props.items));
const sessionTotal = computed(() => changedPathCount(groups.value));
// Writes whose arguments are still streaming: the row only says "a file is
// being written" until its change can settle into the groups above.
const incomingWrites = computed(() => collectIncomingWrites(props.items));

// --- workspace scope (journal API) ------------------------------------------
const wsChanges = ref<WorkspaceChange[] | null>(null);
const wsFailed = ref("");
// Per-path diff payloads, fetched lazily on expand and cached.
const wsDiffs = reactive(new Map<string, WorkspaceDiff>());
const wsLoading = reactive(new Set<string>());

async function loadWorkspace() {
  try {
    const res = await api.workspaceChanges();
    wsChanges.value = res.changes;
    wsFailed.value = "";
  } catch (e) {
    wsFailed.value = e instanceof Error ? e.message : String(e);
  }
}

// The journal is server-root-wide; with a session workspace set, only the
// subtree under it is this session's business.
const wsShown = computed(() => {
  const all = wsChanges.value ?? [];
  const ws = props.workspace ?? "";
  return ws ? all.filter((c) => c.path.startsWith(ws + "/")) : all;
});

watch(scope, (s) => {
  if (s === "workspace" && wsChanges.value === null) loadWorkspace();
});

// A tool_end in this session almost always means the journal moved, so the
// workspace list follows the session's own activity without polling.
const sessionTick = computed(() =>
  groups.value.reduce((n, g) => n + g.files.reduce((m, f) => m + f.calls.length, 0), 0),
);
watch(sessionTick, () => {
  if (scope.value === "workspace") loadWorkspace();
});

// --- shared expand/toggle mechanics -----------------------------------------
const openSet = reactive(new Set<string>());
function toggleKey(k: string) {
  if (openSet.has(k)) openSet.delete(k);
  else openSet.add(k);
}

function toggleWs(path: string) {
  toggleKey(`w:${path}`);
  if (!wsDiffs.has(path) && !wsLoading.has(path)) {
    wsLoading.add(path);
    api
      .workspaceDiff(path)
      .then((d) => wsDiffs.set(path, d))
      .catch(() => wsDiffs.set(path, { path, status: "?", added: 0, removed: 0, base_available: false }))
      .finally(() => wsLoading.delete(path));
  }
}

// --- clear baseline: two-step inline confirm, no modal ----------------------
const confirmClear = ref(false);
let clearTimer: number | undefined;

async function clearBaseline() {
  if (!confirmClear.value) {
    confirmClear.value = true;
    clearTimer = window.setTimeout(() => (confirmClear.value = false), 3000);
    return;
  }
  window.clearTimeout(clearTimer);
  confirmClear.value = false;
  try {
    await api.clearWorkspaceJournal();
    wsDiffs.clear();
    await loadWorkspace();
  } catch (e) {
    wsFailed.value = e instanceof Error ? e.message : String(e);
  }
}

/** The unified patch carried alongside the rendered diff; missing for a
 *  created file's write, which has nothing to diff against. */
function patchOf(call: TimelineCall): string | null {
  const d = call.result?.details;
  if (!d || !("patch" in d) || !d.patch) return null;
  return d.patch;
}

/** unifiedBody drops the ---/+++ header lines so DiffView's prefix colouring
 *  does not paint them as a deletion and an addition. */
function unifiedBody(patch: string): string {
  return patch.split("\n").slice(2).join("\n");
}

/** A write whose diff the server capped away keeps only its stats — the same
 *  situation as the workspace scope's too_big row. */
function cappedWrite(call: TimelineCall): boolean {
  const d = call.result?.details;
  return isWriteDetails(d) && !d.created && !d.diff;
}

const badgeText = computed<Record<string, string>>(() => ({
  added: t("changesView.badge.added"),
  modified: t("changesView.badge.modified"),
  deleted: t("changesView.badge.deleted"),
}));
</script>

<template>
  <div class="changes">
    <div class="scopes">
      <button class="scope" :class="{ on: scope === 'session' }" @click="scope = 'session'">{{ t("changesView.scopeSession") }}</button>
      <button class="scope" :class="{ on: scope === 'workspace' }" @click="scope = 'workspace'">{{ t("changesView.scopeWorkspace") }}</button>
      <span class="spacer" />
      <button class="mode" :class="{ on: !side }" @click="side = false">{{ t("changesView.modeUnified") }}</button>
      <button class="mode" :class="{ on: side }" @click="side = true">{{ t("changesView.modeSplit") }}</button>
    </div>

    <!-- ======================= session scope ======================= -->
    <div v-if="scope === 'session'">
      <div v-if="!groups.length && !incomingWrites.length" class="empty">{{ t("changesView.emptySession") }}</div>
      <template v-else>
        <!-- still-streaming writes: same row anatomy as a settled change —
             the +N climbs as content arrives; only the 写入中 badge says the
             diff is not computed yet. Muted and not clickable. -->
        <div v-for="w in incomingWrites" :key="`inc-${w.callId}`" class="file">
          <div class="row">
            <div class="main live">
              <Icon v-if="w.path" class="ficon" :icon="fileIcon(baseName(w.path))" />
              <el-icon v-else class="ficon"><Document /></el-icon>
              <span class="badge writing">{{ t("changesView.writing") }}</span>
              <span class="fpath" :title="w.path ?? undefined">{{ w.path ?? "…" }}</span>
              <span class="stat">
                <span class="plus">+{{ w.lines }}</span>
                <span class="minus">-0</span>
              </span>
            </div>
          </div>
        </div>
        <div v-if="groups.length" class="toolbar">
          <span class="total">{{ t("changesView.filesChanged", { n: sessionTotal }) }}</span>
        </div>
        <div v-for="g in groups" :key="g.turn" class="turn">
          <div class="turn-head">{{ t("changesView.turnHead", { turn: g.turn, n: g.files.length }) }}</div>
          <div v-for="f in g.files" :key="f.path" class="file">
            <div class="row">
              <button class="main" @click="toggleKey(`${g.turn}:${f.path}`)">
                <Icon class="ficon" :icon="fileIcon(baseName(f.path))" />
                <span class="badge" :class="f.status">{{ badgeText[f.status] }}</span>
                <span class="fpath" :title="f.path">{{ f.path }}</span>
                <span class="stat">
                  <span class="plus">+{{ f.added }}</span>
                  <span class="minus">-{{ f.removed }}</span>
                </span>
                <el-icon class="chev" :class="{ open: openSet.has(`${g.turn}:${f.path}`) }"><CaretRight /></el-icon>
              </button>
              <button class="peek" :title="t('changesView.viewFile')" @click="emit('preview', f.path)">
                <el-icon><Document /></el-icon>
              </button>
            </div>

            <div v-if="openSet.has(`${g.turn}:${f.path}`)" class="cards">
              <template v-for="call in f.calls" :key="call.callId">
                <SideBySideDiff v-if="side && patchOf(call)" :patch="patchOf(call)!" />
                <DiffView v-else-if="diffOf(call.result?.details)" v-bind="diffOf(call.result?.details)!" />
                <div v-else-if="cappedWrite(call)" class="nodiff">{{ t("changesView.diffTooBig") }}</div>
                <div v-else class="nodiff">
                  {{ t("changesView.newFileNoDiff") }}
                  <button class="link" @click="emit('preview', f.path)">{{ t("changesView.viewFile") }}</button>
                </div>
              </template>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- ====================== workspace scope ======================= -->
    <!-- Keyed on the workspace: switching sessions re-enters with the same
         short fade the conversation area uses, never a hard row swap. -->
    <div v-else class="wsscope" :key="workspace ?? ''">
      <div v-if="wsFailed" class="empty bad">{{ wsFailed }}</div>
      <div v-else-if="!wsChanges" class="empty">{{ t("common.loading") }}</div>
      <div v-else-if="!wsShown.length" class="empty">{{ t("changesView.emptyWorkspace") }}</div>
      <template v-else>
        <div class="toolbar">
          <span class="total">{{ t("changesView.sinceBaseline", { n: wsShown.length }) }}</span>
          <span class="spacer" />
          <button class="clear" :class="{ confirm: confirmClear }" @click="clearBaseline">
            {{ confirmClear ? t("changesView.confirmClear") : t("changesView.clearBaseline") }}
          </button>
        </div>
        <div v-for="c in wsShown" :key="c.path" class="file">
          <div class="row">
            <button class="main" @click="toggleWs(c.path)">
              <Icon class="ficon" :icon="fileIcon(baseName(c.path))" />
              <span class="badge" :class="c.status">{{ badgeText[c.status] ?? "?" }}</span>
              <span class="fpath" :title="c.path">{{ c.path }}</span>
              <span class="stat">
                <span class="plus">+{{ c.added }}</span>
                <span class="minus">-{{ c.removed }}</span>
              </span>
              <el-icon class="chev" :class="{ open: openSet.has(`w:${c.path}`) }"><CaretRight /></el-icon>
            </button>
            <button v-if="c.status !== 'deleted'" class="peek" :title="t('changesView.viewFile')" @click="emit('preview', c.path)">
              <el-icon><Document /></el-icon>
            </button>
          </div>

          <div v-if="openSet.has(`w:${c.path}`)" class="cards">
            <div v-if="wsLoading.has(c.path)" class="nodiff">{{ t("common.loading") }}</div>
            <template v-else-if="wsDiffs.get(c.path)">
              <div v-if="!wsDiffs.get(c.path)!.base_available" class="nodiff">
                {{ t("changesView.baseUnavailable") }}
              </div>
              <div v-else-if="wsDiffs.get(c.path)!.too_big" class="nodiff">{{ t("changesView.diffTooBig") }}</div>
              <SideBySideDiff v-else-if="side" :patch="wsDiffs.get(c.path)!.patch!" />
              <DiffView
                v-else
                :diff="unifiedBody(wsDiffs.get(c.path)!.patch!)"
                :path="c.path"
                :added="c.added"
                :removed="c.removed"
              />
            </template>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped lang="scss">
.changes {
  font-size: 13px;
}

/* Session-switch fade for the workspace scope: the div is keyed on the
   workspace, so a switch re-creates it and this enter animation plays.
   transform/opacity only. */
.wsscope {
  animation: wsscope-in 0.16s ease;
}

@keyframes wsscope-in {
  from {
    opacity: 0;
    transform: translateY(3px);
  }

  to {
    opacity: 1;
    transform: none;
  }
}

.scopes {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 4px 10px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  margin-bottom: 6px;

  .spacer {
    flex: 1;
  }
}

.scope {
  border: 0;
  background: transparent;
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
    font-weight: 600;
  }
}

.mode {
  border: 0;
  background: transparent;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &.on {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.empty {
  padding: 16px 12px;
  font-size: 12px;
  color: var(--el-text-color-secondary);

  &.bad {
    color: var(--el-color-danger);
  }
}

.toolbar {
  display: flex;
  align-items: center;
  padding: 4px 10px 8px;

  .total {
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }

  .spacer {
    flex: 1;
  }
}

.clear {
  border: 0;
  background: transparent;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }

  &.confirm {
    color: var(--el-color-danger);
    background: color-mix(in srgb, var(--el-color-danger) 10%, transparent);
  }
}

.turn-head {
  padding: 6px 10px 4px;
  font-size: 11px;
  font-weight: 600;
  color: var(--el-text-color-secondary);
}

.file {
  margin: 0 6px 4px;
}

.row {
  display: flex;
  align-items: center;

  .main {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 6px;
    border: 0;
    background: transparent;
    padding: 4px 6px;
    border-radius: 4px;
    cursor: pointer;
    text-align: left;

    &:hover {
      background: var(--el-fill-color-light);
    }

    /* A still-streaming write is a label, not a button. */
    &.live {
      cursor: default;

      &:hover {
        background: transparent;
      }
    }
  }
}

/* Same pulse as the IncomingArgs preview card's dot, here on the badge of a
   still-streaming write. */
@keyframes pulse {
  50% {
    opacity: 0.25;
  }
}

.badge.writing {
  font-weight: 600;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color);
  animation: pulse 1.6s ease-in-out infinite;
}

.ficon {
  flex: 0 0 auto;
  width: 15px;
  height: 15px;
}

.badge {
  flex: 0 0 auto;
  min-width: 16px;
  height: 16px;
  padding: 0 4px;
  box-sizing: border-box;
  border-radius: 3px;
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;

  &.added {
    color: var(--el-color-success);
    background: color-mix(in srgb, var(--el-color-success) 15%, transparent);
  }

  &.modified {
    color: var(--el-color-warning);
    background: color-mix(in srgb, var(--el-color-warning) 15%, transparent);
  }

  &.deleted {
    color: var(--el-color-danger);
    background: color-mix(in srgb, var(--el-color-danger) 15%, transparent);
  }
}

.fpath {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--el-text-color-regular);
}

.stat {
  margin-left: auto;
  display: flex;
  gap: 6px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  flex: 0 0 auto;

  .plus {
    color: var(--el-color-success);
  }

  .minus {
    color: var(--el-color-danger);
  }
}

.chev {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  transition: transform 0.12s;

  &.open {
    transform: rotate(90deg);
  }
}

.peek {
  flex: 0 0 auto;
  display: inline-flex;
  border: 0;
  background: transparent;
  padding: 4px;
  border-radius: 4px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }
}

.cards {
  margin: 2px 6px 8px 22px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.nodiff {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  padding: 6px 8px;
  background: var(--el-fill-color-lighter);
  border-radius: 6px;

  .link {
    border: 0;
    background: transparent;
    color: var(--el-text-color-primary);
    text-decoration: underline;
    cursor: pointer;
    font-size: 12px;
    padding: 0 2px;
  }
}
</style>
