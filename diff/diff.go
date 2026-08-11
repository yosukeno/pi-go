// Package diff computes line-level differences.
//
// pi gets this from the npm `diff` package; Go has no equivalent in the standard
// library, so the algorithm lives here rather than pulling in a dependency and
// giving up pi-go's stdlib-only property. The implementation is Myers' O(ND)
// algorithm with the usual prefix/suffix trim, which is what makes it fast for
// the case that actually matters: a large file with a small edit.
package diff

// Kind describes what happened to a run of lines.
type Kind int

const (
	Equal Kind = iota
	Delete
	Insert
)

// Chunk is a maximal run of consecutive lines sharing one Kind. The shape
// mirrors the npm diff package's parts (value/added/removed) so the formatters
// port over from pi directly.
type Chunk struct {
	Kind  Kind
	Lines []string
}

// maxMyersLines caps the region handed to the quadratic-worst-case search.
// Beyond it, reporting the region as one wholesale replacement is far better
// than spending seconds and hundreds of megabytes to find a prettier diff that
// nobody will read anyway.
const maxMyersLines = 5000

// Lines diffs two slices of lines.
func Lines(a, b []string) []Chunk {
	// Trim the common prefix and suffix first. For an agent editing one function
	// in a 2000-line file this reduces the search space to a handful of lines.
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix &&
		a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	var out []Chunk
	add := func(k Kind, lines []string) {
		if len(lines) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1].Kind == k {
			out[n-1].Lines = append(out[n-1].Lines, lines...)
			return
		}
		out = append(out, Chunk{Kind: k, Lines: lines})
	}

	add(Equal, a[:prefix])
	switch {
	case len(midA) == 0 && len(midB) == 0:
		// nothing changed in the middle
	case len(midA) > maxMyersLines || len(midB) > maxMyersLines:
		add(Delete, midA)
		add(Insert, midB)
	default:
		for _, c := range myers(midA, midB) {
			add(c.Kind, c.Lines)
		}
	}
	add(Equal, a[len(a)-suffix:])
	return out
}

// myers walks the edit graph, recording one V vector per edit-distance step, and
// then backtracks through those snapshots to recover the script.
func myers(a, b []string) []Chunk {
	n, m := len(a), len(b)
	if n == 0 {
		return []Chunk{{Kind: Insert, Lines: b}}
	}
	if m == 0 {
		return []Chunk{{Kind: Delete, Lines: a}}
	}

	maxD := n + m
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, maxD+1)

	for d := 0; d <= maxD; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)

		for k := -d; k <= d; k += 2 {
			var x int
			// Move down (insert from b) when that reaches further right.
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, offset, d, k)
			}
		}
	}
	// Unreachable: an edit script of length n+m always exists.
	return []Chunk{{Kind: Delete, Lines: a}, {Kind: Insert, Lines: b}}
}

// backtrack replays the recorded V vectors from the end point to the origin,
// producing the edit script in reverse and then flipping it.
func backtrack(trace [][]int, a, b []string, offset, d, k int) []Chunk {
	type step struct {
		kind Kind
		line string
	}
	var rev []step

	// The end point is always the far corner; trace[d] holds the V vector as it
	// stood before step d, i.e. the state we came from.
	x, y := len(a), len(b)
	for ; d > 0; d-- {
		v := trace[d]
		var prevK int
		if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[offset+prevK]
		prevY := prevX - prevK

		// Diagonal moves are the matched lines between the two snake ends.
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, step{Equal, a[x]})
		}
		if x == prevX {
			y--
			rev = append(rev, step{Insert, b[y]})
		} else {
			x--
			rev = append(rev, step{Delete, a[x]})
		}
		k = prevK
	}
	// Leading diagonal run before the first edit.
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, step{Equal, a[x]})
	}

	var out []Chunk
	for i := len(rev) - 1; i >= 0; i-- {
		s := rev[i]
		if n := len(out); n > 0 && out[n-1].Kind == s.kind {
			out[n-1].Lines = append(out[n-1].Lines, s.line)
			continue
		}
		out = append(out, Chunk{Kind: s.kind, Lines: []string{s.line}})
	}
	return out
}
