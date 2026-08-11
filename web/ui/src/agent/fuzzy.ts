// Quick-open fuzzy matching: a subsequence matcher with two bonuses — runs of
// consecutive hits, and hits at a segment start (after `/_.-`). The same
// school as fzf/VSCode: nothing clever enough to surprise you.

/** fuzzyScore returns null for no match; higher is better otherwise. */
export function fuzzyScore(query: string, path: string): number | null {
  if (!query) return 0;
  const q = query.toLowerCase();
  const p = path.toLowerCase();
  let score = 0;
  let qi = 0;
  let streak = 0;
  for (let i = 0; i < p.length && qi < q.length; i++) {
    if (p[i] === q[qi]) {
      streak++;
      score += 1 + streak * 2;
      if (i === 0 || "/_.-".includes(p[i - 1])) score += 5;
      qi++;
    } else {
      streak = 0;
    }
  }
  if (qi < q.length) return null;
  // Shorter paths win ties: a match in a short name is more deliberate.
  return score - p.length * 0.01;
}

/** fuzzyFilter returns the best `limit` paths for the query, best first. */
export function fuzzyFilter(query: string, paths: string[], limit = 50): string[] {
  return paths
    .map((p) => ({ p, s: fuzzyScore(query, p) }))
    .filter((x): x is { p: string; s: number } => x.s !== null)
    .sort((a, b) => b.s - a.s || a.p.localeCompare(b.p))
    .slice(0, limit)
    .map((x) => x.p);
}
