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

export type Language =
  | "go"
  | "ts"
  | "java"
  | "c"
  | "python"
  | "rust"
  | "sql"
  | "yara"
  | "css"
  | "xml"
  | "json"
  | "shell"
  | "yaml"
  | "markdown"
  | "plain";

interface Spec {
  lineComment?: string[];
  blockComment?: [string, string];
  /** quotes that behave like strings; the third field marks a raw (no-escape) one. */
  strings?: { open: string; close: string; raw?: boolean }[];
  keywords?: Set<string>;
  types?: Set<string>;
  /** callLike highlights `name(` as a function reference. */
  callLike?: boolean;
  /** foldCase matches keywords case-insensitively; SQL is written both ways. */
  foldCase?: boolean;
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
// One set covers the C-family curly-brace languages (Java, Kotlin, C#, Scala): the
// scanner only needs to know which words are keywords, not the grammar around them.
const javaKeywords = new Set(
  ("abstract as assert break case catch class const continue default do else enum extends final finally " +
    "for fun get goto if implements import in instanceof interface internal is lateinit native new object " +
    "open operator out override package private protected public record return sealed set static super " +
    "suspend switch synchronized this throw throws transient try typealias val var volatile when where " +
    "while yield null true false").split(" "),
);
const javaTypes = new Set(
  ("boolean byte char double float int long short void var String Object Integer Long Double Float Boolean " +
    "Character Byte Short Number List Map Set Array Collection Optional Stream Any Unit Nothing " +
    "bool string decimal object dynamic").split(" "),
);
const cKeywords = new Set(
  ("alignas alignof asm auto break case catch class const consteval constexpr continue decltype default " +
    "delete do else enum explicit export extern false for friend goto if inline mutable namespace new " +
    "noexcept nullptr operator private protected public register return sizeof static static_assert " +
    "struct switch template this throw true try typedef typeid typename union using virtual volatile " +
    "while NULL include define ifdef ifndef endif pragma").split(" "),
);
const cTypes = new Set(
  ("bool char double float int long short signed unsigned void size_t ssize_t int8_t int16_t int32_t " +
    "int64_t uint8_t uint16_t uint32_t uint64_t wchar_t FILE string vector map set pair auto").split(" "),
);
const pythonKeywords = new Set(
  ("and as assert async await break class continue def del elif else except finally for from global if " +
    "import in is lambda match nonlocal not or pass raise return try while with yield None True False " +
    "self cls").split(" "),
);
const pythonTypes = new Set(
  ("bool bytes complex dict float frozenset int list object set str tuple type Any Callable Dict Iterable " +
    "Iterator List Optional Sequence Tuple Union").split(" "),
);
const rustKeywords = new Set(
  ("as async await break const continue crate dyn else enum extern false fn for if impl in let loop match " +
    "mod move mut pub ref return self Self static struct super trait true type unsafe use where while").split(" "),
);
const rustTypes = new Set(
  ("bool char f32 f64 i8 i16 i32 i64 i128 isize str u8 u16 u32 u64 u128 usize String Vec Option Result Box " +
    "Rc Arc HashMap HashSet BTreeMap").split(" "),
);
const sqlKeywords = new Set(
  ("add all alter and any as asc begin between by case cast check column commit constraint create cross " +
    "cursor database default delete desc distinct drop else end exists foreign from full group having if " +
    "in index inner insert intersect into is join key left like limit not null offset on or order outer " +
    "primary references rename right rollback select set table then to transaction union unique update " +
    "using values view when where with").split(" "),
);
const sqlTypes = new Set(
  ("bigint binary bit blob boolean char clob date datetime decimal double float int integer json numeric " +
    "real serial smallint text time timestamp uuid varchar").split(" "),
);
// YARA: rule structure and condition operators are keywords; the string modifiers
// and the integer-reading functions read as types, which keeps a rule's shape
// (rule / strings / condition) visually distinct from its matching logic.
const yaraKeywords = new Set(
  ("rule meta strings condition private global import include and or not any all none of them for in at " +
    "contains icontains matches startswith istartswith endswith iendswith defined true false them").split(" "),
);
const yaraTypes = new Set(
  ("ascii wide nocase fullword base64 base64wide xor filesize entrypoint uint8 uint16 uint32 uint64 int8 " +
    "int16 int32 int64 uint8be uint16be uint32be int8be int16be int32be pe elf math hash cuckoo magic " +
    "console time dotnet").split(" "),
);
const cssKeywords = new Set(
  ("important media supports keyframes import charset font-face inherit initial unset none auto flex grid " +
    "block inline absolute relative fixed sticky hidden visible").split(" "),
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
  java: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"""', close: '"""', raw: true },
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    keywords: javaKeywords,
    types: javaTypes,
    callLike: true,
  },
  c: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    keywords: cKeywords,
    types: cTypes,
    callLike: true,
  },
  python: {
    lineComment: ["#"],
    strings: [
      { open: '"""', close: '"""', raw: true },
      { open: "'''", close: "'''", raw: true },
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    keywords: pythonKeywords,
    types: pythonTypes,
    callLike: true,
  },
  rust: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    keywords: rustKeywords,
    types: rustTypes,
    callLike: true,
  },
  sql: {
    lineComment: ["--"],
    blockComment: ["/*", "*/"],
    strings: [
      { open: "'", close: "'" },
      { open: '"', close: '"' },
    ],
    keywords: sqlKeywords,
    types: sqlTypes,
    foldCase: true,
  },
  yara: {
    lineComment: ["//"],
    blockComment: ["/*", "*/"],
    // A regex literal is scanned as a string. It is listed after the comment
    // markers, which tokenize() checks first, so `//` stays a comment; a non-raw
    // quote cannot span lines, so an unpaired `/` colours one line at worst.
    strings: [
      { open: '"', close: '"' },
      { open: "/", close: "/" },
    ],
    keywords: yaraKeywords,
    types: yaraTypes,
  },
  css: {
    blockComment: ["/*", "*/"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
    keywords: cssKeywords,
  },
  xml: {
    blockComment: ["<!--", "-->"],
    strings: [
      { open: '"', close: '"' },
      { open: "'", close: "'" },
    ],
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
    case "java":
    case "kt":
    case "kts":
    case "kotlin":
    case "scala":
    case "cs":
    case "csharp":
    case "groovy":
      return "java";
    case "c":
    case "h":
    case "cc":
    case "cpp":
    case "cxx":
    case "hpp":
    case "hh":
    case "m":
    case "mm":
      return "c";
    case "py":
    case "python":
    case "pyi":
      return "python";
    case "rs":
    case "rust":
      return "rust";
    case "sql":
      return "sql";
    case "yar":
    case "yara":
      return "yara";
    case "css":
    case "scss":
    case "sass":
    case "less":
      return "css";
    case "xml":
    case "html":
    case "htm":
    case "svg":
    case "xhtml":
      return "xml";
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
      const lookup = spec.foldCase ? word.toLowerCase() : word;
      let kind: TokenKind = "plain";
      if (spec.keywords?.has(lookup)) kind = "keyword";
      else if (spec.types?.has(lookup)) kind = "type";
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
