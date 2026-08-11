import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { parseFrame } from "./sse";
import { buildTimeline, isBashDetails, isEditDetails } from "./timeline";
import { useAgentStream } from "./useAgentStream";
import type { AgentEvent } from "@/api/types";

// A recorded SSE stream from a real run against a real model: edit a file, get
// stopped by the approval gate on bash, get approved, finish.
//
// Hand-written events prove the client agrees with my idea of the protocol.
// A recording proves it agrees with the server.
const fixture = readFileSync(fileURLToPath(new URL("./__fixtures__/real-run.sse", import.meta.url)), "utf8");

function frames(): AgentEvent[] {
  return fixture
    .split("\n\n")
    .map(parseFrame)
    .filter((e): e is AgentEvent => e !== null);
}

describe("replaying a recorded run", () => {
  it("has the frames the run produced", () => {
    const kinds = new Set(frames().map((e) => e.type));
    for (const want of ["snapshot", "run_start", "user_message", "turn_start", "tool_start", "tool_end", "gate_request", "gate_resolved", "token", "message", "run_end"]) {
      expect(kinds, `missing ${want}`).toContain(want);
    }
  });

  it("shows the pending approval on the call it belongs to", () => {
    const s = useAgentStream();
    const all = frames();
    const upToGate = all.slice(0, all.findIndex((e) => e.type === "gate_request") + 1);
    for (const e of upToGate) s.apply(e);

    expect(s.busy.value).toBe(true);
    expect(s.live.value.pending_gates).toHaveLength(1);

    const items = buildTimeline(s.messages.value, s.results.value, s.live.value);
    const gated = items.flatMap((i) => (i.kind === "turn" ? i.calls : [])).filter((c) => c.gate);
    expect(gated).toHaveLength(1);
    expect(gated[0].name).toBe("bash");
    // Absolute epoch milliseconds, so a reloaded page can still compute the
    // remaining time.
    expect(gated[0].gate!.deadline).toBeGreaterThan(1_700_000_000_000);
  });

  it("ends with the edit diff, the command output, and no run in flight", () => {
    const s = useAgentStream();
    for (const e of frames()) s.apply(e);

    expect(s.busy.value).toBe(false);
    expect(s.live.value.pending_gates).toHaveLength(0);
    expect(s.live.value.pending_tools).toHaveLength(0);
    expect(s.usage.value.input).toBeGreaterThan(0);
    expect(s.run.value.context_window).toBeGreaterThan(0);

    // Context occupancy is the last turn's prompt, not the session total. In this
    // recording the total is several times the real occupancy after only four
    // turns, because every turn resends the conversation — which is exactly why
    // the meter must not be built on it.
    expect(s.contextTokens.value).toBeGreaterThan(0);
    expect(s.contextTokens.value).toBeLessThan(s.usage.value.input);

    const items = buildTimeline(s.messages.value, s.results.value, s.live.value);
    expect(items[0].kind).toBe("user");

    const calls = items.flatMap((i) => (i.kind === "turn" ? i.calls : []));
    const edit = calls.find((c) => c.name === "edit");
    expect(edit, "the run edited a file").toBeDefined();
    const editDetails = edit!.result?.details;
    if (!isEditDetails(editDetails)) throw new Error("edit details missing");
    expect(editDetails.path).toContain("config.go");
    // The diff is the payload the whole structured-details channel exists for.
    expect(editDetails.diff).toMatch(/^\s*-.*30/m);
    expect(editDetails.diff).toMatch(/^\s*\+.*60/m);
    expect(editDetails.added + editDetails.removed).toBeGreaterThan(0);

    const bash = calls.find((c) => c.name === "bash");
    expect(bash, "the run ran a command").toBeDefined();
    const bashDetails = bash!.result?.details;
    if (!isBashDetails(bashDetails)) throw new Error("bash details missing");
    expect(bashDetails.exit_code).toBe(0);

    // Every call finished: nothing left running and nothing orphaned.
    expect(calls.filter((c) => c.running || c.orphaned)).toHaveLength(0);

    // Turn numbering restarts per question and runs 1..n within it.
    const turnIndexes = items.filter((i) => i.kind === "turn").map((i) => (i.kind === "turn" ? i.index : 0));
    expect(turnIndexes[0]).toBe(1);
    expect(turnIndexes).toEqual([...turnIndexes].sort((a, b) => a - b));

    // The last turn carries the answer text, assembled from token deltas and
    // then replaced by the settled message.
    const last = items[items.length - 1];
    if (last.kind !== "turn") throw new Error("expected a turn last");
    expect(last.text.length).toBeGreaterThan(0);
    expect(last.streaming).toBe(false);
  });

  it("survives a mid-stream reconnect by re-applying a snapshot", () => {
    // What a browser reload does: throw away local state, apply the snapshot the
    // server sends, keep going. The result has to match the uninterrupted replay.
    const all = frames();
    const straight = useAgentStream();
    for (const e of all) straight.apply(e);

    const reconnected = useAgentStream();
    const cut = all.findIndex((e) => e.type === "gate_request");
    for (const e of all.slice(0, cut)) reconnected.apply(e);
    // A snapshot replaces everything, exactly as the first frame of a new
    // subscription does.
    reconnected.apply({
      seq: 999,
      type: "snapshot",
      ts: Date.now(),
      snapshot: {
        seq: 999,
        messages: straight.messages.value,
        results: straight.results.value,
        live: straight.live.value,
        run: straight.run.value,
        policy: straight.policy.value,
        usage: straight.usage.value,
        context_tokens: straight.contextTokens.value,
      },
    });

    expect(reconnected.messages.value).toEqual(straight.messages.value);
    expect(reconnected.busy.value).toBe(false);
  });
});

describe("incoming tool arguments", () => {
  // A big `write` streams its arguments for minutes before tool_start; the
  // tool_args fragments are the only sign of life in that gap.
  it("accumulates fragments under one call_id", () => {
    const s = useAgentStream();
    s.apply({ seq: 1, type: "tool_args", ts: 1, call_id: "c1", name: "write" });
    s.apply({ seq: 2, type: "tool_args", ts: 2, call_id: "c1", text: '{"path": "a.ts", "content": "' });
    s.apply({ seq: 3, type: "tool_args", ts: 3, call_id: "c1", text: "line1\\nline2" });

    const inc = s.live.value.incoming!;
    expect(inc).toHaveLength(1);
    expect(inc[0].name).toBe("write");
    expect(inc[0].bytes).toBe('{"path": "a.ts", "content": "'.length + "line1\\nline2".length);
    expect(inc[0].head).toBe('{"path": "a.ts", "content": "line1\\nline2');
    expect(inc[0].tail).toBe(inc[0].head);
  });

  it("fills in the name when it only arrives on a later fragment", () => {
    const s = useAgentStream();
    s.apply({ seq: 1, type: "tool_args", ts: 1, call_id: "c1", text: "{" });
    expect(s.live.value.incoming![0].name).toBe("");
    s.apply({ seq: 2, type: "tool_args", ts: 2, call_id: "c1", name: "write", text: '"path": "a.ts"' });
    expect(s.live.value.incoming![0].name).toBe("write");
    expect(s.live.value.incoming![0].head).toBe('{"path": "a.ts"');
  });

  it("caps head at 4096 and tail at 8192, matching the server", () => {
    const s = useAgentStream();
    s.apply({ seq: 1, type: "tool_args", ts: 1, call_id: "c1", name: "write", text: "x".repeat(5000) });
    let inc = s.live.value.incoming![0];
    expect(inc.head).toHaveLength(4096);
    expect(inc.bytes).toBe(5000);

    s.apply({ seq: 2, type: "tool_args", ts: 2, call_id: "c1", text: "y".repeat(5000) });
    inc = s.live.value.incoming![0];
    // The head stopped growing once full; the tail kept the newest text.
    expect(inc.head).toHaveLength(4096);
    expect(inc.tail!.length).toBeLessThanOrEqual(8192);
    expect(inc.tail!.endsWith("y".repeat(100))).toBe(true);
    expect(inc.bytes).toBe(10000);
  });

  it("drops the entry on tool_start, and defensively on tool_end", () => {
    const s = useAgentStream();
    s.apply({ seq: 1, type: "tool_args", ts: 1, call_id: "c1", name: "write", text: '{"path": "a.ts"' });
    s.apply({ seq: 2, type: "tool_args", ts: 2, call_id: "c2", name: "write", text: '{"path": "b.ts"' });
    s.apply({ seq: 3, type: "tool_start", ts: 3, call_id: "c1", name: "write", args: { path: "a.ts" } });

    expect(s.live.value.incoming!.map((t) => t.call_id)).toEqual(["c2"]);
    expect(s.live.value.pending_tools.map((t) => t.call_id)).toEqual(["c1"]);

    // A tool_end whose tool_start never arrived (log truncation) still clears
    // the preview.
    s.apply({ seq: 4, type: "tool_end", ts: 4, call_id: "c2", name: "write", text: "ok" });
    expect(s.live.value.incoming).toHaveLength(0);
  });

  it("survives a snapshot round-trip and is cleared when the run ends", () => {
    const straight = useAgentStream();
    straight.apply({ seq: 1, type: "run_start", ts: 1, run_id: "r1" });
    straight.apply({ seq: 2, type: "tool_args", ts: 2, call_id: "c1", name: "write", text: '{"path": "a.ts"' });

    // What a browser reload does mid-stream: throw away local state and apply
    // the snapshot, whose live.incoming is the only record of the preview.
    const reconnected = useAgentStream();
    reconnected.apply({
      seq: 3,
      type: "snapshot",
      ts: 3,
      snapshot: {
        seq: 3,
        messages: straight.messages.value,
        results: straight.results.value,
        live: straight.live.value,
        run: straight.run.value,
        policy: straight.policy.value,
        usage: straight.usage.value,
        context_tokens: straight.contextTokens.value,
      },
    });
    expect(reconnected.live.value.incoming).toEqual(straight.live.value.incoming);

    // An abort mid-stream must not leave a stale card behind.
    reconnected.apply({ seq: 4, type: "run_end", ts: 4 });
    expect(reconnected.live.value.incoming ?? []).toHaveLength(0);
  });
});

describe("a delegated run's frames", () => {
  // While the call runs they accumulate on the pending tool; when it settles
  // they move onto the result, so the card keeps the run's process instead of
  // emptying out the moment the answer arrives.
  it("move from the pending call onto its settled result", () => {
    const s = useAgentStream();
    s.apply({ seq: 1, type: "tool_start", ts: 1, call_id: "c1", name: "subagent" });
    s.apply({
      seq: 2,
      type: "tool_partial",
      ts: 2,
      call_id: "c1",
      name: "subagent",
      frame: { type: "tool_start", call_id: "r1", name: "read", args: { path: "main.go" } },
    });
    s.apply({
      seq: 3,
      type: "tool_partial",
      ts: 3,
      call_id: "c1",
      name: "subagent",
      frame: { type: "tool_end", call_id: "r1", name: "read", text: "package main" },
    });

    expect(s.live.value.pending_tools[0].frames).toHaveLength(2);

    s.apply({ seq: 4, type: "tool_end", ts: 4, call_id: "c1", name: "subagent", text: "the answer" });

    expect(s.live.value.pending_tools).toHaveLength(0);
    const settled = s.results.value["c1"];
    expect(settled.text).toBe("the answer");
    expect(settled.frames).toHaveLength(2);
    expect(settled.frames?.[0]).toMatchObject({ type: "tool_start", name: "read" });
  });
});


describe("session switching", () => {
  it("holds the old conversation until the new session's snapshot lands", async () => {
    const s = useAgentStream();
    s.apply(frames()[0]); // the recording opens with a snapshot
    s.apply({ seq: 90, type: "user_message", ts: 1, message_id: "m1", role: "user", text: "old session" });
    expect(s.messages.value.length).toBeGreaterThan(0);

    // The new session's stream stays in flight: no snapshot yet.
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );
    try {
      void s.connect("another-session");
      expect(s.switching.value).toBe(true);
      // Nothing was reset: the old conversation is still on screen.
      expect(s.messages.value.length).toBeGreaterThan(0);

      // The new session's snapshot ends the hold and replaces wholesale.
      s.apply({ ...frames()[0], snapshot: { ...frames()[0].snapshot!, messages: [], seq: 1 } });
      expect(s.switching.value).toBe(false);
      expect(s.messages.value).toHaveLength(0);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("reconnects without the old session's seq while the switch is unresolved", async () => {
    vi.useFakeTimers();
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        urls.push(String(url));
        return Promise.reject(new Error("down"));
      }),
    );
    try {
      const s = useAgentStream();
      s.apply({ seq: 42, type: "run_end", ts: 1 }); // seq 42 belongs to the OLD session
      void s.connect("another-session");
      await vi.advanceTimersByTimeAsync(0); // the rejection schedules a reconnect
      await vi.advanceTimersByTimeAsync(600); // backoff starts at 500ms

      expect(urls.length).toBeGreaterThanOrEqual(2);
      // A fresh connect never carries `from`; a mid-switch reconnect must not
      // either — replaying from 42 would apply events onto the old state.
      expect(urls[0]).not.toContain("from=");
      expect(urls[1]).not.toContain("from=");
    } finally {
      vi.unstubAllGlobals();
      vi.useRealTimers();
    }
  });
});
