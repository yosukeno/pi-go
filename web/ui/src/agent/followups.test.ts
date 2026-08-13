import { describe, expect, it } from "vitest";
import { followupHaystack, matchFollowups, type FollowupGroup } from "./followups";
import type { TimelineItem } from "./timeline";

const user = (text: string): TimelineItem => ({ kind: "user", id: "u", text });

const turn = (text: string, calls: { name: string; args?: unknown }[] = []): TimelineItem => ({
  kind: "turn",
  id: "t",
  index: 1,
  thinking: "",
  text,
  streaming: false,
  // The cast keeps the fixture readable: only name/args/text are matched on.
  calls: calls.map((c) => ({ callId: "c", name: c.name, args: c.args, running: false, orphaned: false })),
} as TimelineItem);

describe("followupHaystack", () => {
  it("includes the reply, the tool names and the arguments", () => {
    const h = followupHaystack([
      user("反编译它"),
      turn("已经反编译完成", [{ name: "bash", args: { command: "/opt/skills/x/scripts/mal-decompile a.exe" } }]),
    ]);
    expect(h).toContain("已经反编译完成");
    expect(h).toContain("bash");
    expect(h).toContain("mal-decompile");
  });

  it("is lowercased so matching does not depend on the model's casing", () => {
    expect(followupHaystack([turn("Ran MAL-DECOMPILE")])).toContain("mal-decompile");
  });

  // The whole point of last-turn-only: a command from three turns ago must stop
  // driving the suggestions.
  it("looks at the last turn only", () => {
    const h = followupHaystack([
      turn("first", [{ name: "bash", args: { command: "mal-decompile a.exe" } }]),
      user("现在讲讲家族"),
      turn("Qakbot 是一个银行木马", [{ name: "bash", args: { command: "malquery family Qakbot" } }]),
    ]);
    expect(h).toContain("malquery family");
    expect(h).not.toContain("mal-decompile");
  });

  it("returns nothing when no turn has happened yet", () => {
    expect(followupHaystack([])).toBe("");
    expect(followupHaystack([user("hi")])).toBe("");
  });

  it("survives arguments that cannot be serialised", () => {
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(() => followupHaystack([turn("x", [{ name: "bash", args: cyclic }])])).not.toThrow();
  });
});

describe("matchFollowups", () => {
  const groups: FollowupGroup[] = [
    { when: ["mal-decompile"], chips: [{ title: "生成 Yara", prompt: "yara" }] },
    { when: ["malquery family", "家族"], chips: [{ title: "看影响面", prompt: "infra" }] },
  ];

  it("returns the matching group's chips", () => {
    expect(matchFollowups(groups, "ran mal-decompile now")[0].title).toBe("生成 Yara");
    expect(matchFollowups(groups, "malquery family qakbot")[0].title).toBe("看影响面");
  });

  it("matches if any needle in the group hits", () => {
    expect(matchFollowups(groups, "讲讲这个家族")[0].title).toBe("看影响面");
  });

  it("takes the first matching group rather than stacking rows", () => {
    expect(matchFollowups(groups, "mal-decompile then malquery family")).toHaveLength(1);
  });

  it("shows nothing when nothing is relevant", () => {
    expect(matchFollowups(groups, "just chatting about the weather")).toEqual([]);
    expect(matchFollowups(groups, "")).toEqual([]);
    expect(matchFollowups([], "mal-decompile")).toEqual([]);
  });

  it("needles are matched case-insensitively", () => {
    expect(matchFollowups([{ when: ["MAL-Decompile"], chips: [{ title: "x" }] }], "ran mal-decompile")).toHaveLength(1);
  });
});
