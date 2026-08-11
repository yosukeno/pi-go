import { describe, expect, it } from "vitest";
import { ansiToLines, colorizeLongListing, type AnsiToken } from "./ansi";

const text = (tokens: AnsiToken[]) => tokens.map((t) => t.text).join("");
const flat = (lines: AnsiToken[][]) => lines.map(text).join("\n");
const styleOf = (lines: AnsiToken[][], needle: string) =>
  lines.flat().find((t) => t.text.includes(needle))?.style;

describe("ansiToLines", () => {
  it("passes plain text through untouched", () => {
    const lines = ansiToLines("hello\nworld");
    expect(flat(lines)).toBe("hello\nworld");
    expect(lines[0][0].style).toBeUndefined();
  });

  it("colours text after an SGR and clears it on reset", () => {
    const lines = ansiToLines("\x1b[31merror: nope\x1b[0m plain");
    expect(flat(lines)).toBe("error: nope plain");
    expect(styleOf(lines, "error")).toContain("color:#e06c75");
    expect(styleOf(lines, "plain")).toBeUndefined();
  });

  it("combines attributes into one style", () => {
    const lines = ansiToLines("\x1b[1;33mwarn\x1b[0m");
    const style = styleOf(lines, "warn") ?? "";
    expect(style).toContain("font-weight:600");
    expect(style).toContain("color:#e5c07b");
  });

  it("supports bright, 256-colour and truecolour forms", () => {
    expect(styleOf(ansiToLines("\x1b[92mhi"), "hi")).toContain("color:#b5e890");
    expect(styleOf(ansiToLines("\x1b[38;5;196mhi"), "hi")).toContain("color:#ff0000");
    expect(styleOf(ansiToLines("\x1b[38;2;18;52;86mhi"), "hi")).toContain("color:#123456");
    expect(styleOf(ansiToLines("\x1b[41mhi"), "hi")).toContain("background-color:#e06c75");
  });

  it("drops cursor movement and erasing but keeps the text", () => {
    const lines = ansiToLines("ok\x1b[2J\x1b[1;1Hdone");
    expect(flat(lines)).toBe("okdone");
    expect(styleOf(lines, "okdone")).toBeUndefined();
  });

  it("drops OSC hyperlinks and titles", () => {
    const lines = ansiToLines("\x1b]8;;https://evil.example\x07link\x1b]8;;\x07\x1b]0;title\x07x");
    expect(flat(lines)).toBe("linkx");
  });

  it("swallows an unterminated sequence at end of input", () => {
    expect(flat(ansiToLines("tail\x1b[3"))).toBe("tail");
    expect(flat(ansiToLines("tail\x1b"))).toBe("tail");
  });

  it("carries colour state across newlines", () => {
    const lines = ansiToLines("\x1b[32mgreen\ncarried\x1b[0m");
    expect(lines).toHaveLength(2);
    expect(styleOf(lines, "carried")).toContain("color:#98c379");
  });
});

describe("colorizeLongListing", () => {
  it("colours a directory's name sky blue", () => {
    const [line] = colorizeLongListing(ansiToLines("drwxr-xr-x@ 31 wangy staff 992 Aug  6 19:18 tools"));
    expect(line).toHaveLength(2);
    expect(line[1].text).toBe("tools");
    expect(line[1].style).toContain("color:#0ea5e9");
    expect(line[0].text).toBe("drwxr-xr-x@ 31 wangy staff 992 Aug  6 19:18 ");
    expect(line[0].style).toBeUndefined();
  });

  it("colours symlinks cyan and executables green, plain files not at all", () => {
    const link = colorizeLongListing(ansiToLines("lrwxrwxrwx 1 wangy staff 10 Aug 6 19:18 bin -> /usr/bin"));
    expect(styleOf(link, "bin -> /usr/bin")).toContain("color:#56b6c2");
    const exe = colorizeLongListing(ansiToLines("-rwxr-xr-x 1 wangy staff 10 Aug 6 19:18 run.sh"));
    expect(styleOf(exe, "run.sh")).toContain("color:#98c379");
    const plain = colorizeLongListing(ansiToLines("-rw-r--r-- 1 wangy staff 10 Aug 6 19:18 note.txt"));
    expect(plain[0]).toHaveLength(1);
  });

  it("leaves non-listing and already-coloured lines alone", () => {
    expect(colorizeLongListing(ansiToLines("total 56"))[0]).toHaveLength(1);
    const coloured = ansiToLines("\x1b[31mdrwxr-xr-x 1 a b 1 Aug 6 19:18 x\x1b[0m");
    // Untouched: the same line array comes back, style and all.
    expect(colorizeLongListing(coloured)[0]).toBe(coloured[0]);
    expect(coloured[0][0].style).toContain("color:#e06c75");
  });
});
