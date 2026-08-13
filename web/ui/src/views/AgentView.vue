<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import { useResizeObserver } from "@vueuse/core";
import { ElMessageBox } from "element-plus";
import {
  ArrowUp,
  Check,
  Close,
  CopyDocument,
  Delete,
  EditPen,
  Expand,
  Fold,
  FolderOpened,
  MoreFilled,
  RefreshLeft,
  Top,
} from "@element-plus/icons-vue";
import TurnCard from "@/components/TurnCard.vue";
import ContextMeter from "@/components/ContextMeter.vue";
import UsageMeter from "@/components/UsageMeter.vue";
import DockArea from "@/components/DockArea.vue";
import Unreachable from "@/components/Unreachable.vue";
import ModelPicker from "@/components/ModelPicker.vue";
import PolicyPicker from "@/components/PolicyPicker.vue";
import SkillBlock from "@/components/SkillBlock.vue";
import WorkspacePicker from "@/components/WorkspacePicker.vue";
import Logo from "@/components/Logo.vue";
import { Icon } from "@iconify/vue";
import { baseName, fileIcon, messageIcon, sessionIcon } from "@/components/fileIcons";
import { useAgentStream } from "@/agent/useAgentStream";
import { buildTimeline, formatDuration, parseSkillBlock } from "@/agent/timeline";
import { invalidateTree } from "@/components/fileTreeStore";
import { migrateSheet, PANEL_PREFIX, SHEET_KEY, TENANT_KEY } from "@/components/dockSheets";
import StarterCards from "@/components/StarterCards.vue";
import FollowupChips from "@/components/FollowupChips.vue";
import { planIntent } from "@/agent/composerIntent";
import { followupHaystack, matchFollowups } from "@/agent/followups";
import { api, token } from "@/api/client";
import type {
  ModelInfo,
  PanelInfo,
  PolicyMode,
  SessionInfo,
  SkillInfo,
  StarterCard,
  Starters,
} from "@/api/types";
import { LOCALE_LABELS, SUPPORTED_LOCALES, setLocale } from "@/i18n";
import type { Locale } from "@/i18n";

const stream = useAgentStream();
const { t, d, locale } = useI18n();
const currentLocale = computed(() => locale.value as Locale);

const sessions = ref<SessionInfo[]>([]);
const models = ref<ModelInfo[]>([]);
const cwd = ref("");
const current = ref<string | null>(null);
const input = ref("");
const sending = ref(false);
const scroller = ref<HTMLElement | null>(null);
const inputBox = ref<HTMLTextAreaElement | null>(null);

// Sidebar collapse, persisted: anyone who folds it away wants it to stay that
// way on the next visit.
const collapsed = ref(localStorage.getItem("pi-go:sidebar-collapsed") === "1");
watch(collapsed, (v) => localStorage.setItem("pi-go:sidebar-collapsed", v ? "1" : "0"));

const sidebarEl = ref<HTMLElement | null>(null);
const sidebarFullEl = ref<HTMLElement | null>(null);
const mainEl = ref<HTMLElement | null>(null);

// Both directions get a 0.3s slide — as a FLIP transform via WAAPI, never a
// width transition: animating width/flex-basis re-lays out the whole
// unvirtualized conversation every frame (that was the expand stutter).
// Both directions flip the layout FIRST (one reflow, on the click frame) and
// then replay the motion into place with transforms only. Collapsing used to
// do it the other way around (slide, then flip) and the end-of-slide flip
// snapped the main column 194px wider in one frame — the visible "卡一下".
const SLIDE_MS = 300;
const SLIDE_DX = 250 - 56; // sidebar width minus the collapsed rail width
const SLIDE_OPTS: KeyframeAnimationOptions = { duration: SLIDE_MS, easing: "ease-in-out" };

async function expandSidebar() {
  if (!collapsed.value) return;
  collapsed.value = false;
  await nextTick();
  const from = { transform: `translateX(${-SLIDE_DX}px)` };
  const to = { transform: "translateX(0)" };
  sidebarEl.value?.animate([from, to], SLIDE_OPTS);
  mainEl.value?.animate([from, to], SLIDE_OPTS);
}

async function collapseSidebar() {
  if (collapsed.value) return;
  // Flip to the collapsed layout first (one reflow, on the click frame). The
  // wide layer keeps its fixed 250px box and the aside no longer clips it, so
  // right after the flip it still covers exactly the pixels it did before the
  // click — the flip frame is visually identical to the old layout.
  collapsed.value = true;
  await nextTick();
  // Then replay the motion with transforms only: the wide layer slides off to
  // the left while crossfading out (the CSS opacity transition started at the
  // flip), revealing the rail underneath; the main column replays from
  // +SLIDE_DX, its right edge clipped by the shell, so it visibly follows the
  // sidebar's trailing edge into place. The animation's end state IS the
  // layout, so nothing snaps at the end; nothing re-lays out per frame.
  sidebarFullEl.value?.animate(
    [{ transform: "translateX(0)" }, { transform: `translateX(${-SLIDE_DX}px)` }],
    SLIDE_OPTS,
  );
  mainEl.value?.animate(
    [{ transform: `translateX(${SLIDE_DX}px)` }, { transform: "translateX(0)" }],
    SLIDE_OPTS,
  );
}

// The right dock has two sheets — workspace files, and the hub that holds the
// shell and every -web-panel. Which sheet is open and which tenant the hub
// shows both persist; every id older builds wrote is folded onto this pair by
// migrateSheet, which is idempotent so downgrades cannot strand anyone.
const stored = migrateSheet({
  sheet: localStorage.getItem(SHEET_KEY),
  legacyFiles: localStorage.getItem("pi-go:files-open") === "1",
  legacyShell: localStorage.getItem("pi-go:shell-open") === "1",
});
const activeSheet = ref<string | null>(stored.sheet);
// A tenant carried by the old sheet id wins over the remembered one: it is the
// more specific statement of where the user actually was.
const hubTenant = ref<string | null>(stored.tenant ?? localStorage.getItem(TENANT_KEY));
// Deliberately not persisted — see DockArea's maximized comment.
const hubMaximized = ref(false);
watch(activeSheet, (v) => {
  if (v) localStorage.setItem(SHEET_KEY, v);
  else localStorage.removeItem(SHEET_KEY);
  // Maximize belongs to the hub; leaving it set would silently apply to
  // whatever sheet opened next, with no control on screen to undo it.
  if (v !== "hub") hubMaximized.value = false;
});
watch(hubTenant, (v) => (v ? localStorage.setItem(TENANT_KEY, v) : localStorage.removeItem(TENANT_KEY)));
// Rewrite the migrated values now rather than waiting for the first change, so
// a session that never touches the dock still leaves the new keys behind.
if (activeSheet.value) localStorage.setItem(SHEET_KEY, activeSheet.value);
else localStorage.removeItem(SHEET_KEY);
if (hubTenant.value) localStorage.setItem(TENANT_KEY, hubTenant.value);
localStorage.removeItem("pi-go:files-open");
localStorage.removeItem("pi-go:shell-open");

// External panels registered with -web-panel, fetched once at boot (the set is
// fixed for the life of the server, like skills).
const panels = ref<PanelInfo[]>([]);

// A hash route to open the current panel tenant at, set by a starter card that
// deep-links into it. Not persisted and cleared as soon as the user switches
// tenant by hand: it describes one navigation, not a preference.
const hubAt = ref<string | null>(null);

// Empty-state cards contributed by the loaded skills. Absent for a plain pi-go,
// which then keeps its one-line hint.
const starters = ref<Starters | null>(null);
const starterCards = computed(() => starters.value?.cards ?? []);

// Next-step chips, matched against what the last turn actually did. Hidden while
// a run is in flight or a gate is waiting: a suggestion competing with an
// approval card is asking the user to do two things at once, and one of them
// blocks the agent.
const followupChips = computed(() => {
  const groups = starters.value?.followups ?? [];
  if (!groups.length || busy.value || !timeline.value.length) return [];
  if (stream.live.value.pending_gates.length > 0) return [];
  return matchFollowups(groups, followupHaystack(timeline.value));
});

// The panel's dock side: right (the default) or bottom, persisted the same way.
// The toggle itself lives in the panel's header, Chrome DevTools style.
const panelLayout = ref<"right" | "bottom">(localStorage.getItem("pi-go:files-layout") === "bottom" ? "bottom" : "right");
watch(panelLayout, (v) => localStorage.setItem("pi-go:files-layout", v));

// stickToBottom follows new output only while the user is already at the bottom.
// Scrolling up to read history and being yanked back down is the single most
// annoying thing a streaming UI can do.
const stickToBottom = ref(true);

const timeline = computed(() => {
  // liveVersion is throttled by the stream, so streaming deltas rebuild this at
  // a bounded rate instead of once per token.
  void stream.liveVersion.value;
  return buildTimeline(stream.messages.value, stream.results.value, stream.live.value);
});

const busy = computed(() => stream.busy.value);
const policy = computed(() => stream.policy.value);

// waiting covers the gap between sending and the first token, and the same
// gap between turns: the run is active but nothing new is on screen for it
// yet. Tools and gate cards are their own feedback, so they suppress it.
const waiting = computed(() => {
  const l = stream.live.value;
  return (
    busy.value &&
    !(l.text ?? "") &&
    !(l.thinking ?? "") &&
    l.pending_tools.length === 0 &&
    l.pending_gates.length === 0 &&
    (l.incoming?.length ?? 0) === 0
  );
});

// Context occupancy for the warning strip. The meter itself lives in the
// composer bar; this is only the "act now" threshold.
const ctxWindow = computed(() => stream.run.value.context_window ?? 0);
const ctxPct = computed(() =>
  ctxWindow.value ? Math.round((stream.contextTokens.value / ctxWindow.value) * 100) : 0,
);
const ctxHigh = computed(
  () => ctxWindow.value > 0 && stream.contextTokens.value / ctxWindow.value >= 0.85,
);

onMounted(async () => {
  if (!token) {
    showFlash(t("agentView.flash.noToken"), "error", true);
  }
  await Promise.all([loadSessions(), loadModels(), loadSkills(), loadPanels(), loadStarters()]);
  if (sessions.value.length > 0) open(sessions.value[0].id);
  else await createSession();
});

// A session's workspace relative to the server root; "" means the root
// itself, which gets no chip — the common case stays quiet.
function wsOf(s: SessionInfo): string {
  if (!s.cwd || !cwd.value) return "";
  const prefix = cwd.value.endsWith("/") ? cwd.value : cwd.value + "/";
  return s.cwd.startsWith(prefix) ? s.cwd.slice(prefix.length) : "";
}

// The open session's workspace, root-relative ("" = the root): the file
// panel scopes its tree and the changes view's workspace scope to it.
const currentWorkspace = computed(() => {
  const s = sessions.value.find((x) => x.id === current.value);
  return s ? wsOf(s) : "";
});

async function loadSessions() {
  try {
    const res = await api.sessions();
    sessions.value = res.sessions;
    cwd.value = res.cwd;
  } catch (err) {
    showFlash(t("agentView.flash.loadSessionsFailed", { msg: (err as Error).message }), "error");
  }
}

async function loadModels() {
  try {
    models.value = (await api.models()).models;
  } catch {
    // Non-fatal: the picker just stays empty.
  }
}

async function loadSkills() {
  try {
    skills.value = (await api.skills()).skills;
  } catch {
    // Non-fatal: /skill: completion just has nothing to offer.
  }
}

async function loadPanels() {
  try {
    panels.value = (await api.panels()).panels;
  } catch {
    // Non-fatal: the hub just has no external tenants.
  }
}

async function loadStarters() {
  try {
    const res = await api.starters();
    const s = res.starters;
    starters.value = s && (s.cards?.length || s.followups?.length) ? s : null;
  } catch {
    // Non-fatal: the empty state falls back to its built-in hint.
  }
}

// A starter card does one of two things, and neither of them is "run something
// the user did not read". A panel card navigates; a prompt card goes through
// planIntent, which fills the composer unless the deployment opted into sending.
function onStarter(c: StarterCard) {
  if (c.panel) {
    hubTenant.value = PANEL_PREFIX + c.panel;
    hubAt.value = c.at ?? null;
    activeSheet.value = "hub";
    return;
  }
  // The server validated this already; re-checking here keeps the composer's
  // one entry point authoritative rather than trusting the wire.
  const plan = planIntent({ text: c.prompt ?? "", send: starters.value?.send });
  if (plan.kind === "rejected") return;
  input.value = plan.text;
  if (plan.kind === "send") void send();
  else nextTick(() => inputBox.value?.focus());
}

// The slash-command list, in the order the hint row prints it. Order is
// behaviour, not just display: an abbreviated command executes the first match
// in this list ("/s" → "/strict").
const COMMANDS: { name: string; hintKey: string; noAbbrev?: boolean }[] = [
  { name: "/auto", hintKey: "agentView.commands.auto" },
  { name: "/strict", hintKey: "agentView.commands.strict" },
  { name: "/standard", hintKey: "agentView.commands.standard" },
  { name: "/model", hintKey: "agentView.commands.model" },
  { name: "/usage", hintKey: "agentView.commands.usage" },
  { name: "/compact", hintKey: "agentView.commands.compact", noAbbrev: true },
  { name: "/help", hintKey: "agentView.commands.help" },
];

// resolveCommand maps what was typed to a command: exact match wins, else the
// first command the prefix matches. A lone "/" resolves to nothing — running
// the first command on it would make one stray keystroke dangerous.
//
// noAbbrev entries are exempt from the prefix pass for the same reason, one step
// further: "/c" matches nothing else, so without this it would expand to /compact
// and replace the conversation off two keystrokes. They still appear in the hint
// row, which is the version of the shortcut that shows the full name first.
function resolveCommand(word: string): string | null {
  const exact = COMMANDS.find((c) => c.name === word);
  if (exact) return exact.name;
  if (word.length < 2) return null;
  return COMMANDS.find((c) => !c.noAbbrev && c.name.startsWith(word))?.name ?? null;
}

// Command hints above the composer, same row style as the skill hints. Hidden
// once the line has arguments — by then the command is chosen.
const commandMatches = computed(() => {
  const line = input.value;
  if (!line.startsWith("/") || line.startsWith("/skill:")) return [];
  if (line.includes(" ") || line.includes("\n")) return [];
  return COMMANDS.filter((c) => c.name.startsWith(line));
});

function pickCommand(name: string) {
  input.value = `${name} `;
  inputBox.value?.focus();
}

// Skill completion for the composer. Suggestions only appear while the line is
// exactly a /skill: prefix, so typing a path or a sentence is never interrupted.
const skills = ref<SkillInfo[]>([]);
const skillMatches = computed(() => {
  const line = input.value;
  if (!line.startsWith("/skill:") || line.includes(" ") || line.includes("\n")) return [];
  const prefix = line.slice("/skill:".length).toLowerCase();
  return skills.value.filter((s) => s.name.toLowerCase().startsWith(prefix));
});

function pickSkill(name: string) {
  input.value = `/skill:${name} `;
  inputBox.value?.focus();
}

function open(sid: string) {
  if (current.value === sid) return;
  current.value = sid;
  stream.connect(sid);
  stickToBottom.value = true;
  // The rail's dots are measured from the DOM, so they have to wait for the
  // new session's first render.
  void nextTick(scheduleRailCompute);
}

// Opening a session from the sidebar auto-creates at the root; the buttons
// instead route through the workspace picker so the choice is explicit.
const pickerOpen = ref(false);

async function onCreateWorkspace(workspace: string) {
  pickerOpen.value = false;
  await createSession(workspace);
}

async function createSession(workspace = "") {
  try {
    const res = await api.createSession(undefined, workspace);
    await loadSessions();
    open(res.session_id);
  } catch (err) {
    showFlash(t("agentView.flash.createFailed", { msg: (err as Error).message }), "error");
  }
}

async function removeSession(sid: string) {
  // Deleting removes the JSONL transcript, which is the only record of the
  // conversation. There is no undo, so it gets a confirmation naming what goes.
  const title = sessions.value.find((s) => s.id === sid)?.title;
  deleteTarget.value = { sid, title: title ?? "" };
}

const deleteTarget = ref<{ sid: string; title: string } | null>(null);
const deleteBusy = ref(false);

const deleteOpen = computed({
  get: () => deleteTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !deleteBusy.value) deleteTarget.value = null;
  },
});

async function confirmDelete() {
  const target = deleteTarget.value;
  if (!target) return;
  deleteBusy.value = true;
  try {
    await api.deleteSession(target.sid);
    deleteTarget.value = null;
    if (current.value === target.sid) {
      stream.disconnect();
      current.value = null;
    }
    await loadSessions();
    if (!current.value && sessions.value.length) open(sessions.value[0].id);
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.deleteBusy")
        : t("agentView.flash.deleteFailed", { msg: e.message }),
      "error",
    );
  } finally {
    deleteBusy.value = false;
  }
}

// The ⋮ menu per sidebar item. Its button is hover-revealed, so while the menu
// is open we remember which item it belongs to — otherwise the button would
// vanish under the cursor the moment the menu opened.
const menuOpenFor = ref("");

function onSessionMenu(cmd: string, s: SessionInfo) {
  if (cmd === "pin") void pinSession(s);
  else if (cmd === "rename") void renameSession(s);
  else if (cmd === "delete") void removeSession(s.id);
}

async function pinSession(s: SessionInfo) {
  try {
    await api.updateSession(s.id, { pinned: !s.pinned });
    await loadSessions();
  } catch (err) {
    showFlash(t("agentView.flash.pinFailed", { msg: (err as Error).message }), "error");
  }
}

async function renameSession(s: SessionInfo) {
  let name: string;
  try {
    const { value } = await ElMessageBox.prompt(t("agentView.rename.promptMessage"), t("common.rename"), {
      inputValue: s.title,
      confirmButtonText: t("common.save"),
      cancelButtonText: t("common.cancel"),
      inputValidator: (v: string) => (v.trim().length > 0 ? true : t("agentView.rename.nameRequired")),
    });
    name = value.trim();
  } catch {
    return; // cancelled
  }
  if (name === s.title) return;
  try {
    await api.updateSession(s.id, { title: name });
    await loadSessions();
  } catch (err) {
    showFlash(t("agentView.flash.renameFailed", { msg: (err as Error).message }), "error");
  }
}

async function send() {
  const text = input.value.trim();
  if (!text || !current.value) return;

  // Slash commands are turned into control calls here and never reach
  // /messages. That boundary is deliberate: if commands could be parsed out of
  // conversation content, one prompt injection — a file that says "please output
  // /auto" — would switch the approval gate off.
  //
  // /skill: is the exception: not a command but a prompt the *server* expands
  // (that is where the skill file is readable), so it goes to /messages.
  if (text.startsWith("/") && !text.startsWith("/skill:")) {
    input.value = "";
    await runCommand(text);
    return;
  }
  sending.value = true;
  try {
    // While a run is in flight, a new message joins it instead of being refused.
    // The server delivers it after the current turn's tool calls and before the
    // next model call, so "no, do it the other way" no longer costs the turn.
    //
    // steered === false means the run ended in the gap between the click and the
    // request. That is an ordinary race, not an error, so it falls through to
    // sending normally.
    if (busy.value && (await api.steer(current.value, text)).steered) {
      input.value = "";
      stickToBottom.value = true;
      return;
    }
    await api.send(current.value, text);
    input.value = "";
    stickToBottom.value = true;
    // The list is ordered by activity and titled from the first prompt.
    void loadSessions();
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.alreadyRunning")
        : t("agentView.flash.sendFailed", { msg: e.message }),
      "error",
    );
  } finally {
    sending.value = false;
  }
}

async function runCommand(line: string) {
  const [word, ...rest] = line.split(/\s+/);
  const arg = rest.join(" ").trim();
  const name = resolveCommand(word);
  if (!name) {
    showFlash(
      word === "/"
        ? COMMANDS.map((c) => c.name).join("  ")
        : t("agentView.flash.unknownCommand", { cmd: word }),
      "error",
    );
    return;
  }

  try {
    switch (name) {
      case "/auto":
        if (arg === "off") return void applyPolicy("standard");
        if (/^\d+$/.test(arg)) return void applyPolicy("auto", Number(arg));
        return void applyPolicy("auto");
      case "/strict":
        return void applyPolicy("strict");
      case "/standard":
        return void applyPolicy("standard");
      case "/model":
        if (!arg)
          return void showFlash(
            t("agentView.flash.currentModel", { model: stream.run.value.model ?? "?" }),
          );
        return void (await switchModel(arg));
      case "/usage": {
        const u = stream.usage.value;
        const fresh = Math.max(0, u.input - (u.cache_read ?? 0));
        return void showFlash(
          t("agentView.flash.usage", {
            input: tok(u.input),
            cached: tok(u.cache_read ?? 0),
            fresh: tok(fresh),
            output: tok(u.output),
          }),
        );
      }
      case "/compact":
        return void (await compactSession());
      case "/help":
        return void showFlash(COMMANDS.map((c) => c.name).join("  "));
    }
  } catch (err) {
    showFlash(t("agentView.flash.commandFailed", { msg: (err as Error).message ?? err }), "error");
  }
}

// compacting is exposed to the meter so the button can show progress: this is a
// model call on the whole conversation and takes about as long as a turn does.
const compacting = ref(false);

// compactSession replaces the conversation with a summary of it.
//
// Confirmed first, unlike every other command here, because it is the only one
// that throws work away: the model stops being able to see the conversation and
// can only see what the summary chose to keep. The transcript survives on disk,
// which the dialog says, because "can I get it back" is the question being asked
// at that moment and the answer is more reassuring than people expect.
async function compactSession() {
  if (!current.value || compacting.value) return;
  try {
    await ElMessageBox.confirm(
      t("agentView.compact.confirmMessage"),
      t("agentView.compact.confirmTitle"),
      {
        confirmButtonText: t("agentView.compact.confirmButton"),
        cancelButtonText: t("common.cancel"),
        type: "warning",
      },
    );
  } catch {
    return; // dismissed
  }

  compacting.value = true;
  try {
    const res = await api.compact(current.value);
    showFlash(
      t("agentView.compact.done", {
        before: res.before,
        beforeTokens: tok(res.before_tokens),
        afterTokens: tok(res.after_tokens),
      }),
    );
  } catch (err) {
    const e = err as Error & { status?: number };
    // 422 is the server saying this would not help, which is not a failure and
    // reads badly as one — its message already explains which case it was.
    showFlash(
      e.status === 409
        ? t("agentView.compact.busy")
        : e.status === 422
          ? e.message
          : t("agentView.compact.failed", { msg: e.message }),
      e.status === 422 ? "info" : "error",
    );
  } finally {
    compacting.value = false;
  }
}

async function applyPolicy(mode: PolicyMode, turns?: number) {
  // No success feedback of its own: the picker button already says which mode
  // is on (完全访问 in orange), which is exactly where the user is looking.
  await api.setPolicy(current.value!, mode, turns);
}

async function pickPolicy(mode: PolicyMode) {
  if (mode === policy.value.mode) return;
  try {
    await applyPolicy(mode);
  } catch (err) {
    showFlash(t("agentView.flash.switchFailed", { msg: (err as Error).message }), "error");
  }
}

// Switching models keeps the conversation: every provider here speaks the same
// wire format and thinking is not replayed, so there is nothing model-specific in
// the transcript to translate.
async function switchModel(id: string) {
  try {
    await api.setModel(current.value!, id);
    // No success toast: the picker button shows the new model id.
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.modelBusy")
        : t("agentView.flash.switchFailed", { msg: e.message }),
      "error",
    );
  }
}

async function stop() {
  try {
    await api.cancel(current.value!);
  } catch (err) {
    showFlash(t("agentView.flash.cancelFailed", { msg: (err as Error).message }), "error");
  }
}

async function decide(e: { gateId: string; allow: boolean; args?: unknown; remember?: "tool" | "command" }) {
  try {
    await api.decideGate(current.value!, e.gateId, e.allow, { args: e.args, remember: e.remember });
  } catch (err) {
    showFlash(t("agentView.flash.gateStale", { msg: (err as Error).message }), "error");
  }
}

// A steering message the run accepted but ended before delivering. Rather than
// let it vanish, put the text back in the composer and say why it is there.
watch(
  () => stream.undelivered.value,
  (texts) => {
    if (texts.length === 0) return;
    input.value = [input.value, ...texts].filter(Boolean).join("\n");
    stream.undelivered.value = [];
    showFlash(t("agentView.flash.steerUndelivered"));
    inputBox.value?.focus();
  },
);

// A dock panel asked the conversation something. Unlike a starter card this
// always fills and never sends, whatever the deployment configured: a panel is
// content the server hands out without the token because it is content, and
// content that could spend a model call on its own would stop being that.
//
// Un-maximizing is not a nicety — a maximized hub covers the conversation, so
// filling a composer the user cannot see would look like nothing happened.
function onPanelIntent(text: string) {
  const plan = planIntent({ text });
  if (plan.kind === "rejected") return;
  hubMaximized.value = false;
  suggest(plan.text);
}

function suggest(text: string) {
  input.value = text;
  inputBox.value?.focus();
}

function onScroll() {
  const el = scroller.value;
  if (!el) return;
  stickToBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  updateRail();
}

// ── Turn rail ─────────────────────────────────────────────────────────────
// One dot per user message on a right-edge track (after the YX agent-qa
// reference). Positions are measured from the rendered .ask elements rather
// than estimated from message counts, because a turn's height is whatever the
// tools made it.
interface RailDot {
  id: string;
  index: number;
  /** topPct is the dot's position on the rail; topPx its scroll offset. */
  topPct: number;
  topPx: number;
  question: string;
  /** Send time of the user message, epoch ms; absent on old transcripts. */
  ts?: number;
}

const convInner = ref<HTMLElement | null>(null);
const railDots = ref<RailDot[]>([]);
const railActive = ref(-1);
const railFill = ref(0);
const contentOverflow = ref(false);
const railVisible = computed(() => railDots.value.length >= 1 && contentOverflow.value);

// Near the rail's ends the hover card opens one-sided instead of centred, so
// the scroller's clipped edges cannot cut it off.
const tipPos = (topPct: number) => (topPct < 22 ? "tip-down" : topPct > 78 ? "tip-up" : "");

let railRaf = 0;
// rAF batching: streaming and card expansion change heights far more often
// than the eye needs.
function scheduleRailCompute() {
  if (railRaf) return;
  railRaf = requestAnimationFrame(() => {
    railRaf = 0;
    computeRail();
  });
}

function railQuestion(text: string): string {
  // A /skill invocation's raw text is the whole instruction sheet; the useful
  // preview is the invocation line.
  const skill = parseSkillBlock(text);
  const plain = skill ? `/skill:${skill.name} ${skill.trailing}` : text;
  return plain.replace(/\s+/g, " ").trim();
}

function computeRail() {
  const body = scroller.value;
  if (!body) return;
  const bodyRect = body.getBoundingClientRect();
  const total = body.scrollHeight || 1;
  contentOverflow.value = body.scrollHeight > body.clientHeight * 1.2;
  const users = timeline.value.filter((i) => i.kind === "user");
  const list: RailDot[] = [];
  body.querySelectorAll<HTMLElement>(".ask").forEach((el, index) => {
    const top = el.getBoundingClientRect().top - bodyRect.top + body.scrollTop;
    list.push({
      id: users[index]?.id ?? `u${index}`,
      index,
      topPx: top,
      topPct: Math.min(100, Math.max(0, (top / total) * 100)),
      question: railQuestion(users[index]?.text ?? ""),
      ts: users[index]?.ts,
    });
  });
  railDots.value = list;
  updateRail();
}

// The fill tracks the viewport's bottom edge; the active dot is the last one
// at or above a line 80px below the viewport top.
function updateRail() {
  const body = scroller.value;
  if (!body) return;
  const st = body.scrollTop;
  railFill.value = Math.min(100, ((st + body.clientHeight) / (body.scrollHeight || 1)) * 100);
  let ai = -1;
  for (const d of railDots.value) {
    if (d.topPx <= st + 80) ai = d.index;
    else break;
  }
  railActive.value = ai;
}

function jumpToTurn(d: RailDot) {
  scroller.value?.scrollTo({ top: Math.max(0, d.topPx - 12), behavior: "smooth" });
}

// New items move every dot below them; everything else (streaming text, card
// folds, window resizes) shows up as a change in the inner content's height.
watch(
  () => timeline.value.length,
  () => nextTick(scheduleRailCompute),
);
useResizeObserver(convInner, scheduleRailCompute);
onBeforeUnmount(() => {
  if (railRaf) cancelAnimationFrame(railRaf);
});

watch([timeline, () => stream.liveVersion.value], async () => {
  if (!stickToBottom.value) return;
  await nextTick();
  const el = scroller.value;
  if (el) el.scrollTop = el.scrollHeight;
});

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
    e.preventDefault();
    void send();
  }
}

// tok() groups digits and names the unit. A bare "12463" reads as tokens, bytes
// or characters depending on who is looking, and for Chinese text those differ by
// several times over — enough to misjudge the cost badly.
function tok(n: number) {
  return `${n.toLocaleString("en-US")} tok`;
}

// fmtTime renders a message's send time via the locale's short date-time
// format (full date plus HH:mm). The date is always shown — a bare HH:mm says
// nothing once the transcript outlives the day it was written on.
function fmtTime(ts: number) {
  return d(new Date(ts), "short");
}

// No floating alerts in this UI: transient feedback (command output, action
// failures) shows as one inline strip next to the retry/error notices, and
// state feedback shows in the control that changed (picker labels, the copy
// icon below). The strip self-clears; sticky is reserved for the fatal ones.
const flash = ref<{ kind: "info" | "error"; text: string } | null>(null);
let flashTimer: ReturnType<typeof setTimeout> | undefined;

function showFlash(text: string, kind: "info" | "error" = "info", sticky = false) {
  flash.value = { kind, text };
  clearTimeout(flashTimer);
  if (!sticky) {
    flashTimer = setTimeout(() => (flash.value = null), kind === "error" ? 6000 : 4000);
  }
}

// Copy feedback is the icon itself turning into a check for a moment.
const copiedAsk = ref<string | null>(null);
let copiedTimer: ReturnType<typeof setTimeout> | undefined;

async function copyAsk(id: string, text: string) {
  try {
    await navigator.clipboard.writeText(text);
    copiedAsk.value = id;
    clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => (copiedAsk.value = null), 1500);
  } catch {
    showFlash(t("agentView.flash.copyFailed"), "error");
  }
}

// Edit refills the composer and focuses it; what happens next is the user's
// call, same rule as the read-continuation buttons.
function editAsk(text: string) {
  input.value = text;
  nextTick(() => inputBox.value?.focus());
}

// Rewind is edit-with-history: the transcript forks away from the message —
// the message itself and everything after it leaves the branch — and the
// withdrawn text lands back in the composer for a resend. The dialog offers
// restoring the files too when the message's checkpoint has a non-empty diff,
// the same rule as Claude Code's rewind menu: no tracked changes, no option.
const rewindTarget = ref<{ id: string; text: string } | null>(null);
const rewindPreview = ref<{
  available: boolean;
  changes: { path: string; status: string; added: number; removed: number }[];
} | null>(null);
const rewindMode = ref<"both" | "chat">("both");
const rewindBusy = ref(false);

const rewindOpen = computed({
  get: () => rewindTarget.value !== null,
  set: (v: boolean) => {
    if (!v && !rewindBusy.value) rewindTarget.value = null;
  },
});

// The file option's precondition: a checkpoint exists AND restoring it would
// change something. An empty diff offers conversation-only, like Claude Code.
const rewindChanges = computed(() => (rewindPreview.value?.available ? rewindPreview.value.changes : []));

function rewindAsk(item: { id: string; text: string }) {
  if (!current.value || busy.value) return;
  rewindMode.value = "both";
  rewindPreview.value = null;
  rewindTarget.value = item;
  api
    .rewindPreview(current.value, item.id)
    .then((res) => {
      // The dialog may already target a different message by the time this
      // lands — a late preview must not describe the wrong rewind.
      if (rewindTarget.value?.id === item.id) rewindPreview.value = res;
    })
    .catch(() => {
      // The preview is advisory; conversation-only rewind is always offerable.
    });
}

async function confirmRewind() {
  const target = rewindTarget.value;
  if (!target || !current.value) return;
  const files = rewindMode.value === "both" && rewindChanges.value.length > 0;
  rewindBusy.value = true;
  try {
    await api.rewind(current.value, target.id, files);
    rewindTarget.value = null;
    // open() early-returns on an unchanged sid, so the reload goes straight
    // through the stream: the snapshot that lands is the rewound branch.
    stream.connect(current.value);
    // The sidebar's count and title follow the live branch; the file tree
    // follows the restored workspace.
    await loadSessions();
    if (files) invalidateTree();
    editAsk(target.text);
  } catch (err) {
    const e = err as Error & { status?: number };
    showFlash(
      e.status === 409
        ? t("agentView.flash.rewindBusy")
        : t("agentView.flash.rewindFailed", { msg: e.message }),
      "error",
    );
  } finally {
    rewindBusy.value = false;
  }
}

// What a status letter means in the restore preview: it describes what the
// restore WILL do, not what the abandoned run did.
function rewindStatusLabel(status: string) {
  switch (status) {
    case "M":
      return t("agentView.rewind.statusRestore");
    case "D":
      return t("agentView.rewind.statusRecover");
    default:
      return t("agentView.rewind.statusDelete");
  }
}
</script>

<template>
  <div class="shell" :class="`dock-${panelLayout}`">
    <aside ref="sidebarEl" class="sidebar" :class="{ collapsed }">
      <!-- Both states stay mounted and crossfade (opacity, same 0.3s as the
           slide) instead of a v-if swap on the first frame — the icon that
           was just clicked used to vanish instantly, which read as a cut.
           The hidden layer is inert via pointer-events: none. -->
      <div ref="sidebarFullEl" class="sidebar-full">
        <div class="brand">
          <Logo />
          <strong>pi-go</strong>
          <button class="new" @click="pickerOpen = true">
            <el-icon><EditPen /></el-icon>
            {{ t("agentView.sidebar.newSession") }}
          </button>
          <!-- Gemini pattern: the collapse control only shows itself while the
               cursor is over the sidebar, so it costs no attention otherwise. -->
          <button class="collapse" :title="t('agentView.sidebar.collapse')" @click="collapseSidebar">
            <el-icon><Fold /></el-icon>
          </button>
        </div>
        <ul class="sessions">
          <li
            v-for="s in sessions"
            :key="s.id"
            :class="{ active: s.id === current }"
            @click="open(s.id)"
          >
            <div class="title">{{ s.title || t("agentView.sidebar.emptySession") }}</div>
            <div class="sub">
              <span v-if="s.pinned" class="pinned-tag">{{ t("agentView.sidebar.pinned") }}</span>
              <span v-if="wsOf(s)" class="ws-tag" :title="t('agentView.sidebar.workspace', { path: s.cwd })">
                <el-icon><FolderOpened /></el-icon>{{ wsOf(s) }}
              </span>
              <span>{{ s.model }}</span>
              <span>{{ t("agentView.sidebar.messageCount", { n: s.messages }) }}</span>
            </div>
            <!-- Gemini pattern: the ⋮ shows itself on hover and holds the
                 item's actions. Delete moved in here; the bare × is gone. -->
            <el-dropdown
              class="sess-menu"
              trigger="click"
              placement="right-start"
              @command="(cmd: string) => onSessionMenu(cmd, s)"
              @visible-change="
                (v: boolean) => (menuOpenFor = v ? s.id : menuOpenFor === s.id ? '' : menuOpenFor)
              "
            >
              <button
                class="menu-btn"
                :class="{ open: menuOpenFor === s.id }"
                :title="t('agentView.sidebar.more')"
                @click.stop
              >
                <el-icon><MoreFilled /></el-icon>
              </button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="pin">
                    <el-icon><Top /></el-icon>{{ s.pinned ? t("agentView.sidebar.unpin") : t("agentView.sidebar.pin") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="rename">
                    <el-icon><EditPen /></el-icon>{{ t("common.rename") }}
                  </el-dropdown-item>
                  <el-dropdown-item command="delete" divided>
                    <el-icon><Delete /></el-icon>{{ t("common.delete") }}
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </li>
        </ul>
        <div class="side-foot">
          <el-dropdown trigger="click" placement="top-start" @command="(l: Locale) => setLocale(l)">
            <button class="lang-btn" :title="t('agentView.lang.label')">
              <el-icon
                ><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><path d="M2 12h20" /><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" /></svg
              ></el-icon>
              <span>{{ LOCALE_LABELS[currentLocale] }}</span>
            </button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item v-for="l in SUPPORTED_LOCALES" :key="l" :command="l">
                  {{ l === currentLocale ? "✓ " + LOCALE_LABELS[l] : LOCALE_LABELS[l] }}
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </div>
      <!-- Collapsed: Gemini's icon rail — just the two things you still want
           one click away: reopening the list and starting a new session. -->
      <div class="rail-icons">
        <Logo :size="24" class="rail-logo" />
        <button class="rail-icon" :title="t('agentView.sidebar.expand')" @click="expandSidebar">
          <el-icon><Expand /></el-icon>
        </button>
        <button class="rail-icon" :title="t('agentView.sidebar.newSession')" @click="pickerOpen = true">
          <el-icon><EditPen /></el-icon>
        </button>
        <el-dropdown trigger="click" placement="right-start" @command="(l: Locale) => setLocale(l)">
          <button class="rail-icon" :title="t('agentView.lang.label')">
            <el-icon
              ><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10" /><path d="M2 12h20" /><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" /></svg
            ></el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item v-for="l in SUPPORTED_LOCALES" :key="l" :command="l">
                {{ l === currentLocale ? "✓ " + LOCALE_LABELS[l] : LOCALE_LABELS[l] }}
              </el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </aside>

    <main ref="mainEl" class="main">
      <header class="topbar">
        <!-- Server root moved here from the sidebar: it describes the whole
             window, not the session list, and stays visible when the
             sidebar is collapsed. -->
        <span class="cwd" :title="cwd">{{ cwd }}</span>
      </header>

      <div v-if="flash" class="notice" :class="{ bad: flash.kind === 'error' }">{{ flash.text }}</div>
      <div v-if="stream.policyReverted.value" class="notice">
        {{ t("agentView.notice.policyReverted", { reason: stream.policyReverted.value }) }}
      </div>
      <div v-if="stream.retry.value" class="notice">
        {{
          t("agentView.notice.retrying", {
            attempt: stream.retry.value.attempt,
            max: stream.retry.value.max,
            delay: formatDuration(stream.retry.value.delayMs),
            reason: stream.retry.value.reason,
          })
        }}
      </div>
      <div v-if="stream.streamError.value" class="notice bad">{{ stream.streamError.value }}</div>

      <!-- In the bottom dock the composer column moves right of the
           conversation (see .shell.dock-bottom .chat); otherwise this is a
           plain vertical stack, exactly as before. -->
      <div class="chat">
      <div class="conv-wrap">
        <div ref="scroller" class="conversation" @scroll.passive="onScroll">
          <!-- Session switch: the old conversation stays (faded, sinking
               4px) until the new session's snapshot lands, then fades back —
               a crossfade, not a blank-and-pop. useAgentStream.hold. -->
          <div ref="convInner" class="conv-inner" :class="{ switching: stream.switching.value }">
            <!-- The empty state is where a deployment says what it is for. With
                 no skill-provided cards this is the original one-line hint, so
                 a plain pi-go looks exactly as before. -->
            <template v-if="!timeline.length">
              <StarterCards
                v-if="starterCards.length"
                :heading="starters?.heading"
                :cards="starterCards"
                :fallback="t('agentView.empty.hint')"
                @pick="onStarter"
              />
              <div v-else class="empty">
                {{ t("agentView.empty.hint") }}
              </div>
            </template>
            <template v-for="item in timeline" :key="item.id">
              <div v-if="item.kind === 'user'" class="ask">
                <!-- WeChat/iMessage pattern: the send time sits centred above
                     the message. The row below the bubble carries
                     hover-revealed copy/edit actions. Edit refills the
                     composer instead of resending — never speak for the user. -->
                <span v-if="item.ts" class="ask-time">{{ fmtTime(item.ts) }}</span>
                <!-- A /skill invocation keeps the wide card: it is a document,
                     not a chat line, and a bubble would only squeeze it. -->
                <SkillBlock
                  v-if="parseSkillBlock(item.text)"
                  class="ask-skill"
                  :block="parseSkillBlock(item.text)!"
                />
                <div v-else class="ask-bubble">{{ item.text }}</div>
                <div v-if="!parseSkillBlock(item.text)" class="ask-meta">
                  <button class="ask-act" :title="t('common.copy')" @click="copyAsk(item.id, item.text)">
                    <el-icon><Check v-if="copiedAsk === item.id" /><CopyDocument v-else /></el-icon>
                  </button>
                  <button class="ask-act" :title="t('agentView.ask.edit')" @click="editAsk(item.text)">
                    <el-icon><EditPen /></el-icon>
                  </button>
                  <!-- Rewind is disabled mid-run rather than hidden: a control
                       that vanishes exactly when it cannot act reads as a bug,
                       a greyed one reads as "wait for the run". -->
                  <button class="ask-act" :title="t('agentView.ask.rewindTitle')" :disabled="busy" @click="rewindAsk(item)">
                    <el-icon><RefreshLeft /></el-icon>
                  </button>
                </div>
              </div>
              <TurnCard
                v-else
                :turn="item"
                :run-active="busy"
                :skills="skills"
                :cwd="cwd"
                @suggest="suggest"
                @decide="decide"
                @freeze="(id: string) => api.freezeGate(current!, id)"
                @thaw="(id: string) => api.thawGate(current!, id)"
              />
            </template>
            <!-- The gap between sending and the first token, and the same gap
                 between turns: the run is active but nothing new is on screen
                 yet. Tools and gate cards are their own feedback. -->
            <div v-if="waiting" class="waiting">
              <span class="pend-dots"><i /><i /><i /></span>
              {{ t("agentView.waiting") }}
            </div>
            <!-- Next-step chips, after the answer they follow from. Only when a
                 skill's condition matches what the last turn did; see
                 agent/followups.ts for why silence is the default. -->
            <FollowupChips v-if="followupChips.length" :chips="followupChips" @pick="onStarter" />
          </div>
        </div>

        <!-- Turn rail (after the YX agent-qa reference): one dot per user
             message on a right-edge track. The fill is scroll progress, the
             enlarged dot is the turn the viewport is in, hover previews the
             question, click jumps. It appears only once the conversation
             actually overflows — a short one needs no map. -->
        <div v-if="railVisible" class="turn-rail">
          <div class="rail-track">
            <div class="rail-fill" :style="{ height: railFill + '%' }" />
          </div>
          <button
            v-for="d in railDots"
            :key="d.id"
            class="rail-dot"
            :class="{ active: d.index === railActive }"
            :style="{ top: d.topPct + '%' }"
            :aria-label="t('agentView.rail.jumpTo', { q: d.question })"
            @click="jumpToTurn(d)"
          >
            <span class="rail-tip" :class="tipPos(d.topPct)">
              <span class="rail-tip-question">{{ d.question }}</span>
              <span v-if="d.ts" class="rail-tip-time">{{ fmtTime(d.ts) }}</span>
            </span>
          </button>
        </div>
      </div>

      <div class="compose-col">
      <div v-if="skillMatches.length > 0" class="skill-hints">
        <button v-for="s in skillMatches" :key="s.name" class="hint" @click="pickSkill(s.name)">
          <span class="hint-name">/skill:{{ s.name }}</span>
          <span class="hint-desc">{{ s.description }}</span>
        </button>
      </div>

      <!-- Command hints while the line is a bare command prefix. Enter runs the
           first row (hence the ⏎); clicking fills instead of running. -->
      <div v-else-if="commandMatches.length > 0" class="skill-hints cmd-hints">
        <button v-for="c in commandMatches" :key="c.name" class="hint" @click="pickCommand(c.name)">
          <span class="hint-name">{{ c.name }}</span>
          <span class="hint-desc">{{ t(c.hintKey) }}</span>
        </button>
      </div>

      <!-- Context warning lives above the composer rather than in the topbar:
           "should I still send this" is decided here, so the warning is too.
           Non-dismissible while it applies: forgetting it is how a turn dies. -->
      <div v-if="ctxHigh" class="ctx-warn">
        {{ t("agentView.ctxWarn.text", { pct: ctxPct }) }}
        <button @click="pickerOpen = true">{{ t("agentView.ctxWarn.newSession") }}</button>
      </div>

      <div class="input-card">
          <textarea
            ref="inputBox"
            v-model="input"
            :placeholder="
              busy
                ? t('agentView.composer.placeholderBusy')
                : t('agentView.composer.placeholder')
            "
            rows="3"
            @keydown="onKeydown"
          />
          <div class="input-bar">
            <ModelPicker
              :models="models"
              :current="stream.run.value.model"
              :disabled="busy"
              @change="switchModel"
            />
            <ContextMeter
              :used="stream.contextTokens.value"
              :window="stream.run.value.context_window ?? 0"
              :overhead="stream.overheadTokens.value"
              :messages="stream.messages.value"
              :results="stream.results.value"
              :clear-trigger="stream.clearTrigger.value"
              :disabled="busy"
              :compacting="compacting"
              @compact="compactSession"
            />
            <UsageMeter :usage="stream.usage.value" />
            <PolicyPicker
              :mode="policy.mode"
              :remaining="policy.remaining_turns"
              @change="pickPolicy"
            />
            <div class="spacer" />
            <!-- The wrapper is transparent (display: contents) in the right
                 dock; in the bottom dock it floats the actions to the card's
                 top-right corner. -->
            <div class="actions">
              <button v-if="busy" class="stop" @click="stop">{{ t("agentView.composer.stop") }}</button>
              <!-- Enabled while a run is in flight: the message joins that run
                   instead of being refused. The steering variant turns the
                   button blue so the changed action gets a changed look. -->
              <button
                class="send-btn"
                :class="{ steering: busy }"
                :disabled="sending || !input.trim()"
                :title="busy ? t('agentView.composer.steerTitle') : t('common.send')"
                @click="send"
              >
                <span>{{ busy ? t("agentView.composer.steer") : t("common.send") }}</span>
                <el-icon><ArrowUp /></el-icon>
              </button>
            </div>
          </div>
        </div>
      </div>
      </div>
    </main>

    <!-- The dock is a sheet container docked on the right by default or along
         the bottom (Chrome DevTools style): workspace files, the session
         shell, and each registered -web-panel are sheets switched by the
         always-visible rail. -->
    <DockArea
      v-model:layout="panelLayout"
      :active="activeSheet"
      :tenant="hubTenant"
      :at="hubAt"
      :maximized="hubMaximized"
      :panels="panels"
      :items="timeline"
      :workspace="currentWorkspace"
      :session-id="current"
      @update:active="activeSheet = $event"
      @update:tenant="hubTenant = $event; hubAt = null"
      @update:maximized="hubMaximized = $event"
      @intent="onPanelIntent"
    />

    <WorkspacePicker v-if="pickerOpen" :cwd="cwd" @create="onCreateWorkspace" @close="pickerOpen = false" />

    <!-- Rewind: conversation fork, optionally with the workspace restored to
         the checkpoint taken when that message was sent. The file option
         only exists when restoring would change something — the Claude Code
         rule that an empty diff gets no code choice. The dialog shows the
         message it targets and, per file, the line counts the restore itself
         would add and remove — the rewind's impact, never the content. -->
    <!-- The shared confirm-dialog design: icon + title + consequence, the
         object in a quote block, cancel ghost + solid destructive CTA. -->
    <el-dialog
      v-model="deleteOpen"
      :title="t('agentView.deleteDialog.title')"
      width="440px"
      class="confirm-dialog"
      :show-close="false"
      :close-on-click-modal="!deleteBusy"
    >
      <div class="rewind-body">
        <div class="rw-head">
          <span class="rw-icon"><Icon :icon="sessionIcon" /></span>
          <div class="rw-headtext">
            <h3>{{ t("agentView.deleteDialog.title") }}</h3>
            <p>{{ t("agentView.deleteDialog.desc") }}</p>
          </div>
          <button class="rw-x" :title="t('common.cancel')" :disabled="deleteBusy" @click="deleteOpen = false">
            <el-icon><Close /></el-icon>
          </button>
        </div>

        <div v-if="deleteTarget" class="rw-quote" :title="deleteTarget.title">
          {{ deleteTarget.title || t("agentView.deleteDialog.untitled") }}
        </div>
      </div>
      <template #footer>
        <div class="rw-foot">
          <el-button :disabled="deleteBusy" @click="deleteOpen = false">{{ t("common.cancel") }}</el-button>
          <el-button type="danger" :loading="deleteBusy" @click="confirmDelete">{{ t("common.delete") }}</el-button>
        </div>
      </template>
    </el-dialog>

    <el-dialog
      v-model="rewindOpen"
      :title="t('agentView.rewind.title')"
      width="520px"
      class="confirm-dialog"
      :show-close="false"
      :close-on-click-modal="!rewindBusy"
    >
      <div class="rewind-body">
        <div class="rw-head">
          <span class="rw-icon"><el-icon><RefreshLeft /></el-icon></span>
          <div class="rw-headtext">
            <h3>{{ t("agentView.rewind.title") }}</h3>
            <p>{{ t("agentView.rewind.desc") }}</p>
          </div>
          <button class="rw-x" :title="t('common.cancel')" :disabled="rewindBusy" @click="rewindOpen = false">
            <el-icon><Close /></el-icon>
          </button>
        </div>

        <div v-if="rewindTarget" class="rw-quote" :title="rewindTarget.text">
          <Icon class="qicon" :icon="messageIcon" />
          <span class="qtext">{{ rewindTarget.text }}</span>
        </div>

        <template v-if="rewindChanges.length">
          <button class="rw-opt" :class="{ on: rewindMode === 'both' }" @click="rewindMode = 'both'">
            <span class="rw-radio" />
            <span class="rw-opttext">
              <span class="rw-title">{{ t("agentView.rewind.bothTitle") }}</span>
              <span class="rw-desc">{{ t("agentView.rewind.bothDesc") }}</span>
            </span>
          </button>

          <div v-if="rewindMode === 'both'" class="rw-files">
            <div class="rw-files-head">{{ t("agentView.rewind.filesHead", { n: rewindChanges.length }) }}</div>
            <div class="rw-files-list">
              <div v-for="c in rewindChanges.slice(0, 30)" :key="c.path" class="rw-file">
                <Icon class="ficon" :icon="fileIcon(baseName(c.path))" />
                <span class="st" :data-s="c.status">{{ rewindStatusLabel(c.status) }}</span>
                <span class="p" :title="c.path">{{ c.path }}</span>
                <span class="stat">
                  <template v-if="c.added >= 0 && c.removed >= 0">
                    <span class="plus">+{{ c.added }}</span>
                    <span class="minus">-{{ c.removed }}</span>
                  </template>
                  <span v-else class="bin">{{ t("agentView.rewind.binary") }}</span>
                </span>
              </div>
              <div v-if="rewindChanges.length > 30" class="rw-more">
                {{ t("agentView.rewind.moreFiles", { n: rewindChanges.length - 30 }) }}
              </div>
            </div>
            <p class="rw-warn">{{ t("agentView.rewind.warn") }}</p>
          </div>

          <button class="rw-opt" :class="{ on: rewindMode === 'chat' }" @click="rewindMode = 'chat'">
            <span class="rw-radio" />
            <span class="rw-opttext">
              <span class="rw-title">{{ t("agentView.rewind.chatTitle") }}</span>
              <span class="rw-desc">{{ t("agentView.rewind.chatDesc") }}</span>
            </span>
          </button>
        </template>
        <!-- The preview is still in flight or found nothing: say which. -->
        <p v-else-if="!rewindPreview" class="rw-warn">{{ t("agentView.rewind.checking") }}</p>
        <p v-else class="rw-warn">{{ t("agentView.rewind.noChanges") }}</p>
      </div>
      <template #footer>
        <div class="rw-foot">
          <el-button :disabled="rewindBusy" @click="rewindOpen = false">{{ t("common.cancel") }}</el-button>
          <el-button type="danger" :loading="rewindBusy" @click="confirmRewind">{{ t("agentView.rewind.confirm") }}</el-button>
        </div>
      </template>
    </el-dialog>

    <!-- Past the grace window with no event stream, the whole page gives way
         to the browser-style cannot-connect screen; the background backoff
         loop still closes it by itself the moment a reconnect lands. -->
    <Unreachable v-if="stream.unreachable.value" :outage="stream.outage.value" @retry="stream.retryNow()" />
  </div>
</template>

<style scoped lang="scss">
.shell {
  display: flex;
  height: 100vh;
  overflow: hidden;

  /* Bottom dock: switch to a two-column grid — the sidebar spans both rows,
     main and the panel stack in the second column. Row tracks keep the
     conversation on the remaining height and the panel at its own height. */
  &.dock-bottom {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    grid-template-rows: minmax(0, 1fr) auto;

    .sidebar {
      grid-row: 1 / 3;
    }

    .main {
      min-height: 0;
    }
  }
}

.sidebar {
  width: 250px;
  flex: 0 0 250px;
  border-right: 1px solid var(--el-border-color-lighter);
  /* Both states are absolute layers inside. No overflow: hidden here: while
     collapsing, the full layer keeps its 250px box and slides out to the
     left of the 56px-wide aside (collapseSidebar) — clipping would cut that
     ghost off. In the steady collapsed state the full layer is opacity: 0 +
     pointer-events: none, so nothing leaks visually or interactively. */
  position: relative;
  background: var(--el-fill-color-lighter);
  /* No width transition here, on purpose: width/flex-basis are layout
     properties, so animating them re-lays out the whole page every frame —
     and the unvirtualized conversation re-wraps with it while the rail's
     ResizeObserver forces a synchronous layout read on top. That was the
     visible stutter on expand. The slide in both directions is a
     transform-only WAAPI animation (expandSidebar/collapseSidebar); the
     layout itself flips instantly. */

  &.collapsed {
    width: 56px;
    flex-basis: 56px;
  }
}

/* The two sidebar states are both mounted as absolute layers and crossfade
   (same 0.3s as the slide); the inactive one is transparent and inert. */
.sidebar-full,
.rail-icons {
  position: absolute;
  inset: 0;
  transition: opacity 0.3s ease-in-out;
}

.sidebar-full {
  display: flex;
  flex-direction: column;
  /* Fixed expanded width, never squashed by the collapsed 56px aside: on
     collapse this layer is the wide sidebar's ghost that slides off to the
     left. The border rides along, so the separator slides away with the
     sidebar instead of teleporting from 250px to 56px on the click frame.
     (Expanded it overlaps the aside's own border exactly — same 1px line.) */
  width: 250px;
  border-right: 1px solid var(--el-border-color-lighter);
}

.sidebar:not(.collapsed) .rail-icons,
.sidebar.collapsed .sidebar-full {
  opacity: 0;
  pointer-events: none;
}

/* Collapsed icon rail (Gemini pattern): expand + new session, nothing else. */
.rail-icons {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 6px;
  padding: 12px 0;
}

.rail-logo {
  margin-bottom: 6px;
}

.rail-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border: 0;
  border-radius: 50%;
  background: transparent;
  color: var(--el-text-color-primary);
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 18px;
  }

  &:hover {
    background: var(--el-fill-color);
  }
}

.side-foot {
  padding: 8px 12px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.lang-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--el-text-color-regular);
  font-size: 12px;
  cursor: pointer;
  transition: background 0.15s;

  .el-icon {
    font-size: 14px;
  }

  &:hover {
    background: var(--el-fill-color);
  }
}

.brand {
  display: flex;
  align-items: center;
  padding: 12px;
  gap: 8px;

  strong {
    font-size: 15px;
    font-weight: 700;
    letter-spacing: 0.2px;
  }
}

.new {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-left: auto;
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  border-radius: 4px;
  font-size: 11px;
  padding: 3px 8px;
  cursor: pointer;

  .el-icon {
    font-size: 12px;
  }
}

/* Revealed on sidebar hover (or keyboard focus), per the Gemini pattern. */
.collapse {
  display: inline-flex;
  align-items: center;
  border: 0;
  background: transparent;
  color: var(--el-text-color-secondary);
  border-radius: 4px;
  padding: 3px;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .sidebar:hover &,
  &:focus-visible {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }
}

/* Lives in the topbar now: left-aligned and bold, ellipsis when the path
   outruns the bar, full path in the tooltip. */
.cwd {
  min-width: 0;
  max-width: 60%;
  font: 600 11px ui-monospace, monospace;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sessions {
  list-style: none;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  flex: 1;

  li {
    position: relative;
    padding: 8px 12px;
    cursor: pointer;
    border-left: 2px solid transparent;

    &:hover {
      background: var(--el-fill-color);
    }

    &.active {
      background: var(--el-fill-color);
      border-left-color: var(--el-color-primary);
    }
  }
}

.title {
  font-size: 12px;
  line-height: 1.4;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sub {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
  font-size: 10px;
  color: var(--el-text-color-secondary);
}

.pinned-tag {
  padding: 0 5px;
  border-radius: 3px;
  background: color-mix(in srgb, var(--el-color-primary) 12%, transparent);
  color: var(--el-color-primary);
}

.ws-tag {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 0 5px;
  border-radius: 3px;
  background: var(--el-fill-color);
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;

  .el-icon {
    font-size: 10px;
    flex: 0 0 auto;
  }
}

/* The ⋮ menu anchor: hover-revealed like Gemini's, held visible while its
   menu is open (menuOpenFor in the script). */
.sess-menu {
  position: absolute;
  right: 6px;
  top: 50%;
  transform: translateY(-50%);
}

.menu-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .el-icon {
    font-size: 14px;
    transform: rotate(90deg);
  }

  .sessions li:hover &,
  &.open,
  &:focus-visible {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color-darker);
    color: var(--el-text-color-primary);
  }
}

.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.topbar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 16px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  font-size: 12px;
}

.notice {
  padding: 6px 16px;
  font-size: 12px;
  color: var(--el-color-warning);
  background: color-mix(in srgb, var(--el-color-warning) 8%, transparent);

  &.bad {
    color: var(--el-color-danger);
    background: color-mix(in srgb, var(--el-color-danger) 8%, transparent);
  }
}

.ctx-warn {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 16px;
  font-size: 12px;
  color: var(--el-color-danger);
  background: color-mix(in srgb, var(--el-color-danger) 10%, transparent);

  button {
    margin-left: auto;
    flex: none;
    border: 1px solid currentcolor;
    background: transparent;
    color: inherit;
    border-radius: 4px;
    font-size: 11px;
    padding: 2px 8px;
    cursor: pointer;
  }
}

.chat {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

.compose-col {
  display: flex;
  flex-direction: column;
}

.conv-wrap {
  flex: 1;
  min-height: 0;
  position: relative;
  display: flex;
  flex-direction: column;
}

.conversation {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px 24px;
}

/* Session-switch crossfade: opacity/transform only — never a layout
   property, so a long conversation cannot re-wrap mid-animation. The same
   class drives both directions: added, the old content sinks out; removed
   (snapshot arrived), the new content rises in. */
.conv-inner {
  transition:
    opacity 0.18s ease,
    transform 0.18s ease;

  &.switching {
    opacity: 0;
    transform: translateY(4px);
  }
}

.turn-rail {
  position: absolute;
  top: 12px;
  right: 3px;
  bottom: 12px;
  z-index: 5;
  width: 14px;
}

.rail-track {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 50%;
  width: 2px;
  overflow: hidden;
  background: var(--el-border-color-lighter);
  border-radius: 2px;
  transform: translateX(-50%);
}

.rail-fill {
  width: 100%;
  background: color-mix(in srgb, var(--el-color-primary) 45%, transparent);
  border-radius: 2px;
}

.rail-dot {
  position: absolute;
  left: 50%;
  width: 8px;
  height: 8px;
  padding: 0;
  cursor: pointer;
  background: var(--el-text-color-placeholder);
  border: none;
  border-radius: 999px;
  transform: translate(-50%, -50%);
  transition: all 0.15s;

  /* Enlarged hit area: the visual dot is 8px, the clickable one is not. */
  &::before {
    position: absolute;
    inset: -5px;
    content: "";
  }

  &:hover {
    background: var(--el-color-primary);
    transform: translate(-50%, -50%) scale(1.3);
  }

  &.active {
    width: 11px;
    height: 11px;
    background: var(--el-color-primary);
  }
}

/* Hover card opens to the left (the dots sit on the window's right edge). */
.rail-tip {
  position: absolute;
  top: 50%;
  right: 16px;
  width: max-content;
  max-width: 420px;
  padding: 8px 12px;
  display: block;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-primary);
  text-align: left;
  word-break: break-word;
  white-space: normal;
  pointer-events: none;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  box-shadow: var(--el-box-shadow-light);
  opacity: 0;
  transform: translateY(-50%) translateX(4px);
  transition: all 0.15s;
}

.rail-tip-question {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 4;
  overflow: hidden;
}

.rail-tip-time {
  display: block;
  margin-top: 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.rail-dot:hover .rail-tip {
  opacity: 1;
  transform: translateY(-50%);
}

/* Top-of-rail dots: the card hangs below the dot, or the scroller's clipped
   top edge would cut it. */
.rail-tip.tip-down {
  top: calc(100% + 4px);
  transform: translateY(0) translateX(4px);
}

.rail-dot:hover .rail-tip.tip-down {
  transform: translateY(0);
}

/* Bottom-of-rail dots: above the dot, same reason. */
.rail-tip.tip-up {
  top: auto;
  bottom: calc(100% + 4px);
  transform: translateY(0) translateX(4px);
}

.rail-dot:hover .rail-tip.tip-up {
  transform: translateY(0);
}

.empty {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  padding: 24px 0;
}

.ask {
  margin: 18px 0 4px;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.ask-bubble {
  max-width: 76%;
  padding: 10px 16px;
  /* ChatGPT's user bubble: a quiet light-grey tile, not a coloured one. */
  background: var(--el-fill-color);
  color: #000;
  border-radius: 18px;
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.ask-skill {
  width: 100%;
}

.ask-meta {
  display: flex;
  align-items: center;
  gap: 2px;
  margin-top: 4px;
  min-height: 20px;
}

.ask-time {
  /* Centred above the bubble inside the right-aligned column. */
  align-self: center;
  margin-bottom: 4px;
  font-size: 11px;
  color: var(--el-text-color-placeholder);
}

/* Copy/edit icons cost no attention until the cursor is over the message. */
.ask-act {
  display: inline-flex;
  align-items: center;
  border: 0;
  background: transparent;
  padding: 3px;
  border-radius: 4px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;

  .ask:hover &,
  &:focus-visible {
    opacity: 1;
  }

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  &:disabled {
    cursor: default;

    /* The hover-reveal still applies, only dimmer: a control that shows
       itself greyed exactly when it cannot act reads as "wait for the run". */
    .ask:hover &,
    &:focus-visible {
      opacity: 0.35;
    }

    &:hover {
      background: transparent;
      color: var(--el-text-color-secondary);
    }
  }
}

.waiting {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* Three bouncing dots, after the YX agent-qa reference. */
.pend-dots {
  display: inline-flex;
  gap: 3px;
  align-items: center;

  i {
    width: 4px;
    height: 4px;
    background: currentcolor;
    border-radius: 50%;
    animation: pend-bounce 1s ease-in-out infinite;

    &:nth-child(2) {
      animation-delay: 0.15s;
    }

    &:nth-child(3) {
      animation-delay: 0.3s;
    }
  }
}

@keyframes pend-bounce {
  0%,
  100% {
    opacity: 0.5;
    transform: translateY(0);
  }

  50% {
    opacity: 1;
    transform: translateY(-3px);
  }
}

.skill-hints {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 6px 16px 0;
  max-height: 168px;
  overflow-y: auto;
}

/* The first command row is what Enter runs — mark it. */
.cmd-hints .hint:first-child .hint-name::after {
  content: "⏎";
  margin-left: 6px;
  color: var(--el-text-color-placeholder);
}

.hint {
  display: flex;
  align-items: baseline;
  gap: 10px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
  padding: 4px 6px;
  border-radius: 4px;

  &:hover,
  &:focus-visible {
    background: var(--el-fill-color);
  }
}

.hint-name {
  font-family: ui-monospace, monospace;
  font-size: 12px;
  color: var(--el-color-primary);
  flex: 0 0 auto;
}

.hint-desc {
  font-size: 11px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* The composer card floats directly in the layout — no wrapping strip around
   it (that padded wrapper was the "parent rectangle"). The gap is the card's
   own margin; one focus ring around the whole card instead of one around
   just the textarea. */
.input-card {
  display: flex;
  flex-direction: column;
  margin: 0px 16px 14px;
  border: 1px solid var(--el-border-color);
  border-radius: 12px;
  padding: 8px 10px 6px;
  transition:
    border-color 0.2s,
    box-shadow 0.2s;

  &:focus-within {
    border-color: color-mix(in srgb, var(--el-color-primary) 45%, transparent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--el-color-primary) 10%, transparent);
  }

  textarea {
    display: block;
    width: 100%;
    resize: none;
    border: 0;
    background: transparent;
    padding: 2px 0;
    font: 13px/1.6 inherit;
    outline: none;
  }
}

.input-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
}

/* Transparent in the right dock; only the bottom dock positions it (as the
   floating corner actions). */
.actions {
  display: contents;
}

/* Bottom dock: the composer column docks right of the conversation, so the
   messages keep the full height above the panel (the old full-width strip
   wasted the whole right side). Hints and the ctx warning stack above it. */
.shell.dock-bottom .chat {
  flex-direction: row;

  .conv-wrap {
    min-width: 0;
  }

  .compose-col {
    flex: 0 0 clamp(340px, 30%, 460px);
    min-height: 0;
    overflow-y: auto;

    /* The floating card fills the right column (the gap is its own margin),
       and the textarea stretches with it; the send/stop actions float at
       the card's top-right corner. */
    > .input-card {
      flex: 1;
      margin: 10px 12px 12px;
      position: relative;

      textarea {
        flex: 1;
        padding-right: 72px;
      }

      .actions {
        position: absolute;
        top: 6px;
        right: 8px;
        display: flex;
        align-items: center;
        gap: 6px;
      }
    }

    .input-bar {
      flex-wrap: wrap;
    }
  }
}

.spacer {
  flex: 1;
}

/* Send/stop buttons after the YX agent-qa reference: one shared box, the
   primary variant carries hover/active feedback and a real disabled look. */
.send-btn,
.stop {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 30px;
  padding: 0 14px;
  border-radius: 6px;
  border: 1px solid var(--el-border-color);
  background: var(--el-bg-color);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition:
    background 0.15s,
    border-color 0.15s,
    transform 0.15s;

  &:disabled {
    cursor: not-allowed;
  }
}

.stop {
  color: var(--el-color-danger);
}

.send-btn {
  /* Pure black, not the primary ramp: the one affirmative action on the page
     gets the strongest contrast. */
  color: #fff;
  background: #000;
  border-color: #000;

  .el-icon {
    font-size: 15px;
  }

  /* Mid-run the action changes from "start a run" to "join this one", so the
     look changes with it: primary blue instead of black. Declared before
     :disabled — equal specificity, so the grey still wins when both apply. */
  &.steering {
    background: var(--el-color-primary);
    border-color: var(--el-color-primary);

    &:hover:not(:disabled) {
      background: var(--el-color-primary-light-3);
      border-color: var(--el-color-primary-light-3);
    }
  }

  &:hover:not(:disabled) {
    background: #333;
    border-color: #333;
  }

  &:active:not(:disabled) {
    transform: scale(0.92);
  }

  &:disabled {
    color: var(--el-text-color-placeholder);
    background: var(--el-fill-color-light);
    border-color: var(--el-border-color);
  }
}

/* The confirm dialogs render in place (not teleported), so scoped styles
   reach them; :deep() is for the el-dialog chrome itself. */
:deep(.confirm-dialog) {
  border-radius: 12px;

  .el-dialog__header {
    display: none; // the body carries its own head
  }

  .el-dialog__body {
    padding: 18px 20px 4px;
  }

  .el-dialog__footer {
    padding: 10px 20px 16px;
  }
}

.rewind-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.rw-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
}

.rw-icon {
  flex: 0 0 auto;
  width: 34px;
  height: 34px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 17px;
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
}

.rw-headtext {
  flex: 1;
  min-width: 0;

  h3 {
    margin: 2px 0 4px;
    font-size: 15px;
    font-weight: 600;
    color: var(--el-text-color-primary);
  }

  p {
    margin: 0;
    font-size: 12px;
    line-height: 1.5;
    color: var(--el-text-color-secondary);
  }
}

.rw-x {
  flex: 0 0 auto;
  border: none;
  background: none;
  padding: 4px;
  border-radius: 5px;
  cursor: pointer;
  color: var(--el-text-color-secondary);

  &:hover {
    background: var(--el-fill-color);
    color: var(--el-text-color-primary);
  }

  &:disabled {
    opacity: 0.5;
    cursor: default;
  }
}

/* The message this rewind targets: the dialog's object, not just its act.
   A quiet grey tile echoing the user's own chat bubble. */
.rw-quote {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  background: var(--el-fill-color);
  border-radius: 10px;
  padding: 8px 12px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--el-text-color-regular);

  .qicon {
    flex: 0 0 auto;
    margin-top: 2px;
    font-size: 14px;
    color: var(--el-text-color-secondary);
  }

  .qtext {
    min-width: 0;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    word-break: break-all;
  }
}

/* Options are cards, not bare radios: title + consequence per card. */
.rw-opt {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  width: 100%;
  text-align: left;
  background: var(--el-bg-color);
  border: 1px solid var(--el-border-color);
  border-radius: 8px;
  padding: 10px 12px;
  cursor: pointer;
  transition: border-color 0.12s, background 0.12s;

  &:hover {
    border-color: var(--el-border-color-hover);
  }

  &.on {
    border-color: var(--el-color-danger);
    background: var(--el-color-danger-light-9);

    .rw-radio {
      border-color: var(--el-color-danger);

      &::after {
        content: "";
        position: absolute;
        inset: 3px;
        border-radius: 50%;
        background: var(--el-color-danger);
      }
    }
  }
}

.rw-radio {
  flex: 0 0 auto;
  position: relative;
  width: 14px;
  height: 14px;
  margin-top: 2px;
  border-radius: 50%;
  border: 1.5px solid var(--el-border-color);
  transition: border-color 0.12s;
}

.rw-opttext {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.rw-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.rw-desc {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

/* The file impact nests under the option it belongs to, and its rows reuse
   the changes panel's anatomy: icon, badge, path, +N/-N. */
.rw-files {
  margin-left: 24px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  overflow: hidden;
}

.rw-files-head {
  padding: 6px 10px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light);
  border-bottom: 1px solid var(--el-border-color-lighter);
}

.rw-files-list {
  max-height: 180px;
  overflow: auto;
  padding: 4px 0;
}

.rw-file {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 3px 10px;
  font-size: 12px;

  .ficon {
    flex: 0 0 auto;
    font-size: 14px;
  }

  .st {
    flex: 0 0 auto;
    font-size: 11px;
    padding: 0 5px;
    border-radius: 4px;
    background: var(--el-fill-color);
    color: var(--el-text-color-secondary);
  }

  /* A = created afterwards, so the restore deletes it; D = deleted, so the
     restore brings it back. The colors follow the consequence, not the act. */
  .st[data-s="A"] {
    background: var(--el-color-danger-light-9);
    color: var(--el-color-danger);
  }

  .st[data-s="D"] {
    background: var(--el-color-success-light-9);
    color: var(--el-color-success);
  }

  .p {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--el-text-color-regular);
  }

  .stat {
    flex: 0 0 auto;
    display: flex;
    gap: 6px;
    font-size: 11px;
    font-variant-numeric: tabular-nums;

    .plus {
      color: var(--el-color-success);
    }

    .minus {
      color: var(--el-color-danger);
    }

    .bin {
      color: var(--el-text-color-secondary);
    }
  }
}

.rw-more {
  padding: 2px 10px 4px;
  font-size: 11px;
  color: var(--el-text-color-secondary);
}

.rw-warn {
  margin: 0;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.rw-files .rw-warn {
  padding: 6px 10px 8px;
  border-top: 1px solid var(--el-border-color-lighter);
}

.rw-foot {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
