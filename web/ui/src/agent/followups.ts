import type { StarterCard } from "@/api/types";
import type { TimelineItem } from "./timeline";

// Follow-up chips are the next step offered once a turn has finished.
//
// Two decisions shape this module, and both are about not being annoying:
//
//  1. Deterministic, not generated. Asking the model what to suggest costs a
//     call and delays the answer on every single turn, and the one thing a
//     follow-up must never do is make the response feel slower. A skill declares
//     its chips and the condition for them; matching is a substring search over
//     text already on screen.
//  2. Relevant or absent. Matching is against the *last* turn only, and no match
//     shows nothing. A fixed row after every turn would suggest decompiling
//     something when the user just asked about a family profile, and a
//     suggestion that is wrong half the time trains people to ignore the row.
export interface FollowupGroup {
  when: string[];
  chips: StarterCard[];
}

// HAYSTACK_LIMIT keeps matching cheap and bounded. Tool arguments can be a whole
// file's contents (a write call), and a `when` needle worth matching — a command
// name, a tool name — appears early or not at all.
const HAYSTACK_LIMIT = 4000;

/**
 * followupHaystack renders what the last turn did into one lowercased string:
 * the assistant's reply plus each tool call's name and arguments.
 *
 * Only the last turn, deliberately. Over the whole conversation, one appearance
 * of a command would keep matching for the rest of the session, so the chips
 * would stop describing what just happened.
 */
export function followupHaystack(items: TimelineItem[]): string {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.kind !== "turn") continue;
    const parts: string[] = [item.text];
    for (const call of item.calls) {
      parts.push(call.name);
      try {
        parts.push(JSON.stringify(call.args) ?? "");
      } catch {
        // Args that will not serialise (cycles) are simply not matchable.
      }
    }
    return parts.join("\n").slice(0, HAYSTACK_LIMIT).toLowerCase();
  }
  return "";
}

/**
 * matchFollowups returns the chips of the first group whose condition holds.
 *
 * First match rather than all matches: stacking two rows of chips would turn a
 * hint into a menu, and the file's order is the author's statement of priority.
 */
export function matchFollowups(groups: FollowupGroup[], haystack: string): StarterCard[] {
  if (!haystack) return [];
  for (const g of groups) {
    if (g.when.some((w) => w && haystack.includes(w.toLowerCase()))) return g.chips;
  }
  return [];
}
