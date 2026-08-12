<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Icon } from "@iconify/vue";
import FilesPanel from "./FilesPanel.vue";
import ShellPanel from "./ShellPanel.vue";
import { folderIcon, terminalIcon } from "./fileIcons";
import type { PanelInfo } from "@/api/types";
import type { TimelineItem } from "@/agent/timeline";

const { t } = useI18n();

// The dock is a sheet container: every openable thing — workspace files, the
// session shell, and each registered -web-panel — is a sheet, and exactly one
// is visible at a time. A slim rail (VS Code activity-bar style) is always
// visible at the window's right edge and switches sheets; clicking the active
// icon collapses the dock.
//
// Sheet ids: "files" | "shell" | "panel:<name>". The panel sheet is an iframe
// loading /panels/<name>/, reverse-proxied by the server (same origin).
const props = defineProps<{
  active: string | null;
  panels: PanelInfo[];
  layout: "right" | "bottom";
  items: TimelineItem[];
  workspace?: string;
  sessionId: string | null;
}>();
const emit = defineEmits<{
  "update:active": [string | null];
  "update:layout": ["right" | "bottom"];
}>();

const open = computed(() => props.active !== null);

function toggle(id: string) {
  emit("update:active", props.active === id ? null : id);
}

const panelName = computed(() =>
  props.active?.startsWith("panel:") ? props.active.slice("panel:".length) : null,
);
const panelPath = computed(() => {
  const p = props.panels.find((p) => p.name === panelName.value);
  return p?.path ?? null;
});

// Generic app-window icon for external panels (lucide-style stroke).
const panelIcon = {
  body:
    '<rect x="3" y="4" width="18" height="16" rx="2" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<path d="M3 9h18" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<circle cx="6.5" cy="6.5" r="0.5" fill="currentColor"/>' +
    '<circle cx="9.5" cy="6.5" r="0.5" fill="currentColor"/>',
  width: 24,
  height: 24,
};

// The dock's width is a fraction of the space left after the sidebar, not a
// fixed pixel count: collapsing the sidebar or resizing the window must keep
// the split (default is an even 1/2). Drags update the fraction; the pixel
// width is derived from the measured parent.
const RATIO_KEY = "pi-go:files-ratio";
// The 0.5 cap is a hard lock, not a default: the conversation never shrinks
// below half of the post-sidebar space, no matter where the drag ends.
const ratio = ref(Math.min(0.5, Math.max(0.15, Number(localStorage.getItem(RATIO_KEY)) || 0.5)));
watch(ratio, (r) => localStorage.setItem(RATIO_KEY, String(r)));
localStorage.removeItem("pi-go:files-width");

// The bottom dock's size is a height; same persistence rule as the width.
const HEIGHT_KEY = "pi-go:files-height";
const height = ref(Number(localStorage.getItem(HEIGHT_KEY)) || 340);
watch(height, (h) => localStorage.setItem(HEIGHT_KEY, String(h)));

const width = ref(0);
const panelEl = ref<HTMLElement | null>(null);
let panelRO: ResizeObserver | undefined;

function availableWidth(): number {
  const parent = panelEl.value?.parentElement;
  if (!parent) return 0;
  const sidebar = parent.querySelector(".sidebar");
  return parent.clientWidth - (sidebar?.clientWidth ?? 0);
}

// applyRatio derives the pixel width from the fraction. It runs on every
// relevant resize: the observer covers window resizes and the sidebar's own
// collapse, the watchers cover layout flips and drag updates.
function applyRatio() {
  if (props.layout !== "right") return;
  const avail = availableWidth();
  if (avail <= 0) return;
  width.value = Math.round(Math.min(avail * 0.5, Math.max(240, ratio.value * avail)));
}
watch([ratio, () => props.layout], applyRatio);

onMounted(() => {
  const parent = panelEl.value?.parentElement;
  if (parent) {
    panelRO = new ResizeObserver(applyRatio);
    panelRO.observe(parent);
    const sidebar = parent.querySelector(".sidebar");
    if (sidebar) panelRO.observe(sidebar);
  }
  applyRatio();
});
onBeforeUnmount(() => panelRO?.disconnect());

// The rail stays at the edge even when no sheet is open: it is the only way
// to open one. So the aside's size is sheet size + rail, or rail alone.
const RAIL = 40;
const panelStyle = computed(() =>
  props.layout === "bottom"
    ? { height: open.value ? `${height.value + RAIL}px` : `${RAIL}px` }
    : { width: open.value ? `${width.value}px` : `${RAIL}px` },
);

const dragging = ref(false);

// Dragging the docked edge resizes: the left edge (col-resize) when docked
// right, the top edge (row-resize) when docked bottom. The transition is
// suspended for the drag or it lags behind.
function startDrag(e: MouseEvent) {
  e.preventDefault();
  const vertical = props.layout === "bottom";
  const startPos = vertical ? e.clientY : e.clientX;
  const startSize = vertical ? height.value : width.value;
  const avail = vertical ? 0 : availableWidth();
  dragging.value = true;
  const move = (ev: MouseEvent) => {
    if (vertical) {
      height.value = Math.min(window.innerHeight * 0.5, Math.max(200, startSize + (startPos - ev.clientY)));
    } else {
      // The drag sets pixels live; the fraction follows so the next sidebar
      // collapse or window resize rebuilds the same split.
      width.value = Math.round(Math.min(avail * 0.5, Math.max(240, startSize + (startPos - ev.clientX))));
      if (avail > 0) ratio.value = width.value / avail;
    }
  };
  const up = () => {
    dragging.value = false;
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", up);
  };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", up);
}
</script>

<template>
  <aside ref="panelEl" class="dock" :class="{ open, dragging, bottom: layout === 'bottom' }" :style="panelStyle">
    <div v-if="open" class="grip" @mousedown="startDrag" />
    <div v-if="open" class="dock-inner">
      <div class="slot">
        <FilesPanel
          v-if="active === 'files'"
          :items="items"
          :layout="layout"
          :workspace="workspace"
          @close="emit('update:active', null)"
          @update:layout="(v) => emit('update:layout', v)"
        />
        <ShellPanel
          v-else-if="active === 'shell'"
          :session-id="sessionId"
          :layout="layout"
          :workspace="workspace"
          @close="emit('update:active', null)"
          @update:layout="(v) => emit('update:layout', v)"
        />
        <section v-else-if="panelName && panelPath" class="panel-sheet">
          <header class="sheet-head">
            <span class="sheet-title" :title="panelPath">{{ panelName }}</span>
            <button
              class="ghost layout-btn"
              :title="layout === 'right' ? '底部布局' : '右侧布局'"
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
            <button class="ghost layout-btn" title="关闭" @click="emit('update:active', null)">✕</button>
          </header>
          <iframe class="sheet-frame" :src="panelPath" :title="panelName" />
        </section>
      </div>
    </div>
    <nav class="rail" :class="{ bottom: layout === 'bottom' }">
      <button
        class="rail-btn"
        :class="{ on: active === 'files' }"
        :title="t('agentView.topbar.files')"
        @click="toggle('files')"
      >
        <Icon :icon="folderIcon" width="18" />
      </button>
      <button
        class="rail-btn"
        :class="{ on: active === 'shell' }"
        title="Shell"
        @click="toggle('shell')"
      >
        <Icon :icon="terminalIcon" width="18" />
      </button>
      <button
        v-for="p in panels"
        :key="p.name"
        class="rail-btn"
        :class="{ on: active === `panel:${p.name}` }"
        :title="p.name"
        @click="toggle(`panel:${p.name}`)"
      >
        <Icon :icon="panelIcon" width="18" />
      </button>
    </nav>
  </aside>
</template>

<style scoped lang="scss">
.dock {
  position: relative;
  flex: 0 0 auto;
  overflow: hidden;
  transition: width 0.18s ease;
  display: flex;

  &.dragging {
    transition: none;
  }

  &.open {
    /* Pure black divider: on an all-white panel the light-grey line read as
       no boundary at all. */
    border-left: 1px solid #000;
  }

  /* Bottom dock: sized by height, grip on the top edge. In the grid shell the
     aside stretches to full width on its own. */
  &.bottom {
    transition: height 0.18s ease;
    flex-direction: column;

    &.dragging {
      transition: none;
    }

    &.open {
      border-top: 1px solid #000;
      border-left: 0;
    }

    .grip {
      top: 0;
      right: 0;
      bottom: auto;
      left: 0;
      width: auto;
      height: 5px;
      cursor: row-resize;
    }

    .dock-inner {
      width: 100%;
    }

    .rail {
      width: 100%;
      height: 40px;
      flex-direction: row;
      border-left: 0;
      border-top: 1px solid var(--el-border-color-lighter);
    }
  }
}

.grip {
  position: absolute;
  left: 0;
  top: 0;
  bottom: 0;
  width: 5px;
  cursor: col-resize;
  z-index: 2;

  &:hover {
    background: var(--el-fill-color);
  }
}

.dock-inner {
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
}

.slot {
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  display: flex;
  overflow: hidden;

  > * {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
  }
}

// The sheet rail: always visible, this is how sheets are opened at all.
.rail {
  flex: 0 0 40px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 6px 0;
  border-left: 1px solid var(--el-border-color-lighter);
  background: var(--el-fill-color-lighter);
}

.rail-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  &.on {
    background: var(--el-fill-color-darker);
    color: var(--el-text-color-primary);
  }
}

.panel-sheet {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.sheet-head {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  flex: 0 0 auto;
}

.sheet-title {
  flex: 1 1 0;
  font-size: 13px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.layout-btn {
  border: 0;
  background: transparent;
  cursor: pointer;
  color: var(--el-text-color-secondary);
  padding: 2px 4px;
  border-radius: 4px;

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

.sheet-frame {
  flex: 1 1 0;
  border: 0;
  min-height: 0;
}
</style>
