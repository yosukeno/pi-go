// The panel bridge lets a dock panel hand text to the conversation — "ask the
// agent about what I am showing" — without the panel knowing anything about the
// composer, the session, or the token.
//
// The wire format is deliberately MCP Apps' `ui/message` (SEP-1865): JSON-RPC 2.0
// over postMessage, params `{role, content:{type:"text", text}}`. That standard
// exists for exactly this problem, is what ChatGPT's Apps SDK speaks, and picking
// it now means a panel written against this bridge keeps working if pi-go ever
// grows a real MCP Apps host. Inventing a private message shape would buy nothing
// and cost that.
//
// What this module does *not* do is decide what happens to the text. It returns
// an intent; composerIntent owns the rules (fill rather than send, no slash
// commands). Parsing and policy stay separate so both can be tested alone.
//
// The security model is the caller's other half and cannot be skipped:
//
//   1. event.origin must equal this page's origin. Panels are reverse-proxied to
//      the same origin, so a message from anywhere else is not a panel.
//   2. event.source must be the panel iframe's own contentWindow. Origin alone
//      would also accept the page itself and any other same-origin frame.
//   3. Everything below treats the payload as untrusted data — text stays text
//      and never becomes markup.
//   4. Replies must name the target origin explicitly, never "*".
//
// A panel runs in the page's origin, so it is already trusted with the token that
// page holds; these checks are not what makes a hostile panel safe. They make an
// *unrelated* frame unable to speak for one. Hosting genuinely untrusted panels
// would need a separate sandbox origin, which is what the MCP Apps spec requires
// of web hosts and what pi-go's same-origin proxy deliberately does not attempt.

export const PANEL_MESSAGE_METHOD = "ui/message";

export type PanelParse =
  | { ok: true; id: string | number | null; text: string }
  | { ok: false; reason: "not-rpc" | "unknown-method" | "bad-params" };

/**
 * parsePanelMessage validates a raw postMessage payload and pulls out the text.
 *
 * Unknown methods are reported rather than ignored so the caller can log them:
 * a panel calling `ui/open-link` today gets silence, and the operator deserves to
 * see why instead of assuming the bridge is broken.
 */
export function parsePanelMessage(data: unknown): PanelParse {
  if (typeof data !== "object" || data === null) return { ok: false, reason: "not-rpc" };
  const msg = data as Record<string, unknown>;
  if (msg.jsonrpc !== "2.0" || typeof msg.method !== "string") {
    return { ok: false, reason: "not-rpc" };
  }
  if (msg.method !== PANEL_MESSAGE_METHOD) return { ok: false, reason: "unknown-method" };

  const params = msg.params;
  if (typeof params !== "object" || params === null) return { ok: false, reason: "bad-params" };
  const p = params as Record<string, unknown>;
  // role is part of the standard's shape; anything but a user message would be
  // the panel putting words in the assistant's mouth.
  if (p.role !== undefined && p.role !== "user") return { ok: false, reason: "bad-params" };

  const content = p.content;
  if (typeof content !== "object" || content === null) return { ok: false, reason: "bad-params" };
  const c = content as Record<string, unknown>;
  if (c.type !== "text" || typeof c.text !== "string" || !c.text.trim()) {
    return { ok: false, reason: "bad-params" };
  }

  // A notification (no id) wants no answer; a request gets one echoed back.
  const rawID = msg.id;
  const id =
    typeof rawID === "string" || typeof rawID === "number" ? rawID : null;
  return { ok: true, id, text: c.text };
}
