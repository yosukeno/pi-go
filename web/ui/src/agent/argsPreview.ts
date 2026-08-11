// Display helpers for the raw argument text of a call that is still being
// generated (see IncomingArgs in api/types.ts). Everything here is lenient on
// purpose: the text is a fragment of not-yet-valid JSON, so JSON.parse would
// throw on every frame but the last. Nothing in this file is authoritative —
// it exists to draw a typewriter preview, not to recover the arguments.

/**
 * extractPath finds the "path" (or "file_path") key in the raw head of the
 * arguments and returns its value, or null while the value's closing quote
 * has not arrived yet.
 *
 * The match requires the quote-colon-quote key shape so a `path` mentioned
 * inside the content itself is far less likely to be mistaken for the key —
 * and the key being looked for is conventionally the first one, so the
 * earliest match is the right one in practice. Never JSON.parse: the head is
 * capped mid-argument, so it is almost never valid JSON.
 */
export function extractPath(head: string): string | null {
  const m = /"(?:path|file_path)"\s*:\s*"/.exec(head);
  if (!m) return null;
  let raw = "";
  for (let i = m.index + m[0].length; i < head.length; i++) {
    const c = head[i];
    if (c === "\\") {
      // An escape pair: keep both chars for the unescape pass and skip ahead.
      // A lone backslash at the end means the pair is still in flight, and so
      // is the closing quote.
      if (i + 1 >= head.length) return null;
      raw += head.slice(i, i + 2);
      i++;
      continue;
    }
    if (c === '"') return unescapeForDisplay(raw);
    raw += c;
  }
  return null;
}

/**
 * incomingPath resolves an incoming call's path from whatever windows are
 * available. The head carries it when the model emits path first; a
 * content-first model (k3 with an unordered schema) only puts the path at the
 * very end of the arguments — inside the tail window. Trying both means the
 * card gets its name early in the common case and late in the worst case,
 * instead of never.
 */
export function incomingPath(incoming: { head?: string; tail?: string }): string | null {
  return extractPath(incoming.head ?? "") ?? extractPath(incoming.tail ?? "");
}

/**
 * createNewlineCounter counts content newlines across raw argument fragments
 * — the `\n` escape pairs of the JSON text. A `\\` pair (escaped backslash)
 * makes the following n literal text, and a `\n` can be split across two
 * fragments, so the one-character escape carry lives between push() calls.
 * The count is a live progress number, not a parse: the settled result's
 * exact stats replace it at tool_end.
 */
export function createNewlineCounter(): { push(fragment: string): number } {
  let lines = 0;
  let carry = false;
  return {
    push(fragment: string): number {
      let i = 0;
      if (carry) {
        if (fragment.length === 0) return lines;
        if (fragment[0] === "n") lines++;
        carry = false;
        i = 1;
      }
      for (; i < fragment.length; i++) {
        if (fragment[i] !== "\\") continue;
        if (i + 1 >= fragment.length) {
          carry = true;
          break;
        }
        if (fragment[i + 1] === "n") lines++;
        i++; // the pair is consumed whatever it escapes
      }
      return lines;
    },
  };
}

/**
 * previewLines turns the raw tail window into the last few displayable lines.
 * The tail is clipped mid-line more often than not, so the first line is
 * usually partial — it is shown anyway; the point is a typewriter effect,
 * not fidelity.
 */
export function previewLines(tail: string, maxLines: number): string[] {
  const lines = unescapeForDisplay(tail).split("\n");
  return lines.slice(-maxLines);
}

/**
 * unescapeForDisplay resolves the JSON string escapes a reader actually sees
 * (`\n`, `\t`, `\"`, `\\`, `\/`, `\uXXXX`) and leaves anything unknown
 * verbatim rather than inventing a meaning for it. A trailing incomplete
 * escape (a lone `\`, or `\u12` with the rest still in flight) is dropped:
 * showing half an escape would flicker on every fragment.
 */
function unescapeForDisplay(s: string): string {
  let out = "";
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== "\\") {
      out += s[i];
      continue;
    }
    const n = s[i + 1];
    if (n === undefined) break; // lone trailing backslash
    switch (n) {
      case "n":
        out += "\n";
        i++;
        break;
      case "t":
        out += "\t";
        i++;
        break;
      case '"':
        out += '"';
        i++;
        break;
      case "\\":
        out += "\\";
        i++;
        break;
      case "/":
        out += "/";
        i++;
        break;
      case "u": {
        const rest = s.slice(i + 2);
        const hex = /^[0-9a-fA-F]{4}/.exec(rest);
        if (hex) {
          out += String.fromCharCode(parseInt(hex[0], 16));
          i += 5;
        } else if (/^[0-9a-fA-F]{0,3}$/.test(rest)) {
          // Only hex digits left, fewer than four: the rest of the escape has
          // not arrived yet.
          return out;
        } else {
          // Malformed mid-fragment: keep it visible rather than guessing.
          out += "\\u";
          i++;
        }
        break;
      }
      default:
        out += s[i] + n;
        i++;
    }
  }
  return out;
}
