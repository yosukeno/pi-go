import type { TodoStatus } from "@/api/types";

/**
 * MARKS is the glanceable half of a task list item.
 *
 * Marks rather than the status words. Five spelled-out statuses down the left
 * margin push every task into a ragged column and make the list harder to scan
 * than the plain numbered text the model reads — while the state of an item is
 * exactly the thing that is glanceable and the text is not.
 *
 * Shared by the inline card and the pinned bar, in its own module rather than
 * exported from one of the two components: the same list rendered with two
 * different symbol tables is a bug that nobody would think to look for.
 */
export const MARKS: Record<TodoStatus, string> = {
  pending: "○",
  in_progress: "▸",
  completed: "✓",
  cancelled: "–",
  blocked: "✗",
};

/** markOf falls back to the pending mark, so an unknown status still renders. */
export function markOf(status: TodoStatus): string {
  return MARKS[status] ?? MARKS.pending;
}
