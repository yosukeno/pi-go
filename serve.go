package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yosukeno/pi-go/memory"
	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/skills"
	"github.com/yosukeno/pi-go/tui"
	"github.com/yosukeno/pi-go/web"
)

type webOptions struct {
	listen              string
	dev                 string
	model               string
	cwd                 string
	maxTurns            int
	retries             int
	stagnationThreshold int
	tokenBudget         int64
	costBudget          float64
	timeBudget          time.Duration
	gateTimeout         time.Duration
	// contextEdit is the raw -context-edit spec, resolved per session by the Manager:
	// "auto" is a fraction of the model's window, and the browser can switch models
	// mid-session.
	contextEdit string
	skills      skills.Options
	memory      memory.Options
}

// serveWeb runs the HTTP/SSE server.
//
// Two defaults here are security decisions rather than conveniences. The bind
// address is loopback, and a token is always required — there is no way to turn
// it off. pi-go's bash tool has no path restriction (read, write and edit are
// confined by the path guard; bash is a shell), so an open instance is a remote
// shell. A generated token printed on startup, like Jupyter does, costs nothing
// and removes the failure mode where someone ships the unauthenticated version.
func serveWeb(o webOptions) error {
	cwd := o.cwd
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return err
		}
	}

	token := strings.TrimSpace(os.Getenv("PIGO_WEB_TOKEN"))
	generated := false
	if token == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		token, generated = hex.EncodeToString(b), true
	}

	// Discovered once for the whole server: Cwd is fixed, so every session sees
	// the same skills, and re-scanning per session could only produce a set that
	// disagrees with the one already written into a session's metadata.
	o.skills.Cwd = cwd
	skillList, skillDiags := skills.Load(o.skills)
	reportSkillDiagnostics(skillDiags)

	// Resolved once for the whole server, like skills and for the same reason.
	o.memory.Cwd = cwd
	mem, memDiags := memory.Load(o.memory)
	for _, d := range memDiags {
		fmt.Fprintf(os.Stderr, "%s%s%s\n", tui.Yellow, d, tui.Reset)
	}

	// Sweep sessions that were created but never used: the web UI writes the
	// file at creation, so every abandoned "new session" would otherwise pile
	// up in the sidebar. Best-effort — a failure here must not block startup.
	sessionDir := session.DefaultDir()
	cleaned, _ := session.CleanEmpty(sessionDir)

	mgr, err := web.NewManager(web.Config{
		Cwd:         cwd,
		SessionDir:  sessionDir,
		Model:       o.model,
		Retries:     o.retries,
		MaxTurns:    o.maxTurns,
		GateTimeout: o.gateTimeout,
		ContextEdit: o.contextEdit,
		Skills:      skillList,
		Memory:      mem,
	})
	if err != nil {
		return err
	}
	defer mgr.Close()

	opts := web.ServerOptions{
		Token:    token,
		DevProxy: o.dev,
		Logger:   log.New(os.Stderr, "pi-go: ", 0),
	}
	if o.dev != "" {
		// The vite dev server serves the page, so its origin has to be accepted.
		opts.AllowedOrigins = []string{strings.TrimSuffix(o.dev, "/")}
	}
	srv, err := web.NewServer(mgr, opts)
	if err != nil {
		return err
	}

	fmt.Printf("pi-go web  model=%s  cwd=%s\n", o.model, mgr.Cwd())
	if cleaned > 0 {
		fmt.Printf("%s  cleaned %d empty session(s)%s\n", tui.Dim, cleaned, tui.Reset)
	}
	if len(skillList) > 0 {
		fmt.Printf("%s  %d skill(s): %s%s\n", tui.Dim, len(skillList),
			strings.Join(skills.Names(skillList), ", "), tui.Reset)
	}
	url := fmt.Sprintf("http://%s/?token=%s", displayAddr(o.listen), token)
	fmt.Printf("  %s\n", tui.Link(url, url))
	if generated {
		fmt.Printf("%s  token generated for this run; set PIGO_WEB_TOKEN to pin it%s\n", tui.Dim, tui.Reset)
	}
	if !loopback(o.listen) {
		fmt.Printf("%s  ! %s is not loopback. The bash tool runs commands with your privileges\n"+
			"    and is not sandboxed — only do this on a network you control.%s\n", tui.Red, o.listen, tui.Reset)
	}
	if o.dev != "" {
		fmt.Printf("%s  proxying non-API routes to %s%s\n", tui.Dim, o.dev, tui.Reset)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return srv.Serve(ctx, o.listen)
}

// loopback reports whether an address is reachable only from this machine. A
// hostname that does not parse as an IP is treated as remote: guessing in the
// permissive direction is the wrong way to be wrong here.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// displayAddr turns a wildcard bind into something clickable.
func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return "127.0.0.1:" + port
	}
	return addr
}
