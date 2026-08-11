package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func skillFile(name, description, body string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
}

// --- frontmatter ---

func TestFrontmatterSubset(t *testing.T) {
	cases := []struct {
		label string
		in    string
		want  map[string]string
		body  string
	}{
		{
			label: "plain scalars",
			in:    "---\nname: demo\ndescription: does a thing\n---\nbody\n",
			want:  map[string]string{"name": "demo", "description": "does a thing"},
			body:  "body\n",
		},
		{
			label: "quotes are stripped and escapes undone",
			in:    "---\nname: \"demo\"\ndescription: 'it''s fine'\nother: \"a \\\"b\\\"\"\n---\n",
			want:  map[string]string{"name": "demo", "description": "it's fine", "other": `a "b"`},
		},
		{
			label: "folded block joins lines with spaces",
			in:    "---\nname: demo\ndescription: >\n  first part\n  second part\n---\nx\n",
			want:  map[string]string{"name": "demo", "description": "first part second part"},
			body:  "x\n",
		},
		{
			label: "literal block keeps newlines",
			in:    "---\ndescription: |\n  line one\n  line two\n---\n",
			want:  map[string]string{"description": "line one\nline two"},
		},
		{
			label: "CRLF is normalised",
			in:    "---\r\nname: demo\r\ndescription: d\r\n---\r\nbody\r\n",
			want:  map[string]string{"name": "demo", "description": "d"},
			body:  "body\n",
		},
		{
			label: "comments are ignored",
			in:    "---\n# a comment\nname: demo\ndescription: d # trailing\n---\n",
			want:  map[string]string{"name": "demo", "description": "d"},
		},
		// The important negative case: an indented key belongs to a nested
		// structure this parser skips, and must not surface as a top-level field.
		{
			label: "nested maps do not leak keys",
			in:    "---\nname: demo\nmetadata:\n  name: WRONG\n  version: 2\ndescription: d\n---\n",
			want:  map[string]string{"name": "demo", "metadata": "", "description": "d"},
		},
		{
			label: "no frontmatter at all",
			in:    "# just markdown\n",
			want:  map[string]string{},
			body:  "# just markdown\n",
		},
		{
			label: "unterminated block is treated as body",
			in:    "---\nname: demo\ndescription: d\n",
			want:  map[string]string{},
			body:  "---\nname: demo\ndescription: d\n",
		},
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			got, body := parseFrontmatter(c.in)
			for k, want := range c.want {
				if got[k] != want {
					t.Errorf("field %q: got %q, want %q", k, got[k], want)
				}
			}
			for k := range got {
				if _, ok := c.want[k]; !ok {
					t.Errorf("unexpected field %q = %q", k, got[k])
				}
			}
			if c.body != "" && strings.TrimSpace(body) != strings.TrimSpace(c.body) {
				t.Errorf("body: got %q, want %q", body, c.body)
			}
		})
	}
}

// --- discovery ---

// A directory holding SKILL.md is a skill root, not a directory to search: the
// examples and references a bundle ships would otherwise each become a skill.
func TestSkillRootStopsRecursion(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "outer/SKILL.md", skillFile("outer", "the real one", "body"))
	writeSkill(t, dir, "outer/examples/SKILL.md", skillFile("inner", "should not load", "body"))

	list, _ := Load(Options{UserDir: dir})
	if len(list) != 1 || list[0].Name != "outer" {
		t.Fatalf("got %+v, want only outer", Names(list))
	}
}

func TestDiscoveryRules(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "bundled/SKILL.md", skillFile("bundled", "a directory skill", "b"))
	writeSkill(t, dir, "solo.md", skillFile("solo", "a root-level file skill", "b"))
	writeSkill(t, dir, "nested/deeper/SKILL.md", skillFile("deep", "found by recursing", "b"))
	writeSkill(t, dir, ".hidden/SKILL.md", skillFile("hidden", "should be skipped", "b"))
	writeSkill(t, dir, "node_modules/pkg/SKILL.md", skillFile("vendored", "should be skipped", "b"))
	// A .md file below the root is documentation, not a skill.
	writeSkill(t, dir, "nested/notes.md", skillFile("notes", "should be skipped", "b"))

	list, _ := Load(Options{UserDir: dir})
	got := strings.Join(Names(list), ",")
	if got != "bundled,deep,solo" {
		t.Fatalf("got %q, want %q", got, "bundled,deep,solo")
	}
}

// User beats project. Both because it is what pi does and because the safe
// direction is that a cloned repository cannot shadow a skill you wrote.
func TestUserSkillWinsOverProject(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, user, "dup/SKILL.md", skillFile("dup", "from user", "b"))
	writeSkill(t, filepath.Join(project, ".pi-go", "skills"), "dup/SKILL.md", skillFile("dup", "from project", "b"))

	list, diags := Load(Options{UserDir: user, Cwd: project, IncludeProject: true})
	if len(list) != 1 {
		t.Fatalf("got %d skills, want 1", len(list))
	}
	if list[0].Description != "from user" || list[0].Source != "user" {
		t.Errorf("wrong winner: %+v", list[0])
	}
	var collisions int
	for _, d := range diags {
		if d.Kind == "collision" {
			collisions++
		}
	}
	if collisions != 1 {
		t.Errorf("got %d collision diagnostics, want 1", collisions)
	}
}

// Project skills are off unless asked for, because a skill is a file that
// rewrites the system prompt.
func TestProjectSkillsAreOptIn(t *testing.T) {
	user := t.TempDir()
	project := t.TempDir()
	writeSkill(t, filepath.Join(project, ".pi-go", "skills"), "p/SKILL.md", skillFile("p", "project skill", "b"))

	if list, _ := Load(Options{UserDir: user, Cwd: project}); len(list) != 0 {
		t.Fatalf("project skills loaded without opt-in: %v", Names(list))
	}
	if list, _ := Load(Options{UserDir: user, Cwd: project, IncludeProject: true}); len(list) != 1 {
		t.Fatalf("project skills did not load with opt-in: %v", Names(list))
	}
}

// An explicit --skill is a request, not a default, so -no-skills must not cancel
// it.
func TestExplicitPathsSurviveDisabled(t *testing.T) {
	user := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, user, "u/SKILL.md", skillFile("u", "default location", "b"))
	writeSkill(t, extra, "e/SKILL.md", skillFile("e", "explicit path", "b"))

	list, _ := Load(Options{UserDir: user, Disabled: true, Paths: []string{extra}})
	if len(list) != 1 || list[0].Name != "e" || list[0].Source != "path" {
		t.Fatalf("got %+v", list)
	}
}

func TestSymlinkedSkillLoadsOnce(t *testing.T) {
	dir := t.TempDir()
	real := t.TempDir()
	writeSkill(t, real, "demo/SKILL.md", skillFile("demo", "d", "b"))
	if err := os.Symlink(filepath.Join(real, "demo"), filepath.Join(dir, "demo")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	list, diags := Load(Options{UserDir: dir, Paths: []string{filepath.Join(real, "demo")}})
	if len(list) != 1 {
		t.Fatalf("got %d skills, want 1", len(list))
	}
	for _, d := range diags {
		if d.Kind == "collision" {
			t.Errorf("the same file reached twice should not collide with itself: %v", d)
		}
	}
}

// --- validation ---

func TestDescriptionIsRequiredButNameIsOnlyAdvisory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "nodesc/SKILL.md", "---\nname: nodesc\n---\nbody\n")
	writeSkill(t, dir, "Bad--Name/SKILL.md", skillFile("Bad--Name", "still usable", "b"))

	list, diags := Load(Options{UserDir: dir})
	if len(list) != 1 || list[0].Name != "Bad--Name" {
		t.Fatalf("got %v, want the badly named skill to load and the undescribed one not to", Names(list))
	}
	// Asserting the specific complaints rather than a count: the count changes
	// whenever a rule is added, while these three are the behaviour under test.
	joined := ""
	for _, d := range diags {
		joined += d.Kind + ":" + d.Message + "\n"
	}
	for _, want := range []string{
		"description is required",
		"invalid characters",
		"consecutive hyphens",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing diagnostic %q in:\n%s", want, joined)
		}
	}
}

// A name is inferred from the directory when frontmatter omits it, which is what
// makes a minimal SKILL.md possible.
func TestNameFallsBackToDirectory(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "inferred/SKILL.md", "---\ndescription: d\n---\nbody\n")

	list, _ := Load(Options{UserDir: dir})
	if len(list) != 1 || list[0].Name != "inferred" {
		t.Fatalf("got %v", Names(list))
	}
}

func TestOversizedBodyWarns(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "big/SKILL.md", skillFile("big", "d", strings.Repeat("x", MaxBodyBytes+1)))

	list, diags := Load(Options{UserDir: dir})
	if len(list) != 1 {
		t.Fatalf("an oversized skill should still load")
	}
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("no truncation warning: %+v", diags)
	}
}

// --- prompt ---

func TestPromptSection(t *testing.T) {
	list := []Skill{
		{Name: "a", Description: "does <x> & \"y\"", Path: "/s/a/SKILL.md", Dir: "/s/a"},
		{Name: "hidden", Description: "manual only", Path: "/s/h/SKILL.md", DisableModelInvocation: true},
	}
	out := PromptSection(list)

	if strings.Contains(out, "hidden") {
		t.Error("disable-model-invocation skills must not reach the prompt")
	}
	// Unescaped angle brackets in a description would end the XML element early
	// and hand the model a malformed skill list.
	if !strings.Contains(out, "does &lt;x&gt; &amp; &quot;y&quot;") {
		t.Errorf("description was not XML-escaped: %s", out)
	}
	if !strings.Contains(out, "<location>/s/a/SKILL.md</location>") {
		t.Errorf("location missing: %s", out)
	}
	if PromptSection(nil) != "" {
		t.Error("an empty skill set must produce no section at all")
	}
	if PromptSection([]Skill{list[1]}) != "" {
		t.Error("a set of only hidden skills must produce no section")
	}
}

func TestInvocationStripsFrontmatterAndAppendsArgs(t *testing.T) {
	dir := t.TempDir()
	path := writeSkill(t, dir, "demo/SKILL.md", skillFile("demo", "d", "# Demo\n\nrun ./go.sh"))
	s := Skill{Name: "demo", Path: path, Dir: filepath.Dir(path)}

	got, err := Invocation(s, "extract page 3")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "description:") {
		t.Errorf("frontmatter leaked into the invocation: %s", got)
	}
	for _, want := range []string{
		`<skill name="demo"`, "References are relative to " + filepath.Dir(path),
		"run ./go.sh", "</skill>", "extract page 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in         string
		name, args string
		ok         bool
	}{
		{"/skill:pdf", "pdf", "", true},
		{"/skill:pdf extract page 3", "pdf", "extract page 3", true},
		{"/skill:", "", "", false},
		{"/skills", "", "", false},
		{"read the skill file", "", "", false},
	}
	for _, c := range cases {
		name, args, ok := ParseCommand(c.in)
		if ok != c.ok || name != c.name || args != c.args {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q, %v)", c.in, name, args, ok, c.name, c.args, c.ok)
		}
	}
}

// The badge must be derivable from the call arguments alone, because those are
// what the session file keeps. Relative paths and symlinks both have to resolve.
func TestMatchReadUsesArguments(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeSkill(t, root, "skills/demo/SKILL.md", skillFile("demo", "d", "b"))
	list := []Skill{{Name: "demo", Path: path, Dir: filepath.Dir(path)}}

	abs, _ := json.Marshal(map[string]string{"path": path})
	if s, ok := MatchRead(list, cwd, abs); !ok || s.Name != "demo" {
		t.Errorf("absolute path did not match: %v %v", s, ok)
	}

	rel, _ := json.Marshal(map[string]string{"path": "../skills/demo/SKILL.md"})
	if _, ok := MatchRead(list, cwd, rel); !ok {
		t.Error("a relative path did not match")
	}

	other, _ := json.Marshal(map[string]string{"path": "main.go"})
	if _, ok := MatchRead(list, cwd, other); ok {
		t.Error("an ordinary read must not be labelled as a skill load")
	}
	if _, ok := MatchRead(list, cwd, json.RawMessage(`{"command":"ls"}`)); ok {
		t.Error("arguments without a path must not match")
	}
}

func TestRootsAreTheSkillDirectories(t *testing.T) {
	list := []Skill{
		{Name: "a", Dir: "/s/a"},
		{Name: "b", Dir: "/s/b"},
		{Name: "c", Dir: "/s/a"}, // two skills, one directory
	}
	got := Roots(list)
	if len(got) != 2 {
		t.Fatalf("got %v, want two unique roots", got)
	}
}
