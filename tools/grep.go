package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const (
	// DefaultGrepLimit caps how many matching lines one search returns. Lower than
	// find's cap because each result is a whole line, not a path.
	DefaultGrepLimit = 100
	// maxGrepFileBytes skips files too large to be source. Reading a 200MB log
	// line by line to find nothing is time spent for no possible benefit.
	maxGrepFileBytes = 8 << 20
	// maxGrepLine truncates a single matching line. A minified bundle is one line
	// of several hundred KB, and one match should not fill the context.
	maxGrepLine = 400
)

// Grep searches file contents by regular expression.
//
// Like ls and find, it exists so that a read-only search does not have to go
// through bash and therefore the approval gate. It is also the only one of the
// three whose output shape (path:line:text) the model can feed straight back into
// read.
type Grep struct {
	Cwd string
	// Roots are extra directories this tool may search outside Cwd, already
	// canonical. See tools.CanonicalRoots.
	Roots []string
}

type grepArgs struct {
	Pattern string `json:"pattern" required:"true" description:"Go regular expression, e.g. 'func \\w+Handler' or '(?i)todo'"`
	Path    string `json:"path,omitempty" description:"File or directory to search (relative or absolute). Defaults to the working directory"`
	Include string `json:"include,omitempty" description:"Only search files whose name matches this glob, e.g. '*.go'"`
	Limit   int    `json:"limit,omitempty" description:"Maximum matching lines to return (default 100)"`
}

func (*Grep) Name() string { return "grep" }

func (*Grep) Description() string {
	return fmt.Sprintf(
		"Search file contents with a Go regular expression. Returns 'path:line:text' for each match, "+
			"capped at %d matches. Use '(?i)' at the start of the pattern for a case-insensitive search. "+
			"Binary files, files over %dMB, .git and node_modules are skipped.",
		DefaultGrepLimit, maxGrepFileBytes>>20)
}

// ExecutionMode is Parallel: searching is read-only and cannot interfere with a
// sibling call.
func (*Grep) ExecutionMode() ExecutionMode { return Parallel }

func (*Grep) InputSchema() map[string]any {
	return object([]string{"pattern"}, map[string]any{
		"pattern": prop("string", "Go regular expression, e.g. 'func \\w+Handler' or '(?i)todo'"),
		"path":    prop("string", "File or directory to search (relative or absolute). Defaults to the working directory"),
		"include": prop("string", "Only search files whose name matches this glob, e.g. '*.go'"),
		"limit":   prop("number", fmt.Sprintf("Maximum matching lines to return (default %d)", DefaultGrepLimit)),
	})
}

// Schema returns the JSON schema for grep tool using reflection.
func (*Grep) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(grepArgs{}))
}

func (t *Grep) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a grepArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if a.Pattern == "" {
		return Result{}, fmt.Errorf("pattern must not be empty")
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		// The compile error names the offending construct, which is more useful to
		// the model than "no matches found".
		return Result{}, fmt.Errorf("invalid pattern %q: %w", a.Pattern, err)
	}
	if a.Include != "" {
		if _, err := filepath.Match(a.Include, "probe"); err != nil {
			return Result{}, fmt.Errorf("invalid include %q: %w", a.Include, err)
		}
	}

	root, err := searchRoot(t.Cwd, a.Path, t.Roots)
	if err != nil {
		return Result{}, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = DefaultGrepLimit
	}

	var lines []string
	files, skippedBinary := 0, 0
	limited := false
	visited, walkCut := walkFiles(root, func(path string, d fs.DirEntry) bool {
		if ctx.Err() != nil {
			return false
		}
		if a.Include != "" {
			if ok, _ := filepath.Match(a.Include, d.Name()); !ok {
				return true
			}
		}
		if info, err := d.Info(); err == nil && info.Size() > maxGrepFileBytes {
			return true
		}

		found, binary := grepFile(path, re, limit-len(lines))
		if binary {
			skippedBinary++
			return true
		}
		if len(found) == 0 {
			return true
		}
		files++
		shown := relTo(t.Cwd, path)
		for _, m := range found {
			lines = append(lines, fmt.Sprintf("%s:%d:%s", shown, m.line, m.text))
			if len(lines) >= limit {
				limited = true
				return false
			}
		}
		return true
	})
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}

	details := GrepDetails{
		Pattern: a.Pattern, Path: a.Path, Include: a.Include,
		Matches: len(lines), Files: files, Scanned: visited,
		SkippedBinary: skippedBinary, Truncated: limited || walkCut, LimitHit: limited,
	}
	if len(lines) == 0 {
		return Result{
			Text:    fmt.Sprintf("No matches for %q (searched %d entries)", a.Pattern, visited),
			Details: details,
		}, nil
	}

	tr := TruncateHead(strings.Join(lines, "\n"))
	out := tr.Content
	if notes := searchNotes(limited, walkCut, tr.Truncated, limit, "matches"); notes != "" {
		out += "\n\n" + notes
	}
	details.Truncated = details.Truncated || tr.Truncated
	return Result{Text: out, Details: details}, nil
}

type grepMatch struct {
	line int
	text string
}

// grepFile scans one file, returning at most want matches. It reports binary
// files so the caller can count them rather than emit control characters into the
// conversation.
func grepFile(path string, re *regexp.Regexp, want int) (matches []grepMatch, binary bool) {
	if want <= 0 {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	// A NUL byte in the first block is the same heuristic grep uses, and it is
	// what keeps compiled binaries and images out of the output.
	head := make([]byte, 1024)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, true
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, false
	}

	sc := bufio.NewScanner(f)
	// A minified file can be one enormous line; without a bigger buffer the scan
	// stops at 64KB and the file is silently half-searched.
	sc.Buffer(make([]byte, 0, 64*1024), maxGrepFileBytes)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := sc.Text()
		if !re.MatchString(line) {
			continue
		}
		matches = append(matches, grepMatch{line: lineNo, text: clipLine(line)})
		if len(matches) >= want {
			break
		}
	}
	return matches, false
}

// clipLine bounds one matching line, counting runes so multi-byte text is not
// cut mid-character.
func clipLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if r := []rune(s); len(r) > maxGrepLine {
		return string(r[:maxGrepLine]) + "…"
	}
	return s
}
