package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// DefaultLsLimit caps how many entries one listing returns. Same number pi uses.
//
// An entry cap rather than only a byte cap because the failure mode worth
// guarding is `ls node_modules`: tens of thousands of short names would pass a
// byte limit only after filling the context with noise.
const DefaultLsLimit = 500

// Ls lists one directory. It exists as its own tool rather than leaving the job
// to bash for two reasons: it is read-only, so it can skip the approval gate that
// bash rightly triggers, and its output is a list the UI can render instead of
// raw shell text.
type Ls struct {
	Cwd string
	// Roots are extra directories this tool may list outside Cwd, already
	// canonical. See tools.CanonicalRoots.
	Roots []string
}

type lsArgs struct {
	Path  string `json:"path,omitempty" description:"Directory to list (relative or absolute). Defaults to the working directory"`
	Limit int    `json:"limit,omitempty" description:"Maximum entries to return (default 500)"`
}

func (*Ls) Name() string { return "ls" }

func (*Ls) Description() string {
	return fmt.Sprintf(
		"List the contents of a directory, one level deep. Entries are sorted alphabetically and "+
			"directories carry a trailing '/'. Dotfiles are included. Truncated to %d entries or %dKB, "+
			"whichever is hit first.",
		DefaultLsLimit, MaxBytes/1024)
}

// ExecutionMode is Parallel: listing is pure I/O and cannot interfere with a
// sibling call.
func (*Ls) ExecutionMode() ExecutionMode { return Parallel }

func (*Ls) InputSchema() map[string]any {
	return object(nil, map[string]any{
		"path":  prop("string", "Directory to list (relative or absolute). Defaults to the working directory"),
		"limit": prop("number", fmt.Sprintf("Maximum entries to return (default %d)", DefaultLsLimit)),
	})
}

// Schema returns the JSON schema for ls tool using reflection.
func (*Ls) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(lsArgs{}))
}

func (t *Ls) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a lsArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if a.Path == "" {
		a.Path = "."
	}
	dir, err := resolve(t.Cwd, a.Path, t.Roots...)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return Result{}, err
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("not a directory: %s", a.Path)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return Result{}, err
	}

	limit := a.Limit
	if limit <= 0 {
		limit = DefaultLsLimit
	}

	// Case-insensitive so that a mixed-case directory reads the way a person
	// would list it, with a case-sensitive tiebreak to keep the order stable.
	sort.Slice(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
		if a == b {
			return entries[i].Name() < entries[j].Name()
		}
		return a < b
	})

	names := make([]string, 0, min(len(entries), limit))
	dirs, files := 0, 0
	limited := false
	for _, e := range entries {
		if len(names) >= limit {
			limited = true
			break
		}
		name := e.Name()
		// Follow symlinks for the type marker only. A link to a directory is a
		// directory as far as the next call is concerned, and a broken link is
		// still worth listing, so a failed stat is not a reason to skip it.
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir {
			name += "/"
			dirs++
		} else {
			files++
		}
		names = append(names, name)
	}

	if len(names) == 0 {
		return Result{
			Text:    "(empty directory)",
			Details: LsDetails{Path: a.Path, Entries: 0},
		}, nil
	}

	tr := TruncateHead(strings.Join(names, "\n"))
	out := tr.Content
	var notes []string
	if limited {
		notes = append(notes, fmt.Sprintf("%d entries limit reached, use limit=%d for more", limit, limit*2))
	}
	if tr.Truncated {
		notes = append(notes, formatSize(MaxBytes)+" limit reached")
	}
	if len(notes) > 0 {
		out += fmt.Sprintf("\n\n[%s]", strings.Join(notes, ". "))
	}

	return Result{Text: out, Details: LsDetails{
		Path:         a.Path,
		Entries:      tr.OutputLines,
		Dirs:         dirs,
		Files:        files,
		Total:        len(entries),
		Truncated:    limited || tr.Truncated,
		EntryLimited: limited,
	}}, nil
}
