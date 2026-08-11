package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangy/pi-go/llm"
)

// TestWorkspaceChangesEndpoints drives two real runs through the scripted
// model — an edit and a write — and reads the workspace view back over HTTP.
// The journal hook inside the tools is what must have fired.
func TestWorkspaceChangesEndpoints(t *testing.T) {
	var cwd string
	h := newHarness(t, func(n int) (llm.Response, error) {
		switch n {
		case 1:
			return toolTurn("edit", `{"path":"f.txt","edits":[{"oldText":"before","newText":"after"}]}`), nil
		case 2:
			return textTurn("edited"), nil
		case 3:
			return toolTurn("write", `{"path":"new.txt","content":"fresh\n"}`), nil
		default:
			return textTurn("wrote"), nil
		}
	}, func(c *Config) { cwd = c.Cwd })
	if err := os.WriteFile(filepath.Join(cwd, "f.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sid := h.createSession()
	h.setPolicy(sid, ModeAuto, 0)
	s1 := h.start(sid, "edit it")
	s1.wait(t, EvRunEnd)
	s1.close()
	s2 := h.start(sid, "write it")
	s2.wait(t, EvRunEnd)
	s2.close()

	byPath := func(body map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, c := range body["changes"].([]any) {
			m := c.(map[string]any)
			out[m["path"].(string)] = m
		}
		return out
	}

	changes := byPath(getJSON(t, h, "/api/workspace/changes", http.StatusOK))
	if len(changes) != 2 {
		t.Fatalf("changes = %v, want f.txt + new.txt", changes)
	}
	f := changes["f.txt"]
	if f["status"] != "modified" || f["added"].(float64) != 1 || f["removed"].(float64) != 1 {
		t.Errorf("f.txt = %v, want modified +1 -1", f)
	}
	if f["base_available"] != true || f["sid"] != sid {
		t.Errorf("f.txt base_available=%v sid=%v", f["base_available"], f["sid"])
	}
	if n := changes["new.txt"]; n["status"] != "added" {
		t.Errorf("new.txt = %v, want added", n)
	}

	d := getJSON(t, h, "/api/workspace/diff?path=f.txt", http.StatusOK)
	patch, _ := d["patch"].(string)
	if patch == "" || !containsAll(patch, "-before", "+after") {
		t.Errorf("diff patch = %q, want it to contain both sides", patch)
	}

	// A file deleted behind the agent's back reads as deleted, not missing.
	if err := os.Remove(filepath.Join(cwd, "f.txt")); err != nil {
		t.Fatal(err)
	}
	changes = byPath(getJSON(t, h, "/api/workspace/changes", http.StatusOK))
	if f := changes["f.txt"]; f["status"] != "deleted" {
		t.Errorf("after rm: f.txt = %v, want deleted", f)
	}

	// Unknown paths and cleared journals behave.
	resp := h.do(http.MethodGet, "/api/workspace/diff?path=never.txt", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: %d, want 404", resp.StatusCode)
	}
	h.post("/api/workspace/journal/clear", "", http.StatusOK)
	changes = byPath(getJSON(t, h, "/api/workspace/changes", http.StatusOK))
	if len(changes) != 0 {
		t.Errorf("after clear: %v, want empty", changes)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
