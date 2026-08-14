package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// SystemPrompt builds the system prompt: a short role statement plus the
// environment facts the model cannot discover without spending a tool call.
// Kept deliberately short. Long prompts crowd out the actual task. Both system
// prompts and tool schemas cost tokens on every single turn. Tool schemas are
// ~5.8x larger than system prompts and should be optimized first.
//
// sections are appended verbatim, for things this package should not know how to
// build. skills.PromptSection is the first of them; keeping it a plain string is
// what keeps the agent package unaware that skills exist.
func SystemPrompt(cwd string, sections ...string) string {
	var b strings.Builder
	b.WriteString(`You are pi-go, a coding agent operating in a terminal on the user's machine.

You work by calling tools. Read files before changing them, and prefer edit over
write so you touch only the lines that need to change. Verify your work with bash
(build, test, lint) when the project offers a way to do so.

Each tool call is a round trip, so do independent work in one: several files in a
single read with paths, and independent calls together in one message. Anything
whose arguments depend on another call's output has to wait for it.

Be concise. Answer in the user's language. When you have finished, state what
changed in a sentence or two rather than restating the whole diff.

A tool failure is information, not a dead end: read the error, adjust, retry.
Do not delete files, reset version control, or touch anything outside the working
directory unless the user asked for it. If you commit, stage the paths you
changed rather than everything, and never bypass hooks with --no-verify or
rewrite history with amend, reset or force-push.
`)
	// The batching paragraph is about the loop, which is why it is here and not in a
	// tool description: it is the one instruction that no single tool owns, and a
	// session's tools can all be present without anything telling the model that a
	// turn may carry more than one call. What belongs to a tool stays with the tool —
	// read's paths, and bash's non-persistent shell, are documented on those two and
	// nowhere else, so a session without them is never told about them.
	//
	// Costed rather than assumed, because this file's own header says tool schemas
	// are ~5.8x the system prompt and are what to optimise first: the paragraph is
	// 226 bytes on a prompt of ~1.1KB, against 4785 bytes of schemas. Three sentences
	// is the budget; the third one exists because "batch everything" is worse advice
	// than none — a call whose arguments come from another call's output cannot be
	// batched, and a model that tries produces a turn's worth of wrong arguments.
	//
	// Nothing here mentions the task list on purpose. That guidance lives in the
	// todo tool's own description, which is loaded only when the tool is registered
	// — so a subagent child, which does not get the tool, is never told to keep a
	// list it has no way to write. A line here would have to be made conditional on
	// the registry, which this function deliberately knows nothing about.
	fmt.Fprintf(&b, "\n<env>\nworking directory: %s\nplatform: %s/%s\ndate: %s\n",
		cwd, runtime.GOOS, runtime.GOARCH, time.Now().Format("2006-01-02"))
	if shell := os.Getenv("SHELL"); shell != "" {
		fmt.Fprintf(&b, "shell: %s\n", shell)
	}
	b.WriteString("</env>\n")

	// Project conventions, same idea as pi reading AGENTS.md.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n<%s>\n%s\n</%s>\n", name, strings.TrimSpace(string(data)), name)
		break
	}

	for _, s := range sections {
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString("\n" + s + "\n")
		}
	}
	return b.String()
}
