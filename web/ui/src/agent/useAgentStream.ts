import { computed, ref, shallowRef, type Ref } from "vue";
import { api, authHeaders, streamURL } from "@/api/client";
import { flushTreeInvalidate, invalidateIndex, scheduleTreeInvalidate } from "@/components/fileTreeStore";
import { parseFrame } from "./sse";
import { createNewlineCounter } from "./argsPreview";
import { emptyLive, type AgentEvent, type Live, type Message, type PolicyState, type RunInfo, type ToolResult, type Usage } from "@/api/types";
import { gt } from "@/i18n";

// liveThrottleMs bounds how often streaming deltas invalidate the timeline.
//
// Without it, every token rebuilds the whole view — the single biggest
// performance mistake in the reference implementation this UI is modelled on.
// Deltas still arrive individually; only the recomputation is batched.
const liveThrottleMs = 80;

// maxPendingOutput mirrors web/hub.go. Both sides cap the live output of a running
// call, and they have to agree: if the client kept more than a snapshot carries,
// reconnecting would visibly shrink what is on screen.
const maxPendingOutput = 32 * 1024;

// maxPendingFrames mirrors web/hub.go for the same reason, and is a count rather
// than a byte budget because a frame is a whole event: dropping the oldest costs one
// line of a delegated run's history, while cutting bytes would leave a fragment that
// cannot be rendered at all.
const maxPendingFrames = 400;

// maxIncomingHead/maxIncomingTail cap the raw argument text kept for a call that
// is still being generated. They mirror the server's caps on the snapshot's
// live.incoming for the same reason as maxPendingOutput: disagree, and a
// reconnect mid-stream would visibly change what the preview card shows.
const maxIncomingHead = 4096;
const maxIncomingTail = 8192;

// fsMutatingTools are the calls whose tool_end can change the workspace
// listing: the file panel refreshes (debounced) when one settles, so a file
// the agent just wrote shows up without a manual refresh. Mirrors the gated
// set in tools/guard.go — the read-only tools never change a byte.
const fsMutatingTools = new Set(["write", "edit", "bash", "subagent"]);

/** clipTail keeps the last n characters, starting at a line boundary when one is near. */
export function clipTail(s: string, n: number): string {
  if (s.length <= n) return s;
  const tail = s.slice(s.length - n);
  const nl = tail.indexOf("\n");
  return nl >= 0 && nl < 200 ? tail.slice(nl + 1) : tail;
}

export interface RetryNotice {
  attempt: number;
  max: number;
  delayMs: number;
  reason: string;
  at: number;
}

/**
 * Outage is the detail behind the unreachable page: how many reconnect
 * attempts have fired, whether auto-retry gave up (a refused request stays
 * refused), and the last error text.
 */
export interface Outage {
  attempts: number;
  gaveUp: boolean;
  message: string;
}

// outageGraceMs is how long the link may flap before the error page takes
// over. A sub-second blip — a reload, a wifi hop — must not flash a
// full-screen error; a browser's error page does not appear during loading
// either.
const outageGraceMs = 2500;

/**
 * useAgentStream keeps one session's state in sync with the server.
 *
 * It is a client-side mirror of the server's event fold (web/hub.go), which is
 * why a snapshot and a live stream can never disagree: both are the same state
 * machine, applied in the same order.
 */
export function useAgentStream() {
  const messages = ref<Message[]>([]);
  const results = ref<Record<string, ToolResult>>({});
  const live = ref<Live>(emptyLive());
  const run = ref<RunInfo>({ active: false });
  const policy = ref<PolicyState>({ mode: "standard" });
  const usage = ref<Usage>({ input: 0, output: 0 });
  // contextTokens is the latest turn's prompt size. Kept apart from `usage` on
  // purpose: that one is the running total and says nothing about how full the
  // window is.
  const contextTokens = ref(0);
  // overheadTokens is the server's fixed-cost estimate (system prompt + tool
  // schemas). It only arrives with a snapshot — it never changes mid-session.
  const overheadTokens = ref(0);
  // clearTrigger is the prompt size at which the server starts dropping old tool
  // results, or 0 when clearing is off. Only the server knows it — "auto" is a
  // fraction of the current model's window — and the gauge needs it because clearing
  // holds occupancy just below it.
  const clearTrigger = ref(0);
  const seq = ref(0);

  const connected = ref(false);
  const streamError = ref<string | null>(null);
  // unreachable drives the full-page "cannot connect" view; outage carries its
  // detail row. connected=false alone is transient — the page waits out the
  // grace window.
  const unreachable = ref(false);
  const outage = ref<Outage | null>(null);
  const retry = shallowRef<RetryNotice | null>(null);
  const policyReverted = ref<string | null>(null);
  // Steering messages a run accepted but never delivered. The view clears this
  // once it has handed the text back to the user.
  const undelivered = ref<string[]>([]);
  // switching is set when a session change begins and cleared by the new
  // session's first snapshot: the view holds the old conversation (faded)
  // instead of blanking it, which is what used to make switching flicker.
  const switching = ref(false);

  // liveVersion is bumped at most every liveThrottleMs so consumers can memoise
  // on it instead of on the delta stream.
  const liveVersion = ref(0);
  // Bare setTimeout rather than window.setTimeout: the fold is exercised by unit
  // tests that replay a recorded stream, and those run without a DOM.
  let liveTimer: ReturnType<typeof setTimeout> | null = null;
  const touchLive = () => {
    if (liveTimer !== null) return;
    liveTimer = setTimeout(() => {
      liveTimer = null;
      liveVersion.value++;
    }, liveThrottleMs);
  };
  const flushLive = () => {
    if (liveTimer !== null) {
      clearTimeout(liveTimer);
      liveTimer = null;
    }
    liveVersion.value++;
  };

  let controller: AbortController | null = null;
  let sessionId: string | null = null;
  let closedByUs = false;
  let backoff = 500;
  let attempts = 0;
  let outageTimer: ReturnType<typeof setTimeout> | null = null;

  // noteOutage records one failed (or ended) connection attempt. The error
  // page appears immediately when retrying is pointless, otherwise only after
  // the grace window — the timer is shared, so a flapping link arms it once.
  function noteOutage(message: string, gaveUp: boolean) {
    attempts++;
    outage.value = { attempts, gaveUp, message };
    if (gaveUp) {
      unreachable.value = true;
      return;
    }
    if (outageTimer === null && !unreachable.value) {
      outageTimer = setTimeout(() => {
        outageTimer = null;
        unreachable.value = true;
      }, outageGraceMs);
    }
  }

  // clearOutage runs on a successful connect and when the session is dropped:
  // the page closes, the counters reset, the grace timer disarms.
  function clearOutage() {
    attempts = 0;
    outage.value = null;
    unreachable.value = false;
    if (outageTimer !== null) {
      clearTimeout(outageTimer);
      outageTimer = null;
    }
  }

  // One newline counter per still-streaming call. base seeds a counter made
  // for a snapshot-restored entry: the server already counted those lines, so
  // the client counter only adds what arrives after the reconnect.
  const incomingCounters = new Map<string, { counter: ReturnType<typeof createNewlineCounter>; base: number }>();

  // A session switch does NOT reset: the old conversation stays on screen
  // (faded, see `switching`) until the new session's snapshot arrives. The
  // snapshot below overwrites every field wholesale, so nothing stale can
  // survive it — and nothing blank precedes it.
  function apply(e: AgentEvent) {
    if (e.seq) seq.value = e.seq;

    switch (e.type) {
      case "snapshot": {
        const s = e.snapshot;
        if (!s) return;
        messages.value = s.messages ?? [];
        results.value = s.results ?? {};
        live.value = s.live ?? emptyLive();
        incomingCounters.clear();
        run.value = s.run ?? { active: false };
        policy.value = s.policy ?? { mode: "standard" };
        usage.value = s.usage ?? { input: 0, output: 0 };
        contextTokens.value = s.context_tokens ?? 0;
        overheadTokens.value = s.overhead_tokens ?? 0;
        clearTrigger.value = s.clear_trigger ?? 0;
        seq.value = s.seq;
        retry.value = null;
        policyReverted.value = null;
        undelivered.value = [];
        switching.value = false;
        flushLive();
        return;
      }

      case "run_start":
        live.value = { ...emptyLive(), run_id: e.run_id, active: true };
        run.value = { ...run.value, active: true, run_id: e.run_id, model: e.model ?? run.value.model };
        retry.value = null;
        flushLive();
        return;

      case "user_message":
        messages.value = [
          ...messages.value,
          { id: e.message_id ?? `u${messages.value.length}`, role: "user", content: [{ type: "text", text: e.text ?? "" }], ts: e.ts },
        ];
        flushLive();
        return;

      case "turn_start":
        live.value = { ...live.value, turn: e.turn, message_id: e.message_id, text: "", thinking: "" };
        flushLive();
        return;

      case "token":
        live.value.text = (live.value.text ?? "") + (e.text ?? "");
        touchLive();
        return;

      case "thinking":
        live.value.thinking = (live.value.thinking ?? "") + (e.text ?? "");
        touchLive();
        return;

      case "message":
        // A message's usage is that turn's own report, so its prompt size is the
        // current context occupancy.
        if (e.usage?.input) contextTokens.value = e.usage.input;
        // The settled message supersedes whatever was accumulated, so the live
        // copy is dropped rather than rendered twice.
        messages.value = [
          ...messages.value,
          { id: e.message_id ?? `m${messages.value.length}`, role: e.role ?? "assistant", content: e.content ?? [], ts: e.ts },
        ];
        live.value = { ...live.value, text: "", thinking: "" };
        flushLive();
        return;

      // Raw argument fragments of a call whose tool_start has not arrived yet:
      // a big `write` streams for minutes and this is the only sign of life in
      // that gap. Accumulated and throttled like tool_partial, and capped like
      // the server caps it, so a snapshot mid-stream matches what is on screen.
      case "tool_args": {
        if (!e.call_id) return;
        const list = (live.value.incoming ??= []);
        let entry = list.find((t) => t.call_id === e.call_id);
        if (!entry) {
          entry = { call_id: e.call_id, name: e.name ?? "", bytes: 0, ts: e.ts };
          list.push(entry);
        } else if (e.name && !entry.name) {
          // Only the first fragment names the tool, but a late join can see the
          // name arrive after the entry already exists.
          entry.name = e.name;
        }
        const text = e.text ?? "";
        entry.bytes += text.length;
        if (text) {
          let slot = incomingCounters.get(e.call_id);
          if (!slot) {
            slot = { counter: createNewlineCounter(), base: entry.lines ?? 0 };
            incomingCounters.set(e.call_id, slot);
          }
          entry.lines = slot.base + slot.counter.push(text);
        }
        const head = entry.head ?? "";
        if (text && head.length < maxIncomingHead) {
          // The head's only job is to carry the path, so it stops growing once
          // full rather than shadowing the tail's budget.
          entry.head = head + text.slice(0, maxIncomingHead - head.length);
        }
        if (text) entry.tail = clipTail((entry.tail ?? "") + text, maxIncomingTail);
        touchLive();
        return;
      }

      case "tool_start":
        live.value.pending_tools = [
          ...live.value.pending_tools,
          { call_id: e.call_id!, name: e.name!, args: e.args, started_at: e.ts },
        ];
        // The arguments are done generating: the pending-tool card takes over
        // from the incoming preview.
        if (live.value.incoming) {
          live.value.incoming = live.value.incoming.filter((t) => t.call_id !== e.call_id);
        }
        incomingCounters.delete(e.call_id!);
        flushLive();
        return;

      // Appended in place and throttled like the token deltas: a command can
      // print far faster than a browser can usefully repaint. Only the tail is
      // kept, matching what the server puts in a snapshot, so a reconnect does not
      // change how much is on screen.
      case "tool_partial": {
        const call = live.value.pending_tools.find((t) => t.call_id === e.call_id);
        if (!call) return; // a fragment for a call that already settled
        if (e.text) call.output = clipTail((call.output ?? "") + e.text, maxPendingOutput);
        // A subagent reports structured events rather than bytes. Bounded by count
        // and mirroring the server's cap, so a reconnect does not change how much
        // history is on screen.
        if (e.frame) {
          const frames = [...(call.frames ?? []), e.frame];
          call.frames = frames.length > maxPendingFrames ? frames.slice(-maxPendingFrames) : frames;
        }
        touchLive();
        return;
      }

      case "tool_end": {
        // A subagent's frames move onto the result before the pending call goes:
        // they are the run's process and the result is its answer, so unlike
        // live output — whose bytes the result replaces — keeping both renders
        // nothing twice, and the card does not empty out the moment it settles.
        const settled = live.value.pending_tools.find((t) => t.call_id === e.call_id);
        live.value.pending_tools = live.value.pending_tools.filter((t) => t.call_id !== e.call_id);
        // Defensive: a tool_end whose tool_start was missed (log truncation)
        // must not leave the incoming preview behind either.
        if (live.value.incoming) {
          live.value.incoming = live.value.incoming.filter((t) => t.call_id !== e.call_id);
        }
        incomingCounters.delete(e.call_id!);
        results.value = {
          ...results.value,
          [e.call_id!]: {
            call_id: e.call_id!,
            name: e.name,
            text: e.text ?? "",
            is_error: e.is_error,
            details: e.details,
            frames: settled?.frames,
          },
        };
        flushLive();
        // A settled mutating call may have added, renamed or deleted entries;
        // the tree hears about it debounced — failures too, since a half-run
        // command can still have written something.
        if (e.name && fsMutatingTools.has(e.name)) scheduleTreeInvalidate();
        return;
      }

      case "gate_request":
        live.value.pending_gates = [
          ...live.value.pending_gates,
          {
            gate_id: e.gate_id!,
            call_id: e.call_id!,
            tool: e.name!,
            args: e.args,
            deadline: e.deadline ?? 0,
            danger: e.danger,
          },
        ];
        flushLive();
        return;

      case "gate_deadline":
        live.value.pending_gates = live.value.pending_gates.map((g) =>
          g.gate_id === e.gate_id ? { ...g, deadline: e.deadline ?? g.deadline } : g,
        );
        flushLive();
        return;

      case "gate_resolved":
        live.value.pending_gates = live.value.pending_gates.filter((g) => g.gate_id !== e.gate_id);
        flushLive();
        return;

      case "policy_changed":
        if (e.policy) policy.value = e.policy;
        return;

      case "policy_reverted":
        if (e.policy) policy.value = e.policy;
        policyReverted.value = e.reason ?? "auto mode expired";
        return;

      case "model_changed":
        run.value = {
          ...run.value,
          model: e.model,
          provider: e.provider,
          context_window: e.context_window ?? run.value.context_window,
        };
        return;

      case "retry":
        retry.value = {
          attempt: e.attempt ?? 0,
          max: e.max ?? 0,
          delayMs: e.delay_ms ?? 0,
          reason: e.reason ?? "",
          at: e.ts,
        };
        return;

      case "run_end":
        if (e.usage) usage.value = e.usage;
        live.value = emptyLive();
        incomingCounters.clear();
        run.value = { ...run.value, active: false, run_id: undefined };
        retry.value = null;
        if (e.error) streamError.value = e.error;
        // A steering message the run accepted but never delivered. Surfacing it is
        // the difference between the user being told and watching what they typed
        // disappear; the view offers the text back.
        if (e.undelivered?.length) undelivered.value = e.undelivered;
        // Final sync: any debounced tree refresh pending from the run's writes
        // fires now, and the quick-open index — the one expensive listing —
        // is busted so the next ⌘P lazily rewalks the workspace.
        flushTreeInvalidate();
        invalidateIndex();
        flushLive();
        return;

      case "error":
        streamError.value = e.error ?? gt("stream.unknownError");
        return;

      case "gate_auto":
        // Audit only: the call went through under a rule and no card was shown.
        return;

      case "rewound":
      case "compacted": {
        // Two different events with one response, because the response is the same
        // one: whatever this tab holds no longer describes the session's history.
        // A rewind abandoned a branch, a compaction replaced the whole history with
        // a summary of it, and in both cases only a fresh snapshot rebuilds the
        // truth. A REST refetch does that without dropping this still-good stream;
        // the seq guard keeps a snapshot that raced live events from regressing them.
        const sid = sessionId;
        if (!sid) return;
        void api
          .session(sid)
          .then((res) => {
            if (sessionId !== sid || !res.snapshot) return;
            if ((res.snapshot.seq ?? 0) < seq.value) return;
            apply({ seq: 0, type: "snapshot", ts: Date.now(), snapshot: res.snapshot });
          })
          .catch(() => {
            // The stream is intact; the next visit reloads from scratch anyway.
          });
        return;
      }
    }
  }

  async function open(sid: string, from?: number) {
    controller?.abort();
    controller = new AbortController();
    sessionId = sid;
    closedByUs = false;

    try {
      // fetch + ReadableStream rather than EventSource: the stream needs an
      // Authorization header, and EventSource cannot set one.
      const res = await fetch(streamURL(sid, from), {
        headers: authHeaders(),
        signal: controller.signal,
      });
      if (!res.ok || !res.body) {
        streamError.value = res.status === 401 ? gt("stream.unauthorized") : gt("stream.connectFailed", { status: res.status });
        const gaveUp = res.status === 401 || res.status === 404;
        noteOutage(streamError.value, gaveUp);
        // A rejected token will be rejected again; retrying would only spin.
        // Giving up also ends the hold: there is no snapshot coming.
        if (gaveUp) {
          switching.value = false;
          return;
        }
        scheduleReconnect();
        return;
      }

      connected.value = true;
      streamError.value = null;
      backoff = 500;
      clearOutage();

      const reader = res.body.getReader();
      const decoder = new TextDecoder("utf-8");
      let buffer = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let sep: number;
        while ((sep = buffer.indexOf("\n\n")) !== -1) {
          const frame = buffer.slice(0, sep);
          buffer = buffer.slice(sep + 2);
          const e = parseFrame(frame);
          if (e) apply(e);
        }
      }
    } catch (err) {
      if ((err as Error)?.name === "AbortError") return;
      streamError.value = (err as Error)?.message ?? gt("stream.interrupted");
      noteOutage(streamError.value, false);
      scheduleReconnect();
      return;
    } finally {
      connected.value = false;
    }

    // A clean EOF mid-session is the server going away — same outage path as
    // a refused connection.
    if (!closedByUs) {
      noteOutage(gt("stream.disconnected"), false);
      scheduleReconnect();
    }
  }

  // scheduleReconnect resumes from the last sequence number seen, so only the
  // events missed in the gap are replayed. The server degrades to a snapshot on
  // its own if its log no longer reaches back that far.
  function scheduleReconnect() {
    if (closedByUs || !sessionId) return;
    const sid = sessionId;
    const from = resumeFrom();
    const delay = backoff;
    backoff = Math.min(backoff * 2, 5000);
    setTimeout(() => {
      if (!closedByUs && sessionId === sid) void open(sid, from);
    }, delay);
  }

  // resumeFrom is undefined while a session switch is still waiting for its
  // first snapshot: the seq held then belongs to the PREVIOUS session, and
  // replaying the new one from it would apply its events onto stale state.
  // No `from` asks for a snapshot, which is always correct.
  function resumeFrom(): number | undefined {
    return switching.value ? undefined : seq.value;
  }

  function connect(sid: string) {
    closedByUs = false;
    if (sessionId !== sid) {
      // Hold the old session on screen until the new one's snapshot lands;
      // the view fades it meanwhile. See the comment above apply().
      switching.value = true;
      // A new session gets a fresh link: the old one's error page goes away
      // now, not when the fetch happens to succeed.
      clearOutage();
    }
    // No `from` on a fresh connect: a page with no local state wants the
    // snapshot, which is always correct.
    return open(sid);
  }

  // retryNow is the error page's button: one immediate attempt, outside the
  // backoff schedule. The page stays up until an attempt actually succeeds —
  // hiding it on the click would flash it straight back on failure.
  function retryNow() {
    if (!sessionId) return Promise.resolve();
    backoff = 500;
    return open(sessionId, resumeFrom());
  }

  function disconnect() {
    closedByUs = true;
    sessionId = null;
    clearOutage();
    controller?.abort();
    controller = null;
  }

  // A run that has already finished is reported by the snapshot's run.active,
  // not by a replayed run_end. Anything waiting on "the run is over" has to look
  // at this, not only at the event.
  const busy = computed(() => run.value.active);

  return {
    messages: messages as Ref<Message[]>,
    results,
    live,
    run,
    policy,
    usage,
    contextTokens,
    overheadTokens,
    clearTrigger,
    seq,
    busy,
    connected,
    streamError,
    unreachable,
    outage,
    retry,
    undelivered,
    policyReverted,
    liveVersion,
    switching,
    connect,
    disconnect,
    retryNow,
    apply,
  };
}
