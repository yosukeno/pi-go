import { describe, expect, it, vi, afterEach, beforeEach } from "vitest";
import type { FileEntry } from "@/api/types";
import {
  sortEntries,
  toggleSort,
  treeSort,
  invalidateTree,
  treeCache,
  treeEpoch,
  treeLoadError,
  indexEpoch,
  invalidateIndex,
  scheduleTreeInvalidate,
  flushTreeInvalidate,
} from "./fileTreeStore";

const f = (name: string, size: number, mtime: number, dir = false): FileEntry => ({
  name,
  dir,
  size,
  mtime_ms: mtime,
});

// One of everything out of order: two directories and three files whose name,
// size and time orders all disagree, so each key has real work to do.
const listing = () => [f("b.md", 10, 300), f("zdir", 0, 50, true), f("A.md", 30, 100), f("adir", 0, 400, true), f("c.md", 20, 200)];

const names = (es: FileEntry[]) => es.map((e) => e.name).join(",");

describe("sortEntries", () => {
  it("sorts by name case-insensitively, directories first", () => {
    expect(names(sortEntries(listing(), "name", true))).toBe("adir,zdir,A.md,b.md,c.md");
  });

  it("sorts by size and by time, still directories first", () => {
    expect(names(sortEntries(listing(), "size", true))).toBe("adir,zdir,b.md,c.md,A.md");
    expect(names(sortEntries(listing(), "time", true))).toBe("zdir,adir,A.md,c.md,b.md");
  });

  it("descending reverses within each group, not the grouping", () => {
    expect(names(sortEntries(listing(), "size", false))).toBe("zdir,adir,A.md,c.md,b.md");
    expect(names(sortEntries(listing(), "time", false))).toBe("adir,zdir,b.md,c.md,A.md");
  });

  it("does not mutate the input", () => {
    const input = listing();
    const before = names(input);
    sortEntries(input, "size", false);
    expect(names(input)).toBe(before);
  });
});

describe("toggleSort", () => {
  it("starts a new column on its natural direction and flips the active one", () => {
    treeSort.key = "name";
    treeSort.asc = true;

    toggleSort("name"); // active column flips
    expect(treeSort).toMatchObject({ key: "name", asc: false });

    toggleSort("size"); // new column: largest first
    expect(treeSort).toMatchObject({ key: "size", asc: false });

    toggleSort("time"); // new column: newest first
    expect(treeSort).toMatchObject({ key: "time", asc: false });

    toggleSort("name"); // back to names: A to Z
    expect(treeSort).toMatchObject({ key: "name", asc: true });
  });
});

describe("invalidateTree", () => {
  it("clears cached listings and errors, and bumps the remount epoch", () => {
    treeCache.set("", [f("stale.md", 1, 1)]);
    treeLoadError.set("sub", "boom");
    const before = treeEpoch.value;

    invalidateTree();

    expect(treeCache.size).toBe(0);
    expect(treeLoadError.size).toBe(0);
    expect(treeEpoch.value).toBe(before + 1);
  });
});

describe("scheduleTreeInvalidate", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it("merges a burst into one debounced refresh", () => {
    const before = treeEpoch.value;

    scheduleTreeInvalidate();
    scheduleTreeInvalidate();
    scheduleTreeInvalidate();
    expect(treeEpoch.value).toBe(before); // nothing yet — the burst is still open
    vi.advanceTimersByTime(499);
    expect(treeEpoch.value).toBe(before);
    vi.advanceTimersByTime(1);
    expect(treeEpoch.value).toBe(before + 1); // one refresh for the whole burst
  });

  it("flushTreeInvalidate refreshes immediately and cancels the pending one", () => {
    const before = treeEpoch.value;

    scheduleTreeInvalidate();
    flushTreeInvalidate();
    expect(treeEpoch.value).toBe(before + 1);
    vi.advanceTimersByTime(1000);
    expect(treeEpoch.value).toBe(before + 1); // the scheduled fire was cancelled
  });
});

describe("invalidateIndex", () => {
  it("bumps only the quick-open epoch, not the tree's", () => {
    const t = treeEpoch.value;
    const i = indexEpoch.value;

    invalidateIndex();

    expect(indexEpoch.value).toBe(i + 1);
    expect(treeEpoch.value).toBe(t);
  });
});
