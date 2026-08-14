import type {
  IncomingArgs,
  Live,
  Message,
  PendingGate,
  ReadFileDetails,
  ReadManyDetails,
  SubagentDetails,
  SubagentFrame,
  TodoDetails,
  TodoItem,
  ToolDetails,
  ToolName,
  ToolResult,
} from "@/api/types";
import { gt } from "@/i18n";

// This module is deliberately free of Vue imports: it is a pure projection of
// server state into what the view renders, so it can be unit tested without a
// component, a DOM, or a running agent.

export interface TimelineCall {
  callId: string;
  name: ToolName;
  args: unknown;
  result?: ToolResult;
  /** running is true between tool_start and tool_end. */
  running: boolean;
  /**
   * liveOutput is what a still-running call has printed so far. Only bash reports
   * it, and it is replaced by `result` once the call settles, so the same output
   * is never shown twice.
   */
  liveOutput?: string;
  /**
   * liveFrames is the delegated run's own events. Structured rather than text
   * because a delegation has structure — turns, tool calls, an ending — and a
   * card that only got "· read" could not show it. While the call runs they
   * come from the pending tool; once it settles, from the copy the client
   * retained on the result, so the card keeps its process instead of emptying
   * out the moment the answer arrives.
   */
  liveFrames?: SubagentFrame[];
  /** gate is set while this call is waiting for a human. */
  gate?: PendingGate;
  /** orphaned: never finished and no run is active — the process died mid-call. */
  orphaned: boolean;
  /**
   * corrects points at an earlier failed call this one appears to fix. It is a
   * heuristic (same tool, same target, later, succeeded) and the UI renders it
   * as a hint, not as an assertion.
   */
  corrects?: string;
  /**
   * superseded marks a task-list write that a later one replaced.
   *
   * The todo tool holds no state: the current plan is whichever of these wrote
   * last, and every earlier one is a snapshot of a plan that no longer applies.
   * Rendering them all identically would put several contradictory checklists on
   * one timeline, and someone scrolling up would read an old "1/3 done" as the
   * present state. So the flag exists to let the card collapse the old ones —
   * they stay visible, because how the plan changed is worth seeing, but they
   * stop claiming to be the plan.
   */
  superseded?: boolean;
}

export interface UserItem {
  kind: "user";
  id: string;
  text: string;
  /** Send time, epoch ms. Absent on transcripts old enough to lack it. */
  ts?: number;
}

export interface TurnItem {
  kind: "turn";
  id: string;
  /** index is the 1-based turn number within the current exchange. */
  index: number;
  thinking: string;
  text: string;
  calls: TimelineCall[];
  /**
   * Calls whose arguments are still streaming in, rendered as preview cards
   * where their pending-tool card will later appear. Never present on a
   * settled turn's own row: every entry is adopted onto the in-flight turn,
   * the same place an orphan gate goes.
   */
  incoming?: IncomingArgs[];
  /** streaming is true for the turn whose message has not settled yet. */
  streaming: boolean;
}

export type TimelineItem = UserItem | TurnItem;

/**
 * buildTimeline projects the server state into a flat list of exchanges.
 *
 * pi-go's shape is a flat sequence of turns — one LLM call plus the tools it
 * asked for. There is no triage, no plan, no sub-task fan-out, so there is
 * nothing here that corresponds to the phase/wave structure a plan-execute agent
 * needs.
 */
export function buildTimeline(
  messages: Message[],
  results: Record<string, ToolResult>,
  live: Live,
): TimelineItem[] {
  const items: TimelineItem[] = [];
  let turnIndex = 0;

  for (const m of messages) {
    if (m.role === "user") {
      const text = m.content
        .filter((b): b is Extract<typeof b, { type: "text" }> => b.type === "text")
        .map((b) => b.text)
        .join("");
      items.push({ kind: "user", id: m.id, text, ts: m.ts });
      turnIndex = 0;
      continue;
    }

    turnIndex++;
    const thinking: string[] = [];
    const text: string[] = [];
    const calls: TimelineCall[] = [];
    for (const b of m.content) {
      if (b.type === "thinking") thinking.push(b.text);
      else if (b.type === "text") text.push(b.text);
      else if (b.type === "tool_use") {
        const result = results[b.id];
        const pending = live.pending_tools.find((t) => t.call_id === b.id);
        const running = pending !== undefined;
        calls.push({
          callId: b.id,
          name: b.name,
          args: b.input,
          result,
          running,
          liveOutput: pending?.output,
          liveFrames: pending?.frames ?? result?.frames,
          gate: live.pending_gates.find((g) => g.call_id === b.id),
          orphaned: !result && !running && !live.active,
        });
      }
    }
    items.push({
      kind: "turn",
      id: m.id,
      index: turnIndex,
      thinking: thinking.join(""),
      text: text.join(""),
      calls,
      streaming: false,
    });
  }

  // The turn in flight, before its message settles. Once `message` arrives the
  // server clears live.text, so this cannot duplicate anything.
  if (live.active && ((live.text ?? "") !== "" || (live.thinking ?? "") !== "")) {
    items.push({
      kind: "turn",
      id: live.message_id ?? "live",
      index: turnIndex + 1,
      thinking: live.thinking ?? "",
      text: live.text ?? "",
      calls: [],
      streaming: true,
    });
  }

  adoptOrphanGates(items, live, turnIndex);
  adoptIncoming(items, live, turnIndex);
  linkCorrections(items);
  markSupersededTodos(items);
  return items;
}

/**
 * adoptOrphanGates surfaces an approval whose tool_use has not arrived yet.
 *
 * Gate events are published by the gate itself rather than through the loop's
 * event channel, so a gate_request can overtake the assistant message that
 * requested the call — it happens routinely, since the two are emitted
 * microseconds apart from different goroutines. A card that only rendered once
 * its call was known would blink into existence late, and after a log truncation
 * might never appear at all. The run is blocked on this card, so it has to show
 * up regardless.
 */
function adoptOrphanGates(items: TimelineItem[], live: Live, turnIndex: number): void {
  const known = new Set<string>();
  for (const item of items) {
    if (item.kind === "turn") for (const c of item.calls) known.add(c.callId);
  }
  const orphans = live.pending_gates.filter((g) => !known.has(g.call_id));
  if (orphans.length === 0) return;

  const host = hostForLive(items, live, turnIndex);
  for (const gate of orphans) {
    host.calls.push({
      callId: gate.call_id,
      name: gate.tool,
      args: gate.args,
      running: false,
      gate,
      orphaned: false,
    });
  }
}

/**
 * adoptIncoming puts the calls whose arguments are still streaming onto the
 * turn in flight, so the preview card sits exactly where the pending-tool
 * card will appear once tool_start lands. Like an orphan gate, an incoming
 * call has no tool_use block yet, so without adoption it would render
 * nowhere.
 */
function adoptIncoming(items: TimelineItem[], live: Live, turnIndex: number): void {
  if (!live.incoming?.length) return;
  const host = hostForLive(items, live, turnIndex);
  host.incoming = [...(host.incoming ?? []), ...live.incoming];
}

/** hostForLive finds or creates the turn item that represents the run in flight. */
function hostForLive(items: TimelineItem[], live: Live, turnIndex: number): TurnItem {
  const last = items[items.length - 1];
  if (last && last.kind === "turn") return last;
  const host: TurnItem = {
    kind: "turn",
    id: live.message_id ?? "live",
    index: turnIndex + 1,
    thinking: live.thinking ?? "",
    text: "",
    calls: [],
    streaming: true,
  };
  items.push(host);
  return host;
}

/**
 * linkCorrections marks a successful call as the repair of an earlier failed one.
 *
 * This is pi-go's signature behaviour: a tool failure is fed back as text and the
 * model fixes itself. Showing the two calls as unrelated failures hides the most
 * interesting thing the agent does. The match is intentionally narrow — same
 * tool, same target, later in time, and the earlier one failed.
 */
function linkCorrections(items: TimelineItem[]): void {
  const failed = new Map<string, string>(); // "tool\0target" -> callId
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      const key = `${call.name}\u0000${targetOf(call.name, call.args)}`;
      if (call.result?.is_error) {
        failed.set(key, call.callId);
        continue;
      }
      if (call.result && !call.result.is_error) {
        const earlier = failed.get(key);
        if (earlier && earlier !== call.callId) {
          call.corrects = earlier;
          failed.delete(key);
        }
      }
    }
  }
}

/**
 * markSupersededTodos flags every task-list write except the last one that
 * settled.
 *
 * A second pass rather than a check inside the projection, for the same reason
 * linkCorrections is one: "is this the newest" is a fact about the whole timeline,
 * and a loop building items one at a time cannot know it until it has finished.
 *
 * Only settled, successful writes count as the current plan. A rejected call
 * changed nothing — a list with two items in_progress never became the state — so
 * it must not demote the good list before it. A still-running one has not written
 * anything yet either, and letting it demote its predecessor would blank the plan
 * for as long as the call is in flight.
 */
function markSupersededTodos(items: TimelineItem[]): void {
  let newest: TimelineCall | undefined;
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const call of item.calls) {
      if (call.name !== "todo") continue;
      // No result means nothing was written, which covers both a call still in
      // flight and one orphaned by a dead process — a `running` clause would be
      // unreachable here, since the result only arrives at tool_end.
      if (!call.result || call.result.is_error) continue;
      if (newest) newest.superseded = true;
      newest = call;
    }
  }
}

/** targetOf is what two calls have to agree on to be "the same work". */
export function targetOf(name: ToolName, args: unknown): string {
  const a = (args ?? {}) as Record<string, unknown>;
  if (name === "bash") return String(a.command ?? "");
  return String(a.path ?? "");
}

/** summarizeArgs is the one-line label next to a tool call. */
export function summarizeArgs(name: ToolName, args: unknown): string {
  const a = (args ?? {}) as Record<string, unknown>;
  if (name === "bash") return String(a.command ?? "");
  // A delegation's label is its task: the first line says what was delegated,
  // which is the one thing a bare "subagent" does not. Head-truncated rather
  // than tail-truncated, because a task reads like a sentence, not a path.
  if (name === "subagent") {
    const first = String(a.task ?? "").split("\n", 1)[0].trim();
    return first.length > 72 ? first.slice(0, 71) + "…" : first;
  }
  const path = String(a.path ?? "");
  // ls defaults to the working directory, and a bare label reads as a missing
  // argument rather than as that default.
  if (name === "ls") return path || ".";
  // For a search the pattern is the point; the path is the qualifier.
  if (name === "find" || name === "grep") {
    const pattern = String(a.pattern ?? "");
    const include = name === "grep" && a.include ? ` (${String(a.include)})` : "";
    const where = path && path !== "." ? ` ${gt("timeline.searchIn", { path })}` : "";
    return `${pattern}${include}${where}`;
  }
  if (name === "read" && (a.offset || a.limit)) {
    const offset = Number(a.offset ?? 0);
    const limit = Number(a.limit ?? 0);
    return `${path}  ${offset + 1}${limit ? `-${offset + limit}` : "+"}`;
  }
  // A task-list write labels itself with the item being started. The list itself
  // is right below in the card, so repeating it on the header line would say
  // nothing; what the line can add is which of the items is the live one.
  if (name === "todo") {
    const todos = Array.isArray(a.todos) ? (a.todos as TodoItem[]) : [];
    const current = todos.find((t) => t?.status === "in_progress");
    if (current) return String(current.task ?? "");
    if (todos.length === 0) return gt("timeline.todoCleared");
    return todos.length === 1
      ? gt("timeline.todoCountOne", { n: todos.length })
      : gt("timeline.todoCount", { n: todos.length });
  }
  return path;
}

/** shortPath keeps the tail of a long path, which is the part that identifies it. */
export function shortPath(path: string, max = 44): string {
  if (path.length <= max) return path;
  return "…" + path.slice(path.length - max + 1);
}

export interface DiffPayload {
  diff: string;
  added: number;
  removed: number;
  path: string;
}

/**
 * diffOf pulls the rendered diff out of a tool's details. Only edit and write
 * produce one, and write only when it overwrote an existing file.
 */
export function diffOf(details: ToolDetails | undefined): DiffPayload | null {
  if (!details || !("diff" in details) || !details.diff) return null;
  return {
    diff: details.diff,
    added: details.added ?? 0,
    removed: details.removed ?? 0,
    path: details.path,
  };
}

export function isBashDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { command: string }> {
  return !!d && "command" in d;
}

/** SubagentStep is one row of a delegated run, as the card shows it. */
export interface SubagentStep {
  kind: "turn" | "tool" | "end";
  label: string;
  detail?: string;
  bad?: boolean;
  /** Present on tool rows, so a result attaches to the call it belongs to. */
  callId?: string;
  done?: boolean;
  /** Tool rows: the tool's own name, so the result renderer can switch on it. */
  name?: string;
  /**
   * Tool rows: the one-line argument summary the parent timeline shows next to
   * its own calls. Absent when there is nothing useful to say (an empty
   * summary, or a tool_end whose start was dropped).
   */
  summary?: string;
  /**
   * Tool rows that finished: the call's output and structured details, so the
   * row can expand into what it actually produced — a read's lines, a command's
   * output, an edit's diff — rather than only that it happened.
   */
  text?: string;
  details?: ToolDetails;
}

/**
 * subagentSteps projects a delegated run's events into rows.
 *
 * Here rather than in the component for the reason at the top of this file: it is a
 * projection of server state, and the one piece of it that has already been wrong
 * once. The first version paired tool_start with tool_end by tool name, and a live
 * run then produced a turn holding `ls start / read start / read ok / ls ok` — a
 * child runs calls in parallel, so completion order is not start order and names
 * repeat. Pairing on call_id is the fix, and this is where it can be pinned.
 */
export function subagentSteps(frames: SubagentFrame[] | undefined): SubagentStep[] {
  const out: SubagentStep[] = [];
  for (const f of frames ?? []) {
    switch (f.type) {
      case "turn_start":
        out.push({ kind: "turn", label: gt("timeline.turnLabel", { n: f.turn ?? "?" }) });
        break;
      case "tool_start": {
        const name = f.name ?? "tool";
        // The same one-line summary the parent puts next to its own calls: an
        // empty one is dropped rather than rendered as a blank gap in the row.
        const summary = summarizeArgs(name as ToolName, f.args) || undefined;
        out.push({ kind: "tool", label: name, name, summary, callId: f.call_id });
        break;
      }
      case "tool_end": {
        // Folded onto the call it belongs to rather than added as a second row: a
        // call that started and finished is one thing that happened, not two. The
        // `!done` guard matters when a child reuses ids across turns.
        const started = out.find((s) => s.kind === "tool" && s.callId === f.call_id && !s.done);
        const target =
          started ??
          (out.push({ kind: "tool", label: f.name ?? "tool", name: f.name, callId: f.call_id }),
          out[out.length - 1]);
        target.done = true;
        target.bad = f.is_error === true;
        target.detail = f.is_error ? gt("timeline.failed") : undefined;
        // The output and details ride the same frame kinds the parent renders
        // its own results from, so an expanded row shows what the call produced.
        target.text = f.text;
        target.details = f.details;
        break;
      }
      case "run_end":
        out.push({
          kind: "end",
          label: f.error ? gt("timeline.endedError") : gt("timeline.ended"),
          detail: f.error || f.stop_reason,
          bad: Boolean(f.error),
        });
        break;
      default:
        // `session` is shown as a command in the footer, not as a row. Nothing else
        // is forwarded — see frameWorthForwarding in tools/subagent.go, which is an
        // allowlist precisely so this branch stays empty.
        break;
    }
  }
  return out;
}

/**
 * Subagent details, recognised by `mode` — the one field every delegated run has and
 * no other tool's details do, whether it committed anything or not.
 */
export function isSubagentDetails(d: ToolDetails | undefined): d is SubagentDetails {
  return !!d && "mode" in d;
}

export function isReadDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { total_lines: number }> {
  return !!d && "total_lines" in d && !("command" in d);
}

/**
 * A multi-file read. `files` is the discriminator, and it is the reason the Go type
 * has no top-level `total_lines`: with one, isReadDetails above would claim it first
 * and the single-file component would be handed a result it cannot draw.
 */
export function isReadManyDetails(d: ToolDetails | undefined): d is ReadManyDetails {
  return !!d && "files" in d && Array.isArray((d as ReadManyDetails).files);
}

/**
 * readBodies picks each file's content out of a multi-file read's text.
 *
 * A slice at recorded byte offsets, not a parse. The tool writes the offsets while it
 * writes the text (ReadFileDetails.BodyOffset), so the two cannot disagree — whereas
 * splitting on the `==> path <==` headers has a failure this must not have: a file
 * whose own contents contain such a line ends its section early, and the remainder is
 * attributed to the next file. A viewer showing one file's content under another
 * file's name is worse than showing none.
 *
 * `undefined` for an entry with no body: an unreadable path, or a transcript recorded
 * before the offsets existed. The component shows the row without content rather than
 * guessing at one.
 *
 * Go counts bytes and a JS string is indexed in UTF-16 code units, so the text is
 * encoded once and the ranges are taken from the bytes. Slicing the string directly
 * and validating the result's byte length is not enough, and the way it fails is
 * instructive: with three files where one holds CJK text, the wrong slice for the
 * next file was `"=> plain.go <"` — thirteen ASCII bytes — against a body of
 * `"done ✅ 🎉"`, also thirteen bytes. A length check passes and the reader sees one
 * file's fragment under another file's name, which is exactly the failure the
 * offsets exist to remove. So this does the conversion rather than checking it.
 */
export function readBodies(text: string, files: ReadFileDetails[]): (string | undefined)[] {
  if (!files.some((f) => f.body_offset && f.body_length)) return files.map(() => undefined);
  // Encoded once per result. The text is bounded by the tool's own output budget, so
  // this is a copy of tens of kilobytes, not of a file.
  const bytes = new TextEncoder().encode(text);
  const decoder = new TextDecoder();
  return files.map((f) => {
    if (!f.body_offset || !f.body_length) return undefined;
    const end = f.body_offset + f.body_length;
    // A range past the end means the text is not the one the offsets were measured
    // against. Nothing legitimate produces that, and a partial slice would be a
    // fragment presented as a file.
    if (end > bytes.byteLength) return undefined;
    return decoder.decode(bytes.subarray(f.body_offset, end));
  });
}

export function isLsDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { entries: number }> {
  return !!d && "entries" in d;
}

/**
 * isSearchDetails covers both find and grep, which share a shape apart from
 * grep's extra counters. The discriminator is "scanned": it is the field that
 * exists precisely because a search reports how much ground it covered, and no
 * other tool has anything like it.
 */
export function isSearchDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { scanned: number }> {
  return !!d && "scanned" in d;
}

/**
 * matchSkillRead reports which skill a read call was loading, if any.
 *
 * It matches on the call's arguments rather than on the result details, because
 * arguments live in the assistant message and therefore survive into the session
 * file, while details are dropped on the way to disk. A badge derived from details
 * would vanish on reload, which is precisely when you want to know why the agent
 * did what it did.
 */
export function matchSkillRead(
  name: ToolName,
  args: unknown,
  skills: { name: string; path: string }[],
  cwd: string,
): string | null {
  if (name !== "read" || skills.length === 0) return null;
  const raw = (args as { path?: unknown } | null)?.path;
  if (typeof raw !== "string" || raw === "") return null;
  const target = normalizePath(raw.startsWith("/") ? raw : `${cwd}/${raw}`);
  return skills.find((s) => normalizePath(s.path) === target)?.name ?? null;
}

// normalizePath resolves . and .. textually. Enough for comparing two paths the
// server produced; it is not a filesystem and does not follow symlinks, which is
// why the Go side does the authoritative matching for the terminal.
function normalizePath(path: string): string {
  const out: string[] = [];
  for (const part of path.split("/")) {
    if (part === "" || part === ".") continue;
    if (part === "..") out.pop();
    else out.push(part);
  }
  return "/" + out.join("/");
}

export interface SkillBlock {
  name: string;
  location: string;
  body: string;
  /** trailing is whatever the user typed after /skill:name. */
  trailing: string;
}

// The server wraps an explicit /skill:name invocation in <skill name= location=>.
// Parsing it back out is what lets a 400-line instruction sheet collapse to one
// line instead of burying the question the user actually asked.
const SKILL_BLOCK = /^<skill name="([^"]*)" location="([^"]*)">\n([\s\S]*?)\n<\/skill>(?:\n\n([\s\S]*))?$/;

export function parseSkillBlock(text: string): SkillBlock | null {
  const m = SKILL_BLOCK.exec(text.trim());
  if (!m) return null;
  let body = m[3];
  // The first line is the "References are relative to ..." note the server adds;
  // it is guidance for the model, not something a reader needs twice.
  const nl = body.indexOf("\n\n");
  if (body.startsWith("References are relative to ") && nl !== -1) {
    body = body.slice(nl + 2);
  }
  return { name: m[1], location: m[2], body, trailing: m[4] ?? "" };
}

export function isWriteDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { bytes: number }> {
  return !!d && "bytes" in d;
}

/**
 * The task list is the only details payload with a `todos` array, and an empty
 * array is a real value — the agent clearing its list — so the guard tests for
 * the key rather than for length.
 */
export function isTodoDetails(d: ToolDetails | undefined): d is TodoDetails {
  return !!d && "todos" in d;
}

/**
 * liveTodos is the plan as it stands: the newest settled task list, or undefined
 * when there is no plan to show.
 *
 * It exists so the pinned bar above the composer and the inline cards cannot
 * disagree about which write is current. Both answers come from the same rule that
 * markSupersededTodos applies — newest settled, successful write wins — and this
 * reads it back off the flag rather than recomputing it, so there is one rule and
 * not two implementations of it.
 *
 * An empty list is undefined, not an empty array. `todo` with no items means the
 * agent cleared its list, and a pinned bar reading "0/0" for the rest of the
 * session is worse than no bar: the point of pinning is that it says what is
 * happening now, and nothing is.
 */
export function liveTodos(items: TimelineItem[]): TodoItem[] | undefined {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind !== "turn") continue;
    for (let j = item.calls.length - 1; j >= 0; j--) {
      const call = item.calls[j];
      if (call.name !== "todo" || call.superseded) continue;
      // Same exclusions as markSupersededTodos: a call with no result wrote
      // nothing, and a rejected one changed nothing. Neither is the plan, and
      // neither demoted the plan before it — so keep looking backwards.
      if (!call.result || call.result.is_error) continue;
      if (!isTodoDetails(call.result.details)) continue;
      return call.result.details.todos.length ? call.result.details.todos : undefined;
    }
  }
  return undefined;
}

export function isEditDetails(d: ToolDetails | undefined): d is Extract<ToolDetails, { edits: number }> {
  return !!d && "edits" in d;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  return `${m}m${Math.round((ms % 60_000) / 1000)}s`;
}
