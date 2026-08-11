package tui

import "os"

// Colours are variables, not constants, because they get blanked when stdout is
// redirected. Escape codes in a piped transcript are noise at best and corrupt
// whatever parses the output at worst.
var (
	Dim    = "\033[2m"
	Cyan   = "\033[36m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Bold   = "\033[1m"
	// DirBlue is sky blue (bold bright blue): a black & white transcript still
	// wants directories to stand out from files, and plain blue (34) reads as
	// navy on most terminal themes.
	DirBlue = "\033[1;94m"
	Reset   = "\033[0m"
)

// interactive reports whether stdout can be drawn on in place. It gates the
// dock and the markdown filter for the same reason the colours are blanked: an
// in-place rewrite makes sense on a terminal and is corruption in a file.
var interactive = true

func init() {
	if !IsCharDevice(os.Stdout) {
		SetPlain()
	}
}

// SetPlain blanks the colours and turns in-place drawing off. Two callers:
// init when stdout is not a terminal, and main under -mode json — escape codes
// belong to neither stream there, and colouring the diagnostics on stderr only
// makes them harder to grep.
func SetPlain() {
	Dim, Cyan, Red, Green, Yellow, Bold, DirBlue, Reset = "", "", "", "", "", "", "", ""
	interactive = false
}

// IsCharDevice reports whether f is a character device. A terminal always is, a
// pipe or a regular file never is. Note that /dev/null is one too, so this tells
// you "not redirected to a file or pipe" rather than "definitely a terminal";
// proving the latter needs an ioctl. Every caller here only needs the former.
func IsCharDevice(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
