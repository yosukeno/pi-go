import { describe, expect, it } from "vitest";
import { chooseLever, contextLevel, createContextEstimator, estimate } from "./contextEstimate";
import type { Message, ToolResult } from "@/api/types";

const msg = (id: string, text: string): Message => ({
  id,
  role: "assistant",
  content: [{ type: "text", text }],
});

describe("estimate", () => {
  it("counts ASCII at 4 chars/token and the rest at 1.5", () => {
    expect(estimate("")).toBe(0);
    expect(estimate("abcd")).toBe(1);
    expect(estimate("abcde")).toBe(2);
    expect(estimate("中文")).toBe(Math.ceil(2 / 1.5));
  });
});

describe("createContextEstimator", () => {
  it("recomputes only for a new message object, never for the same one", () => {
    const est = createContextEstimator();
    const m = msg("m1", "hello world");
    expect(est.ofMessage(m)).toEqual({ tokens: 3, first: "hello world" });
    // The stream never mutates a settled message, so a stale read here is the
    // proof that the second call came from the cache.
    m.content.push({ type: "text", text: "more text" });
    expect(est.ofMessage(m)).toEqual({ tokens: 3, first: "hello world" });
    // A new object is computed on its own, even when its content starts out
    // identical to a cached one's.
    const bigger = msg("m1-bigger", "hello world");
    bigger.content.push({ type: "text", text: "more text" });
    expect(est.ofMessage(bigger).tokens).toBe(3 + estimate("more text"));
  });

  it("counts tool_use input as JSON and keeps no label for a text-less message", () => {
    const est = createContextEstimator();
    const m: Message = {
      id: "m2",
      role: "assistant",
      content: [{ type: "tool_use", id: "c1", name: "write", input: { path: "a.txt" } }],
    };
    expect(est.ofMessage(m)).toEqual({
      tokens: estimate(JSON.stringify({ path: "a.txt" })),
      first: "",
    });
  });

  it("caches results by object identity too", () => {
    const est = createContextEstimator();
    const r: ToolResult = { call_id: "c1", text: "abcd" };
    expect(est.ofResult(r)).toBe(1);
    r.text = "abcdabcdabcdabcd";
    expect(est.ofResult(r)).toBe(1); // same object: still the cached value
    expect(est.ofResult({ call_id: "c1", text: "abcdabcdabcdabcd" })).toBe(4);
  });
});

describe("chooseLever", () => {
  // The common real shape: this project's large sessions were 88%–99% tool output,
  // and there the honest advice is that compaction is not the lever.
  it("names tools when tool output dominates", () => {
    const l = chooseLever(200, 300, 9500);
    expect(l).toEqual({ kind: "tools", share: 95 });
  });

  it("names conversation when the talking dominates", () => {
    const l = chooseLever(4000, 5000, 1000);
    expect(l).toEqual({ kind: "conversation", share: 90 });
  });

  // User and assistant text are one side: both are conversation, and neither leaves
  // the prompt on its own.
  it("counts user and assistant text together", () => {
    expect(chooseLever(600, 0, 500)?.kind).toBe("conversation");
    expect(chooseLever(0, 600, 500)?.kind).toBe("conversation");
    // Either alone would lose to tools; together they win.
    expect(chooseLever(300, 300, 500)?.kind).toBe("conversation");
  });

  // A tie is not evidence that conversation text is the problem, and recommending a
  // lossy rewrite needs evidence.
  it("gives ties to tools", () => {
    expect(chooseLever(250, 250, 500)).toEqual({ kind: "tools", share: 50 });
  });

  // Nothing measured yet: no advice rather than advice derived from zero.
  it("returns null with nothing to weigh", () => {
    expect(chooseLever(0, 0, 0)).toBeNull();
  });

  it("reports the dominant side's share, not the loser's", () => {
    expect(chooseLever(0, 100, 900)?.share).toBe(90);
    expect(chooseLever(0, 900, 100)?.share).toBe(90);
  });
});

describe("contextLevel", () => {
  const W = 1_000_000;
  const T = 800_000; // four fifths, what "auto" resolves to

  // The bug this exists to prevent: with the trigger at four fifths, a gauge using
  // fixed percentages would be amber for the whole of any busy session, because
  // clearing holds occupancy right below the trigger. That is where normal is.
  it("stays green just below the trigger", () => {
    expect(contextLevel(790_000, W, T)).toBe("low");
    expect(contextLevel(799_999, W, T)).toBe("low");
  });

  it("goes amber at the trigger, where clearing has engaged", () => {
    expect(contextLevel(800_000, W, T)).toBe("mid");
    expect(contextLevel(880_000, W, T)).toBe("mid");
  });

  // Halfway from the trigger to the ceiling: derived, so it cannot drift from the
  // trigger. With a trigger at 80% this is 90%, and the last tenth is what the
  // model's own output needs.
  it("goes red only once clearing is not keeping up", () => {
    expect(contextLevel(899_999, W, T)).toBe("mid");
    expect(contextLevel(900_000, W, T)).toBe("high");
    expect(contextLevel(990_000, W, T)).toBe("high");
  });

  // The boundary follows the trigger rather than being a second constant.
  it("moves the red boundary with the trigger", () => {
    expect(contextLevel(600_000, W, 500_000)).toBe("mid"); // red would be 750_000
    expect(contextLevel(750_000, W, 500_000)).toBe("high");
  });

  // With nothing pulling the prompt back down, the fixed bands are the right ones —
  // they were chosen for exactly this case.
  it("falls back to fixed bands when clearing is off", () => {
    expect(contextLevel(600_000, W, 0)).toBe("low");
    expect(contextLevel(700_000, W, 0)).toBe("mid");
    expect(contextLevel(850_000, W, 0)).toBe("high");
    // Omitting the argument entirely is the same as off.
    expect(contextLevel(850_000, W)).toBe("high");
  });

  // A trigger at or past the window cannot describe bands, so the fixed ones stand
  // in. Reachable via -context-edit <n> with a number larger than the window.
  it("ignores a trigger the window cannot reach", () => {
    expect(contextLevel(900_000, W, W)).toBe("high");
    expect(contextLevel(900_000, W, 2 * W)).toBe("high");
  });

  it("is green with nothing measured or no known window", () => {
    expect(contextLevel(0, W, T)).toBe("low");
    expect(contextLevel(900_000, 0, T)).toBe("low");
  });
});
