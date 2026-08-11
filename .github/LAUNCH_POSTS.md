# Launch posts for pi-go

Ready-to-use posts for the initial release campaign. Target audience:
English-speaking developers. Post in this order on launch day.

## Recommended launch window

- **Hacker News**: Tue or Wed, 08:00–10:00 US Eastern (= Tue/Wed 21:00–23:00 Beijing time).
  Avoid Mon/Fri (low traffic) and weekends.
- **Reddit**: same morning, but **stagger 30–60 min after HN** so they don't cannibalize each other.
- **Twitter/X**: anytime, can fire in parallel with HN.

## Critical: first-hour engagement

- Reply to **every** comment in the first 2 hours. HN/Reddit ranking heavily weights early upvotes + comment velocity.
- Be genuinely responsive to criticism — defensive founders get downvoted.
- If someone reports a bug, **push a fix commit within 24h** and reply with the commit link. This signals "actively maintained" and is worth 100 stars.

---

## 1. Hacker News (Show HN)

**Title (≤80 chars, no clickbait, no marketing words):**
```
Show HN: I built a coding agent in Go with only 2 third-party dependencies
```

**Body (Show HN allows 2–3 short paragraphs from the submitter):**

```
Hi HN. pi-go is a terminal coding agent — one loop, nine tools, one protocol — that I wrote in Go to mirror the harness design of pi (https://github.com/earendil-works/pi).

The whole thing is ~43k lines of Go and depends on exactly two third-party packages: creack/pty and nhooyr.io/websocket. Both serve only the optional browser-terminal feature. Everything else — the agent loop, the LLM client, the TUI, the web UI backend, the worktree isolation, context editing, compaction — is pure stdlib. For comparison, opencode has 30 direct deps and aider has 86.

It's not trying to out-feature Claude Code. It's a study in how little you actually need: the README walks through why each tool exists, why compaction isn't automatic, why the subagent runs on a fresh git worktree, and where the context-cleaning shape was lifted from Anthropic's clear_tool_uses. Apache-2.0, works with Kimi and GLM out of the box.

Happy to dig into any of the design choices in the comments.
```

**Submit URL**: `https://github.com/yosukeno/pi-go`

**Why this works**: HN rewards "I built X with surprising constraint Y" + honest engineering framing. No superlatives, no "better than Z", concrete numbers, invites scrutiny.

---

## 2. Reddit — r/golang (20k+ subs, Go developers)

**Title:**
```
pi-go: a coding agent in ~43k lines of Go with 2 third-party deps (pure stdlib except the browser terminal)
```

**Body:**

```
I open-sourced a terminal coding agent I've been working on. The hook: it's ~43k lines of Go and the entire dependency tree is two packages — `creack/pty` and `nhooyr.io/websocket` — both of which only serve an optional browser terminal. The agent loop, LLM HTTP client, TUI, web UI backend, git worktree isolation, context editing, and compaction are all stdlib.

One loop, nine tools, one protocol. It's a harness reimagining of pi [0], not a Claude Code competitor.

A few things r/golang might find interesting:

- `-mode json` emits one event per line on stdout; everything human-readable goes to stderr so `| jq` just works.
- The `wire` package is the single source of truth for event names — the SSE stream (browser) and the JSONL stream (stdout) share one contract.
- `-context-edit auto` drops old tool output once the prompt exceeds 4/5 of the model window. The shape is lifted from Anthropic's `clear_tool_uses_20250919`, adapted in four spots.
- `-web` gives a browser UI with file panel, terminal, rewind/checkpoint, and per-session subagent cards.

Apache-2.0. Works with Kimi and GLM out of the box; bring your own API key via env var.

Repo: https://github.com/yosukeno/pi-go

Would love feedback from fellow Go devs, especially on the harness design and the context-editing shape.

[0] https://github.com/earendil-works/pi
```

**Why this works for r/golang**: Leads with line count + dep count (Go devs love stdlib purity), then lists 4 concrete technical things they can engage with. Ends asking for feedback, not stars.

---

## 3. Reddit — r/LocalLLaMA (300k+ subs, local/non-OpenAI model users)

**Title:**
```
pi-go: open-source terminal coding agent with first-class Kimi + GLM support (no OpenAI dependency)
```

**Body:**

```
Most open-source coding agents assume OpenAI/Anthropic. pi-go is a Go coding agent built around Kimi (for Coding plan) and GLM (Zhipu Coding Plan) as first-class providers — bring a key via env var and you're running.

- One loop, nine tools, one protocol. ~43k lines of Go, only 2 third-party deps (both for the optional browser terminal).
- Interactive TUI + `-web` browser UI (file panel, terminal, rewind, per-session subagent cards).
- Context editing, compaction, cost budget (`-cost-budget`), token budget, and chained runs (`-max-runs` + `-evaluate`) so a task can span multiple runs with handoff.
- Apache-2.0. Configs for providers live in `~/.pi-go/providers.json`; you can add your own endpoint and price.

Repo: https://github.com/yosukeno/pi-go

If you're on Kimi/GLM coding plans and want a terminal agent that treats them as the default rather than an afterthought, give it a try. Bug reports and provider configs welcome.
```

**Why this works for r/LocalLLaMA**: This sub is hungry for tools that treat non-OpenAI models as first-class. The whole framing is "built for your models, not theirs". Dep count is secondary here; model support is primary.

---

## 4. Twitter/X — thread (8 tweets, attach the comparison SVG to tweet 1)

**Tweet 1** (attach `assets/dependency-comparison.svg`):
```
I open-sourced pi-go, a coding agent written in Go.

~43,000 lines of code. Two third-party dependencies.

Both deps only serve an optional browser terminal. The rest — agent loop, LLM client, TUI, web UI, worktrees, context editing — is pure stdlib. 🧵
```

**Tweet 2:**
```
For comparison, other open-source coding agents carry far more:

pi-go:       2 deps
opencode:   30 deps
aider:      86 deps

This isn't a flex about hating dependencies. It's a study in how little you actually need to build a real coding agent.
```

**Tweet 3:**
```
The architecture is deliberately small:

• 1 loop
• 9 tools (bash, edit, write, read, glob, grep, todo, webfetch, subagent)
• 1 event protocol (shared by the TUI, the browser UI, and JSON stdout)

One contract, three front-ends.
```

**Tweet 4:**
```
The harness design is a reimagining of @earendil-works' pi — the same "loop + small tool set + auditable protocol" philosophy, rebuilt in Go with a browser UI and git-worktree isolation for parallel sessions.
```

**Tweet 5:**
```
Some engineering choices I'd defend:

• Compaction is manual (`/compact`), never automatic — you should decide when context gets lossy.
• Subagents run on a fresh git worktree; their commit is the audit record.
• `run_end.end_reason` distinguishes "this reply ended" from "this run ended".
```

**Tweet 6:**
```
It treats Kimi and GLM as first-class providers, not an afterthought. Bring an API key via env var, pick a model with `-model`, and you're running. No OpenAI requirement.

Provider configs (including price for `-cost-budget`) live in `~/.pi-go/providers.json`.
```

**Tweet 7:**
```
`-mode json` emits one structured event per line on stdout. Everything human-readable goes to stderr. So:

pi-go -mode json -p "..." | jq -c 'select(.type=="tool_start")'

just works. The same event contract drives the browser UI's SSE stream.
```

**Tweet 8:**
```
Apache-2.0, ~43k lines of Go, full bilingual README (中文 + English), deep docs on every design choice.

Repo: https://github.com/yosukeno/pi-go

Feedback welcome — especially from Go devs on the harness shape and the context-editing design.
```

---

## 5. Optional: dev.to / Medium long-form (post 1–2 weeks after launch)

Title template: **"How I built a coding agent in 43,000 lines of Go with 2 dependencies"**

This format travels well on dev.to and gets picked up by aggregators. Outline:
1. The constraint (2 deps) and why it's not just "hating dependencies"
2. The loop: one-turn-at-a-time, stop_reason vs end_reason
3. Context editing: lifting Anthropic's clear_tool_uses shape
4. Subagent on a worktree: why the commit is the audit record
5. Why compaction is manual
6. What I deliberately didn't build

---

## Accounts worth @-mentioning on Twitter (research before posting)

- `@golang` (official Go account)
- Aggregators that retweet OSS launches: search "gitremind", "ossified", "_benjamintd" etc. and check if they're still active before tagging.

Do **not** tag Anthropic/OpenAI/Cursor officials — that reads as piggybacking and gets ratioed.

---

## Anti-patterns (do NOT do these)

- ❌ Don't title any post "better than Claude Code / Cursor / Aider". Comparison invites hostility; let readers compare.
- ❌ Don't post on Monday, Friday, or weekend. HN/Reddit traffic is worst then.
- ❌ Don't use a brand-new Reddit account. Reddit auto-filters new accounts; use one with some history, or your post will be silently shadow-hidden.
- ❌ Don't crosspost the same title to HN and Reddit within 5 minutes — Google indexes both and it looks spammy. Stagger.
- ❌ Don't reply to criticism defensively. "Good point, I'll think about that" beats arguing every time.
- ❌ Don't buy stars or ask friends to mass-upvote HN/Reddit. Both platforms detect coordinated voting and shadowban.
