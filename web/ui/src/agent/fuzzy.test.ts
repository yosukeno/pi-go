import { describe, expect, it } from "vitest";
import { fuzzyFilter, fuzzyScore } from "./fuzzy";

describe("fuzzyScore", () => {
  it("matches subsequences case-insensitively and rejects non-subsequences", () => {
    expect(fuzzyScore("mgo", "main.go")).not.toBeNull();
    expect(fuzzyScore("MGO", "main.go")).not.toBeNull();
    expect(fuzzyScore("omg", "main.go")).toBeNull(); // right letters, wrong order
    expect(fuzzyScore("xyz", "main.go")).toBeNull();
  });

  it("prefers consecutive and segment-start matches", () => {
    // "ansi" is a clean prefix run in ansi.go, scattered mid-word in the other.
    expect(fuzzyScore("ansi", "ansi.go")!).toBeGreaterThan(fuzzyScore("ansi", "xanxysxi.txt")!);
    // A hit after "/" beats the same hit buried mid-word.
    expect(fuzzyScore("main", "src/main.go")!).toBeGreaterThan(fuzzyScore("main", "src/domain.go")!);
  });

  it("prefers the shorter path on a tie", () => {
    expect(fuzzyScore("main", "main.go")!).toBeGreaterThan(fuzzyScore("main", "very/deep/nested/main.go")!);
  });
});

describe("fuzzyFilter", () => {
  it("sorts best-first, caps the list, and keeps alphabetical order among equals", () => {
    const paths = ["zz/main.go", "aa/main.go", "readme.md", "src/main_test.go"];
    const out = fuzzyFilter("main", paths, 10);
    expect(out).not.toContain("readme.md");
    // same-stem ties fall back to alphabetical: aa before zz, and the short
    // names beat the nested one.
    expect(out.indexOf("aa/main.go")).toBeLessThan(out.indexOf("zz/main.go"));
    expect(out.indexOf("aa/main.go")).toBeLessThan(out.indexOf("src/main_test.go"));
    expect(fuzzyFilter("main", paths, 1)).toHaveLength(1);
  });
});
