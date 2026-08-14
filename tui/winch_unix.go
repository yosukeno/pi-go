//go:build unix

package tui

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyResize subscribes sigc to terminal-resize notifications and returns the
// unsubscribe. SIGWINCH is the only signal the Dock cares about, and naming it
// here rather than at the call site is what keeps the watcher itself portable —
// signal.Notify with an empty signal list means *every* signal, so a platform
// without SIGWINCH cannot simply pass nothing.
func notifyResize(sigc chan os.Signal) (stop func()) {
	signal.Notify(sigc, syscall.SIGWINCH)
	return func() { signal.Stop(sigc) }
}
