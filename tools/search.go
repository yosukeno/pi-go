package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxWalkEntries bounds how many filesystem entries one search visits.
//
// The limits on results are not enough on their own: a search that matches
// nothing still has to walk everything, and a stray `find` at the filesystem root
// would sit there for minutes with no output to show it was working. This turns
// that into a bounded walk with a note saying it was cut short.
const maxWalkEntries = 200_000

// prunedDirs are skipped by both search tools.
//
// Deliberately just these two rather than "every dotted directory": .git and
// node_modules are the ones that make a walk pathological, while .github,
// .pi-go and friends hold files a coding agent has real reason to search. No
// .gitignore support — that would need a parser and would surprise anyone
// searching for a build artefact on purpose.
var prunedDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// searchRoot resolves the directory a search starts from, applying the same
// read-only widening as read and ls.
func searchRoot(cwd, path string, roots []string) (string, error) {
	if path == "" {
		path = "."
	}
	dir, err := resolve(cwd, path, roots...)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		// Searching one file is a reasonable thing to ask for, so allow it rather
		// than insisting on a directory.
		return dir, nil
	}
	return dir, nil
}

// walkFiles visits regular files under root, pruning noisy directories and
// stopping at maxWalkEntries. visit returns false to stop early.
//
// Symlinks are not followed: WalkDir hands back the link itself, which keeps the
// walk finite without needing cycle detection.
func walkFiles(root string, visit func(path string, d fs.DirEntry) bool) (visited int, truncated bool) {
	// A single file as the root is walked as itself, which is what makes
	// "grep this one file" work without a special case at the call site.
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		if d, err := statDirEntry(root); err == nil {
			visit(root, d)
		}
		return 1, false
	}

	stopped := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is skipped rather than failing the search:
			// one permission-denied subtree should not hide every other match.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxWalkEntries {
			stopped = true
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != root && prunedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !visit(path, d) {
			return filepath.SkipAll
		}
		return nil
	})
	return visited, stopped
}

func statDirEntry(path string) (fs.DirEntry, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return fs.FileInfoToDirEntry(info), nil
}

// relTo renders a path for output: relative to base when it is underneath it,
// absolute otherwise. Relative paths are shorter and are what the model should
// pass to read next.
func relTo(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
