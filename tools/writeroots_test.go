package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The asymmetry between ReadRoots and WriteRoots is a security property, so it gets
// tested from both directions rather than once.
//
// ReadRoots exists for skill bundles: instructions the model must be able to read and
// must not be able to rewrite. WriteRoots exists for memory notes: the model's own
// records, which it has to be able to change. One mechanism, two grants, and the
// interesting failure is the one where the first quietly becomes the second.

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func callTool(t *testing.T, tool Tool, args any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), raw)
}

// find returns a registered tool by name.
func find(t *testing.T, r *Registry, name string) Tool {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %q is not registered", name)
	}
	return tool
}

// A skill bundle must stay readable and unwritable. This is the invariant that
// existed before memory, and the reason WriteRoots is a separate field rather than a
// flag on the existing one: no caller can widen a read root by passing a different
// boolean.
func TestReadRootsGrantNoWriteAccess(t *testing.T) {
	cwd, skill := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(skill, "SKILL.md"), "instructions")

	r := New(Options{Cwd: cwd, ReadRoots: []string{skill}})
	target := filepath.Join(skill, "SKILL.md")

	if _, err := callTool(t, find(t, r, "read"), map[string]any{"path": target}); err != nil {
		t.Errorf("read refused a skill file: %v", err)
	}
	if _, err := callTool(t, find(t, r, "write"), map[string]any{
		"path": target, "content": "rewritten",
	}); err == nil {
		t.Error("write succeeded inside a read-only root; a model that can rewrite its " +
			"own instructions has no instructions")
	}
	if _, err := callTool(t, find(t, r, "edit"), map[string]any{
		"path": target, "edits": []map[string]string{{"oldText": "instructions", "newText": "x"}},
	}); err == nil {
		t.Error("edit succeeded inside a read-only root")
	}
	// And the file is untouched, not merely the call refused.
	if got, _ := os.ReadFile(target); string(got) != "instructions" {
		t.Errorf("skill file is now %q; a refused write still changed it", got)
	}
}

// The memory grant, from the other side: writable and, necessarily, readable. A note
// the model cannot read back is not a note.
func TestWriteRootsGrantReadAndWrite(t *testing.T) {
	cwd, mem := t.TempDir(), t.TempDir()
	r := New(Options{Cwd: cwd, WriteRoots: []string{mem}})
	note := filepath.Join(mem, "conventions.md")

	if _, err := callTool(t, find(t, r, "write"), map[string]any{
		"path": note, "content": "this project uses pnpm\n",
	}); err != nil {
		t.Fatalf("write refused a memory note: %v", err)
	}
	if got, err := os.ReadFile(note); err != nil || !strings.Contains(string(got), "pnpm") {
		t.Fatalf("note content = %q, err = %v", got, err)
	}
	if _, err := callTool(t, find(t, r, "read"), map[string]any{"path": note}); err != nil {
		t.Errorf("read refused a memory note: %v", err)
	}
	if _, err := callTool(t, find(t, r, "edit"), map[string]any{
		"path": note, "edits": []map[string]string{{"oldText": "pnpm", "newText": "npm"}},
	}); err != nil {
		t.Errorf("edit refused a memory note: %v", err)
	}
	if _, err := callTool(t, find(t, r, "ls"), map[string]any{"path": mem}); err != nil {
		t.Errorf("ls refused a memory directory: %v", err)
	}
}

// A writable root is still a root, not an escape hatch. The canonicalising guard
// applies inside it exactly as it does to the working directory — otherwise memory
// would be a way to reach the whole filesystem through a relative path.
func TestWriteRootsStillRefuseEscape(t *testing.T) {
	cwd, mem := t.TempDir(), t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, outside, "not yours")

	r := New(Options{Cwd: cwd, WriteRoots: []string{mem}})

	for _, path := range []string{
		outside,
		filepath.Join(mem, "..", "escaped.txt"),
		filepath.Join(mem, "..", "..", "escaped.txt"),
	} {
		if _, err := callTool(t, find(t, r, "write"), map[string]any{
			"path": path, "content": "x",
		}); err == nil {
			t.Errorf("write escaped to %s", path)
		}
		if _, err := callTool(t, find(t, r, "read"), map[string]any{"path": path}); err == nil {
			t.Errorf("read escaped to %s", path)
		}
	}
	if got, _ := os.ReadFile(outside); string(got) != "not yours" {
		t.Errorf("a file outside every root was modified: %q", got)
	}
}

// With no extra roots at all, nothing changes. Every existing caller passes neither
// field, and the confinement they have always had must be byte-for-byte the same.
func TestNoExtraRootsIsUnchangedConfinement(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "f.txt")
	mustWrite(t, outside, "x")

	r := New(Options{Cwd: cwd})
	for _, name := range []string{"read", "write", "edit", "ls"} {
		args := map[string]any{"path": outside}
		if name == "write" {
			args["content"] = "y"
		}
		if name == "edit" {
			args["edits"] = []map[string]string{{"oldText": "x", "newText": "y"}}
		}
		if _, err := callTool(t, find(t, r, name), args); err == nil {
			t.Errorf("%s reached outside the working directory with no roots configured", name)
		}
	}
}

// Both sets reach the read tools, and only one reaches the write tools. Asserted on
// the tool structs rather than through behaviour so that a future refactor which
// swaps the two fields fails here with a clear reason.
func TestRootsAreDistributedAsymmetrically(t *testing.T) {
	cwd, skill, mem := t.TempDir(), t.TempDir(), t.TempDir()
	r := New(Options{Cwd: cwd, ReadRoots: []string{skill}, WriteRoots: []string{mem}})

	readable := find(t, r, "read").(*Read).Roots
	if len(readable) != 2 {
		t.Errorf("read has %d roots, want both the skill and the memory root: %v", len(readable), readable)
	}
	writable := find(t, r, "write").(*Write).Roots
	if len(writable) != 1 {
		t.Fatalf("write has %d roots, want only the memory root: %v", len(writable), writable)
	}
	if !strings.HasSuffix(writable[0], filepath.Base(mem)) {
		t.Errorf("write's root is %q, want the memory root %q", writable[0], mem)
	}
	if find(t, r, "edit").(*Edit).Roots[0] != writable[0] {
		t.Error("edit and write disagree about the writable roots")
	}
}

// A read-only session gets no write tools at all, so a writable root grants it
// nothing. Worth pinning because it is the structural half of the explore-subagent
// promise: the isolation is that the tools are absent, not that they refuse.
func TestReadOnlySessionHasNoWriteToolsToGrantRootsTo(t *testing.T) {
	cwd, mem := t.TempDir(), t.TempDir()
	r := New(Options{Cwd: cwd, WriteRoots: []string{mem}, ReadOnly: true})

	for _, name := range []string{"write", "edit", "bash"} {
		if _, ok := r.Get(name); ok {
			t.Errorf("read-only session registered %q", name)
		}
	}
	// The notes are still readable, which is the point of putting the roots on the
	// read tools rather than gating them behind ReadOnly.
	if _, err := callTool(t, find(t, r, "read"), map[string]any{"path": mem}); err == nil {
		// Reading a directory fails for its own reason; what matters is that it is not
		// refused as being outside the roots.
		t.Log("read of a directory succeeded, which is unexpected but not the property here")
	} else if strings.Contains(err.Error(), "outside") {
		t.Errorf("a read-only session cannot reach its memory root: %v", err)
	}
}

// bash is given neither set, and that is deliberate rather than an oversight: it never
// had a path restriction to widen. Documented as a test so nobody "fixes" it by adding
// roots to bash and concludes memory is protected from shell commands.
func TestBashIsGivenNoRootsBecauseItHasNoPathGuard(t *testing.T) {
	cwd, mem := t.TempDir(), t.TempDir()
	r := New(Options{Cwd: cwd, WriteRoots: []string{mem}})
	b, ok := find(t, r, "bash").(*Bash)
	if !ok {
		t.Fatal("bash is not a *Bash")
	}
	// There is no Roots field to check, so the assertion is that bash reaches the
	// memory directory anyway — via the shell, as it reaches everything else.
	out, err := callTool(t, b, map[string]any{
		"command": fmt.Sprintf("printf hi > %s/from-bash.txt", mem),
	})
	if err != nil {
		t.Fatalf("bash failed: %v (%s)", err, out.Text)
	}
	if _, err := os.Stat(filepath.Join(mem, "from-bash.txt")); err != nil {
		t.Errorf("bash could not write to the memory directory, which contradicts the "+
			"documented caveat that memory is not protected from bash: %v", err)
	}
}
