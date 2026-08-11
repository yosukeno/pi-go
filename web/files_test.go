package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pi-go/llm"
)

// filesHarness builds the standard harness around an empty scripted model: the
// file API never talks to one, so any call failing the test below is a bug.
func filesHarness(t *testing.T) *harness {
	t.Helper()
	return newHarness(t, func(int) (llm.Response, error) {
		return llm.Response{}, errors.New("the file API must not call the model")
	})
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func getJSON(t *testing.T, h *harness, path string, want int) map[string]any {
	t.Helper()
	resp := h.do(http.MethodGet, path, "")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s: %d, want %d (%s)", path, resp.StatusCode, want, bytes.TrimSpace(body))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, body)
	}
	return out
}

func entryNames(t *testing.T, body map[string]any) (names []string, dirs []bool) {
	t.Helper()
	for _, e := range body["entries"].([]any) {
		m := e.(map[string]any)
		names = append(names, m["name"].(string))
		dirs = append(dirs, m["dir"].(bool))
	}
	return names, dirs
}

func TestFilesListsDirectoriesFirstAndSkipsGit(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	writeFixture(t, cwd, "zeta.txt", "z")
	writeFixture(t, cwd, "Beta.go", "package x")
	writeFixture(t, cwd, "alpha/note.md", "hi")
	writeFixture(t, cwd, ".git/HEAD", "ref: refs/heads/main")

	body := getJSON(t, h, "/api/files", http.StatusOK)
	names, dirs := entryNames(t, body)
	want := []string{"alpha", "Beta.go", "zeta.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v, want %v (dirs first, .git skipped)", names, want)
	}
	if !dirs[0] || dirs[1] || dirs[2] {
		t.Fatalf("dir flags = %v, want [true false false]", dirs)
	}
	if body["truncated"].(bool) {
		t.Fatal("three entries must not truncate")
	}
}

func TestFilesRejectsEscapes(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	outside := t.TempDir()
	writeFixture(t, outside, "secret.txt", "nope")
	if err := os.Symlink(outside, filepath.Join(cwd, "link-out")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/files?path=..",
		"/api/files?path=" + url.QueryEscape(outside),
		"/api/files?path=link-out",
		"/api/files/content?path=../main.go",
		"/api/files/content?path=link-out/secret.txt",
	} {
		resp := h.do(http.MethodGet, path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s: %d, want 400", path, resp.StatusCode)
		}
	}
}

func TestFilesContentTruncatesByLinesAndBytes(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()

	var many []string
	for i := 1; i <= maxContentLines+100; i++ {
		many = append(many, fmt.Sprintf("line %d", i))
	}
	writeFixture(t, cwd, "many.txt", strings.Join(many, "\n"))
	body := getJSON(t, h, "/api/files/content?path=many.txt", http.StatusOK)
	if !body["truncated"].(bool) || body["truncated_by"] != "lines" {
		t.Errorf("many.txt: truncated=%v by=%v, want lines truncation", body["truncated"], body["truncated_by"])
	}
	if got := strings.Count(body["text"].(string), "\n") + 1; got != maxContentLines {
		t.Errorf("many.txt: got %d lines, want %d", got, maxContentLines)
	}

	writeFixture(t, cwd, "wide.txt", strings.Repeat("x", maxContentBytes+4096))
	body = getJSON(t, h, "/api/files/content?path=wide.txt", http.StatusOK)
	if !body["truncated"].(bool) || body["truncated_by"] != "bytes" {
		t.Errorf("wide.txt: truncated=%v by=%v, want bytes truncation", body["truncated"], body["truncated_by"])
	}
}

func TestFilesContentDetectsBinary(t *testing.T) {
	h := filesHarness(t)
	bin := append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0}, 64)...)
	if err := os.WriteFile(filepath.Join(h.mgr.Cwd(), "a.zip"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	body := getJSON(t, h, "/api/files/content?path=a.zip", http.StatusOK)
	if body["binary"] != true {
		t.Fatalf("a.zip: binary=%v, want true", body["binary"])
	}
	if _, hasText := body["text"]; hasText {
		t.Fatal("binary response must not carry text")
	}
}

func TestFilesRawServesImagesOnly(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(cwd, "logo.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, cwd, "page.html", "<script>alert(1)</script>")

	resp := h.do(http.MethodGet, "/api/files/content?path=logo.png&raw=1", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw png: %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %s, want image/png", ct)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}

	resp = h.do(http.MethodGet, "/api/files/content?path=page.html&raw=1", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("raw html: %d, want 415 — serving it would hand the origin to a script", resp.StatusCode)
	}
}

func TestFilesIndexSkipsNoise(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	writeFixture(t, cwd, "a.go", "package x")
	writeFixture(t, cwd, "sub/z.go", "package x")
	writeFixture(t, cwd, "node_modules/leftpad/index.js", "x")
	writeFixture(t, cwd, "dist/bundle.js", "x")
	writeFixture(t, cwd, ".git/HEAD", "x")

	body := getJSON(t, h, "/api/files/index", http.StatusOK)
	paths := body["paths"].([]any)
	got := make([]string, 0, len(paths))
	for _, p := range paths {
		got = append(got, p.(string))
	}
	if strings.Join(got, ",") != "a.go,sub/z.go" {
		t.Fatalf("index = %v, want [a.go sub/z.go]", got)
	}
	if body["capped"].(bool) {
		t.Fatal("four files must not trip the cap")
	}
}

func TestFilesShapeErrors(t *testing.T) {
	h := filesHarness(t)
	writeFixture(t, h.mgr.Cwd(), "f.txt", "x")
	if err := os.MkdirAll(filepath.Join(h.mgr.Cwd(), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]int{
		"/api/files?path=f.txt":           http.StatusBadRequest, // a file is not a listing
		"/api/files?path=missing":         http.StatusNotFound,
		"/api/files/content?path=sub":     http.StatusBadRequest, // a directory is not content
		"/api/files/content?path=missing": http.StatusNotFound,
	} {
		resp := h.do(http.MethodGet, path, "")
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}

	// The API token gate covers the file endpoints too.
	resp := h.get("/api/files")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", resp.StatusCode)
	}
}

func TestFilesTruncatesHugeListings(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	for i := 0; i < maxListEntries+10; i++ {
		writeFixture(t, cwd, fmt.Sprintf("f%04d.txt", i), "x")
	}
	body := getJSON(t, h, "/api/files", http.StatusOK)
	if !body["truncated"].(bool) {
		t.Fatal("510 entries must truncate")
	}
	if got := len(body["entries"].([]any)); got != maxListEntries {
		t.Fatalf("entries = %d, want %d", got, maxListEntries)
	}
}

// TestFilesSaveConflictAndForce covers the save path: optimistic mtime
// concurrency, force override, and that a panel save is journaled like an
// agent edit.
func TestFilesSaveConflictAndForce(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	writeFixture(t, cwd, "note.txt", "v1\n")
	st, err := os.Stat(filepath.Join(cwd, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	mtime := st.ModTime().UnixMilli()

	put := func(body string) *http.Response {
		t.Helper()
		resp := h.do(http.MethodPut, "/api/files/content", body)
		return resp
	}
	readFile := func() string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(cwd, "note.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	// A stale base mtime is a conflict and writes nothing.
	resp := put(`{"path":"note.txt","text":"v2\n","base_mtime_ms":1}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale base: %d, want 409", resp.StatusCode)
	}
	if got := readFile(); got != "v1\n" {
		t.Fatalf("conflict must not write: %q", got)
	}

	// The correct mtime saves and reports the new one.
	resp = put(fmt.Sprintf(`{"path":"note.txt","text":"v2\n","base_mtime_ms":%d}`, mtime))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh save: %d, want 200", resp.StatusCode)
	}
	if got := readFile(); got != "v2\n" {
		t.Fatalf("saved content = %q, want v2", got)
	}

	// The journal saw it: the workspace view lists the file as modified.
	changes := getJSON(t, h, "/api/workspace/changes", http.StatusOK)["changes"].([]any)
	found := false
	for _, c := range changes {
		m := c.(map[string]any)
		if m["path"] == "note.txt" && m["status"] == "modified" {
			found = true
		}
	}
	if !found {
		t.Errorf("workspace changes after save = %v, want note.txt modified", changes)
	}

	// Stale again (the mtime moved), and force overrides it.
	resp = put(`{"path":"note.txt","text":"v3\n","base_mtime_ms":1}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("post-save stale base: %d, want 409", resp.StatusCode)
	}
	resp = put(`{"path":"note.txt","text":"v3\n","base_mtime_ms":1,"force":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("force save: %d, want 200", resp.StatusCode)
	}
	if got := readFile(); got != "v3\n" {
		t.Fatalf("force-saved content = %q, want v3", got)
	}
}

func TestFilesSaveRejects(t *testing.T) {
	h := filesHarness(t)
	cwd := h.mgr.Cwd()
	writeFixture(t, cwd, "bin.dat", "PK\x00\x00")
	writeFixture(t, cwd, "note2.txt", "x")

	cases := []struct {
		name string
		body string
		want int
	}{
		{"escape", `{"path":"../x.txt","text":"x","force":true}`, http.StatusBadRequest},
		{"absent", `{"path":"missing.txt","text":"x","force":true}`, http.StatusNotFound},
		{"binary on disk", `{"path":"bin.dat","text":"x","force":true}`, http.StatusBadRequest},
		{"NUL in text", `{"path":"note2.txt","text":"has\x00null","force":true}`, http.StatusBadRequest},
		{"legit force save", `{"path":"note2.txt","text":"plain","force":true}`, http.StatusOK},
	}
	for _, c := range cases {
		resp := h.do(http.MethodPut, "/api/files/content", c.body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: PUT %s: %d, want %d", c.name, c.body, resp.StatusCode, c.want)
		}
	}
}

// The workspace picker's "new folder" button: one level at a time, inside the
// root, with distinct statuses for "exists" and "no parent".
func TestFileMkdir(t *testing.T) {
	h := filesHarness(t)
	writeFixture(t, h.mgr.Cwd(), "sub/.keep", "")

	cases := []struct {
		name string
		body string
		want int
	}{
		{"at root", `{"path":"fresh"}`, http.StatusCreated},
		{"nested under existing", `{"path":"sub/inner"}`, http.StatusCreated},
		{"exists", `{"path":"sub"}`, http.StatusConflict},
		{"missing parent", `{"path":"nope/inner"}`, http.StatusNotFound},
		{"escape", `{"path":"../elsewhere"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		resp := h.do(http.MethodPost, "/api/files/mkdir", c.body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: POST mkdir %s: %d, want %d", c.name, c.body, resp.StatusCode, c.want)
		}
	}

	for _, dir := range []string{"fresh", "sub/inner"} {
		if fi, err := os.Stat(filepath.Join(h.mgr.Cwd(), dir)); err != nil || !fi.IsDir() {
			t.Errorf("%s was not created: %v", dir, err)
		}
	}
}
