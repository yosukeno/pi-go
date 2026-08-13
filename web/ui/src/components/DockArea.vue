<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { ArrowDown, Check, Close } from "@element-plus/icons-vue";
import { Icon } from "@iconify/vue";
import FilesPanel from "./FilesPanel.vue";
import ShellPanel from "./ShellPanel.vue";
import { hubTenants, resolveTenant, type Tenant } from "./dockSheets";
import { parsePanelMessage } from "@/agent/panelBridge";
import { terminalIcon, workspaceIcon } from "./fileIcons";
import type { PanelInfo } from "@/api/types";
import type { TimelineItem } from "@/agent/timeline";

const { t } = useI18n();

// The dock is a two-sheet container docked right (default) or along the bottom.
// A slim rail is always visible and switches sheets; clicking the active icon
// collapses the dock. The rail is deliberately fixed at two buttons — see
// dockSheets.ts for why the shell and every -web-panel moved inside the hub
// instead of each claiming one.
const props = defineProps<{
  active: string | null;
  /** Which hub tenant to show: "shell" or "panel:<name>". */
  tenant: string | null;
  /** Hash route to open the panel tenant at, e.g. "#/clusters". */
  at?: string | null;
  /** Hub fills the whole post-sidebar area; only honoured while the hub is open. */
  maximized: boolean;
  panels: PanelInfo[];
  layout: "right" | "bottom";
  items: TimelineItem[];
  workspace?: string;
  sessionId: string | null;
}>();
const emit = defineEmits<{
  "update:active": [string | null];
  "update:tenant": [string];
  "update:maximized": [boolean];
  "update:layout": ["right" | "bottom"];
  /** A panel asked the conversation something; the text is not yet policy-checked. */
  intent: [string];
}>();

const open = computed(() => props.active !== null);
const hubOpen = computed(() => props.active === "hub");

function toggle(id: string) {
  emit("update:active", props.active === id ? null : id);
}

// Generic app-window icon: the hub's rail icon while collapsed, and the icon of
// every iframe tenant in the menu.
const panelIcon = {
  body:
    '<rect x="3" y="4" width="18" height="16" rx="2" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<path d="M3 9h18" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<circle cx="6.5" cy="6.5" r="0.5" fill="currentColor"/>' +
    '<circle cx="9.5" cy="6.5" r="0.5" fill="currentColor"/>',
  width: 24,
  height: 24,
};

const tenants = computed(() => hubTenants(props.panels, t("agentView.dock.shell")));
const tenant = computed(() => resolveTenant(tenants.value, props.tenant));
const tenantIcon = (tn: Tenant) => (tn.kind === "shell" ? terminalIcon : panelIcon);

// Collapsed, the rail shows the generic hub icon rather than the remembered
// tenant's: an icon that changes while the dock is shut would read as a button
// that does different things on different days. Open, it mirrors the tenant,
// which is what gives the single button back some of the at-a-glance identity
// that per-panel icons used to carry.
const hubIcon = computed(() =>
  hubOpen.value && tenant.value ? tenantIcon(tenant.value) : panelIcon,
);
const hubTitle = computed(() =>
  tenant.value
    ? `${t("agentView.dock.hub")} · ${tenant.value.label}`
    : t("agentView.dock.hub"),
);

// A deep link is appended to the tenant's own path rather than replacing it, so
// the panel keeps loading from the same reverse-proxied prefix. Panels route on
// the hash precisely so they can be mounted under a prefix they do not know.
const frameSrc = computed(() => {
  const path = tenant.value?.path;
  if (!path) return undefined;
  return props.at ? path + props.at : path;
});

// The panel bridge lives here rather than in the view because the two checks that
// matter need the iframe element, and this component is the only thing that has
// it. See agent/panelBridge.ts for why each check exists.
const frameEl = ref<HTMLIFrameElement | null>(null);

function onPanelMessage(e: MessageEvent) {
  // 1. Panels are reverse-proxied to this origin; anything else is not a panel.
  if (e.origin !== window.location.origin) return;
  // 2. Origin alone would also accept the page itself and any other same-origin
  //    frame, so the sender must be this iframe. No iframe, no bridge.
  const frame = frameEl.value;
  if (!frame || !e.source || e.source !== frame.contentWindow) return;

  const parsed = parsePanelMessage(e.data);
  if (!parsed.ok) {
    // Same-origin pages produce plenty of unrelated postMessage traffic, so a
    // malformed payload is only worth reporting when it was aimed at us.
    if (parsed.reason !== "not-rpc") {
      console.warn(`panel bridge: ignored message (${parsed.reason})`);
    }
    return;
  }
  emit("intent", parsed.text);
  // 4. A reply names its target origin; "*" would post it to whatever the frame
  //    navigated to in the meantime.
  if (parsed.id !== null) {
    frame.contentWindow?.postMessage(
      { jsonrpc: "2.0", id: parsed.id, result: {} },
      window.location.origin,
    );
  }
}

const pickerOpen = ref(false);

function pickTenant(tn: Tenant) {
  pickerOpen.value = false;
  emit("update:tenant", tn.id);
  if (!hubOpen.value) emit("update:active", "hub");
}

// Maximize is an explicit, temporary override of the 0.5 conversation floor
// below — a force graph or a wide table is worth the whole width for a moment.
// It is scoped to the hub because that is where the sheets that need it live,
// and it is not persisted: a maximized dock on next boot would hide the
// conversation with nothing on screen to explain why.
const maximized = computed(() => props.maximized && hubOpen.value);

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
const availW = ref(0);
const availH = ref(0);
const panelEl = ref<HTMLElement | null>(null);
let panelRO: ResizeObserver | undefined;

// measure caches the space the dock may take: everything right of the sidebar
// for the side dock, the shell's height for the bottom one. Maximize reads it
// from a computed, so it has to live in refs rather than be measured on demand.
function measure() {
  const parent = panelEl.value?.parentElement;
  if (!parent) return;
  const sidebar = parent.querySelector(".sidebar");
  availW.value = parent.clientWidth - (sidebar?.clientWidth ?? 0);
  availH.value = parent.clientHeight;
  applyRatio();
}

// applyRatio derives the pixel width from the fraction. It runs on every
// relevant resize: the observer covers window resizes and the sidebar's own
// collapse, the watchers cover layout flips and drag updates.
function applyRatio() {
  if (props.layout !== "right") return;
  if (availW.value <= 0) return;
  width.value = Math.round(Math.min(availW.value * 0.5, Math.max(240, ratio.value * availW.value)));
}
watch([ratio, () => props.layout], measure);

onMounted(() => {
  const parent = panelEl.value?.parentElement;
  if (parent) {
    panelRO = new ResizeObserver(measure);
    panelRO.observe(parent);
    const sidebar = parent.querySelector(".sidebar");
    if (sidebar) panelRO.observe(sidebar);
  }
  measure();
  window.addEventListener("message", onPanelMessage);
});
onBeforeUnmount(() => {
  panelRO?.disconnect();
  window.removeEventListener("message", onPanelMessage);
});

// The rail stays at the edge even when no sheet is open: it is the only way
// to open one. So the aside's size is sheet size + rail, or rail alone.
const RAIL = 40;
const panelStyle = computed(() => {
  if (!open.value) {
    return props.layout === "bottom" ? { height: `${RAIL}px` } : { width: `${RAIL}px` };
  }
  if (maximized.value) {
    return props.layout === "bottom"
      ? { height: `${Math.max(200, availH.value)}px` }
      : { width: `${Math.max(240, availW.value)}px` };
  }
  return props.layout === "bottom"
    ? { height: `${height.value + RAIL}px` }
    : { width: `${width.value}px` };
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
  const avail = vertical ? 0 : availW.value;
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
  <aside
    ref="panelEl"
    class="dock"
    :class="{ open, dragging, maximized, bottom: layout === 'bottom' }"
    :style="panelStyle"
  >
    <div v-if="open && !maximized" class="grip" @mousedown="startDrag" />
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
        <!-- The hub: one header for whichever tenant is showing, so switching
             occupant does not move the layout and close controls around. -->
        <section v-else-if="hubOpen" class="hub-sheet">
          <header class="sheet-head">
            <el-popover
              v-model:visible="pickerOpen"
              placement="bottom-start"
              trigger="click"
              :width="216"
              :teleported="false"
              popper-class="tenant-pop"
            >
              <template #reference>
                <button class="tenant-btn" :title="t('agentView.dock.switchTenant')">
                  <Icon v-if="tenant" :icon="tenantIcon(tenant)" width="14" />
                  <span class="tenant-name">{{ tenant?.label }}</span>
                  <el-icon class="caret" :class="{ open: pickerOpen }"><ArrowDown /></el-icon>
                </button>
              </template>
              <div class="list">
                <div
                  v-for="tn in tenants"
                  :key="tn.id"
                  class="item"
                  :class="{ active: tn.id === tenant?.id }"
                  @click="pickTenant(tn)"
                >
                  <Icon :icon="tenantIcon(tn)" width="14" />
                  <span class="item-title">{{ tn.label }}</span>
                  <el-icon v-if="tn.id === tenant?.id" class="tick"><Check /></el-icon>
                </div>
              </div>
            </el-popover>
            <button
              class="ghost head-btn"
              :title="maximized ? t('agentView.dock.restore') : t('agentView.dock.maximize')"
              @click="emit('update:maximized', !maximized)"
            >
              <svg v-if="!maximized" width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
                <path d="M6 2.5H2.5V6M9 12.5H12.5V9" stroke="currentColor" stroke-linecap="round" />
                <path d="M8.5 6.5L12.5 2.5M6.5 8.5L2.5 12.5" stroke="currentColor" stroke-linecap="round" />
              </svg>
              <svg v-else width="15" height="15" viewBox="0 0 15 15" fill="none" aria-hidden="true">
                <path d="M9.5 6V2.5M9.5 6H6M5.5 9V12.5M5.5 9H9" stroke="currentColor" stroke-linecap="round" />
                <path d="M13 2.5L9.5 6M2 12.5L5.5 9" stroke="currentColor" stroke-linecap="round" />
              </svg>
            </button>
            <button
              class="ghost head-btn"
              :title="layout === 'right' ? t('agentView.dock.toStacked') : t('agentView.dock.toSideBySide')"
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
            <button class="ghost head-btn" :title="t('common.close')" @click="emit('update:active', null)">
              <el-icon><Close /></el-icon>
            </button>
          </header>
          <!-- Tenants render body only: the header above is theirs. -->
          <ShellPanel
            v-if="tenant?.kind === 'shell'"
            :header="false"
            :session-id="sessionId"
            :layout="layout"
            :workspace="workspace"
            @close="emit('update:active', null)"
            @update:layout="(v) => emit('update:layout', v)"
          />
          <iframe
            v-else-if="tenant?.path"
            ref="frameEl"
            class="sheet-frame"
            :src="frameSrc"
            :title="tenant.label"
          />
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
        <Icon :icon="workspaceIcon" width="18" />
      </button>
      <button class="rail-btn" :class="{ on: hubOpen }" :title="hubTitle" @click="toggle('hub')">
        <Icon :icon="hubIcon" width="18" />
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

.hub-sheet {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;

  /* The tenant fills what the header leaves. */
  > :not(.sheet-head) {
    flex: 1 1 0;
    min-height: 0;
  }
}

.sheet-head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
  flex: 0 0 auto;
}

// The tenant switcher doubles as the sheet title: what is showing and how to
// change it are the same control, which is the browser side-panel pattern.
.tenant-btn {
  flex: 1 1 0;
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 0;
  background: transparent;
  padding: 3px 6px;
  border-radius: 4px;
  font: inherit;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }

  .tenant-name {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .caret {
    font-size: 12px;
    color: var(--el-text-color-secondary);
    transition: transform 0.15s ease;

    &.open {
      transform: rotate(180deg);
    }
  }
}

.list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 4px;
  font-size: 13px;
  cursor: pointer;

  &:hover {
    background: var(--el-fill-color);
  }

  &.active {
    color: var(--el-color-primary);
  }

  .item-title {
    flex: 1 1 0;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .tick {
    font-size: 12px;
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
    color: var(--el-text-color-primary);
  }
}

.head-btn svg {
  display: block;
}

.sheet-frame {
  border: 0;
  min-height: 0;
}
</style>
