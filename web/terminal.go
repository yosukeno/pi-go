package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// Terminal is one login shell on a pty, owned by a Session and spawned lazily
// the first time the panel attaches. It answers the obvious question — "the
// agent edits my files; let me poke at the same directory myself" — in the
// session's own workspace, under the same token gate as every other endpoint.
//
// The shell deliberately outlives the websocket: closing the tab or switching
// sessions detaches, and the next attach replays the backlog ring, so a
// half-finished `make` is still scrolling when you come back. The shell dies
// only with its session (evict/delete/server shutdown) or by the user typing
// exit — after which the next attach spawns a fresh one.
type Terminal struct {
	cmd *exec.Cmd
	ptm *os.File

	// wmu serializes pty writes: keystrokes and resizes can arrive while the
	// backlog replay is mid-flight on another goroutine.
	wmu sync.Mutex

	// mu guards the attachment and the size. The read loop and Kill touch
	// neither, so a slow client never blocks output.
	mu     sync.Mutex
	conn   *websocket.Conn
	cols   int
	rows   int
	exited bool
	// cmu serializes writes to the attached connection: the pump and a backlog
	// replay would otherwise interleave frames, which the library forbids.
	cmu sync.Mutex

	// backlog is the last backlogCap bytes of raw output, replayed to every
	// new attachment so a reconnecting client repaints its scrollback.
	backlogMu sync.Mutex
	backlog   []byte

	done chan struct{} // closed when the shell process exits
}

// backlogCap is how much output survives a detach. 256KB of terminal bytes is
// several screens of even a noisy build; beyond that the client has scrollback
// of its own anyway.
const backlogCap = 256 * 1024

// termMessage is the one frame shape in both directions. Data carries raw
// terminal bytes as a JSON string — UTF-8 safe, no base64 tax.
type termMessage struct {
	Type string `json:"type"` // "in" | "out" | "resize" | "exit"
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Code int    `json:"code,omitempty"`
}

// startTerminal spawns the shell in cwd. The shell is the user's own $SHELL,
// inherited environment and all — anything less would feel broken next to the
// terminal they already use.
func startTerminal(cwd string, cols, rows int) (*Terminal, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		// $SHELL is almost never set inside a container (no login shell sets it),
		// so a hardcoded fallback determines whether the panel opens at all. zsh is
		// a nice interactive shell but absent from stock Debian/Alpine — defaulting
		// to it made startTerminal fail with "no such file" and the panel could never
		// attach. bash is near-universal, and sh is the POSIX floor beneath it.
		if _, err := os.Stat("/bin/bash"); err == nil {
			shell = "/bin/bash"
		} else {
			shell = "/bin/sh"
		}
	}
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	ptm, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	t := &Terminal{
		cmd:  cmd,
		ptm:  ptm,
		cols: cols,
		rows: rows,
		done: make(chan struct{}),
	}
	go t.pump()
	go t.wait()
	return t, nil
}

// pump copies pty output to the backlog and the attached client. It is the
// only reader and the only backlog writer.
func (t *Terminal) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptm.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			t.backlogMu.Lock()
			t.backlog = append(t.backlog, chunk...)
			if len(t.backlog) > backlogCap {
				t.backlog = t.backlog[len(t.backlog)-backlogCap:]
			}
			t.backlogMu.Unlock()
			t.send(termMessage{Type: "out", Data: string(chunk)})
		}
		if err != nil {
			return // the wait goroutine reports the exit
		}
	}
}

// wait turns the shell's exit into state the next attach can see, then closes
// done so Kill and Attach stop waiting on it.
func (t *Terminal) wait() {
	err := t.cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	t.mu.Lock()
	t.exited = true
	t.mu.Unlock()
	t.send(termMessage{Type: "exit", Code: code})
	close(t.done)
}

// Exited reports whether the shell process is gone.
func (t *Terminal) Exited() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exited
}

// Attach drives the connection until the client goes away: replay backlog,
// then relay keystrokes in and sizes across. Attaching a second client kicks
// the first — one view at a time, and the newest window is always the one the
// user is actually looking at.
func (t *Terminal) Attach(ctx context.Context, c *websocket.Conn) {
	t.mu.Lock()
	if t.conn != nil {
		t.conn.Close(websocket.StatusGoingAway, "replaced by a newer view")
	}
	t.conn = c
	t.mu.Unlock()
	defer t.detach(c)

	c.SetReadLimit(64 * 1024)

	// Replay before anything live, so the repaint lands underneath new output
	// rather than after it.
	t.backlogMu.Lock()
	backlog := append([]byte(nil), t.backlog...)
	t.backlogMu.Unlock()
	if len(backlog) > 0 {
		if !t.writeTo(c, ctx, termMessage{Type: "out", Data: string(backlog)}) {
			return
		}
	}

	for {
		var m termMessage
		if err := wsjson.Read(ctx, c, &m); err != nil {
			return
		}
		switch m.Type {
		case "in":
			t.wmu.Lock()
			_, _ = t.ptm.Write([]byte(m.Data))
			t.wmu.Unlock()
		case "resize":
			if m.Cols > 0 && m.Rows > 0 {
				t.resize(m.Cols, m.Rows)
			}
		}
	}
}

// detach drops the attachment only if it is still ours: a replacing connection
// must not be detached by the one it replaced finishing its defer first.
func (t *Terminal) detach(c *websocket.Conn) {
	t.mu.Lock()
	if t.conn == c {
		t.conn = nil
	}
	t.mu.Unlock()
	c.Close(websocket.StatusNormalClosure, "")
}

// send writes to the attached client, if any. Failures are swallowed: the
// attach loop's own read will notice the dead connection and clean up.
func (t *Terminal) send(m termMessage) {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.writeTo(c, ctx, m)
}

func (t *Terminal) writeTo(c *websocket.Conn, ctx context.Context, m termMessage) bool {
	data, err := json.Marshal(m)
	if err != nil {
		return false
	}
	t.cmu.Lock()
	defer t.cmu.Unlock()
	return c.Write(ctx, websocket.MessageText, data) == nil
}

func (t *Terminal) resize(cols, rows int) {
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()
	t.wmu.Lock()
	_ = pty.Setsize(t.ptm, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	t.wmu.Unlock()
}

// Kill ends the shell and frees the pty. Idempotent, and safe beside an
// active attachment: closing the master fails the pump, the exit frame
// reaches the client, and done unblocks anything waiting.
func (t *Terminal) Kill() {
	t.mu.Lock()
	c := t.conn
	t.mu.Unlock()
	if c != nil {
		c.Close(websocket.StatusGoingAway, "session closed")
	}
	_ = t.ptm.Close()
	if t.cmd.Process != nil {
		// Kill the group: a shell's children (the dev server it launched) are
		// its process group, and reaping the leader alone orphans them.
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		_ = t.cmd.Process.Kill()
	}
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
	}
}
