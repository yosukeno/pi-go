package web

// The workspace-changes API: the file journal's read face. Session changes
// (per-turn) are projected on the client from the event stream; these
// endpoints answer the question the client cannot — what does the whole
// workspace look like against the first-touch pre-images.

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/yosukeno/pi-go/diff"
	"github.com/yosukeno/pi-go/tools"
)

// maxDiffSides caps the content fed into the differ; beyond it the changes
// list still names the file and its stats, only the patch is withheld —
// limiting rendering, never navigation.
const maxDiffSides = 2 << 20

// maxPatchRows keeps a monster patch from wedging the tab.
const maxPatchRows = 5000

type workspaceChange struct {
	Path          string `json:"path"`
	Status        string `json:"status"` // added | modified | deleted
	Added         int    `json:"added"`
	Removed       int    `json:"removed"`
	FirstMS       int64  `json:"first_ms"`
	LastMS        int64  `json:"last_ms"`
	Sid           string `json:"sid"`
	BaseAvailable bool   `json:"base_available"`
}

// changeOf reads both sides of a journaled path and summarises the difference.
// The zero change ("" base, missing current, and friends) is reported with
// ok=false so the list stays free of noise.
func (s *Server) changeOf(e tools.JournalEntry) (workspaceChange, bool) {
	out := workspaceChange{
		Path:    e.Path,
		FirstMS: e.FirstMS,
		LastMS:  e.LastMS,
		Sid:     e.Sid,
	}
	base, ok := s.mgr.Journal().Base(e.Path)
	if !ok {
		base = nil
		e.NoBase = true
	}
	out.BaseAvailable = !e.NoBase

	abs, err := tools.Resolve(s.mgr.Cwd(), e.Path)
	if err != nil {
		return out, false // journaled paths are in-root by construction
	}
	current, readErr := os.ReadFile(abs)
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		if e.Created {
			return out, false // created and then deleted: a net zero
		}
		out.Status = "deleted"
	case readErr != nil:
		return out, false
	case e.Created:
		out.Status = "added"
	default:
		out.Status = "modified"
	}

	if out.BaseAvailable {
		if out.Status == "modified" && string(base) == string(current) {
			return out, false // a bash revert put the content back: not a change
		}
		out.Added, out.Removed = diff.Stat(string(base), string(current))
	}
	return out, true
}

func (s *Server) handleWorkspaceChanges(w http.ResponseWriter, r *http.Request) {
	out := make([]workspaceChange, 0)
	for _, e := range s.mgr.Journal().List() {
		if c, ok := s.changeOf(e); ok {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func (s *Server) handleWorkspaceDiff(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	e, ok := s.mgr.Journal().Entry(rel)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("not a journaled path: "+rel))
		return
	}
	c, ok := s.changeOf(e)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no net change for "+rel))
		return
	}
	resp := map[string]any{
		"path":           c.Path,
		"status":         c.Status,
		"added":          c.Added,
		"removed":        c.Removed,
		"base_available": c.BaseAvailable,
	}
	if !c.BaseAvailable {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	base, _ := s.mgr.Journal().Base(rel)
	abs, _ := tools.Resolve(s.mgr.Cwd(), rel)
	current, _ := os.ReadFile(abs) // missing reads as empty: a deleted file
	if len(base)+len(current) > maxDiffSides {
		resp["too_big"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	patch := diff.Unified(rel, string(base), string(current), diff.DefaultContext)
	if strings.Count(patch, "\n") > maxPatchRows {
		resp["too_big"] = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["patch"] = patch
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkspaceJournalClear(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Journal().Clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
