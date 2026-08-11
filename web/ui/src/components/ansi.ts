// Terminal colour escapes, rendered as inline styles.
//
// Command output may carry ANSI escapes. The SGR ones (colour, bold, …) are
// worth keeping — a red error line is information — so they become inline
// styles. Every other escape class (cursor movement, erasing, OSC hyperlinks)
// would corrupt the layout or print odd glyphs, so it is dropped. Tokens carry
// a style string, never HTML, so the template keeps rendering plain text nodes
// exactly like the syntax highlighter does: nothing a command prints can
// become markup.

export interface AnsiToken {
  text: string;
  style?: string;
}

// One Dark–ish ramps: the terminal block's background is #1e1f22 and these
// read on it. "Black" is lifted to a grey so black-on-dark stays visible.
const FG = ["#5c6370", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf"];
const FG_BRIGHT = ["#828997", "#ff7b82", "#b5e890", "#ffd787", "#7cc0ff", "#e2a8ff", "#7ce3f0", "#ffffff"];
const BG = ["#3a3f44", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#abb2bf"];
const BG_BRIGHT = ["#5c6370", "#ff7b82", "#b5e890", "#ffd787", "#7cc0ff", "#e2a8ff", "#7ce3f0", "#ffffff"];

interface SgrState {
  fg?: string;
  bg?: string;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  underline: boolean;
}

function freshState(): SgrState {
  // fg/bg must be present (as undefined) or Object.assign on reset leaves a
  // stale colour behind: assign only copies keys the source actually has.
  return { fg: undefined, bg: undefined, bold: false, dim: false, italic: false, underline: false };
}

function styleOf(s: SgrState): string | undefined {
  const parts: string[] = [];
  if (s.fg) parts.push(`color:${s.fg}`);
  if (s.bg) parts.push(`background-color:${s.bg}`);
  if (s.bold) parts.push("font-weight:600");
  if (s.dim) parts.push("opacity:.65");
  if (s.italic) parts.push("font-style:italic");
  if (s.underline) parts.push("text-decoration:underline");
  return parts.length ? parts.join(";") : undefined;
}

/** The xterm 256-colour cube, for `38;5;n` / `48;5;n`. */
function palette256(n: number): string | undefined {
  if (n < 0 || n > 255) return undefined;
  if (n < 8) return FG[n];
  if (n < 16) return FG_BRIGHT[n - 8];
  if (n < 232) {
    const levels = [0, 95, 135, 175, 215, 255];
    const v = n - 16;
    const r = levels[Math.floor(v / 36)];
    const g = levels[Math.floor((v % 36) / 6)];
    const b = levels[v % 6];
    return `#${[r, g, b].map((x) => x.toString(16).padStart(2, "0")).join("")}`;
  }
  const grey = 8 + (n - 232) * 10;
  return `#${grey.toString(16).padStart(2, "0")}`.repeat(3);
}

function applySgr(state: SgrState, params: string): void {
  // An empty parameter list is a reset, and ";" within a sequence means the
  // same as ";0;" — normalise both before walking.
  const codes = params === "" ? [0] : params.split(";").map((p) => (p === "" ? 0 : parseInt(p, 10)));
  for (let i = 0; i < codes.length; i++) {
    const c = codes[i];
    if (Number.isNaN(c)) continue;
    if (c === 0) Object.assign(state, freshState());
    else if (c === 1) state.bold = true;
    else if (c === 2) state.dim = true;
    else if (c === 3) state.italic = true;
    else if (c === 4) state.underline = true;
    else if (c === 22) {
      state.bold = false;
      state.dim = false;
    } else if (c === 23) state.italic = false;
    else if (c === 24) state.underline = false;
    else if (c >= 30 && c <= 37) state.fg = FG[c - 30];
    else if (c === 38 || c === 48) {
      const target = c === 38 ? "fg" : "bg";
      if (codes[i + 1] === 5 && codes[i + 2] !== undefined) {
        state[target] = palette256(codes[i + 2]);
        i += 2;
      } else if (codes[i + 1] === 2 && codes[i + 4] !== undefined) {
        const [, r, g, b] = [0, codes[i + 2], codes[i + 3], codes[i + 4]];
        state[target] = `#${[r, g, b].map((x) => x.toString(16).padStart(2, "0")).join("")}`;
        i += 4;
      }
    } else if (c === 39) state.fg = undefined;
    else if (c >= 40 && c <= 47) state.bg = BG[c - 40];
    else if (c === 49) state.bg = undefined;
    else if (c >= 90 && c <= 97) state.fg = FG_BRIGHT[c - 90];
    else if (c >= 100 && c <= 107) state.bg = BG_BRIGHT[c - 100];
    // Anything else is a valid SGR we have no opinion about (blink, reverse):
    // ignoring it costs a decoration, never a wrong render.
  }
}

// skipEscape consumes one escape sequence starting at i (which points at ESC).
// SGR sequences are applied to state; everything else is just swallowed.
// Returns the index after the sequence.
function skipEscape(text: string, i: number, state: SgrState): number {
  const next = text[i + 1];
  if (next === "[") {
    // CSI: parameter bytes 0x30–0x3F, intermediate bytes 0x20–0x2F, final 0x40–0x7E.
    let j = i + 2;
    while (j < text.length && text[j] >= "0" && text[j] <= "?") j++;
    const paramsEnd = j;
    while (j < text.length && text[j] >= " " && text[j] <= "/") j++;
    if (j < text.length && text[j] >= "@" && text[j] <= "~") {
      if (text[j] === "m") applySgr(state, text.slice(i + 2, paramsEnd));
      return j + 1;
    }
    return j; // unterminated: drop the partial sequence
  }
  if (next === "]") {
    // OSC: runs until BEL or ST (ESC \). Window titles, hyperlinks — all noise here.
    let j = i + 2;
    while (j < text.length) {
      if (text[j] === "\x07") return j + 1;
      if (text[j] === "\x1b" && text[j + 1] === "\\") return j + 2;
      j++;
    }
    return j;
  }
  // ESC + optional intermediates + one final byte (charset selects and friends).
  let j = i + 1;
  while (j < text.length && text[j] >= " " && text[j] <= "/") j++;
  if (j < text.length && text[j] >= "0" && text[j] <= "~") return j + 1;
  return j;
}

/**
 * ansiToLines parses terminal output into per-line styled tokens.
 *
 * Newlines split lines; carriage returns are left in the text (the same
 * compromise the pre-render had before this parser existed — full line
 * rewriting for progress bars is a terminal emulator's job, not this one's).
 */
export function ansiToLines(text: string): AnsiToken[][] {
  const lines: AnsiToken[][] = [[]];
  const state = freshState();
  let buf = "";
  const flush = () => {
    if (!buf) return;
    const style = styleOf(state);
    lines[lines.length - 1].push(style ? { text: buf, style } : { text: buf });
    buf = "";
  };

  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === "\x1b") {
      flush();
      i = skipEscape(text, i, state);
    } else if (c === "\n") {
      flush();
      lines.push([]);
      i++;
    } else {
      buf += c;
      i++;
    }
  }
  flush();
  return lines;
}

// --- Long-listing fallback colouring -------------------------------------
//
// `bash ls -la` through a pipe prints no colours: macOS's BSD ls refuses every
// forcing knob short of a `--color` flag (verified: CLICOLOR_FORCE and -G do
// nothing when stdout is a pipe), and injecting flags into the model's
// commands is not an option. So the renderer recognises long-listing lines
// and colours the name column itself, the way ls would have.

// Perms (with a macOS `@`/`+` suffix), then seven fields — links, owner,
// group, size, and the three date columns — then the name, which is the rest
// of the line. Device lines (`8, 1` is two fields) split one column early;
// the name still ends up coloured, just starting at the year. Lines that only
// almost match cost nothing: they render exactly as before.
const lsLongRe = /^([bcdlps-][rwxstST-]{9}[@+]?)(?:\s+\S+){7}\s+(.+)$/;

// The same sky/cyan/green family the rest of the terminal palette uses.
const LS_DIR = "color:#0ea5e9;font-weight:600";
const LS_LINK = "color:#56b6c2";
const LS_EXEC = "color:#98c379";

/**
 * colorizeLongListing colours the name column of `ls -l`-format lines. A line
 * that already carries a style is left alone — the command coloured it
 * itself and knows better than the heuristic.
 */
export function colorizeLongListing(lines: AnsiToken[][]): AnsiToken[][] {
  return lines.map((line) => {
    if (line.length !== 1 || line[0].style) return line;
    const m = lsLongRe.exec(line[0].text);
    if (!m) return line;
    const [whole, perms, name] = m;
    const style =
      perms[0] === "d"
        ? LS_DIR
        : perms[0] === "l"
          ? LS_LINK
          : perms[0] === "-" && /[xs]/.test(perms)
            ? LS_EXEC
            : undefined;
    if (!style) return line;
    return [{ text: whole.slice(0, whole.length - name.length) }, { text: name, style }];
  });
}
