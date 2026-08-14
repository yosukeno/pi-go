package main

import (
	"io"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/tools"
)

// toolOptions is the fork point: one function decides what a top-level run gets and
// what a child gets, and the difference between them is the containment. Tested
// here rather than through a spawned process because the inputs are three
// environment variables and the output is a struct — going through a real child
// would test the pipes again and this not at all.
func TestToolOptionsPerSituation(t *testing.T) {
	cases := []struct {
		name     string
		depth    string
		readOnly string
		// want
		readOnlyOpt bool
		guard       bool
		subagent    bool
		todo        bool
	}{{
		// The case that must not change: a person at a terminal has unrestricted
		// bash and can delegate.
		name: "top level", subagent: true, todo: true,
	}, {
		// An edit child: bash is guarded so it cannot reach the main checkout
		// through git, and it cannot delegate further.
		name: "edit child", depth: "1", guard: true,
	}, {
		// An explore child: no bash to guard, and no subagent tool, so the
		// read-only promise cannot be escaped by delegating.
		name: "explore child", depth: "1", readOnly: "1", readOnlyOpt: true,
	}, {
		// Depth is absent but read-only is set. Should still be read-only: the two
		// markers are independent, and treating a missing depth as "top level" must
		// not silently hand write tools to a child.
		//
		// It does get the task list, and that is correct rather than an oversight:
		// the list is withheld by depth, not by write access, because what makes it
		// pointless in a child is having no reader — no compaction boundary to
		// survive and a parent card already showing the progress. A read-only
		// top-level session has both.
		name: "read-only without depth", readOnly: "1", readOnlyOpt: true, todo: true,
	}, {
		// Garbage depth reads as zero rather than failing, because a broken
		// environment must not make pi-go unusable at a terminal.
		name: "unparseable depth", depth: "not a number", subagent: true, todo: true,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(tools.EnvSubagentDepth, c.depth)
			t.Setenv(tools.EnvSubagentReadOnly, c.readOnly)
			o := toolOptions("/tmp/wd", "some-model", nil, []string{"/tmp/mem"})

			// Memory follows the task list exactly: top level only, withheld by not
			// granting the root rather than by refusing the call. The reason is the todo
			// one — a child lives for one run and never crosses the session boundary
			// memory exists for, so its notes have no reader, and what would be left is
			// a second writer for "what do I know about this project".
			if hasMemory := len(o.WriteRoots) > 0; hasMemory != c.todo {
				t.Errorf("WriteRoots granted = %v, want %v (same rule as the task list)",
					hasMemory, c.todo)
			}

			if o.ReadOnly != c.readOnlyOpt {
				t.Errorf("ReadOnly = %v, want %v", o.ReadOnly, c.readOnlyOpt)
			}
			if (o.Guard != nil) != c.guard {
				t.Errorf("Guard set = %v, want %v", o.Guard != nil, c.guard)
			}
			if (o.Subagent != nil) != c.subagent {
				t.Errorf("Subagent set = %v, want %v", o.Subagent != nil, c.subagent)
			}
			if o.Todo != c.todo {
				t.Errorf("Todo = %v, want %v", o.Todo, c.todo)
			}
			// Whatever the situation, a registry built from these options never
			// contains a tool that can change something unless write tools were
			// meant to be there.
			r := tools.New(o)
			for _, name := range []string{"write", "edit", "bash", "subagent"} {
				if _, ok := r.Get(name); ok && c.readOnlyOpt {
					t.Errorf("read-only options produced a registry containing %q", name)
				}
			}
			// Withheld structurally, the same way the subagent tool is. A child
			// that could see a "todo" tool could spend a turn on it, and the
			// registry is the only place able to make that impossible.
			if _, ok := r.Get("todo"); ok != c.todo {
				t.Errorf("registry has todo = %v, want %v", ok, c.todo)
			}
		})
	}
}

// The child is told what it is through the environment, not through a flag. A model
// can propose flags via the tool call it is making; it never writes the environment
// of a process its parent spawns. If these ever became flags, both the depth bound
// and the read-only promise would be things the model could argue with.
func TestChildMarkersComeFromTheEnvironment(t *testing.T) {
	t.Setenv(tools.EnvSubagentReadOnly, "1")
	if !readOnlyChild() {
		t.Error("readOnlyChild() = false with the marker set")
	}
	// Only an exact "1" counts. Anything else is a misconfiguration, and the safe
	// reading of a misconfiguration here is "not a read-only child", because the
	// parent that spawns one always sets it correctly — a stray value means this is
	// not a child at all.
	for _, v := range []string{"", "0", "true", "yes", "2"} {
		t.Setenv(tools.EnvSubagentReadOnly, v)
		if readOnlyChild() {
			t.Errorf("readOnlyChild() = true for %q", v)
		}
	}
	t.Setenv(tools.EnvSubagentDepth, "3")
	if got := subagentDepth(); got != 3 {
		t.Errorf("subagentDepth() = %d, want 3", got)
	}
	t.Setenv(tools.EnvSubagentDepth, "-1")
	if got := subagentDepth(); got != 0 {
		t.Errorf("subagentDepth() = %d for a negative value, want 0", got)
	}
}

// A child's turn limit is its own, not the parent's. A turn limit bounds one run,
// and a subagent is a different run: how many steps a delegated task needs has
// nothing to do with how much conversation the parent has left. Observed live —
// a parent at -max-turns 4 gave its child four turns for a whole task, and the
// child spent them reading files and returned nothing.
//
// Model is inherited and turns are not, which looks inconsistent until you ask what
// each one measures. A different model changes the answer; a different turn budget
// only changes how many steps are allowed to produce it.
func TestChildDoesNotInheritTheParentsTurnLimit(t *testing.T) {
	t.Setenv(tools.EnvSubagentDepth, "")
	t.Setenv(tools.EnvSubagentReadOnly, "")
	o := toolOptions("/tmp/wd", "parent-model", nil, nil)
	if o.Subagent == nil {
		t.Fatal("no subagent tool registered at the top level")
	}
	if o.Subagent.MaxTurns != 0 {
		t.Errorf("Subagent.MaxTurns = %d, want 0 so the child uses its own default",
			o.Subagent.MaxTurns)
	}
	// The model still is inherited, or an expensive parent would silently delegate
	// to whatever the default happens to be.
	if o.Subagent.Model != "parent-model" {
		t.Errorf("Subagent.Model = %q, want the parent's", o.Subagent.Model)
	}
}

// Shared isolation removes a safety property — a delegated run can touch the
// working directory, immediately and with nothing to review — and it is switched on
// by an environment variable, which is the least visible place a decision can live.
// So it has to announce itself, and a misspelling has to stop the process rather
// than fall back quietly to the safe mode: an operator who typed `shard` believes
// children share the workspace and would find commits instead.
func TestCheckSubagentEnvAnnouncesSharedAndRefusesTypos(t *testing.T) {
	t.Setenv(tools.EnvSubagentDepth, "")

	var notices strings.Builder
	t.Setenv(tools.EnvIsolation, tools.IsolationShared)
	if err := checkSubagentEnv(&notices); err != nil {
		t.Fatalf("shared isolation was rejected: %v", err)
	}
	for _, want := range []string{tools.EnvIsolation, "no isolated worktree", "cannot be reviewed"} {
		if !strings.Contains(notices.String(), want) {
			t.Errorf("notice %q is missing %q", notices.String(), want)
		}
	}

	// The default says nothing. A notice on every ordinary run is a notice nobody
	// reads by the time it matters.
	notices.Reset()
	t.Setenv(tools.EnvIsolation, "")
	if err := checkSubagentEnv(&notices); err != nil || notices.String() != "" {
		t.Errorf("default run: err = %v, notices = %q, want silence", err, notices.String())
	}

	// A child says nothing either: once per session, not once per delegation.
	notices.Reset()
	t.Setenv(tools.EnvIsolation, tools.IsolationShared)
	t.Setenv(tools.EnvSubagentDepth, "1")
	if err := checkSubagentEnv(&notices); err != nil || notices.String() != "" {
		t.Errorf("child process: err = %v, notices = %q, want silence", err, notices.String())
	}
	t.Setenv(tools.EnvSubagentDepth, "")

	// Both variables are refused loudly, and the refusal names the variable so the
	// reader knows which of the two to fix.
	for _, c := range []struct{ env, value string }{
		{tools.EnvIsolation, "shard"},
		{tools.EnvConcurrency, "lots"},
		{tools.EnvConcurrency, "0"},
	} {
		t.Setenv(tools.EnvIsolation, "")
		t.Setenv(tools.EnvConcurrency, "")
		t.Setenv(c.env, c.value)
		err := checkSubagentEnv(io.Discard)
		if err == nil {
			t.Errorf("%s=%q was accepted", c.env, c.value)
			continue
		}
		if !strings.Contains(err.Error(), c.env) {
			t.Errorf("%s=%q: err %v does not name the variable", c.env, c.value, err)
		}
	}
}
