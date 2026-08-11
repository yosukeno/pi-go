// A small hand-written tokenizer.
//
// Deliberately not highlight.js or prism: they weigh more than this whole app's
// logic, and the reason to avoid them is not size but safety. Those libraries
// produce HTML that has to be injected with v-html; this one produces tokens the
// template renders as text, so nothing a tool prints can become markup. A coding
// agent displays file contents it did not write, all day long.
//
// It is a scanner, not a parser: it knows about comments, strings, numbers and
// keywords, and gives up gracefully on anything else. Being wrong about a token
// costs a wrong colour, never a broken render — the invariant below is what
// guarantees that.

export type TokenKind = "plain" | "comment" | "string" | "number" | "keyword" | "type" | "func";

export interface Token {
  text: string;
  kind: TokenKind;
}

export type Language = "go" | "ts" | "json" | "shell" | "yaml" | "markdown" | "plain";

interface Spec {
  lineComment?: string[];
  blockComment?: [string, string];
  /** quotes that behave like strings; the third field marks a raw (no-escape) one. */
  strings?: { open: string; close: string; raw?: boolean }[];
  keywords?: Set<string>;
  types?: Set<string>;
  /** callLike highlights `name(` as a function reference. */
  callLike?: boolean;
}

const goKeywords = new Set(
  ("break case chan const continue default defer else fallthrough for func go goto if import " +
    "interface map package range return select struct switch type var nil true false iota").split(" "),
);
const goTypes = new Set(
  ("string bool byte rune error int int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr " +
    "float32 float64 complex64 complex128 any").split(" "),
);
const tsKeywords = new Set(
  ("as async await break case catch class const continue debugger default delete do else enum export " +
    "extends false finally for from function get if implements import in instanceof interface let new " +
    "null of private protected public readonly return satisfies set static super switch this throw true " +
    "try type typeof undefined var void while yield").split(" "),
);
const tsTypes = new Set("string number boolean bigint symbol object unknown never any Record Array Promise".split(" "));
const shellKeywords = new Set(
  ("if then else elif fi for while until do done case esac function in return exit export local " +
    "set unset source echo cd test true false").split(" "),
);

const specs: Record<Language, Spec> = {
  go: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
      { open: "`", close: "`", raw: true },
    ],
    keywords: goKeywords,
    types: goTypes,
    callLike: true,
  },
  ts: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
      { open: "`", close: "`" },
    ],
    keywords: tsKeywords,
    types: tsTypes,
    callLike: true,
  },
  json: {
    strings: [{ open: '"', close: '"' }],
    keywords: new Set(["true", "false", "null"]),
  },
  shell: {
    lineComment: ["#"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'", raw: true },
    ],
    keywords: shellKeywords,
  },
  yaml: {
    lineComment: ["#"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'", raw: true },
    ],
    keywords: new Set(["true", "false", "null", "yes", "no"]),
  },
  markdown: { blockComment: ["```", "```"] },
  plain: {},
};

/** detectLanguage maps a file extension or language hint onto a scanner. */
export function detectLanguage(hint: string | undefined): Language {
  const h = (hint ?? "").toLowerCase().replace(/^\./, "");
  switch (h) {
    case "go":
    case "golang":
      return "go";
    case "ts":
    case "tsx":
    case "js":
    case "jsx":
    case "mjs":
    case "cjs":
    case "vue":
      return "ts";
    case "json":
    case "jsonc":
      return "json";
    case "sh":
    case "bash":
    case "zsh":
    case "shell":
      return "shell";
    case "yml":
    case "yaml":
    case "toml":
      return "yaml";
    case "md":
    case "markdown":
      return "markdown";
    default:
      return "plain";
  }
}

const isIdentStart = (c: string) => /[A-Za-z_$]/.test(c);
const isIdent = (c: string) => /[A-Za-z0-9_$]/.test(c);
const isDigit = (c: string) => c >= "0" && c <= "9";

/**
 * tokenize splits code into coloured runs.
 *
 * Invariant: `tokenize(code, lang).map(t => t.text).join("") === code`, always.
 * Every branch either consumes at least one character or falls through to the
 * plain-text case, so it terminates on any input, including an unterminated
 * string or comment.
 */
export function tokenize(code: string, lang: Language): Token[] {
  const spec = specs[lang];
  if (lang === "plain") return code ? [{ text: code, kind: "plain" }] : [];

  const out: Token[] = [];
  let plain = "";
  const flush = () => {
    if (plain) {
      out.push({ text: plain, kind: "plain" });
      plain = "";
    }
  };
  const push = (text: string, kind: TokenKind) => {
    flush();
    out.push({ text, kind });
  };

  let i = 0;
  while (i < code.length) {
    const rest = code.slice(i);

    // Block comment. Unterminated runs to the end rather than looping.
    if (spec.blockComment && rest.startsWith(spec.blockComment[0])) {
      const [open, close] = spec.blockComment;
      const end = code.indexOf(close, i + open.length);
      const stop = end === -1 ? code.length : end + close.length;
      push(code.slice(i, stop), "comment");
      i = stop;
      continue;
    }

    // Line comment. `#` inside a shell string is handled by the string branch
    // below, because that branch runs first when a quote opened earlier.
    const line = spec.lineComment?.find((c) => rest.startsWith(c));
    if (line) {
      const nl = code.indexOf("\n", i);
      const stop = nl === -1 ? code.length : nl;
      push(code.slice(i, stop), "comment");
      i = stop;
      continue;
    }

    const quote = spec.strings?.find((s) => rest.startsWith(s.open));
    if (quote) {
      let j = i + quote.open.length;
      while (j < code.length) {
        if (!quote.raw && code[j] === "\\") {
          j += 2;
          continue;
        }
        if (code.startsWith(quote.close, j)) {
          j += quote.close.length;
          break;
        }
        // A non-raw quote does not span lines: an unterminated one would
        // otherwise paint the rest of the file.
        if (!quote.raw && code[j] === "\n") break;
        j++;
      }
      push(code.slice(i, Math.min(j, code.length)), "string");
      i = Math.min(j, code.length);
      continue;
    }

    const c = code[i];

    if (isDigit(c) || (c === "." && isDigit(code[i + 1] ?? ""))) {
      let j = i;
      while (j < code.length && /[0-9a-fA-FxXoObB._+-]/.test(code[j])) {
        // Stop at a `-` or `+` that is not part of an exponent.
        if ((code[j] === "-" || code[j] === "+") && !/[eE]/.test(code[j - 1] ?? "")) break;
        j++;
      }
      push(code.slice(i, j), "number");
      i = j;
      continue;
    }

    if (isIdentStart(c)) {
      let j = i;
      while (j < code.length && isIdent(code[j])) j++;
      const word = code.slice(i, j);
      let kind: TokenKind = "plain";
      if (spec.keywords?.has(word)) kind = "keyword";
      else if (spec.types?.has(word)) kind = "type";
      else if (spec.callLike && code[j] === "(") kind = "func";
      if (kind === "plain") plain += word;
      else push(word, kind);
      i = j;
      continue;
    }

    plain += c;
    i++;
  }
  flush();
  return out;
}

/**
 * highlightLines tokenizes the whole text and then splits on newlines.
 *
 * Splitting first and tokenizing per line would break multi-line constructs: a
 * Go raw string or a `/* *\/` comment would be re-scanned from a state the
 * scanner never had.
 */
export function highlightLines(code: string, lang: Language): Token[][] {
  const lines: Token[][] = [[]];
  for (const token of tokenize(code, lang)) {
    const parts = token.text.split("\n");
    parts.forEach((part, idx) => {
      if (idx > 0) lines.push([]);
      if (part) lines[lines.length - 1].push({ text: part, kind: token.kind });
    });
  }
  return lines;
}
