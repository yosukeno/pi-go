import { describe, expect, it } from "vitest";
import { setLocale } from "@/i18n";
import {
  buildTimeline,
  isSearchDetails,
  isSubagentDetails,
  isTodoDetails,
  liveTodos,
  matchSkillRead,
  readBodies,
  subagentSteps,
  parseSkillBlock,
  summarizeArgs,
  type TimelineCall,
} from "./timeline";
import { clipTail } from "./useAgentStream";
import { parseFrame } from "./sse";
import { emptyLive, type Live, type Message, type ReadFileDetails, type ToolResult } from "@/api/types";

// The asserted copy is Chinese: pin the locale so the tests do not depend on
// the runner's navigator.language.
setLocale("zh-CN");

const user = (id: string, text: string): Message => ({
  id,
  role: "user",
  content: [{ type: "text", text }],
});

const assistant = (id: string, content: Message["content"]): Message => ({
  id,
  role: "assistant",
  content,
});

const result = (callId: string, over: Partial<ToolResult> = {}): ToolResult => ({
  call_id: callId,
  text: "ok",
  ...over,
});

describe("buildTimeline", () => {
  it("projects an exchange into a user item and numbered turns", () => {
    const items = buildTimeline(
      [
        user("u1", "改一下超时"),
        assistant("m2", [
          { type: "thinking", text: "先看文件" },
          { type: "tool_use", id: "c1", name: "read", input: { path: "config.go" } },
        ]),
        assistant("m3", [{ type: "text", text: "改好了" }]),
      ],
      { c1: result("c1") },
      emptyLive(),
    );

    expect(items.map((i) => i.kind)).toEqual(["user", "turn", "turn"]);
    const [, first, second] = items;
    if (first.kind !== "turn" || second.kind !== "turn") throw new Error("expected turns");
    expect(first.index).toBe(1);
    expect(second.index).toBe(2);
    expect(first.thinking).toBe("先看文件");
    expect(first.calls[0]).toMatchObject({ callId: "c1", name: "read", running: false });
    expect(first.calls[0].result?.text).toBe("ok");
  });

  it("restarts turn numbering at each new question", () => {
    const items = buildTimeline(
      [user("u1", "a"), assistant("m2", []), user("u3", "b"), assistant("m4", [])],
      {},
      emptyLive(),
    );
    const turns = items.filter((i) => i.kind === "turn");
    expect(turns.map((t) => (t.kind === "turn" ? t.index : 0))).toEqual([1, 1]);
  });

  it("marks a call running while it is in pending_tools", () => {
    const live: Live = {
      ...emptyLive(),
      active: true,
      pending_tools: [{ call_id: "c1", name: "bash", started_at: 0 }],
    };
    const items = buildTimeline(
      [assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: { command: "go build" } }])],
      {},
      live,
    );
    if (items[0].kind !== "turn") throw new Error("expected a turn");
    expect(items[0].calls[0]).toMatchObject({ running: true, orphaned: false });
  });

  it("attaches a pending gate to the call it belongs to", () => {
    const live: Live = {
      ...emptyLive(),
      active: true,
      pending_gates: [
        { gate_id: "g1", call_id: "c1", tool: "bash", deadline: 123, danger: ["rm -rf"] },
      ],
    };
    const items = buildTimeline(
      [assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: { command: "rm -rf ./b" } }])],
      {},
      live,
    );
    if (items[0].kind !== "turn") throw new Error("expected a turn");
    expect(items[0].calls[0].gate?.gate_id).toBe("g1");
    expect(items[0].calls[0].gate?.deadline).toBe(123);
  });

  it("shows a gate whose tool_use has not arrived yet", () => {
    // Gate events are published out of band, so a gate_request can overtake the
    // assistant message that requested the call. The run is blocked on the card,
    // so it must be rendered anyway.
    const live: Live = {
      ...emptyLive(),
      active: true,
      message_id: "m2",
      pending_gates: [{ gate_id: "g1", call_id: "unknown", tool: "bash", deadline: 1, args: { command: "ls" } }],
    };
    const items = buildTimeline([user("u1", "跑一下")], {}, live);
    const calls = items.flatMap((i) => (i.kind === "turn" ? i.calls : []));
    expect(calls).toHaveLength(1);
    expect(calls[0]).toMatchObject({ callId: "unknown", name: "bash" });
    expect(calls[0].gate?.gate_id).toBe("g1");
  });

  it("adopts incoming argument streams onto the turn in flight", () => {
    // Like an orphan gate, an incoming call has no tool_use block yet, so it
    // attaches to the live turn — creating one when the model went straight
    // to the tool call without emitting text first.
    const live: Live = {
      ...emptyLive(),
      active: true,
      message_id: "m2",
      incoming: [{ call_id: "c1", name: "write", head: '{"path": "a.ts"', bytes: 15 }],
    };
    const items = buildTimeline([user("u1", "写个文件")], {}, live);
    const last = items[items.length - 1];
    if (last.kind !== "turn") throw new Error("expected a turn");
    expect(last.streaming).toBe(true);
    expect(last.incoming?.map((t) => t.call_id)).toEqual(["c1"]);
  });

  it("flags a call that never finished once the run is over", () => {
    // The process died mid-call: no result, nothing pending, no active run.
    const items = buildTimeline(
      [assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: {} }])],
      {},
      emptyLive(),
    );
    if (items[0].kind !== "turn") throw new Error("expected a turn");
    expect(items[0].calls[0].orphaned).toBe(true);
  });

  it("adds the in-flight turn from live state and drops it once settled", () => {
    const streaming: Live = { ...emptyLive(), active: true, message_id: "m2", text: "正在" };
    const withLive = buildTimeline([user("u1", "hi")], {}, streaming);
    expect(withLive).toHaveLength(2);
    const last = withLive[1];
    if (last.kind !== "turn") throw new Error("expected a turn");
    expect(last.streaming).toBe(true);
    expect(last.text).toBe("正在");

    // After `message` arrives the server clears live.text, so no duplicate.
    const settled = buildTimeline(
      [user("u1", "hi"), assistant("m2", [{ type: "text", text: "正在说完了" }])],
      {},
      { ...streaming, text: "", thinking: "" },
    );
    expect(settled).toHaveLength(2);
    expect(settled.filter((i) => i.kind === "turn" && i.streaming)).toHaveLength(0);
  });

  it("links a successful retry to the failed call it repairs", () => {
    const items = buildTimeline(
      [
        assistant("m1", [{ type: "tool_use", id: "c1", name: "edit", input: { path: "dup.txt" } }]),
        assistant("m2", [
          { type: "tool_use", id: "c2", name: "read", input: { path: "dup.txt" } },
          { type: "tool_use", id: "c3", name: "edit", input: { path: "dup.txt" } },
        ]),
      ],
      {
        c1: result("c1", { is_error: true, text: "oldText matches 2 places" }),
        c2: result("c2"),
        c3: result("c3"),
      },
      emptyLive(),
    );

    const turn2 = items[1];
    if (turn2.kind !== "turn") throw new Error("expected a turn");
    const edit = turn2.calls.find((c) => c.name === "edit");
    expect(edit?.corrects).toBe("c1");
    // The read is unrelated work, not a correction.
    expect(turn2.calls.find((c) => c.name === "read")?.corrects).toBeUndefined();
  });

  it("does not link calls that touch different files", () => {
    const items = buildTimeline(
      [
        assistant("m1", [{ type: "tool_use", id: "c1", name: "edit", input: { path: "a.txt" } }]),
        assistant("m2", [{ type: "tool_use", id: "c2", name: "edit", input: { path: "b.txt" } }]),
      ],
      { c1: result("c1", { is_error: true }), c2: result("c2") },
      emptyLive(),
    );
    const turn2 = items[1];
    if (turn2.kind !== "turn") throw new Error("expected a turn");
    expect(turn2.calls[0].corrects).toBeUndefined();
  });
});

// readBodies slices a multi-file read at the offsets the tool recorded. The fixture
// builds text the way tools/read.go's readMany does and computes the offsets the same
// way it does, in bytes, so these cases exercise the real contract rather than a
// convenient one.
describe("readBodies", () => {
  /** layout mirrors readMany: `==> path <==\n` + body, sections joined by a blank line. */
  function layout(sections: [string, string][], note = ""): { text: string; files: ReadFileDetails[] } {
    const encoder = new TextEncoder();
    let text = "";
    const files: ReadFileDetails[] = [];
    for (const [path, body] of sections) {
      if (text) text += "\n\n";
      text += `==> ${path} <==\n`;
      const offset = encoder.encode(text).byteLength;
      text += body;
      files.push({
        path,
        total_lines: 1,
        shown_lines: 1,
        body_offset: offset,
        body_length: encoder.encode(body).byteLength,
      });
      // The truncation note sits after the body and outside its recorded range.
      text += note;
    }
    return { text, files };
  }

  it("returns each file's own content", () => {
    const { text, files } = layout([
      ["a.go", "package a"],
      ["b.go", "package b\nfunc B() {}"],
    ]);
    expect(readBodies(text, files)).toEqual(["package a", "package b\nfunc B() {}"]);
  });

  it("is unaffected by a body that looks like a section header", () => {
    // This is the case that ruled out parsing the text back apart: split on the
    // headers and doc.md's section ends early, with the rest of it credited to b.go.
    const { text, files } = layout([
      ["doc.md", "example output:\n==> b.go <==\nnot really b"],
      ["b.go", "package b"],
    ]);
    expect(readBodies(text, files)).toEqual([
      "example output:\n==> b.go <==\nnot really b",
      "package b",
    ]);
  });

  it("excludes the truncation note, which is addressed to the model", () => {
    const { text, files } = layout([["big.go", "line one"]], "\n\n[Showing lines 1-1 of 900 …]");
    expect(readBodies(text, files)).toEqual(["line one"]);
  });

  it("gives no body to an unreadable path and keeps its neighbours right", () => {
    // One failed path does not fail the call, so the entries around it still have to
    // line up. A failure records no offsets; the row shows `error` instead.
    const { text, files } = layout([
      ["a.go", "package a"],
      ["c.go", "package c"],
    ]);
    const withFailure = [files[0], { path: "gone.go", error: "no such file" }, files[1]];
    expect(readBodies(text, withFailure)).toEqual(["package a", undefined, "package c"]);
  });

  it("handles multi-byte content, where byte offsets and string indices diverge", () => {
    // Go counts bytes; a JS string is indexed in UTF-16 code units. A naive slice
    // would land mid-character and silently return the wrong text.
    const { text, files } = layout([
      ["注释.go", "// 中文注释\npackage a"],
      ["emoji.md", "done ✅ 🎉"],
      ["plain.go", "package b"],
    ]);
    expect(readBodies(text, files)).toEqual(["// 中文注释\npackage a", "done ✅ 🎉", "package b"]);
  });

  it("returns undefined for a transcript recorded before the offsets existed", () => {
    // Rather than guessing: an old transcript's entries carry no range, and a row with
    // no content beats a row showing the wrong file's.
    expect(readBodies("==> a.go <==\npackage a", [{ path: "a.go", total_lines: 1 }])).toEqual([undefined]);
  });
});

// liveTodos feeds the bar pinned above the composer. It has to agree with the
// supersede pass below in every case, because the two together decide which copy
// of the plan a reader is looking at: disagreement puts a stale list in the fixed
// position, which is worse than the scrolling card it replaced.
describe("liveTodos", () => {
  const todoCall = (id: string) => ({ type: "tool_use" as const, id, name: "todo" as const, input: {} });
  const listOf = (id: string, tasks: string[], over: Partial<ToolResult> = {}): ToolResult =>
    result(id, {
      name: "todo",
      details: { todos: tasks.map((task) => ({ task, status: "pending" as const })) },
      ...over,
    });

  it("returns the newest settled list", () => {
    const todos = liveTodos(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: listOf("c1", ["old"]), c2: listOf("c2", ["new one", "new two"]) },
        emptyLive(),
      ),
    );
    expect(todos?.map((x) => x.task)).toEqual(["new one", "new two"]);
  });

  it("keeps the last good list when a later write was rejected", () => {
    const todos = liveTodos(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: listOf("c1", ["good"]), c2: listOf("c2", ["bad"], { is_error: true, text: "at most one" }) },
        emptyLive(),
      ),
    );
    expect(todos?.map((x) => x.task)).toEqual(["good"]);
  });

  it("treats a cleared list as no plan", () => {
    // The bar disappears rather than pinning "0/0" for the rest of the session:
    // the point of the fixed position is that it says what is happening now.
    const todos = liveTodos(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: listOf("c1", ["a"]), c2: listOf("c2", []) },
        emptyLive(),
      ),
    );
    expect(todos).toBeUndefined();
  });

  it("is undefined when nothing wrote a list", () => {
    expect(liveTodos(buildTimeline([user("u1", "hi")], {}, emptyLive()))).toBeUndefined();
  });
});

describe("superseded task lists", () => {
  const todoCall = (id: string) => ({ type: "tool_use" as const, id, name: "todo" as const, input: {} });
  const todoResult = (id: string, over: Partial<ToolResult> = {}): ToolResult =>
    result(id, { name: "todo", details: { todos: [{ task: "a", status: "pending" }] }, ...over });
  const todoCalls = (items: ReturnType<typeof buildTimeline>): TimelineCall[] =>
    items.flatMap((i) => (i.kind === "turn" ? i.calls.filter((c) => c.name === "todo") : []));

  it("leaves only the newest settled write current", () => {
    const calls = todoCalls(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")]), assistant("m3", [todoCall("c3")])],
        { c1: todoResult("c1"), c2: todoResult("c2"), c3: todoResult("c3") },
        emptyLive(),
      ),
    );
    expect(calls.map((c) => c.superseded)).toEqual([true, true, undefined]);
  });

  it("marks earlier writes in the same turn too", () => {
    // Two writes in one assistant message is a model mistake rather than a
    // pattern, but the pass must not depend on turn boundaries to notice it.
    const calls = todoCalls(
      buildTimeline(
        [assistant("m1", [todoCall("c1"), todoCall("c2")])],
        { c1: todoResult("c1"), c2: todoResult("c2") },
        emptyLive(),
      ),
    );
    expect(calls.map((c) => c.superseded)).toEqual([true, undefined]);
  });

  it("does not let a rejected write demote the list before it", () => {
    // A refused call changed nothing — a list with two items in_progress never
    // became the state — so the good list before it is still the plan.
    const calls = todoCalls(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: todoResult("c1"), c2: todoResult("c2", { is_error: true, text: "at most one" }) },
        emptyLive(),
      ),
    );
    expect(calls.map((c) => c.superseded)).toEqual([undefined, undefined]);
  });

  it("does not let a still-running write demote the list before it", () => {
    // Otherwise the plan would blank out for as long as the call is in flight —
    // which is exactly while someone is watching it.
    const live: Live = {
      ...emptyLive(),
      active: true,
      pending_tools: [{ call_id: "c2", name: "todo", args: {}, started_at: 0 }],
    };
    const calls = todoCalls(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: todoResult("c1") },
        live,
      ),
    );
    expect(calls[1].running).toBe(true);
    expect(calls.map((c) => c.superseded)).toEqual([undefined, undefined]);
  });

  it("does not let an orphaned write demote the list before it", () => {
    // The process died mid-call, so nothing was written. Same reasoning as a
    // rejection: no result means no new state.
    const calls = todoCalls(
      buildTimeline(
        [assistant("m1", [todoCall("c1")]), assistant("m2", [todoCall("c2")])],
        { c1: todoResult("c1") },
        emptyLive(),
      ),
    );
    expect(calls[1].orphaned).toBe(true);
    expect(calls.map((c) => c.superseded)).toEqual([undefined, undefined]);
  });

  it("recognises a cleared list as a write", () => {
    // An empty array is a real value: the agent retracting its plan. The guard
    // must test for the key, not for length, or clearing would fall through to
    // the raw-text renderer.
    expect(isTodoDetails({ todos: [] })).toBe(true);
    expect(isTodoDetails(undefined)).toBe(false);
    expect(isTodoDetails({ path: "a.go", entries: 3 })).toBe(false);
  });
});

describe("summarizeArgs for a task list", () => {
  it("labels the write with the item being started", () => {
    expect(
      summarizeArgs("todo", {
        todos: [
          { task: "找到超时常量", status: "completed" },
          { task: "改成 60s", status: "in_progress" },
          { task: "跑测试", status: "pending" },
        ],
      }),
    ).toBe("改成 60s");
  });

  it("falls back to a count when nothing is in progress", () => {
    // A list written before starting, or one that is finished, has no such line.
    expect(summarizeArgs("todo", { todos: [{ task: "a", status: "pending" }] })).toBe("1 项");
    expect(summarizeArgs("todo", { todos: [] })).toBe("清空");
  });

  it("survives arguments that are not a list", () => {
    // The label is drawn from raw model output, so it must not throw on a shape
    // the schema would have rejected.
    expect(summarizeArgs("todo", { todos: "a, b" })).toBe("清空");
    expect(summarizeArgs("todo", {})).toBe("清空");
    expect(summarizeArgs("todo", { todos: [null] })).toBe("1 项");
  });
});

describe("parseFrame", () => {
  it("parses a data frame", () => {
    const e = parseFrame('id: 7\nevent: token\ndata: {"seq":7,"type":"token","ts":1,"text":"hi"}');
    expect(e).toMatchObject({ seq: 7, type: "token", text: "hi" });
  });

  it("returns null for a keepalive comment", () => {
    expect(parseFrame(":")).toBeNull();
  });

  it("joins a multi-line data payload", () => {
    const e = parseFrame('event: x\ndata: {"seq":1,\ndata: "type":"error","ts":0,"error":"boom"}');
    expect(e).toMatchObject({ type: "error", error: "boom" });
  });

  it("survives a malformed payload", () => {
    expect(parseFrame("data: {not json")).toBeNull();
  });
});

describe("parseSkillBlock", () => {
  const block = (body: string, trailing = "") =>
    `<skill name="pdf-tools" location="/s/pdf-tools/SKILL.md">\nReferences are relative to /s/pdf-tools.\n\n${body}\n</skill>${
      trailing ? `\n\n${trailing}` : ""
    }`;

  it("pulls out the name, location and body", () => {
    const got = parseSkillBlock(block("# PDF\n\nrun ./extract.py"));
    expect(got).toMatchObject({
      name: "pdf-tools",
      location: "/s/pdf-tools/SKILL.md",
      body: "# PDF\n\nrun ./extract.py",
      trailing: "",
    });
  });

  it("keeps what the user typed after the command separate from the skill", () => {
    expect(parseSkillBlock(block("body", "extract page 3"))?.trailing).toBe("extract page 3");
  });

  it("leaves ordinary prompts alone", () => {
    for (const text of ["hello", "<skill> unclosed", "read /s/pdf-tools/SKILL.md"]) {
      expect(parseSkillBlock(text)).toBeNull();
    }
  });
});

describe("matchSkillRead", () => {
  const skills = [{ name: "pdf-tools", path: "/home/u/.pi-go/skills/pdf-tools/SKILL.md" }];

  it("matches the absolute path the prompt advertises", () => {
    expect(matchSkillRead("read", { path: skills[0].path }, skills, "/proj")).toBe("pdf-tools");
  });

  it("matches a relative path by resolving it against cwd", () => {
    const args = { path: "../../home/u/.pi-go/skills/pdf-tools/SKILL.md" };
    expect(matchSkillRead("read", args, skills, "/home/proj")).toBe("pdf-tools");
  });

  it("does not label ordinary reads, other tools, or pathless args", () => {
    expect(matchSkillRead("read", { path: "main.go" }, skills, "/proj")).toBeNull();
    expect(matchSkillRead("bash", { command: "cat SKILL.md" }, skills, "/proj")).toBeNull();
    expect(matchSkillRead("read", {}, skills, "/proj")).toBeNull();
    expect(matchSkillRead("read", { path: skills[0].path }, [], "/proj")).toBeNull();
  });
});

describe("search tool labels and details", () => {
  it("leads with the pattern, which is what a search is about", () => {
    expect(summarizeArgs("find", { pattern: "*_test.go" })).toBe("*_test.go");
    expect(summarizeArgs("grep", { pattern: "func Handler" })).toBe("func Handler");
  });

  it("qualifies with the include glob and the search path when given", () => {
    expect(summarizeArgs("grep", { pattern: "TODO", include: "*.go", path: "web" })).toBe(
      "TODO (*.go) in web",
    );
    // The default path is not worth showing; it is not a narrowed search.
    expect(summarizeArgs("find", { pattern: "*.go", path: "." })).toBe("*.go");
  });

  it("recognises search details without confusing them for other tools", () => {
    expect(isSearchDetails({ pattern: "x", matches: 0, scanned: 12 })).toBe(true);
    expect(isSearchDetails({ pattern: "x", matches: 1, files: 1, scanned: 3 })).toBe(true);
    // Other tools have no scan count, so they must not be routed here.
    expect(isSearchDetails({ path: "a", entries: 2 })).toBe(false);
    expect(isSearchDetails({ command: "ls", exit_code: 0, duration_ms: 1 })).toBe(false);
    expect(isSearchDetails(undefined)).toBe(false);
  });
});

describe("live tool output", () => {
  const pending = (over: Partial<Live["pending_tools"][0]> = {}) => ({
    ...emptyLive(),
    active: true,
    pending_tools: [{ call_id: "c1", name: "bash" as const, started_at: 1, ...over }],
  });

  it("attaches a running call's output to its timeline entry", () => {
    const msgs = [assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: {} }])];
    const items = buildTimeline(msgs, {}, pending({ output: "step 1\n" }));
    const call = (items[0] as { calls: TimelineCall[] }).calls[0];
    expect(call.running).toBe(true);
    expect(call.liveOutput).toBe("step 1\n");
  });

  // Once the call settles it is no longer pending, so the live copy disappears and
  // only the result renders. Showing both would print the same output twice.
  it("drops the live copy once the result arrives", () => {
    const msgs = [assistant("m1", [{ type: "tool_use", id: "c1", name: "bash", input: {} }])];
    const items = buildTimeline(msgs, { c1: result("c1", { text: "step 1\n" }) }, emptyLive());
    const call = (items[0] as { calls: TimelineCall[] }).calls[0];
    expect(call.running).toBe(false);
    expect(call.liveOutput).toBeUndefined();
    expect(call.result?.text).toBe("step 1\n");
  });
});

describe("clipTail", () => {
  it("keeps short input untouched", () => {
    expect(clipTail("abc", 10)).toBe("abc");
  });

  it("keeps the tail, which is the part that says how a command is going", () => {
    expect(clipTail("0123456789", 4)).toBe("6789");
  });

  it("starts at a line boundary when one is close, so the first line is whole", () => {
    // Cutting at 12 lands mid-"cc"; the newline just after is close enough to use.
    expect(clipTail("aaaa\nbbbb\ncccc\ndddd\n", 12)).toBe("cccc\ndddd\n");
  });
});

describe("subagent frames", () => {
  // A delegated run has structure, so the timeline carries its events rather than a
  // line of text. Frames come from the pending call because tool_partial is never
  // replayed: a page that connects mid-delegation sees the snapshot or sees nothing.
  it("reaches the call while the delegation is running", () => {
    const items = buildTimeline(
      [
        {
          id: "m1",
          role: "assistant",
          content: [{ type: "tool_use", id: "c1", name: "subagent", input: { mode: "explore" } }],
        },
      ],
      {},
      {
        active: true,
        pending_tools: [
          {
            call_id: "c1",
            name: "subagent",
            started_at: 0,
            frames: [
              { type: "turn_start", turn: 1 },
              { type: "tool_start", name: "grep" },
            ],
          },
        ],
        pending_gates: [],
      },
    );

    const call = (items[0] as { calls: TimelineCall[] }).calls[0];
    expect(call.running).toBe(true);
    expect(call.liveFrames).toHaveLength(2);
    expect(call.liveFrames?.[1]).toMatchObject({ type: "tool_start", name: "grep" });
  });

  // Once it settles the frames move onto the result (the client keeps them there:
  // they are the run's process, not its answer, so nothing renders twice). A
  // result without them — a transcript replayed from disk — simply has none.
  it("goes away when the delegation settles", () => {
    const items = buildTimeline(
      [
        {
          id: "m1",
          role: "assistant",
          content: [{ type: "tool_use", id: "c1", name: "subagent", input: { mode: "explore" } }],
        },
      ],
      {
        c1: {
          call_id: "c1",
          name: "subagent",
          text: "the entry point is main.go",
          details: { id: "sub1", mode: "explore" },
        },
      },
      { active: false, pending_tools: [], pending_gates: [] },
    );

    const call = (items[0] as { calls: TimelineCall[] }).calls[0];
    expect(call.running).toBe(false);
    expect(call.liveFrames).toBeUndefined();
    expect(call.result?.text).toBe("the entry point is main.go");
    expect(isSubagentDetails(call.result?.details)).toBe(true);
  });

  // The companion to the settle test above: when the live client retained the
  // frames on the result, the settled card keeps showing the run's process
  // instead of emptying out the moment the answer arrives.
  it("keeps the run's process when the settled result retained it", () => {
    const frames = [
      { type: "turn_start" as const, turn: 1 },
      { type: "tool_start" as const, call_id: "r1", name: "read", args: { path: "main.go" } },
      { type: "tool_end" as const, call_id: "r1", name: "read", text: "package main" },
    ];
    const items = buildTimeline(
      [
        {
          id: "m1",
          role: "assistant",
          content: [{ type: "tool_use", id: "c1", name: "subagent", input: { mode: "explore" } }],
        },
      ],
      {
        c1: {
          call_id: "c1",
          name: "subagent",
          text: "the entry point is main.go",
          details: { id: "sub1", mode: "explore" },
          frames,
        },
      },
      { active: false, pending_tools: [], pending_gates: [] },
    );

    const call = (items[0] as { calls: TimelineCall[] }).calls[0];
    expect(call.running).toBe(false);
    expect(call.liveFrames).toHaveLength(3);
  });

  // The guard keys on `mode`, which every delegated run has and no other tool's
  // details do — including a run that changed nothing and so has no commit.
  it("recognises subagent details but not other tools'", () => {
    expect(isSubagentDetails({ id: "s", mode: "explore" } as never)).toBe(true);
    expect(isSubagentDetails({ command: "ls", exit_code: 0, duration_ms: 1 } as never)).toBe(false);
    expect(isSubagentDetails(undefined)).toBe(false);
  });
});

describe("subagentSteps", () => {
  // The exact interleaving a live run produced: a child runs its calls in parallel,
  // so completion order is not start order.
  //
  // This one documents the shape but does not discriminate — checked by mutation, and
  // with distinct names, pairing by name gets the same answer by luck. The test below
  // is the one with teeth, so it is not redundant with this and must not be dropped.
  it("attaches each result to the call that finished, out of order", () => {
    const steps = subagentSteps([
      { type: "turn_start", turn: 2 },
      { type: "tool_start", call_id: "a", name: "ls" },
      { type: "tool_start", call_id: "b", name: "read" },
      { type: "tool_end", call_id: "b", name: "read" },
      { type: "tool_end", call_id: "a", name: "ls", is_error: true },
    ]);

    expect(steps.map((s) => s.label)).toEqual(["第 2 轮", "ls", "read"]);
    const ls = steps.find((s) => s.callId === "a")!;
    const read = steps.find((s) => s.callId === "b")!;
    // The failure belongs to ls, which finished last. By name-or-recency it would
    // have landed on read.
    expect(ls.bad).toBe(true);
    expect(read.bad).toBe(false);
    expect(ls.done && read.done).toBe(true);
  });

  // Three calls with the same name in one turn — a live run produced exactly this.
  // The discriminating test for the bug: verified by mutation that swapping the id
  // match back to a name match fails here and nowhere else.
  it("keeps same-named parallel calls apart", () => {
    const steps = subagentSteps([
      { type: "tool_start", call_id: "g1", name: "grep" },
      { type: "tool_start", call_id: "g2", name: "grep" },
      { type: "tool_start", call_id: "g3", name: "grep" },
      { type: "tool_end", call_id: "g2", name: "grep", is_error: true },
      { type: "tool_end", call_id: "g1", name: "grep" },
    ]);

    expect(steps).toHaveLength(3);
    expect(steps.map((s) => [s.callId, s.bad ?? false, s.done ?? false])).toEqual([
      ["g1", false, true],
      ["g2", true, true],
      ["g3", false, false], // still running: no tool_end arrived for it
    ]);
  });

  // A tool_end with no matching start still gets a row. Frames are capped by count,
  // so a long delegation can drop the oldest ones and leave a result orphaned —
  // losing it entirely would be worse than showing it without its beginning.
  it("still shows a result whose start was dropped", () => {
    const steps = subagentSteps([{ type: "tool_end", call_id: "gone", name: "bash", is_error: true }]);
    expect(steps).toEqual([
      { kind: "tool", label: "bash", name: "bash", callId: "gone", done: true, bad: true, detail: "失败" },
    ]);
  });

  // Turn boundaries and the ending are rows too, because "which turn was it on" and
  // "how did it stop" are the two things you look for when a delegation goes wrong.
  it("records turns and how the run ended", () => {
    expect(subagentSteps([{ type: "run_end", stop_reason: "end_turn" }])).toEqual([
      { kind: "end", label: "结束", detail: "end_turn", bad: false },
    ]);
    const failed = subagentSteps([{ type: "run_end", error: "stopped after 4 turns" }]);
    expect(failed[0]).toMatchObject({ kind: "end", bad: true, detail: "stopped after 4 turns" });
  });

  // Only the five forwarded kinds produce rows. The filter is an allowlist in
  // tools/subagent.go, so anything else arriving here is a contract change and should
  // be silently ignored rather than rendered as an empty line.
  it("ignores frames that are not part of the run's shape", () => {
    expect(
      subagentSteps([
        { type: "session", session: "/tmp/child.jsonl" },
        { type: "token", text: "hello" },
        { type: "thinking", text: "hmm" },
        { type: "message" },
      ]),
    ).toEqual([]);
  });

  it("has nothing to show before the first frame", () => {
    expect(subagentSteps(undefined)).toEqual([]);
    expect(subagentSteps([])).toEqual([]);
  });

  // A row that only says "read" is the complaint this projection exists to answer:
  // the frame carries the call's arguments, so the row says which file, pattern,
  // or command — the same one-line summary the parent puts next to its own calls.
  it("summarises a call's arguments on its row", () => {
    const steps = subagentSteps([
      { type: "tool_start", call_id: "a", name: "read", args: { path: "src/main.go" } },
      { type: "tool_start", call_id: "b", name: "grep", args: { pattern: "foo", path: "src" } },
      { type: "tool_start", call_id: "c", name: "bash", args: { command: "go test ./..." } },
      { type: "tool_start", call_id: "d", name: "write", args: { path: "out.go", content: "package out" } },
    ]);
    expect(steps.map((s) => s.summary)).toEqual(["src/main.go", "foo in src", "go test ./...", "out.go"]);
  });

  // An empty summary is dropped rather than rendered as a blank gap in the row.
  it("omits the summary when there is nothing useful to say", () => {
    const steps = subagentSteps([{ type: "tool_start", call_id: "a", name: "ls", args: {} }]);
    expect(steps[0].summary).toBe(".");
    const bare = subagentSteps([{ type: "tool_start", call_id: "b", name: "read" }]);
    expect(bare[0].summary).toBeUndefined();
  });

  // A finished row can open into what the call produced, because the forwarded
  // tool_end carries the same output and details the parent's own results do.
  it("attaches the output and details when a call finishes", () => {
    const steps = subagentSteps([
      { type: "tool_start", call_id: "a", name: "read", args: { path: "x.go" } },
      {
        type: "tool_end",
        call_id: "a",
        name: "read",
        text: "package x",
        details: { path: "x.go", total_lines: 10, shown_lines: 10, first_line: 1 },
      },
    ]);
    expect(steps).toHaveLength(1);
    expect(steps[0]).toMatchObject({
      kind: "tool",
      done: true,
      text: "package x",
      details: { total_lines: 10 },
    });
  });

  it("labels a delegation with the task's first line", () => {
    expect(summarizeArgs("subagent", { task: "修复登录页样式错位\n并保持移动端兼容", mode: "edit" })).toBe(
      "修复登录页样式错位",
    );
    const long = "x".repeat(100);
    expect(summarizeArgs("subagent", { task: long, mode: "edit" })).toHaveLength(72);
  });
});
