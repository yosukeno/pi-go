package tools

import "sync"

// fileMutations serializes changes to the same file.
//
// It is package-global on purpose. A per-registry lock would let two sessions of
// the Web server edit one file concurrently and lose an update, and the whole
// point of the lock is that the filesystem is shared state no matter how many
// agents are running.
//
// This is the Go equivalent of pi's withFileMutationQueue.
var fileMutations = &pathLocks{m: map[string]*pathEntry{}}

type pathEntry struct {
	mu   sync.Mutex
	refs int
}

// pathLocks hands out one mutex per key, reference-counted so the map does not
// grow with every file a long-running session has ever touched.
type pathLocks struct {
	mu sync.Mutex
	m  map[string]*pathEntry
}

func (p *pathLocks) acquire(key string) *pathEntry {
	p.mu.Lock()
	e := p.m[key]
	if e == nil {
		e = &pathEntry{}
		p.m[key] = e
	}
	e.refs++
	p.mu.Unlock()

	// Taken outside p.mu so a slow holder never blocks lookups for other files.
	e.mu.Lock()
	return e
}

func (p *pathLocks) release(key string, e *pathEntry) {
	e.mu.Unlock()
	p.mu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(p.m, key)
	}
	p.mu.Unlock()
}

// withFileLock runs fn while holding the lock for path.
//
// The key is the canonical path, so `./a.go`, `a.go`, an absolute path, and a
// symlink to the same file all serialize against each other. Keying on the raw
// string instead would make the lock decorative.
func withFileLock[T any](path string, fn func() (T, error)) (T, error) {
	key := canonical(path) // does filesystem work; must happen before locking
	e := fileMutations.acquire(key)
	defer fileMutations.release(key, e)
	return fn()
}
