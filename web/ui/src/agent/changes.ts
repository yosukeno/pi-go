import {
  isEditDetails,
  isWriteDetails,
  type TimelineCall,
  type TimelineItem,
} from "./timeline";
import { incomingPath } from "./argsPreview";

// The session changes projection: what files did this session touch, grouped
// by the turn that touched them. Pure over the timeline — every fact here was
// already in the SSE stream or the reload snapshot, so the panel needs no
// endpoint of its own (web-ui-design.md §16, M3).

export interface FileChange {
  path: string;
  /** added = at least one write created it in this turn; modified otherwise. */
  status: "added" | "modified";
  added: number;
  removed: number;
  /** Every edit/write call on this file in this turn, in order. */
  calls: TimelineCall[];
}

export interface TurnChanges {
  turn: number;
  files: FileChange[];
}

/** collectChanges groups edit/write results by turn, then by path. A call
 *  without details never made a change (failed edits return none), so its
 *  absence from the list is correct, not a hole. */
export function collectChanges(items: TimelineItem[]): TurnChanges[] {
  const groups: TurnChanges[] = [];
  for (const item of items) {
    if (item.kind !== "turn") continue;
    const byPath = new Map<string, FileChange>();
    for (const call of item.calls) {
      const d = call.result?.details;
      let path = "";
      let created = false;
      let added = 0;
      let removed = 0;
      if (isEditDetails(d)) {
        path = d.path;
        added = d.added;
        removed = d.removed;
      } else if (isWriteDetails(d)) {
        path = d.path;
        created = d.created;
        added = d.added ?? 0;
        removed = d.removed ?? 0;
      } else {
        continue;
      }
      if (!path) continue;
      let fc = byPath.get(path);
      if (!fc) {
        fc = { path, status: created ? "added" : "modified", added: 0, removed: 0, calls: [] };
        byPath.set(path, fc);
      }
      if (created) fc.status = "added";
      fc.added += added;
      fc.removed += removed;
      fc.calls.push(call);
    }
    if (byPath.size) groups.push({ turn: item.index, files: [...byPath.values()] });
  }
  return groups;
}

/** changedPathCount dedupes across turns for the tab badge. */
export function changedPathCount(groups: TurnChanges[]): number {
  return new Set(groups.flatMap((g) => g.files.map((f) => f.path))).size;
}

export interface IncomingWrite {
  callId: string;
  name: string;
  /** null while the path argument's closing quote has not streamed in yet. */
  path: string | null;
  bytes: number;
  /** Content newlines streamed so far — the row's live +N. */
  lines: number;
}

// The file-mutation tools whose streaming arguments carry a path: the schema
// declares path first, so it normally closes inside the raw head window; a
// content-first model only puts it at the very end, inside the tail — hence
// incomingPath rather than a head-only extractPath.
const WRITE_TOOLS = new Set(["write", "edit"]);

/** collectIncomingWrites flattens the still-streaming write/edit argument
 *  previews across the timeline, in arrival order. An entry vanishes on its
 *  own once tool_start replaces it and the settled call takes over. */
export function collectIncomingWrites(items: TimelineItem[]): IncomingWrite[] {
  const out: IncomingWrite[] = [];
  for (const item of items) {
    if (item.kind !== "turn") continue;
    for (const inc of item.incoming ?? []) {
      if (!WRITE_TOOLS.has(inc.name)) continue;
      out.push({
        callId: inc.call_id,
        name: inc.name,
        path: incomingPath(inc),
        bytes: inc.bytes,
        lines: inc.lines ?? 0,
      });
    }
  }
  return out;
}
