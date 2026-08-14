// A composer intent is "someone other than the keyboard wants to put text in the
// input box". Two producers want it — a starter card on the empty state, and
// later a dock panel asking the agent about what it is showing — and they must
// not each grow their own path into the composer, because the interesting rules
// are the same for both and belong in one place.
//
// The rules:
//
//  1. Filling is the default; sending is opt-in. pi-go does not speak for the
//     user anywhere else (editing a message refills the composer rather than
//     resending it), and a click that silently spends a model call is worse than
//     one that needs a second click.
//  2. Text is text. It reaches the composer as a string and is rendered as one;
//     nothing here ever becomes markup.
//  3. A slash command is not a prompt. The server refuses to accept one as
//     conversation content so that a single injected line cannot turn off the
//     approval gate; a producer that is not the keyboard gets the same
//     treatment, one step earlier, where it can be reported instead of 400'd.
//     /skill:name is deliberately allowed: it is a prompt that expands.

const SLASH_COMMANDS = [
  "/auto",
  "/strict",
  "/standard",
  "/model",
  "/usage",
  "/compact",
  "/help",
  "/exit",
  "/quit",
];

/** isSlashCommand mirrors the server's backstop: an exact leading word only, so
 *  a prompt that merely opens with a path is untouched. */
export function isSlashCommand(text: string): boolean {
  const word = text.trim().split(" ")[0];
  return SLASH_COMMANDS.includes(word);
}

export interface ComposerIntent {
  text: string;
  /** Send immediately instead of leaving the text in the composer. */
  send?: boolean;
}

export type IntentOutcome =
  | { kind: "fill"; text: string }
  | { kind: "send"; text: string }
  | { kind: "rejected"; reason: "empty" | "command" };

/**
 * planIntent decides what an intent may do. It is separated from applying the
 * result so the decision is testable without a composer, a session or a DOM.
 */
export function planIntent(intent: ComposerIntent): IntentOutcome {
  const text = intent.text.trim();
  if (!text) return { kind: "rejected", reason: "empty" };
  if (isSlashCommand(text)) return { kind: "rejected", reason: "command" };
  return { kind: intent.send ? "send" : "fill", text };
}
