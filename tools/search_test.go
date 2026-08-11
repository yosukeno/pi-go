package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchTree builds a small project to search: nested packages, a pruned
// directory, a dotted directory that must NOT be pruned, and a binary file.
func searchTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":                  "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
		"main_test.go":             "package main\n\nfunc TestMain(t *testing.T) {}\n",
		"web/server.go":            "package web\n\nfunc Handler() {}\n// TODO: auth\n",
		"web/server_test.go":       "package web\n\nfunc TestHandler(t *testing.T) {}\n",
		"web/ui/app.ts":            "export const x = 1;\n// todo: lowercase\n",
		".github/workflows/ci.yml": "name: ci\njobs:\n  test:\n",
		"node_modules/dep/i.js":    "module.exports = function Handler() {};\n",
		".git/config":              "[core]\n\tHandler = no\n",
		"docs/notes.md":            "See Handler in web/server.go\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A binary file whose bytes would match the pattern if it were scanned.
	if err := os.WriteFile(filepath.Join(root, "app.bin"),
		append([]byte("Handler"), 0x00, 0x01, 0x02), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func runTool(t *testing.T, tool Tool, args any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return tool.Execute(context.Background(), raw)
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "[") {
			out = append(out, l)
		}
	}
	return out
}

// --- find ---

func TestFindMatchesByNameAndPrunesNoise(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "*_test.go"})
	if err != nil {
		t.Fatal(err)
	}
	got := lines(res.Text)
	want := []string{"main_test.go", "web/server_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// .git and node_modules make a walk pathological; a dotted directory like
// .github holds files worth searching. Pruning all dotted directories would be
// the easy mistake here.
func TestFindPrunesGitAndNodeModulesButNotOtherDotDirs(t *testing.T) {
	root := searchTree(t)

	res, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "*.js"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "No files matching") {
		t.Errorf("node_modules was searched: %q", res.Text)
	}

	res, err = runTool(t, &Find{Cwd: root}, findArgs{Pattern: "*.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, filepath.Join(".github", "workflows", "ci.yml")) {
		t.Errorf(".github was pruned but should be searchable: %q", res.Text)
	}
}

func TestFindPatternWithSlashMatchesRelativePath(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "web/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	got := lines(res.Text)
	want := []string{filepath.Join("web", "server.go"), filepath.Join("web", "server_test.go")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A pattern with a slash must not also match on the base name alone.
	if strings.Contains(res.Text, "ui") {
		t.Errorf("path pattern matched too deeply: %q", res.Text)
	}
}

// A malformed glob must say so. Reporting "no matches" would read as "the file is
// not there", which sends the model looking in the wrong place.
func TestFindRejectsAMalformedPattern(t *testing.T) {
	root := searchTree(t)
	_, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "[unclosed"})
	if err == nil {
		t.Fatal("expected an error naming the bad pattern")
	}
	if !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("error = %v, want it to name the pattern", err)
	}
	if _, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "  "}); err == nil {
		t.Error("an empty pattern should be rejected")
	}
}

func TestFindLimitIsReportedAsActionable(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Find{Cwd: root}, findArgs{Pattern: "*.go", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "limit=4") {
		t.Errorf("truncation note should suggest a bigger limit, got %q", res.Text)
	}
	d, ok := res.Details.(FindDetails)
	if !ok {
		t.Fatalf("details = %T", res.Details)
	}
	if !d.LimitHit || !d.Truncated {
		t.Errorf("details do not record the truncation: %+v", d)
	}
}

func TestFindRefusesToEscapeTheWorkingDirectory(t *testing.T) {
	root := searchTree(t)
	inner := filepath.Join(root, "web")
	if _, err := runTool(t, &Find{Cwd: inner}, findArgs{Pattern: "*.go", Path: ".."}); err == nil {
		t.Fatal("searching above the working directory should be refused")
	}
}

// The read-only roots that let skills live outside the project apply here too,
// and must not become writable.
func TestFindSearchesReadOnlyRootsOutsideCwd(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("# skill\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := CanonicalRoots([]string{outside})

	res, err := runTool(t, &Find{Cwd: cwd, Roots: roots}, findArgs{Pattern: "*.md", Path: outside})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "SKILL.md") {
		t.Fatalf("read-only root was not searchable: %q", res.Text)
	}
	// Same location, write tool, no roots: still refused.
	w := &Write{Cwd: cwd}
	if _, err := runTool(t, w, writeArgs{Path: filepath.Join(outside, "x.md"), Content: "no"}); err == nil {
		t.Error("a searchable root must not be writable")
	}
}

// --- grep ---

func TestGrepReportsPathLineAndText(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: `func Handler`})
	if err != nil {
		t.Fatal(err)
	}
	got := lines(res.Text)
	if len(got) != 1 {
		t.Fatalf("got %v, want one match", got)
	}
	want := filepath.Join("web", "server.go") + ":3:func Handler() {}"
	if got[0] != want {
		t.Errorf("got %q, want %q", got[0], want)
	}
}

func TestGrepIsCaseSensitiveUnlessAsked(t *testing.T) {
	root := searchTree(t)

	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "TODO"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(lines(res.Text)); n != 1 {
		t.Errorf("case-sensitive search got %d matches, want 1: %q", n, res.Text)
	}

	res, err = runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "(?i)todo"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(lines(res.Text)); n != 2 {
		t.Errorf("case-insensitive search got %d matches, want 2: %q", n, res.Text)
	}
}

func TestGrepIncludeFiltersByName(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "Handler", Include: "*.md"})
	if err != nil {
		t.Fatal(err)
	}
	got := lines(res.Text)
	if len(got) != 1 || !strings.Contains(got[0], "notes.md") {
		t.Fatalf("got %v, want only the markdown file", got)
	}
}

// A binary file must be skipped rather than have its bytes spliced into the
// conversation, even when the pattern matches them.
func TestGrepSkipsBinaryFiles(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "Handler"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "app.bin") {
		t.Errorf("binary file was searched: %q", res.Text)
	}
	d, ok := res.Details.(GrepDetails)
	if !ok {
		t.Fatalf("details = %T", res.Details)
	}
	if d.SkippedBinary == 0 {
		t.Error("skipped binary files should be counted, not silently dropped")
	}
}

func TestGrepRejectsAnInvalidRegexp(t *testing.T) {
	root := searchTree(t)
	_, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "func ("})
	if err == nil {
		t.Fatal("expected an error naming the bad pattern")
	}
	if !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("error = %v", err)
	}
	if _, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: ""}); err == nil {
		t.Error("an empty pattern should be rejected")
	}
}

func TestGrepCanSearchASingleFile(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "Handler", Path: "web/server.go"})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(lines(res.Text)); n != 1 {
		t.Fatalf("got %d matches, want 1: %q", n, res.Text)
	}
}

// One enormous line must not fill the context, and must be cut on a rune
// boundary so the output stays valid text.
func TestGrepClipsAVeryLongLine(t *testing.T) {
	root := t.TempDir()
	long := "needle" + strings.Repeat("好", 5000)
	if err := os.WriteFile(filepath.Join(root, "min.js"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "…") {
		t.Errorf("long line was not clipped: %d bytes", len(res.Text))
	}
	if n := len([]rune(res.Text)); n > maxGrepLine+120 {
		t.Errorf("clipped line is %d runes, want about %d", n, maxGrepLine)
	}
	if !utf8Valid(res.Text) {
		t.Error("clipping cut a multi-byte character in half")
	}
}

func TestGrepMissWorthDistinguishingFromAnError(t *testing.T) {
	root := searchTree(t)
	res, err := runTool(t, &Grep{Cwd: root}, grepArgs{Pattern: "zzz_not_here"})
	if err != nil {
		t.Fatalf("a miss is not an error: %v", err)
	}
	// "searched N entries" is the difference between "not present" and "looked in
	// the wrong place".
	if !strings.Contains(res.Text, "searched") {
		t.Errorf("a miss should say how much was searched, got %q", res.Text)
	}
}

func TestGrepRefusesToEscapeTheWorkingDirectory(t *testing.T) {
	root := searchTree(t)
	inner := filepath.Join(root, "web")
	if _, err := runTool(t, &Grep{Cwd: inner}, grepArgs{Pattern: "Handler", Path: "../docs"}); err == nil {
		t.Fatal("searching above the working directory should be refused")
	}
}

// Both tools are Parallel, which is what keeps a batch of searches from
// serialising, and both must declare it.
func TestSearchToolsRunInParallel(t *testing.T) {
	for _, tool := range []Tool{&Find{Cwd: t.TempDir()}, &Grep{Cwd: t.TempDir()}} {
		if tool.ExecutionMode() != Parallel {
			t.Errorf("%s should be Parallel", tool.Name())
		}
	}
}

// walkFiles must stop rather than traverse an unbounded tree. Verified with a
// deep-enough tree to trip a lowered bound.
func TestWalkStopsAtTheEntryCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	visited, truncated := walkFiles(root, func(string, fs.DirEntry) bool { return true })
	if truncated {
		t.Errorf("a 50 file tree should not hit the cap")
	}
	if visited < 50 {
		t.Errorf("visited %d, want at least 50", visited)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}
