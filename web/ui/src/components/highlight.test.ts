import { describe, expect, it } from "vitest";
import { detectLanguage, highlightLines, tokenize, type Language, type Token } from "./highlight";

const text = (tokens: Token[]) => tokens.map((t) => t.text).join("");
const kindOf = (tokens: Token[], needle: string) => tokens.find((t) => t.text === needle)?.kind;

const languages: Language[] = ["go", "ts", "json", "shell", "yaml", "markdown", "plain"];

describe("tokenize", () => {
  // The one invariant that makes a wrong guess harmless: a mis-scanned token can
  // only be the wrong colour, never lost or duplicated text.
  it("always reproduces the input exactly", () => {
    const samples = [
      'package main\n\nfunc main() {\n\tfmt.Println("hi") // 你好\n}\n',
      "const x = `a\nb`; /* multi\nline */ let y = 0x1f;",
      '{"key": "value", "n": 1.5e3, "ok": true}',
      "#!/bin/bash\nset -e\nrm -rf ./build  # danger\necho 'single'\n",
      "unterminated \"string and then EOF",
      "/* never closed",
      "",
      "\n\n\n",
      "emoji 🎉 and 中文 mixed with `raw`",
    ];
    for (const lang of languages) {
      for (const sample of samples) {
        expect(text(tokenize(sample, lang)), `${lang}: ${JSON.stringify(sample)}`).toBe(sample);
      }
    }
  });

  it("colours Go keywords, types, strings and comments", () => {
    const src = 'func add(a int) string { return "x" } // note';
    const tokens = tokenize(src, "go");
    expect(kindOf(tokens, "func")).toBe("keyword");
    expect(kindOf(tokens, "int")).toBe("type");
    expect(kindOf(tokens, "return")).toBe("keyword");
    expect(kindOf(tokens, '"x"')).toBe("string");
    expect(kindOf(tokens, "// note")).toBe("comment");
    // `add(` is a call site, not a keyword.
    expect(kindOf(tokens, "add")).toBe("func");
  });

  it("does not treat comment markers inside a string as a comment", () => {
    const tokens = tokenize('url := "https://example.com" // real comment', "go");
    expect(kindOf(tokens, '"https://example.com"')).toBe("string");
    expect(kindOf(tokens, "// real comment")).toBe("comment");
    expect(tokens.filter((t) => t.kind === "comment")).toHaveLength(1);
  });

  it("does not let a quote inside a comment open a string", () => {
    const tokens = tokenize('// it\'s fine\nx := 1', "go");
    expect(kindOf(tokens, "// it's fine")).toBe("comment");
    expect(tokens.some((t) => t.kind === "string")).toBe(false);
  });

  it("keeps an unterminated string on its own line", () => {
    // Otherwise one stray quote paints the rest of the file.
    const lines = highlightLines('a := "oops\nb := 2\n', "go");
    expect(lines[1].some((t) => t.kind === "string")).toBe(false);
    expect(kindOf(lines[1], "2")).toBe("number");
  });

  it("handles Go raw strings spanning lines", () => {
    const lines = highlightLines("s := `line1\nline2`\nx := 1", "go");
    expect(lines[0].some((t) => t.kind === "string")).toBe(true);
    expect(lines[1].some((t) => t.kind === "string")).toBe(true);
    expect(kindOf(lines[2], "1")).toBe("number");
  });

  it("colours JSON strings, numbers and literals", () => {
    const tokens = tokenize('{"path": "a.go", "n": 12, "ok": true}', "json");
    // Keys and values are both strings: distinguishing them needs a parser, and
    // the colour it would buy is not worth one.
    expect(kindOf(tokens, '"path"')).toBe("string");
    expect(kindOf(tokens, '"a.go"')).toBe("string");
    expect(kindOf(tokens, "12")).toBe("number");
    expect(kindOf(tokens, "true")).toBe("keyword");
  });

  it("treats shell comments and single quotes", () => {
    const tokens = tokenize("echo 'a # b' # trailing", "shell");
    expect(kindOf(tokens, "'a # b'")).toBe("string");
    expect(kindOf(tokens, "# trailing")).toBe("comment");
    expect(kindOf(tokens, "echo")).toBe("keyword");
  });

  it("leaves plain language untouched", () => {
    const src = 'anything // goes "here"';
    expect(tokenize(src, "plain")).toEqual([{ text: src, kind: "plain" }]);
  });
});

describe("highlightLines", () => {
  it("produces one entry per line and drops no text", () => {
    const src = "a\nb\n\nc";
    const lines = highlightLines(src, "go");
    expect(lines).toHaveLength(4);
    expect(lines.map((l) => text(l)).join("\n")).toBe(src);
  });

  it("returns a single empty line for empty input", () => {
    expect(highlightLines("", "go")).toEqual([[]]);
  });
});

describe("detectLanguage", () => {
  it("maps extensions", () => {
    expect(detectLanguage("go")).toBe("go");
    expect(detectLanguage("tsx")).toBe("ts");
    expect(detectLanguage(".JSON")).toBe("json");
    expect(detectLanguage("zsh")).toBe("shell");
    expect(detectLanguage("yaml")).toBe("yaml");
  });

  it("falls back to plain for anything unknown", () => {
    expect(detectLanguage("bin")).toBe("plain");
    expect(detectLanguage(undefined)).toBe("plain");
    expect(detectLanguage("")).toBe("plain");
  });
});
