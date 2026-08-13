import { describe, expect, it } from "vitest";
import { gitLabel, gitTooltip, isWarn, uncommitted } from "./gitBar";
import type { GitStatus } from "@/api/types";

// t echoes its key and params, so these assert structure rather than copy:
// rewording a string must not fail a test.
const t = (key: string, params?: Record<string, unknown>) =>
  params ? `${key}(${JSON.stringify(params)})` : key;

const clean = (over: Partial<GitStatus> = {}): GitStatus => ({
  repo: true,
  root: "/w",
  branch: "main",
  head: "abc1234",
  subject: "a commit",
  ahead: 0,
  behind: 0,
  staged: 0,
  unstaged: 0,
  untracked: 0,
  conflicted: 0,
  ...over,
});

describe("gitLabel", () => {
  it("shows the branch and says the tree is clean", () => {
    expect(gitLabel(clean(), t)).toBe("main · gitBar.clean");
  });

  it("omits zero divergence instead of printing arrows for it", () => {
    const label = gitLabel(clean({ upstream: "origin/main" }), t);
    expect(label).not.toContain("↑");
    expect(label).not.toContain("↓");
  });

  it("shows divergence when there is any", () => {
    expect(gitLabel(clean({ ahead: 2, behind: 1 }), t)).toContain("↑2 · ↓1");
  });

  // Untracked alone has to register. The backlog this feature exists because of
  // was almost entirely untracked, so a label that only watched staged and
  // unstaged would have kept reporting a clean tree throughout.
  it("counts untracked files as uncommitted", () => {
    const label = gitLabel(clean({ untracked: 120 }), t);
    expect(label).toContain('gitBar.uncommitted({"n":120})');
    expect(label).not.toContain("gitBar.clean");
  });

  it("totals every kind of uncommitted into one number", () => {
    expect(uncommitted(clean({ staged: 1, unstaged: 2, untracked: 3, conflicted: 4 }))).toBe(10);
  });

  it("names a detached HEAD instead of an empty branch", () => {
    const label = gitLabel(clean({ detached: true, branch: undefined }), t);
    expect(label).toContain("gitBar.detached");
  });

  it("distinguishes an unborn repository from a missing one", () => {
    expect(gitLabel(clean({ unborn: true, head: undefined }), t)).toContain("gitBar.noCommits");
    expect(gitLabel(clean({ repo: false }), t)).toBe("gitBar.noRepo");
  });

  it("reports why the state is unavailable, ahead of everything else", () => {
    const s = clean({ repo: false, unavailable: "git is not installed" });
    expect(gitLabel(s, t)).toBe('gitBar.unavailable({"reason":"git is not installed"})');
  });

  it("renders nothing before the first response", () => {
    expect(gitLabel(null, t)).toBe("");
  });
});

describe("gitTooltip", () => {
  it("carries the repository root, because it is not always the workspace", () => {
    expect(gitTooltip(clean({ root: "/parent" }), t)).toContain('gitBar.root({"root":"/parent"})');
  });

  it("carries the head commit with its subject", () => {
    expect(gitTooltip(clean(), t)).toContain("abc1234 a commit");
  });

  it("breaks the total down", () => {
    const tip = gitTooltip(clean({ staged: 1, untracked: 2 }), t);
    expect(tip).toContain('"staged":1');
    expect(tip).toContain('"untracked":2');
  });

  it("explains the consequence when there is no repository", () => {
    expect(gitTooltip(clean({ repo: false }), t)).toBe("gitBar.noRepoHint");
  });
});

describe("isWarn", () => {
  // Exactly one state is coloured: the one a person may not know they are in.
  it("warns only about the absence of version control", () => {
    expect(isWarn(clean({ repo: false }))).toBe(true);
    expect(isWarn(clean())).toBe(false);
    expect(isWarn(clean({ untracked: 500 }))).toBe(false);
    expect(isWarn(clean({ repo: false, unavailable: "timeout" }))).toBe(false);
    expect(isWarn(null)).toBe(false);
  });
});
