package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// DefaultFindLimit caps how many paths one search returns.
const DefaultFindLimit = 200

// Find locates files by name pattern.
//
// Its reason to exist is the same as ls's: it is read-only, so it does not have
// to go through the approval gate that bash rightly triggers. Without it,
// "where is the config for X" costs the user an approval click, and a gate that
// fires on harmless reads becomes a dialog people dismiss without reading.
type Find struct {
	Cwd string
	// Roots are extra directories this tool may search outside Cwd, already
	// canonical. See tools.CanonicalRoots.
	Roots []string
}

type findArgs struct {
	Pattern string `json:"pattern" required:"true" description:"Glob pattern, e.g. '*.go' or 'cmd/*/main.go'"`
	Path    string `json:"path,omitempty" description:"Directory to search under (relative or absolute). Defaults to the working directory"`
	Limit   int    `json:"limit,omitempty" description:"Maximum paths to return (default 200)"`
}

func (*Find) Name() string { return "find" }

func (*Find) Description() string {
	return fmt.Sprintf(
		"Find files by name using a glob pattern. A pattern without '/' matches the file name alone "+
			"(e.g. '*_test.go'); one containing '/' matches the path relative to the search directory "+
			"(e.g. 'web/*.go'). Results are sorted, capped at %d, and .git and node_modules are skipped.",
		DefaultFindLimit)
}

// ExecutionMode is Parallel: searching is read-only and cannot interfere with a
// sibling call.
func (*Find) ExecutionMode() ExecutionMode { return Parallel }

func (*Find) InputSchema() map[string]any {
	return object([]string{"pattern"}, map[string]any{
		"pattern": prop("string", "Glob pattern, e.g. '*.go' or 'cmd/*/main.go'"),
		"path":    prop("string", "Directory to search under (relative or absolute). Defaults to the working directory"),
		"limit":   prop("number", fmt.Sprintf("Maximum paths to return (default %d)", DefaultFindLimit)),
	})
}

// Schema returns the JSON schema for find tool using reflection.
func (*Find) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(findArgs{}))
}

func (t *Find) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a findArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return Result{}, fmt.Errorf("pattern must not be empty")
	}
	// Reject a bad glob up front. Otherwise filepath.Match returns its error on
	// every single entry and the search reports "no matches", which reads as "the
	// file is not there" rather than "your pattern is malformed".
	if _, err := filepath.Match(a.Pattern, "probe"); err != nil {
		return Result{}, fmt.Errorf("invalid pattern %q: %w", a.Pattern, err)
	}

	root, err := searchRoot(t.Cwd, a.Path, t.Roots)
	if err != nil {
		return Result{}, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = DefaultFindLimit
	}
	matchPath := strings.Contains(a.Pattern, string(filepath.Separator))

	var hits []string
	limited := false
	visited, walkCut := walkFiles(root, func(path string, d fs.DirEntry) bool {
		if ctx.Err() != nil {
			return false
		}
		subject := d.Name()
		if matchPath {
			subject = relTo(root, path)
		}
		if ok, _ := filepath.Match(a.Pattern, subject); !ok {
			return true
		}
		hits = append(hits, relTo(t.Cwd, path))
		if len(hits) >= limit {
			limited = true
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	details := FindDetails{
		Pattern: a.Pattern, Path: a.Path, Matches: len(hits),
		Scanned: visited, Truncated: limited || walkCut, LimitHit: limited,
	}
	if len(hits) == 0 {
		return Result{
			Text:    fmt.Sprintf("No files matching %q (scanned %d entries)", a.Pattern, visited),
			Details: details,
		}, nil
	}

	sort.Strings(hits)
	tr := TruncateHead(strings.Join(hits, "\n"))
	out := tr.Content
	if notes := searchNotes(limited, walkCut, tr.Truncated, limit, "paths"); notes != "" {
		out += "\n\n" + notes
	}
	details.Truncated = details.Truncated || tr.Truncated
	return Result{Text: out, Details: details}, nil
}

// searchNotes renders the "there is more than this" footer shared by find and
// grep. An unqualified truncation is worse than useless: the model needs to know
// whether it is looking at everything.
func searchNotes(limitHit, walkCut, byteCut bool, limit int, unit string) string {
	var notes []string
	if limitHit {
		notes = append(notes, fmt.Sprintf("%d %s limit reached, use limit=%d for more", limit, unit, limit*2))
	}
	if walkCut {
		notes = append(notes, fmt.Sprintf("stopped after %d entries, narrow the search path", maxWalkEntries))
	}
	if byteCut {
		notes = append(notes, formatSize(MaxBytes)+" limit reached")
	}
	if len(notes) == 0 {
		return ""
	}
	return "[" + strings.Join(notes, ". ") + "]"
}
