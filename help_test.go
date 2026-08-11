package main

import (
	"testing"

	"github.com/yosukeno/pi-go/tui"
)

func TestResolveCommand(t *testing.T) {
	cases := []struct{ in, want string }{
		// A prefix resolves to the first command the completion list would show.
		{"/e", "/exit"},
		{"/ex", "/exit"},
		{"/q", "/quit"},
		{"/us", "/usage"},
		{"/mod", "/model"},
		// The first listed match wins when several commands share the prefix.
		{"/s", "/skills"},
		{"/st", "/strict"},
		// An exact name is never hijacked by an earlier prefix match.
		{"/model", "/model"},
		{"/skills", "/skills"},
		// Arguments survive the expansion.
		{"/mod glm-5.2", "/model glm-5.2"},
		// A bare slash is not a prefix, and /skill: belongs to the skill
		// parser, not to command resolution.
		{"/", "/"},
		{"/skill:foo bar", "/skill:foo bar"},
		// Typos and plain prompts pass through untouched.
		{"/nope", "/nope"},
		{"hello", "hello"},
		// A command that cannot be undone is not reached by a prefix. Nothing else
		// starts with "/c", so without the exemption these would all expand to
		// /compact and replace the conversation off two keystrokes. They fall through
		// to the caller's unknown-command branch instead.
		{"/c", "/c"},
		{"/co", "/co"},
		{"/compac", "/compac"},
		// Typed in full it resolves, which is the only way to reach it.
		{"/compact", "/compact"},
	}
	for _, c := range cases {
		if got := resolveCommand(c.in); got != c.want {
			t.Errorf("resolveCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The exemption is a property of the command, not a hard-coded name, so a second
// destructive command added later inherits it — and a command that loses the flag
// starts being reachable by prefix again, which this catches.
func TestOnlyUndoableCommandsExpandFromAPrefix(t *testing.T) {
	for _, c := range tui.Commands {
		if !c.NoAbbrev {
			continue
		}
		// Every proper prefix of it, from two characters up, must stay unresolved.
		for n := 2; n < len(c.Name); n++ {
			p := c.Name[:n]
			if got := resolveCommand(p); got != p {
				t.Errorf("%s: prefix %q expanded to %q; a command marked NoAbbrev must not be reachable that way",
					c.Name, p, got)
			}
		}
		if got := resolveCommand(c.Name); got != c.Name {
			t.Errorf("%s did not resolve when typed in full: got %q", c.Name, got)
		}
	}
}

// commandWord screens a one-shot prompt. The trap it exists to close: -p is the
// scripted entry point, and without this `-p /compact` sent the literal text to the
// model, which would answer it as a question inside the conversation it was meant
// to replace. The /skill: expansion beside it was fixed for the same reason.
func TestCommandWordScreensOneShotPrompts(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/compact", "/compact"},
		{"/usage", "/usage"},
		{"  /compact  ", "/compact"},
		// A prompt that merely begins with a slash is a prompt. This is the case that
		// makes exact-word matching necessary rather than a prefix check.
		{"/usr/local/bin is on PATH?", ""},
		{"/compacting is a word", ""},
		{"what does /compact do?", ""},
		{"/nope", ""},
		{"hello", ""},
		{"", ""},
		// No prefix expansion: a script has no completion list to have shown what a
		// guess would become, so a guess must not become anything.
		{"/comp", ""},
		{"/c", ""},
	} {
		if got := commandWord(c.in); got != c.want {
			t.Errorf("commandWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every interactive command is screened, not just the ones spelled out above — so a
// command added later cannot start leaking through -p as a literal prompt.
func TestEveryCommandIsScreenedFromOneShotPrompts(t *testing.T) {
	for _, c := range tui.Commands {
		if got := commandWord(c.Name); got != c.Name {
			t.Errorf("%s is not screened: commandWord returned %q", c.Name, got)
		}
	}
}
