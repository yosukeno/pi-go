// Package tui is the terminal front end: the renderer that consumes the agent
// loop's events (with the streaming markdown filter and the ANSI handling that
// keeps tool output from tearing the screen), the pinned status dock, and the
// interactive line editor. Everything here writes to stdout directly, and the
// colour variables below are blanked when stdout is not a terminal, so a piped
// transcript stays byte-exact. The JSON front end lives in main (jsonmode.go)
// and the browser UI in web — swapping one consumer for another is the whole
// reason the loop emits events instead of printing.
package tui
