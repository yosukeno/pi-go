//go:build !unix

package tui

import "os"

// notifyResize subscribes to nothing on platforms with no SIGWINCH, so the
// channel never fires and the Dock keeps the size it probed at startup.
//
// The consequence is worth stating: resizing the window mid-run leaves the
// pinned rows drawn for the old width until the next run re-probes. That is a
// cosmetic degradation of a terminal UI, and the alternative — polling
// TermSize on a timer — would burn a syscall a second on every platform to fix
// the one where the whole terminal path barely applies, since the bash tool
// needs a Unix shell anyway.
//
// Deliberately *not* signal.Notify(sigc) with no signals: that subscribes to
// every signal, which would turn a Ctrl-C into a redraw.
func notifyResize(chan os.Signal) (stop func()) {
	return func() {}
}
