<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { Close } from "@element-plus/icons-vue";
import { Icon } from "@iconify/vue";
import { terminalIcon } from "./fileIcons";
import { token } from "@/api/client";
import { cssVar, themeVersion } from "@/theme";
import type { Terminal as XTerm } from "@xterm/xterm";
import type { FitAddon as FitAddonT } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

// The session's own shell: xterm.js in front, a pty on the server behind a
// websocket. xterm is dynamically imported on first open — 90KB gzipped that
// nobody pays for until the panel exists. The shell outlives this component
// (detaching only closes the socket), so reopening or switching sessions and
// coming back replays the server's backlog instead of starting over.
const props = withDefaults(
  defineProps<{
    sessionId: string | null;
    layout: "right" | "bottom";
    workspace?: string;
    /**
     * Draw the panel's own title bar. False when the shell is a tenant of the
     * dock's hub, which supplies one header for whichever tenant is showing —
     * two stacked title bars would spend twice the height saying it once.
     */
    header?: boolean;
  }>(),
  { header: true },
);
const emit = defineEmits<{ close: []; "update:layout": ["right" | "bottom"] }>();
const { t } = useI18n();

const host = ref<HTMLElement | null>(null);
let term: XTerm | null = null;
let fit: FitAddonT | null = null;
let ws: WebSocket | null = null;
let ro: ResizeObserver | undefined;
let retryMs = 1000;
let retryTimer: ReturnType<typeof setTimeout> | undefined;

/**
 * The emulator's colours, resolved from the skin.
 *
 * Only the surface and the foreground: the 16 ANSI slots are left at xterm's own
 * defaults on purpose. Those are what a program's own colour codes mean, and a
 * skin repainting them would change what the shell said, not how the panel looks.
 */
function termTheme() {
  return {
    background: cssVar("--pg-term-bg", "#1e1e1e"),
    foreground: cssVar("--pg-term-fg", "#d6d7db"),
    cursor: cssVar("--pg-term-fg", "#d6d7db"),
  };
}
let disposed = false;
let dead = false; // the shell exited; the next keystroke spawns a fresh one

function wsURL(sid: string): string {
  const proto = location.protocol === "https:" ? "wss" : "ws";
  return `${proto}://${location.host}/api/sessions/${sid}/terminal?token=${encodeURIComponent(token)}`;
}

async function ensureTerminal() {
  if (term || !host.value) return;
  // Dynamic import: the emulator is the panel's whole weight, and the panel
  // is closed for most sessions.
  const [{ Terminal }, fitMod, linksMod] = await Promise.all([
    import("@xterm/xterm"),
    import("@xterm/addon-fit"),
    import("@xterm/addon-web-links"),
  ]);
  if (disposed || !host.value) return;
  term = new Terminal({
    fontSize: 12.5,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
    cursorBlink: true,
    scrollback: 5000,
    // xterm paints onto a canvas, so it cannot read a CSS variable: the skin's
    // terminal surface has to be resolved to a real colour and handed over, and
    // handed over again when the skin changes (the watch below).
    theme: termTheme(),
  });
  fit = new fitMod.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new linksMod.WebLinksAddon());
  term.open(host.value);
  term.onData((data) => {
    if (dead) {
      // Session.Terminal() respawns an exited shell on the next attach, so
      // reconnecting is the restart.
      dead = false;
      connect();
      return;
    }
    send({ type: "in", data });
  });
  ro = new ResizeObserver(() => {
    if (!fit || !term) return;
    fit.fit();
    send({ type: "resize", cols: term.cols, rows: term.rows });
  });
  ro.observe(host.value);
}

interface TermMessage {
  type: "in" | "out" | "resize" | "exit";
  data?: string;
  cols?: number;
  rows?: number;
  code?: number;
}

function send(m: TermMessage) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(m));
}

function connect() {
  const sid = props.sessionId;
  if (!term || !sid) return;
  ws?.close();
  ws = null;
  const sock = new WebSocket(wsURL(sid));
  ws = sock;
  sock.onmessage = (ev) => {
    let m: TermMessage;
    try {
      m = JSON.parse(String(ev.data));
    } catch {
      return;
    }
    if (m.type === "out" && m.data) term?.write(m.data);
    if (m.type === "exit") {
      dead = true;
      term?.writeln(`\r\n\x1b[2m[${t("shellPanel.exited", { code: m.code ?? "?" })}]\x1b[0m`);
    }
  };
  sock.onclose = () => {
    if (ws !== sock) return; // replaced by a newer connection
    ws = null;
    if (disposed || dead) return;
    // The page is still here but the socket dropped (server restart, session
    // evicted). Back off and retry; a successful replay resets the budget.
    retryTimer = setTimeout(() => {
      if (!disposed && !dead) connect();
    }, retryMs);
    retryMs = Math.min(retryMs * 2, 8000);
  };
  sock.onopen = () => {
    retryMs = 1000;
    // The element may have resized while disconnected; the server's size
    // follows the client, never the other way round.
    if (fit && term) {
      fit.fit();
      send({ type: "resize", cols: term.cols, rows: term.rows });
    }
  };
}

onMounted(async () => {
  await ensureTerminal();
  connect();
});

// Session follow: same panel, another session's shell. Clear what the old
// one painted — the new attach replays its own backlog.
watch(
  () => props.sessionId,
  () => {
    if (!term) return;
    dead = false;
    term.reset();
    connect();
  },
);

// Skin follow. Handing xterm a new theme object repaints in place, so the
// scrollback survives — clearing it would throw away output nobody asked to lose
// just because the colours changed.
watch(themeVersion, () => {
  if (term) term.options.theme = termTheme();
});

onBeforeUnmount(() => {
  disposed = true;
  if (retryTimer) clearTimeout(retryTimer);
  ro?.disconnect();
  ws?.close();
  ws = null;
  term?.dispose();
  term = null;
});
</script>

<template>
  <section class="shell-panel">
    <header v-if="header" class="head">
      <span class="title">
        <Icon :icon="terminalIcon" width="14" />
        Shell
      </span>
      <span v-if="workspace" class="ws-root" :title="t('shellPanel.workspace', { ws: workspace })">/{{ workspace }}</span>
      <button
        class="ghost layout-btn"
        :title="layout === 'right' ? t('shellPanel.toStacked') : t('shellPanel.toSideBySide')"
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
      <button class="ghost" :title="t('common.close')" @click="emit('close')">
        <el-icon><Close /></el-icon>
      </button>
    </header>
    <div v-if="!sessionId" class="no-session">{{ t("shellPanel.noSession") }}</div>
    <div v-show="sessionId" ref="host" class="term-host" />
  </section>
</template>

<style scoped lang="scss">
.shell-panel {
  flex: 1 1 0;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  /* Matches the emulator's own surface, which is a skin token — the panel is the
     frame around the canvas and a mismatch shows as a border of the wrong colour. */
  background: var(--pg-term-bg);
}

.head {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color);
}

.title {
  flex: 1;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding-left: 6px;
  font-size: 12px;
  font-weight: 600;
  color: var(--el-text-color-primary);
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

.term-host {
  flex: 1;
  min-height: 0;
  padding: 4px 0 4px 8px;

  :deep(.xterm) {
    height: 100%;
  }
}

.no-session {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  font-size: 12px;
  color: var(--el-text-color-placeholder);
  background: var(--el-bg-color);
}
</style>
