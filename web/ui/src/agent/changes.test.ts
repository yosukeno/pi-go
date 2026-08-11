import { describe, expect, it } from "vitest";
import { buildTimeline } from "./timeline";
import { changedPathCount, collectChanges, collectIncomingWrites } from "./changes";
import { emptyLive, type IncomingArgs, type Message, type ToolResult } from "@/api/types";

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

const editDetails = { path: "main.go", edits: 1, diff: "+1 x", patch: "p", added: 2, removed: 1 };
// Created writes carry line stats since M5.1: every line is an addition.
const writeCreated = { path: "notes.md", bytes: 12, created: true, added: 1, removed: 0 };
const writeOverwrite = { path: "main.go", bytes: 40, created: false, diff: "+2 y", patch: "p2", added: 3, removed: 2 };

describe("collectChanges", () => {
  it("groups edit/write results by turn and path", () => {
    const items = buildTimeline(
      [
        user("u1", "改代码"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "edit", input: { path: "main.go" } },
          { type: "tool_use", id: "c2", name: "write", input: { path: "notes.md" } },
        ]),
        assistant("m2", [
          { type: "tool_use", id: "c3", name: "read", input: { path: "main.go" } },
          { type: "tool_use", id: "c4", name: "write", input: { path: "main.go" } },
        ]),
      ],
      {
        c1: result("c1", { details: editDetails }),
        c2: result("c2", { details: writeCreated }),
        c3: result("c3", { details: { path: "main.go", total_lines: 10, shown_lines: 10, first_line: 1 } }),
        c4: result("c4", { details: writeOverwrite }),
      },
      emptyLive(),
    );

    const groups = collectChanges(items);
    expect(groups).toHaveLength(2);

    expect(groups[0].turn).toBe(1);
    expect(groups[0].files).toHaveLength(2);
    const main = groups[0].files.find((f) => f.path === "main.go")!;
    expect(main).toMatchObject({ status: "modified", added: 2, removed: 1 });
    const notes = groups[0].files.find((f) => f.path === "notes.md")!;
    expect(notes).toMatchObject({ status: "added", added: 1, removed: 0 }); // created write counts its lines

    // Turn 2: read is not a change, the overwriting write is.
    expect(groups[1].files.map((f) => f.path)).toEqual(["main.go"]);
    expect(groups[1].files[0]).toMatchObject({ status: "modified", added: 3, removed: 2 });

    expect(changedPathCount(groups)).toBe(2); // main.go in both turns counts once
  });

  it("keeps added status when a created file is edited again in the same turn", () => {
    const items = buildTimeline(
      [
        user("u1", "建文件再改"),
        assistant("m1", [
          { type: "tool_use", id: "c1", name: "write", input: { path: "a.txt" } },
          { type: "tool_use", id: "c2", name: "edit", input: { path: "a.txt" } },
        ]),
      ],
      {
        c1: result("c1", { details: { ...writeCreated, path: "a.txt" } }),
        c2: result("c2", { details: { ...editDetails, path: "a.txt", added: 1, removed: 0 } }),
      },
      emptyLive(),
    );

    const groups = collectChanges(items);
    expect(groups[0].files).toHaveLength(1);
    expect(groups[0].files[0].status).toBe("added");
    expect(groups[0].files[0].calls.map((c) => c.name)).toEqual(["write", "edit"]);
    expect(groups[0].files[0].added).toBe(2); // 1 from the create + 1 from the edit
  });

  it("ignores calls without details (failed edits produce none)", () => {
    const items = buildTimeline(
      [
        user("u1", "改一个不存在的地方"),
        assistant("m1", [{ type: "tool_use", id: "c1", name: "edit", input: { path: "x.go" } }]),
      ],
      { c1: result("c1", { text: "oldText not found", is_error: true }) },
      emptyLive(),
    );
    expect(collectChanges(items)).toHaveLength(0);
  });
});

const inc = (callId: string, name: string, head: string | undefined, bytes: number): IncomingArgs => ({
  call_id: callId,
  name,
  head,
  bytes,
});

// live.incoming rides the run in flight, so the builder is an active run with
// argument fragments; adoptIncoming parks them on the streaming turn.
const streaming = (incoming: IncomingArgs[]) => ({ ...emptyLive(), active: true, incoming });

describe("collectIncomingWrites", () => {
  it("keeps only file-mutation tools, in arrival order", () => {
    const items = buildTimeline(
      [user("u1", "写文件")],
      {},
      streaming([
        inc("c1", "write", '{"path": "a.txt", "content": "…', 100),
        inc("c2", "bash", '{"command": "ls -la', 20),
        inc("c3", "edit", '{"path": "b.go", "edits": [', 50),
      ]),
    );
    const ws = collectIncomingWrites(items);
    expect(ws.map((w) => w.callId)).toEqual(["c1", "c3"]);
    expect(ws[0]).toMatchObject({ name: "write", path: "a.txt", bytes: 100 });
    expect(ws[1]).toMatchObject({ name: "edit", path: "b.go", bytes: 50 });
  });

  it("reports a null path until the value's closing quote arrives", () => {
    const items = buildTimeline(
      [user("u1", "写文件")],
      {},
      streaming([
        inc("c1", "write", '{"path": "a.t', 10), // closing quote still in flight
        inc("c2", "write", undefined, 5), // no head accumulated yet
      ]),
    );
    const ws = collectIncomingWrites(items);
    expect(ws.map((w) => w.path)).toEqual([null, null]);
    expect(ws.map((w) => w.bytes)).toEqual([10, 5]);
  });

  it("is empty when nothing is streaming", () => {
    const items = buildTimeline(
      [user("u1", "hi"), assistant("m1", [{ type: "text", text: "你好" }])],
      {},
      emptyLive(),
    );
    expect(collectIncomingWrites(items)).toEqual([]);
  });
});
