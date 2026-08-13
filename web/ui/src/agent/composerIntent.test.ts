import { describe, expect, it } from "vitest";
import { isSlashCommand, planIntent } from "./composerIntent";

describe("planIntent", () => {
  it("fills the composer by default", () => {
    expect(planIntent({ text: "find a sample" })).toEqual({ kind: "fill", text: "find a sample" });
  });

  it("sends only when the producer asked for it", () => {
    expect(planIntent({ text: "find a sample", send: true })).toEqual({
      kind: "send",
      text: "find a sample",
    });
  });

  it("trims, and rejects text that is only whitespace", () => {
    expect(planIntent({ text: "  spaced  " })).toEqual({ kind: "fill", text: "spaced" });
    expect(planIntent({ text: "   " })).toEqual({ kind: "rejected", reason: "empty" });
  });

  // The gate this protects is the approval gate: a command must not be able to
  // ride in as conversation content from a card or a panel.
  it("rejects a slash command even when told to send", () => {
    expect(planIntent({ text: "/auto", send: true })).toEqual({
      kind: "rejected",
      reason: "command",
    });
  });

  it("allows a skill invocation, which is a prompt that expands", () => {
    expect(planIntent({ text: "/skill:malware-analysis go" }).kind).toBe("fill");
  });
});

describe("isSlashCommand", () => {
  it("matches an exact leading word only", () => {
    expect(isSlashCommand("/compact")).toBe(true);
    expect(isSlashCommand("  /model glm-5.2  ")).toBe(true);
    expect(isSlashCommand("/autopilot please")).toBe(false);
    expect(isSlashCommand("/usr/local/bin matters here")).toBe(false);
    expect(isSlashCommand("read /help.md")).toBe(false);
  });
});
