import { describe, expect, it } from "vitest";
import { parsePanelMessage } from "./panelBridge";

const ask = (text: string, extra: Record<string, unknown> = {}) => ({
  jsonrpc: "2.0",
  method: "ui/message",
  params: { role: "user", content: { type: "text", text } },
  ...extra,
});

describe("parsePanelMessage", () => {
  it("accepts a ui/message notification", () => {
    expect(parsePanelMessage(ask("分析这个样本"))).toEqual({
      ok: true,
      id: null,
      text: "分析这个样本",
    });
  });

  it("keeps a request id so the caller can answer it", () => {
    expect(parsePanelMessage(ask("x", { id: 7 }))).toMatchObject({ ok: true, id: 7 });
    expect(parsePanelMessage(ask("x", { id: "a" }))).toMatchObject({ ok: true, id: "a" });
    // An unusable id is treated as no id rather than echoed back malformed.
    expect(parsePanelMessage(ask("x", { id: { nested: true } }))).toMatchObject({ id: null });
  });

  it("rejects anything that is not JSON-RPC", () => {
    for (const junk of [null, undefined, 42, "hello", [], {}, { method: "ui/message" }]) {
      expect(parsePanelMessage(junk)).toEqual({ ok: false, reason: "not-rpc" });
    }
  });

  // Browsers deliver plenty of unrelated same-origin chatter (dev tooling, xterm,
  // extensions); it must fall out as not-rpc rather than reach the composer.
  it("rejects framework noise that happens to be an object", () => {
    expect(parsePanelMessage({ source: "vite", type: "update" })).toEqual({
      ok: false,
      reason: "not-rpc",
    });
  });

  it("reports an unknown method instead of silently dropping it", () => {
    expect(parsePanelMessage({ jsonrpc: "2.0", method: "ui/open-link", params: {} })).toEqual({
      ok: false,
      reason: "unknown-method",
    });
  });

  it("rejects a payload whose content is not usable text", () => {
    const bad = [
      { jsonrpc: "2.0", method: "ui/message" },
      { jsonrpc: "2.0", method: "ui/message", params: {} },
      { jsonrpc: "2.0", method: "ui/message", params: { content: { type: "image", text: "x" } } },
      { jsonrpc: "2.0", method: "ui/message", params: { content: { type: "text", text: "  " } } },
      { jsonrpc: "2.0", method: "ui/message", params: { content: { type: "text", text: 5 } } },
    ];
    for (const b of bad) {
      expect(parsePanelMessage(b)).toEqual({ ok: false, reason: "bad-params" });
    }
  });

  it("refuses to let a panel speak as the assistant", () => {
    const msg = ask("x");
    msg.params.role = "assistant";
    expect(parsePanelMessage(msg)).toEqual({ ok: false, reason: "bad-params" });
  });
});
