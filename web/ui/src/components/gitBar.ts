import type { GitStatus } from "@/api/types";

// The label and tooltip composition for GitBar.vue, extracted so it can be
// tested — the same split dockSheets.ts and followups.ts use, and for the same
// reason: this project's UI tests exercise logic modules rather than mounting
// components, so logic worth testing has to live outside the .vue.
//
// The translate function is a parameter rather than an import so the tests can
// pass one that echoes its key. Asserting on structure instead of on copy means
// rewording a string is not a test failure.
export type Translate = (key: string, params?: Record<string, unknown>) => string;

/** uncommitted totals every kind of "not committed", untracked included. */
export function uncommitted(s: GitStatus): number {
  return s.staged + s.unstaged + s.untracked + s.conflicted;
}

/**
 * gitLabel renders the one-line summary, or "" when there is nothing to show.
 *
 * Order is fixed: where you are, then how far you have drifted, then how much is
 * uncommitted. It reads left to right as the question a person actually asks.
 */
export function gitLabel(s: GitStatus | null, t: Translate): string {
  if (!s) return "";
  if (s.unavailable) return t("gitBar.unavailable", { reason: s.unavailable });
  if (!s.repo) return t("gitBar.noRepo");

  const bits: string[] = [s.detached ? t("gitBar.detached") : s.branch || t("gitBar.unknownBranch")];
  if (s.unborn) bits.push(t("gitBar.noCommits"));
  // Zero is omitted rather than shown: "↑0 ↓0" is noise, and its absence is
  // already the meaning of "in sync".
  if (s.ahead) bits.push(`↑${s.ahead}`);
  if (s.behind) bits.push(`↓${s.behind}`);
  const n = uncommitted(s);
  bits.push(n === 0 ? t("gitBar.clean") : t("gitBar.uncommitted", { n }));
  return bits.join(" · ");
}

/**
 * gitTooltip carries what does not fit on the line: which repository this
 * actually is, the head commit, and the breakdown behind the total.
 *
 * The root is here rather than nowhere because it is not always the workspace —
 * a session started one directory too high reports a parent repository's state,
 * and seeing the root is the only way to notice.
 */
export function gitTooltip(s: GitStatus | null, t: Translate): string {
  if (!s) return "";
  if (s.unavailable) return t("gitBar.unavailable", { reason: s.unavailable });
  if (!s.repo) return t("gitBar.noRepoHint");

  const lines: string[] = [];
  if (s.root) lines.push(t("gitBar.root", { root: s.root }));
  if (s.head) lines.push(`${s.head} ${s.subject ?? ""}`.trim());
  if (s.upstream) lines.push(t("gitBar.upstream", { upstream: s.upstream }));
  lines.push(
    t("gitBar.breakdown", {
      staged: s.staged,
      unstaged: s.unstaged,
      untracked: s.untracked,
      conflicted: s.conflicted,
    }),
  );
  return lines.join("\n");
}

/**
 * isWarn colours exactly one state: no version control at all. Everything else
 * is a normal condition a person chose, while this is the one they may not know
 * they are in — which is the whole reason this bar exists.
 */
export function isWarn(s: GitStatus | null): boolean {
  return s !== null && !s.unavailable && !s.repo;
}
