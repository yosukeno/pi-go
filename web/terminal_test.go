package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// The terminal is the one endpoint that hands out a raw shell; the token gate
// has to cover the upgrade exactly like it covers every other /api route.
func TestTerminalRequiresTheToken(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") + "/api/sessions/" + sid + "/terminal"
	c, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("dial without a token succeeded, want a 401")
	}
	if resp == nil || resp.StatusCode != 401 {
		t.Fatalf("dial without a token: %+v, want a 401", resp)
	}
}

// A full round trip through a real shell: type a command, read its output.
// The shell is spawned on first attach and cwd'd into the session workspace.
func TestTerminalEchoesBackWhatIsTyped(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") +
		"/api/sessions/" + sid + "/terminal?token=" + h.token
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	if err := wsjson.Write(ctx, c, termMessage{Type: "in", Data: "echo marker-$((40+2))\n"}); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.After(20 * time.Second)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "marker-42") {
		var m termMessage
		read := make(chan error, 1)
		go func() { read <- wsjson.Read(ctx, c, &m) }()
		select {
		case err := <-read:
			if err != nil {
				t.Fatalf("read: %v (saw so far: %q)", err, seen.String())
			}
		case <-deadline:
			t.Fatalf("never saw the command output; saw %q", seen.String())
		}
		if m.Type == "out" {
			seen.WriteString(m.Data)
		}
	}
}

// Detaching must not kill the shell: the backlog replay is what a reopening
// page repaints from, and it only exists if the pty kept running.
func TestTerminalSurvivesADetach(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	url := "ws" + strings.TrimPrefix(h.srv.URL, "http") +
		"/api/sessions/" + sid + "/terminal?token=" + h.token

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c1, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if err := wsjson.Write(ctx, c1, termMessage{Type: "in", Data: "echo keeps-$((20+3))\n"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let the echo land in the backlog
	c1.Close(websocket.StatusNormalClosure, "")

	// Reattach: the replay should contain the earlier echo.
	c2, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("second dial: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")

	deadline := time.After(20 * time.Second)
	var seen strings.Builder
	for !strings.Contains(seen.String(), "keeps-23") {
		var m termMessage
		read := make(chan error, 1)
		go func() { read <- wsjson.Read(ctx, c2, &m) }()
		select {
		case err := <-read:
			if err != nil {
				t.Fatalf("read: %v (saw so far: %q)", err, seen.String())
			}
		case <-deadline:
			t.Fatalf("reconnect never replayed the backlog; saw %q", seen.String())
		}
		if m.Type == "out" {
			seen.WriteString(m.Data)
		}
	}
}

// Evicting the session kills its shell: no orphaned processes outliving the
// thing that spawned them.
func TestTerminalDiesWithTheSession(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("hi")))
	sid := h.createSession()

	sess := h.session(sid)
	term, err := sess.Terminal()
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	h.mgr.evictAll()

	select {
	case <-term.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the shell is still running after its session was evicted")
	}
}
