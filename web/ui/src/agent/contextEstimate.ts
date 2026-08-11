import type { Message, ToolResult } from "@/api/types";

// chars → tokens: ASCII ≈ 4 chars per token, the rest (mostly CJK) ≈ 1.5.
// Only a proportion helper: the absolute magnitude comes from the measured
// prompt size, this decides how the slices relate to each other.
export function estimate(text: string): number {
  let ascii = 0;
  for (let i = 0; i < text.length; i++) {
    if (text.charCodeAt(i) < 128) ascii++;
  }
  return Math.ceil(ascii / 4 + (text.length - ascii) / 1.5);
}

export interface MessageEstimate {
  tokens: number;
  /** first is the message's first text block, used for the row label. */
  first: string;
}

/**
 * createContextEstimator memoises the estimate per object, keyed on identity.
 *
 * The stream appends settled messages and results immutably
 * ([...prev, next] in useAgentStream), so an object once seen never changes
 * and a WeakMap hit is exact; a reconnect's snapshot simply arrives as fresh
 * objects. Without this, every message/tool_end event re-stringified and
 * re-counted the entire transcript — quadratic over a run, and a multi-MB
 * write made each later event a several-hundred-ms main-thread stall.
 */
export function createContextEstimator() {
  const messages = new WeakMap<Message, MessageEstimate>();
  const results = new WeakMap<ToolResult, number>();

  function ofMessage(m: Message): MessageEstimate {
    const hit = messages.get(m);
    if (hit) return hit;
    let tokens = 0;
    let first = "";
    for (const b of m.content) {
      if (b.type === "text") {
        tokens += estimate(b.text);
        if (!first) first = b.text;
      } else if (b.type === "tool_use" && b.input != null) {
        tokens += estimate(JSON.stringify(b.input));
      }
    }
    const est = { tokens, first };
    messages.set(m, est);
    return est;
  }

  function ofResult(r: ToolResult): number {
    const hit = results.get(r);
    if (hit !== undefined) return hit;
    const tokens = estimate(r.text ?? "");
    results.set(r, tokens);
    return tokens;
  }

  return { ofMessage, ofResult };
}

/** Which of the two context mechanisms the current shape calls for. */
export interface Lever {
  kind: "tools" | "conversation";
  /** share is the dominant side's percentage of the conversation-side total. */
  share: number;
}

/**
 * chooseLever decides which advice the context breakdown should give.
 *
 * The two categories are not symmetric, which is the whole reason this exists as a
 * decision rather than a bar. Old tool results leave the prompt on their own and
 * their placeholders say how to fetch them back, so a tools-heavy context is
 * already being managed and compaction would buy little. Conversation text is the
 * shape nothing handles by itself, and the one compaction is for.
 *
 * Measured across this project's own transcripts, the large sessions were 88%–99%
 * tool output — so "compaction is the wrong lever" is the common answer. Saying so
 * is worth more than a button that always looks equally advisable.
 *
 * Ties go to tools, because a tie is not evidence that conversation text is the
 * problem, and recommending a lossy rewrite needs evidence.
 */
export function chooseLever(user: number, assistant: number, tools: number): Lever | null {
  const conv = user + assistant;
  const all = conv + tools;
  if (all <= 0) return null;
  const kind = tools >= conv ? "tools" : "conversation";
  const dominant = kind === "tools" ? tools : conv;
  return { kind, share: Math.round((dominant / all) * 100) };
}

/** How full the context is, in bands a colour can be attached to. */
export type ContextLevel = "low" | "mid" | "high";

/**
 * contextLevel picks the gauge's warning band.
 *
 * The bands are derived from the clearing trigger rather than from fixed fractions of
 * the window, because with clearing on the two say opposite things. Clearing holds
 * occupancy just below the trigger, so once that trigger is four fifths of the window
 * a gauge that turned amber at 70% would be amber for the whole session — a warning
 * colour that is always on carries no information.
 *
 * With clearing on:
 *   low   below the trigger — clearing has not needed to start
 *   mid   at or above it — clearing is running and holding the prompt down
 *   high  halfway from the trigger to the ceiling — clearing is *not* keeping up,
 *         which is the only state worth interrupting for
 *
 * The high boundary is derived rather than picked so it cannot drift from the
 * trigger: with a trigger at 80% it lands at 90%, and the remaining tenth is what the
 * model's own output needs.
 *
 * With clearing off (trigger 0) nothing pulls the prompt back down, so the fixed
 * bands are the right ones — they were chosen for exactly that situation.
 */
export function contextLevel(used: number, window: number, trigger = 0): ContextLevel {
  if (!window || used <= 0) return "low";
  if (trigger > 0 && trigger < window) {
    if (used >= trigger + (window - trigger) / 2) return "high";
    if (used >= trigger) return "mid";
    return "low";
  }
  const pct = (used / window) * 100;
  if (pct >= 85) return "high";
  if (pct >= 70) return "mid";
  return "low";
}
