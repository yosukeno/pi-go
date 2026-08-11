package tui

import (
	"regexp"
	"strings"
)

// sgrOnly filters terminal-bound text: SGR colour/style sequences survive at
// full strength, every other escape class — cursor motion, erasing, OSC — is
// dropped. A command's colours are information (a red error line), but a stray
// "\033[2J" or cursor-positioning sequence would tear the pinned dock apart,
// and no legitimate command output needs to move our cursor.
func sgrOnly(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		keep, n := scanEscape(s[i:])
		if keep {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// Link wraps text in an OSC 8 hyperlink, so a supporting terminal makes it
// clickable (Cmd/Ctrl-click). Non-terminals get the plain text — a piped
// transcript must stay byte-exact, same rule as the colours. Unsupporting
// terminals show the text undecorated.
func Link(uri, text string) string {
	if !interactive || uri == "" {
		return text
	}
	return "\x1b]8;;" + uri + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// FileURL turns an absolute path into a file:// URL for Link(). Only the
// bytes that break URL parsing or annoy picky terminals are encoded.
func FileURL(p string) string {
	return "file://" + urlEscaper.Replace(p)
}

var urlEscaper = strings.NewReplacer(
	"%", "%25", // first: every other sequence introduces one
	" ", "%20",
	"#", "%23",
	"?", "%3F",
)

// scanEscape measures one escape sequence at the start of s (s[0] is ESC) and
// reports whether it is an SGR sequence worth keeping. An unterminated
// sequence is dropped along with whatever bytes were scanned — printing half
// an escape would leak literal "[31" text.
func scanEscape(s string) (keep bool, n int) {
	if len(s) < 2 {
		return false, len(s) // lone ESC
	}
	switch s[1] {
	case '[': // CSI: params 0x30–0x3F, intermediates 0x20–0x2F, final 0x40–0x7E
		j := 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			return s[j] == 'm', j + 1
		}
		return false, j
	case ']': // OSC: window titles, hyperlinks — noise here. Runs to BEL or ST.
		j := 2
		for j < len(s) {
			if s[j] == 0x07 {
				return false, j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return false, j + 2
			}
			j++
		}
		return false, j
	default: // ESC + intermediates + final: charset selects and friends
		j := 1
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
			return false, j + 1
		}
		return false, j
	}
}

// --- Long-listing fallback colouring -------------------------------------
//
// `bash ls -la` through a pipe prints no colours: macOS's BSD ls refuses
// every forcing knob short of a `--color` flag (verified: CLICOLOR_FORCE and
// -G do nothing when stdout is a pipe), and injecting flags into the model's
// commands is not an option. So the renderer recognises long-listing lines
// and colours the name column itself, the way ls would have.

// lsLongRe matches perms (with a macOS `@`/`+` suffix), then seven fields —
// links, owner, group, size, and the three date columns — then the name,
// which is the rest of the line. Device lines (`8, 1` is two fields) split
// one column early; the name still ends up coloured, just starting at the
// year. A line that only almost matches costs nothing: it renders as before.
var lsLongRe = regexp.MustCompile(`^([bcdlps-][rwxstST-]{9}[@+]?)(?:\s+\S+){7}\s+(.+)$`)

// colorLongLine colours the name column of an `ls -l`-format line: sky blue
// directories, cyan symlinks, green executables — the colours ls itself uses
// on a terminal. A line that already carries an escape is left alone: the
// command coloured it itself and knows better than the heuristic.
func colorLongLine(line string) string {
	if strings.ContainsRune(line, 0x1b) {
		return line
	}
	m := lsLongRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	perms, name := m[1], m[2]
	colour := ""
	switch {
	case perms[0] == 'd':
		colour = DirBlue
	case perms[0] == 'l':
		colour = Cyan
	case perms[0] == '-' && strings.ContainsAny(perms, "xs"):
		colour = Green
	}
	if colour == "" {
		return line
	}
	return line[:len(line)-len(name)] + colour + name + Reset
}
