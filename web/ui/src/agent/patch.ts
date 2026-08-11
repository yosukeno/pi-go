// A unified-patch parser producing side-by-side aligned rows.
//
// Input is the Go diff package's Unified() output (git-applyable, covered by
// TestUnifiedPatchAppliesWithGit). Hunk field naming follows the Claude SDK's
// structuredPatch, so anyone who has seen that shape knows this one.
//
// The one alignment rule: inside a change block, deletions pair with the
// insertions that follow them by index, the shorter side padded with empty
// cells. It is the same rule every side-by-side view uses, and being simple
// matters more than being clever — a mis-paired row still shows both texts.

export interface DiffSide {
  kind: "ctx" | "del" | "add" | "empty";
  no?: number;
  text: string;
}

export interface DiffRow {
  left: DiffSide;
  right: DiffSide;
}

export interface DiffHunk {
  header: string;
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  rows: DiffRow[];
}

export interface ParsedPatch {
  path: string;
  hunks: DiffHunk[];
}

const hunkRe = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

/** parsePatch returns null for text that is not a patch (a created file's
 *  write has none), so callers can fall back to a plain note. */
export function parsePatch(patch: string): ParsedPatch | null {
  const lines = patch.split("\n");
  if (!lines[0]?.startsWith("--- a/") || !lines[1]?.startsWith("+++ b/")) return null;
  const path = lines[1].slice("+++ b/".length);

  const hunks: DiffHunk[] = [];
  let i = 2;
  while (i < lines.length) {
    const m = hunkRe.exec(lines[i]);
    if (!m) {
      i++;
      continue;
    }
    const hunk: DiffHunk = {
      header: lines[i],
      oldStart: Number(m[1]),
      oldCount: m[2] === undefined ? 1 : Number(m[2]),
      newStart: Number(m[3]),
      newCount: m[4] === undefined ? 1 : Number(m[4]),
      rows: [],
    };
    i++;

    let oldNo = hunk.oldStart;
    let newNo = hunk.newStart;
    let dels: DiffSide[] = [];
    let adds: DiffSide[] = [];
    const flushChange = () => {
      const n = Math.max(dels.length, adds.length);
      for (let k = 0; k < n; k++) {
        hunk.rows.push({
          left: dels[k] ?? { kind: "empty", text: "" },
          right: adds[k] ?? { kind: "empty", text: "" },
        });
      }
      dels = [];
      adds = [];
    };

    for (; i < lines.length; i++) {
      const ln = lines[i];
      if (ln.startsWith("@@")) break;
      // split("\n") leaves one empty element for the trailing newline.
      if (ln === "" && i === lines.length - 1) continue;
      const c = ln[0];
      if (c === "\\") continue; // "\ No newline" marker (our generator never emits one)
      if (c === "-") {
        dels.push({ kind: "del", no: oldNo++, text: ln.slice(1) });
        continue;
      }
      if (c === "+") {
        adds.push({ kind: "add", no: newNo++, text: ln.slice(1) });
        continue;
      }
      // A context line. A truly empty one is git's convention for a blank line.
      flushChange();
      const text = c === " " ? ln.slice(1) : "";
      hunk.rows.push({
        left: { kind: "ctx", no: oldNo++, text },
        right: { kind: "ctx", no: newNo++, text },
      });
    }
    flushChange();
    hunks.push(hunk);
  }
  return hunks.length ? { path, hunks } : null;
}

/** oldSide / newSide reconstruct what a hunk says each side reads — the
 *  round-trip property the tests lean on. */
export function hunkSide(hunk: DiffHunk, side: "left" | "right"): string[] {
  const keep = side === "left" ? ["ctx", "del"] : ["ctx", "add"];
  return hunk.rows.map((r) => r[side]).filter((s) => keep.includes(s.kind)).map((s) => s.text);
}
