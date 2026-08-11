<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import FilesPanel from "./FilesPanel.vue";
import ShellPanel from "./ShellPanel.vue";
import type { TimelineItem } from "@/agent/timeline";

// The dock hosts the two side panels — workspace files and the session shell —
// and owns every pixel of the arrangement: the outer size (a persisted
// fraction of the post-sidebar space), the open/close slide, and the divider
// between the panels when both are open.
//
// Right dock: files on top, shell below, split by a horizontal divider.
// Bottom dock: files left, shell right, split vertically. One panel alone
// takes the whole dock.
const props = defineProps<{
  filesOpen: boolean;
  shellOpen: boolean;
  layout: "right" | "bottom";
  items: TimelineItem[];
  workspace?: string;
  sessionId: string | null;
}>();
const emit = defineEmits<{
  "close-files": [];
  "close-shell": [];
  "update:layout": ["right" | "bottom"];
}>();

const open = computed(() => props.filesOpen || props.shellOpen);
const both = computed(() => props.filesOpen && props.shellOpen);

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

// split is the files panel's share when both are open: height fraction in the
// right dock, width fraction in the bottom one.
const SPLIT_KEY = "pi-go:dock-split";
const split = ref(Math.min(0.8, Math.max(0.2, Number(localStorage.getItem(SPLIT_KEY)) || 0.5)));
watch(split, (s) => localStorage.setItem(SPLIT_KEY, String(s)));

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

const panelStyle = computed(() =>
  props.layout === "bottom"
    ? { height: open.value ? `${height.value}px` : "0px" }
    : { width: open.value ? `${width.value}px` : "0px" },
);
const innerStyle = computed(() => (props.layout === "bottom" ? { width: "100%" } : { width: `${width.value}px` }));

// With both panels open the first slot takes its split share and the second
// stretches over the rest; alone, each fills the dock.
const filesSlotStyle = computed(() => {
  if (!both.value) return {};
  return props.layout === "bottom" ? { width: `${split.value * 100}%` } : { height: `${split.value * 100}%` };
});

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

// The middle divider: perpendicular to the dock's own grip — horizontal
// (row-resize) in the right dock, vertical (col-resize) in the bottom one.
function startSplit(e: MouseEvent) {
  e.preventDefault();
  const el = panelEl.value;
  if (!el) return;
  const vertical = props.layout === "right"; // dragging adjusts a height share
  const startPos = vertical ? e.clientY : e.clientX;
  const startSplit = split.value;
  const span = vertical ? el.clientHeight : el.clientWidth;
  if (span <= 0) return;
  dragging.value = true;
  const move = (ev: MouseEvent) => {
    const delta = ((vertical ? ev.clientY : ev.clientX) - startPos) / span;
    split.value = Math.min(0.8, Math.max(0.2, startSplit + delta));
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
    <div class="dock-inner" :class="{ row: layout === 'bottom' }" :style="innerStyle">
      <div v-if="filesOpen" class="slot" :style="filesSlotStyle">
        <FilesPanel
          :items="items"
          :layout="layout"
          :workspace="workspace"
          @close="emit('close-files')"
          @update:layout="(v) => emit('update:layout', v)"
        />
      </div>
      <div v-if="both" class="divider" @mousedown="startSplit" />
      <div v-if="shellOpen" class="slot grow">
        <ShellPanel
          :session-id="sessionId"
          :layout="layout"
          :workspace="workspace"
          @close="emit('close-shell')"
          @update:layout="(v) => emit('update:layout', v)"
        />
      </div>
    </div>
  </aside>
</template>

<style scoped lang="scss">
.dock {
  position: relative;
  flex: 0 0 auto;
  overflow: hidden;
  transition: width 0.18s ease;

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
  height: 100%;
  display: flex;
  flex-direction: column;

  &.row {
    flex-direction: row;
  }
}

.slot {
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  display: flex;
  overflow: hidden;

  &.grow {
    flex: 1 1 0;
  }

  > * {
    flex: 1 1 0;
    min-width: 0;
    min-height: 0;
  }
}

.divider {
  flex: 0 0 5px;
  background: var(--el-border-color-lighter);
  cursor: row-resize;
  z-index: 2;

  .dock-inner.row > & {
    cursor: col-resize;
  }

  &:hover {
    background: var(--el-fill-color-darker);
  }
}
</style>
