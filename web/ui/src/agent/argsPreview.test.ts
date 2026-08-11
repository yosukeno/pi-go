import { describe, expect, it } from "vitest";
import { createNewlineCounter, extractPath, incomingPath, previewLines } from "./argsPreview";

// In these fixtures the arguments are raw JSON text exactly as it streams in:
// `\\n` is the two characters backslash + n, not a newline.

describe("extractPath", () => {
  it("returns the path once its closing quote has arrived", () => {
    expect(extractPath('{"path": "src/foo.ts", "content": "')).toBe("src/foo.ts");
  });

  it("returns null while the value is still streaming", () => {
    expect(extractPath('{"path": "src/fo')).toBeNull();
    expect(extractPath('{"path": "src/foo.ts\\')).toBeNull();
    expect(extractPath('{"path"')).toBeNull();
    expect(extractPath("{")).toBeNull();
  });

  it("accepts the file_path alias", () => {
    expect(extractPath('{"file_path": "a/b.go", "oldText": "x"}')).toBe("a/b.go");
  });

  it("unescapes escaped characters in the value", () => {
    expect(extractPath('{"path": "C:\\\\proj\\\\a.ts", "content": ""}')).toBe("C:\\proj\\a.ts");
    expect(extractPath('{"path": "weird \\"quoted\\" name.txt", "c": 1}')).toBe('weird "quoted" name.txt');
  });

  it("does not mistake a path mention inside the content for the key", () => {
    // The real key comes first; the content's `"path": "b.ts"` is text.
    expect(extractPath('{"path": "a.ts", "content": "see \\"path\\": \\"b.ts\\""')).toBe("a.ts");
  });
});

describe("incomingPath", () => {
  it("reads the head when the model emits path first", () => {
    expect(incomingPath({ head: '{"path": "a.ts", "content": "…', tail: "…" })).toBe("a.ts");
  });

  it("falls back to the tail for a content-first model", () => {
    // k3 with an unordered schema: {"content": "…", "path": "x.md"} — the path
    // closes only in the last fragments, inside the tail window.
    expect(incomingPath({ head: '{"content": "# 长文', tail: '结尾", "path": "计算机.md"}' })).toBe("计算机.md");
  });

  it("returns null while neither window has a closed path", () => {
    expect(incomingPath({ head: '{"content": "abc', tail: 'abc' })).toBeNull();
  });
});

describe("createNewlineCounter", () => {
  it("counts \\n escape pairs across fragments", () => {
    const c = createNewlineCounter();
    expect(c.push('{"content": "one\\ntwo\\nthree')).toBe(2);
    expect(c.push("tail")).toBe(2);
  });

  it("does not count an escaped backslash followed by n", () => {
    const c = createNewlineCounter();
    // `\\n` on the wire is the literal text "\n" in the file, not a newline.
    expect(c.push("text\\\\nmore")).toBe(0);
  });

  it("counts an escape split across two fragments once", () => {
    const c = createNewlineCounter();
    expect(c.push("one\\")).toBe(0); // trailing backslash held as carry
    expect(c.push("ntwo")).toBe(1);
  });
});

describe("previewLines", () => {
  it("unescapes the common escapes for display", () => {
    expect(previewLines('line1\\nline2\\tindented \\"q\\" \\\\ /', 10)).toEqual([
      'line1',
      'line2\tindented "q" \\ /',
    ]);
  });

  it("resolves \\uXXXX", () => {
    expect(previewLines("caf\\u00e9", 10)).toEqual(["café"]);
  });

  it("drops a trailing incomplete escape", () => {
    expect(previewLines("abc\\", 10)).toEqual(["abc"]);
    expect(previewLines("abc\\u12", 10)).toEqual(["abc"]);
    expect(previewLines("abc\\n", 10)).toEqual(["abc", ""]);
  });

  it("keeps unknown escapes verbatim", () => {
    expect(previewLines("a\\rb", 10)).toEqual(["a\\rb"]);
  });

  it("returns only the last maxLines lines", () => {
    const tail = Array.from({ length: 20 }, (_, i) => `l${i}`).join("\\n");
    const lines = previewLines(tail, 10);
    expect(lines).toHaveLength(10);
    expect(lines[0]).toBe("l10");
    expect(lines[9]).toBe("l19");
  });

  it("shows a partial first line rather than dropping it", () => {
    // The tail window was clipped mid-line: no newline before "gment".
    expect(previewLines("gment of a line\\nnext", 10)).toEqual(["gment of a line", "next"]);
  });
});
