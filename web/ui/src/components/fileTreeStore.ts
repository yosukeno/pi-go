import { reactive, ref } from "vue";
import type { FileEntry } from "@/api/types";

// Shared state for the recursive FileTree: listings and the expansion set live
// here rather than in component instances, so collapsing a directory neither
// refetches nor forgets what was expanded inside it on the way back down. The
// server fixes its cwd at startup, so nothing here needs to key by root.
export const treeCache = new Map<string, FileEntry[]>();
export const treeExpanded = reactive(new Set<string>());
export const treeLoadError = reactive(new Map<string, string>());

/** clearTreeCache drops every cached listing; a remounted root refetches the
 *  still-expanded directories, which is exactly the refresh semantics wanted. */
export function clearTreeCache() {
  treeCache.clear();
  treeLoadError.clear();
}

// Bumped when something outside the tree changes the filesystem (the workspace
// picker's mkdir, the agent stream's file-mutating tools). FilesPanel keys the
// root tree on it, so the tree remounts and refetches — clearing the cache
// alone would leave mounted nodes showing the stale listing they already hold.
export const treeEpoch = ref(0);

export function invalidateTree() {
  clearTreeCache();
  treeEpoch.value++;
}

// --- event-driven invalidation ----------------------------------------------

// The quick-open index is the one expensive listing (a full workspace walk),
// so it does not ride the tree's epoch: it is busted separately and only when
// a run ends — the next ⌘P then lazily refetches, and never opening ⌘P means
// the walk never happens.
export const indexEpoch = ref(0);

export function invalidateIndex() {
  indexEpoch.value++;
}

// TREE_INVALIDATE_MS coalesces a burst of file-mutating tool events into one
// refresh: a run writing ten files back-to-back must not remount the tree ten
// times. 500ms is below the threshold where the panel reads as stale, and a
// burst rarely outlasts it.
const TREE_INVALIDATE_MS = 500;
let treeInvalidateTimer: ReturnType<typeof setTimeout> | undefined;

/** scheduleTreeInvalidate refreshes the tree soon; bursts merge into one. */
export function scheduleTreeInvalidate() {
  clearTimeout(treeInvalidateTimer);
  treeInvalidateTimer = setTimeout(() => {
    treeInvalidateTimer = undefined;
    invalidateTree();
  }, TREE_INVALIDATE_MS);
}

/** flushTreeInvalidate refreshes now, cancelling any pending scheduled one. */
export function flushTreeInvalidate() {
  clearTimeout(treeInvalidateTimer);
  treeInvalidateTimer = undefined;
  invalidateTree();
}

// --- column sorting ----------------------------------------------------------

export type SortKey = "name" | "size" | "time";

// One sort order for the whole tree: the header lives at the root, but every
// directory level reads the same setting, the way a Finder window sorts every
// folder it shows. Persisted so the panel comes back the way it was left.
const SORT_KEY = "pi-go:files-sort";

export const treeSort = reactive(loadSort());

function loadSort(): { key: SortKey; asc: boolean } {
  try {
    const raw = JSON.parse(localStorage.getItem(SORT_KEY) ?? "{}");
    if (raw.key === "name" || raw.key === "size" || raw.key === "time") {
      return { key: raw.key, asc: raw.asc !== false };
    }
  } catch {
    // A corrupt preference — or a test runner with no localStorage — is worth
    // one reset, not a broken panel.
  }
  return { key: "name", asc: true };
}

// toggleSort is a header click: the active column flips direction; a new
// column takes its natural default — A→Z for names, largest-first for sizes,
// newest-first for times, since the extreme end is the interesting one.
export function toggleSort(key: SortKey) {
  if (treeSort.key === key) {
    treeSort.asc = !treeSort.asc;
  } else {
    treeSort.key = key;
    treeSort.asc = key === "name";
  }
  try {
    localStorage.setItem(SORT_KEY, JSON.stringify({ key: treeSort.key, asc: treeSort.asc }));
  } catch {
    // Sorting still works for the session; only the remembering is lost.
  }
}

// sortEntries orders one directory listing for the tree. Directories always
// lead — a tree row is an expand point first and an entry second — and the
// name comparison mirrors the server's (case-insensitive, case-sensitive
// tiebreak), so the default view is exactly what the API returned.
export function sortEntries(entries: FileEntry[], key: SortKey, asc: boolean): FileEntry[] {
  const by = (a: FileEntry, b: FileEntry): number => {
    if (key === "size") return a.size - b.size;
    if (key === "time") return a.mtime_ms - b.mtime_ms;
    return byName(a, b);
  };
  return [...entries].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    const d = by(a, b) || byName(a, b);
    return asc ? d : -d;
  });
}

function byName(a: FileEntry, b: FileEntry): number {
  const al = a.name.toLowerCase();
  const bl = b.name.toLowerCase();
  if (al !== bl) return al < bl ? -1 : 1;
  if (a.name === b.name) return 0;
  return a.name < b.name ? -1 : 1;
}
