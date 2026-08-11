import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { hunkSide, parsePatch } from "./patch";

// The fixtures are byte-for-byte output of the Go diff.Unified generator, so
// the parser is tested against the real contract, not a hand-typed guess.
const fixture = (name: string) =>
  readFileSync(fileURLToPath(new URL(`__fixtures__/${name}`, import.meta.url)), "utf8");

describe("parsePatch", () => {
  it("parses a single-hunk patch with paired change rows", () => {
    const p = parsePatch(fixture("sample-one-hunk.patch"));
    expect(p).not.toBeNull();
    expect(p!.path).toBe("main.go");
    expect(p!.hunks).toHaveLength(1);

    const h = p!.hunks[0];
    expect(h).toMatchObject({ oldStart: 1, oldCount: 7, newStart: 1, newCount: 10 });

    // The one-line import pairs with the first line of the multi-line one.
    const change = h.rows.filter((r) => r.left.kind === "del" || r.right.kind === "add");
    expect(change[0].left.text).toBe('import "fmt"');
    expect(change[0].right.text).toBe("import (");
    // …and the shorter side is padded with empty cells.
    const pads = change.filter((r) => r.left.kind === "empty");
    expect(pads.length).toBeGreaterThan(0);
    expect(pads[0].right.kind).toBe("add");

    // Round-trip: each side reconstructs its half of the file segment.
    expect(hunkSide(h, "left")).toEqual([
      "package main",
      "",
      'import "fmt"',
      "",
      "func main() {",
      '\tfmt.Println("hello")',
      "}",
    ]);
    expect(hunkSide(h, "right")[2]).toBe("import (");
  });

  it("keeps distant changes in separate hunks with correct counters", () => {
    const p = parsePatch(fixture("sample-two-hunks.patch"));
    expect(p!.hunks).toHaveLength(2);
    expect(p!.hunks[0]).toMatchObject({ oldStart: 1, oldCount: 7, newCount: 7 });
    expect(p!.hunks[1]).toMatchObject({ oldStart: 23, oldCount: 8, newCount: 8 });

    for (const h of p!.hunks) {
      // Line-count integrity: left rows must account for oldCount, right for newCount.
      expect(hunkSide(h, "left")).toHaveLength(h.oldCount);
      expect(hunkSide(h, "right")).toHaveLength(h.newCount);
      // Line numbers run continuously from the hunk start.
      const leftNos = h.rows.map((r) => r.left.no).filter((n): n is number => n !== undefined);
      expect(leftNos[0]).toBe(h.oldStart);
      expect(leftNos[leftNos.length - 1]).toBe(h.oldStart + h.oldCount - 1);
    }
    expect(hunkSide(p!.hunks[1], "left")).toContain("line 27");
    expect(hunkSide(p!.hunks[1], "right")).toContain("line twenty-seven");
  });

  it("returns null for text that is not a patch", () => {
    expect(parsePatch("")).toBeNull();
    expect(parsePatch("Successfully replaced 1 block(s)")).toBeNull();
    expect(parsePatch("--- a/x\n+++ b/x\nno hunks here\n")).toBeNull();
  });
});
