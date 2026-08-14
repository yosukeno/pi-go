package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/session"
	"github.com/yosukeno/pi-go/skills"
	"nhooyr.io/websocket"
)

// keepalive interval for SSE comment frames. Without them an idle stream can be
// closed by an intermediary while the agent is simply thinking.
const keepalive = 20 * time.Second

type ServerOptions struct {
	// Token is required on every request, including the SSE stream. Empty means
	// no authentication, which the caller must only allow for a loopback bind.
	Token string
	// DevProxy points the non-API routes at a vite dev server during development.
	DevProxy string
	// Panels are external web applications shown as dock sheets (-web-panel).
	// Order is display order.
	Panels []Panel
	// AllowedOrigins are extra browser origins accepted besides same-origin.
	AllowedOrigins []string
	Logger         *log.Logger
}

// Panel is an external web application the browser UI can open as a dock sheet.
// The sheet's iframe loads /panels/<name>/, which this server reverse-proxies
// to URL: same origin, so the Origin check covers it and no CORS is opened.
//
// A panel runs in the page's origin — register only backends you trust with the
// token that page already holds, the same bar -skill clears by rewriting the
// system prompt. Panel content is served without the token for the same reason
// the page and its assets are: it is content, not operations. A backend with
// mutations must do its own auth.
type Panel struct {
	Name string // shown in the sheet rail; no "/" or "="
	URL  string // absolute http(s) backend
}

// validatePanel rejects the two shapes that would break routing: a name that
// collides with the URL grammar, and a URL we cannot proxy to.
func validatePanel(p Panel) error {
	if p.Name == "" || strings.ContainsAny(p.Name, "/= \t") {
		return fmt.Errorf("invalid panel name %q: no spaces, slashes or '='", p.Name)
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid panel url %q: want absolute http(s)", p.URL)
	}
	return nil
}

type Server struct {
	mgr   *Manager
	opts  ServerOptions
	mux   *http.ServeMux
	proxy http.Handler
	// panelProxies is keyed by panel name; built once at startup.
	panelProxies map[string]*httputil.ReverseProxy
}

func NewServer(mgr *Manager, opts ServerOptions) (*Server, error) {
	s := &Server{mgr: mgr, opts: opts, mux: http.NewServeMux()}
	if opts.DevProxy != "" {
		u, err := url.Parse(opts.DevProxy)
		if err != nil {
			return nil, fmt.Errorf("invalid -web-dev url: %w", err)
		}
		s.proxy = httputil.NewSingleHostReverseProxy(u)
	}
	s.panelProxies = map[string]*httputil.ReverseProxy{}
	for _, p := range opts.Panels {
		if err := validatePanel(p); err != nil {
			return nil, err
		}
		if _, dup := s.panelProxies[p.Name]; dup {
			return nil, fmt.Errorf("duplicate panel name %q", p.Name)
		}
		s.panelProxies[p.Name] = newPanelProxy(p)
	}
	s.routes()
	return s, nil
}

// newPanelProxy strips the /panels/<name> prefix and forwards to the backend,
// so the app inside can use ordinary absolute-relative links and fetch calls.
func newPanelProxy(p Panel) *httputil.ReverseProxy {
	target, _ := url.Parse(p.URL) // validated by validatePanel
	prefix := "/panels/" + p.Name
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.SetURL(target) // joins target's base path with Out's path
			pr.Out.Host = target.Host
		},
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/models", s.handleModels)
	s.mux.HandleFunc("GET /api/skills", s.handleSkills)
	s.mux.HandleFunc("GET /api/starters", s.handleStarters)
	s.mux.HandleFunc("GET /api/panels", s.handlePanels)
	// Panel proxy: content, not operations, so like the page itself it is not
	// token-gated (see the Panel doc). Bare /panels/<name> redirects to the
	// trailing slash so relative links inside resolve under the prefix.
	s.mux.Handle("/panels/{name}/", http.HandlerFunc(s.handlePanel))
	s.mux.Handle("/panels/{name}", http.HandlerFunc(s.handlePanelRedirect))
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	s.mux.HandleFunc("GET /api/sessions/{sid}", s.handleGetSession)
	s.mux.HandleFunc("PATCH /api/sessions/{sid}", s.handleUpdateSession)
	s.mux.HandleFunc("DELETE /api/sessions/{sid}", s.handleDeleteSession)
	s.mux.HandleFunc("POST /api/sessions/{sid}/messages", s.handleMessages)
	s.mux.HandleFunc("GET /api/sessions/{sid}/stream", s.handleStream)
	s.mux.HandleFunc("GET /api/sessions/{sid}/terminal", s.handleTerminal)
	s.mux.HandleFunc("POST /api/sessions/{sid}/control", s.handleControl)
	s.mux.HandleFunc("GET /api/files", s.handleFiles)
	s.mux.HandleFunc("GET /api/files/content", s.handleFileContent)
	s.mux.HandleFunc("PUT /api/files/content", s.handleFileSave)
	s.mux.HandleFunc("POST /api/files/mkdir", s.handleFileMkdir)
	s.mux.HandleFunc("GET /api/files/index", s.handleFileIndex)
	s.mux.HandleFunc("GET /api/workspace/changes", s.handleWorkspaceChanges)
	s.mux.HandleFunc("GET /api/workspace/diff", s.handleWorkspaceDiff)
	s.mux.HandleFunc("POST /api/workspace/journal/clear", s.handleWorkspaceJournalClear)
	s.mux.HandleFunc("GET /api/workspace/git", s.handleWorkspaceGit)
	s.mux.HandleFunc("/", s.handleUI)
}

// Handler wraps the routes in the two checks that stand between this server and
// a remote shell: a shared token and an Origin check.
//
// Both matter more here than for a typical API. pi-go's bash tool has no path
// restriction — unlike read, write and edit, which the path guard confines to the
// working directory — so an unauthenticated instance is remote code execution as
// a service.
func (s *Server) Handler() http.Handler {
	return s.withOrigin(s.withToken(s.mux))
}

func (s *Server) withToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the API is gated. The page and its bundle are not: a browser
		// cannot attach a header to the script tags it discovers in the HTML it
		// just loaded, so gating them would 401 the app against its own assets.
		// Serving them openly gives nothing away — it is the same code that ships
		// inside the binary — while every operation still needs the token.
		if s.opts.Token == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if given == "" {
		// The query parameter exists for curl and for any future EventSource
		// client, which cannot set headers.
		given = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) == 1
}

// withOrigin rejects cross-site requests instead of opening CORS. A page on
// another origin must not be able to drive this server through a browser that
// already holds the token.
func (s *Server) withOrigin(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.opts.AllowedOrigins))
	for _, o := range s.opts.AllowedOrigins {
		allowed[strings.TrimSuffix(o, "/")] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// No Origin means a non-browser client (curl, a test), which the token
		// already covers.
		if origin == "" || allowed[strings.TrimSuffix(origin, "/")] {
			next.ServeHTTP(w, r)
			return
		}
		if u, err := url.Parse(origin); err == nil && u.Host == r.Host {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "cross-origin request rejected", http.StatusForbidden)
	})
}

// Serve runs until ctx is cancelled, then drains: the HTTP server stops taking
// connections and the Manager cancels every run, which reaches each bash child
// process through its context.
func (s *Server) Serve(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: s.Handler(),
		// No write timeout: an SSE stream is a long-lived response by design, and
		// a deadline here would cut a thinking agent off mid-turn.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	if s.opts.Logger != nil {
		s.opts.Logger.Print("shutting down, cancelling active runs")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutdownCtx)
	s.mgr.Close()
	if errors.Is(err, context.DeadlineExceeded) {
		return srv.Close()
	}
	return err
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID            string   `json:"id"`
		Provider      string   `json:"provider"`
		Aliases       []string `json:"aliases,omitempty"`
		ContextWindow int      `json:"context_window"`
		Configured    bool     `json:"configured"`
		// SubagentModel is what a read-only subagent of this model runs. Reported
		// because it is the one catalogue entry that changes what happens without
		// anyone naming it, and because the terminal's -models already shows it: a
		// field visible in one front end and not the other is how the two drift.
		SubagentModel string `json:"subagent_model,omitempty"`
		// KeyEnv names the variable to set. Without it "configured: false" tells the
		// browser something is wrong but not what to do, and unlike the terminal it
		// cannot print a hint of its own.
		KeyEnv string `json:"key_env,omitempty"`
	}
	out := make([]model, 0, len(config.Catalog()))
	for _, m := range config.Catalog() {
		e := model{
			ID: m.ID, Provider: m.Provider, Aliases: m.Aliases,
			ContextWindow: m.ContextWindow, Configured: config.Configured(m.Provider),
			SubagentModel: m.SubagentModel,
		}
		if !e.Configured {
			e.KeyEnv = config.KeyEnv(m.Provider)
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out, "default": config.DefaultModel()})
}

// handlePanels lists the external panels registered with -web-panel. The UI
// needs it once at boot to know which sheet buttons to show; the backend URL
// is deliberately withheld — the iframe only ever talks to /panels/<name>/,
// so the internal topology stays the server's business.
func (s *Server) handlePanels(w http.ResponseWriter, r *http.Request) {
	type panel struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	out := make([]panel, 0, len(s.opts.Panels))
	for _, p := range s.opts.Panels {
		out = append(out, panel{Name: p.Name, Path: "/panels/" + p.Name + "/"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"panels": out})
}

func (s *Server) handlePanel(w http.ResponseWriter, r *http.Request) {
	proxy, ok := s.panelProxies[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (s *Server) handlePanelRedirect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.panelProxies[name]; !ok {
		http.NotFound(w, r)
		return
	}
	target := "/panels/" + name + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// handleSkills lists the skills in effect. The set is server-wide and fixed at
// startup, so this needs no session and never changes for the life of the process.
//
// The location is included because it is already in the model's system prompt:
// withholding it from the browser would hide from the user something the model can
// see, which is the wrong direction for a tool whose whole job is being watchable.
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	type skill struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Path        string `json:"path"`
		Dir         string `json:"dir"`
		Source      string `json:"source"`
		ManualOnly  bool   `json:"manual_only,omitempty"`
	}
	list := s.mgr.Skills()
	out := make([]skill, 0, len(list))
	for _, sk := range list {
		out = append(out, skill{
			Name: sk.Name, Description: sk.Description, Path: sk.Path,
			Dir: sk.Dir, Source: sk.Source, ManualOnly: sk.DisableModelInvocation,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": out})
}

// handleStarters serves the cards an empty conversation offers. The content is
// the skill's, not pi-go's: a deployment describes its own openings, and this
// server only checks that each one can actually do something.
//
// Read per request so an edited starters.json shows up on reload — the same
// reason /skill:name re-reads the instructions. The files are small and this is
// fetched once per page.
func (s *Server) handleStarters(w http.ResponseWriter, r *http.Request) {
	warn := func(format string, args ...any) {
		if s.opts.Logger != nil {
			s.opts.Logger.Printf(format, args...)
		}
	}
	all, diags := skills.LoadStarters(s.mgr.Skills())
	for _, d := range diags {
		warn("starters: %s: %s", d.Path, d.Message)
	}

	registered := map[string]bool{}
	for _, p := range s.opts.Panels {
		registered[p.Name] = true
	}

	// One empty state, so the first heading and the first send flag win; cards
	// concatenate in skill order and stop at the layout's limit.
	// A card pointing at a panel this process never registered would be a button
	// that does nothing, which is worse than one card fewer.
	keepable := func(skill string, cards []skills.StarterCard) []skills.StarterCard {
		out := make([]skills.StarterCard, 0, len(cards))
		for _, c := range cards {
			if c.Panel != "" && !registered[c.Panel] {
				warn("starters: %s: card %q names unregistered panel %q; skipped", skill, c.Title, c.Panel)
				continue
			}
			out = append(out, c)
		}
		return out
	}

	out := struct {
		Heading   string                 `json:"heading,omitempty"`
		Send      bool                   `json:"send,omitempty"`
		Cards     []skills.StarterCard   `json:"cards"`
		Followups []skills.FollowupGroup `json:"followups"`
	}{Cards: []skills.StarterCard{}, Followups: []skills.FollowupGroup{}}

	for _, st := range all {
		if out.Heading == "" {
			out.Heading = st.Heading
			out.Send = st.Send
		}
		for _, c := range keepable(st.Skill, st.Cards) {
			if len(out.Cards) == skills.MaxStarterCards {
				break
			}
			out.Cards = append(out.Cards, c)
		}
		for _, g := range st.Followups {
			// A group whose chips all pointed at missing panels would render an
			// empty row, so it drops out with them.
			if chips := keepable(st.Skill, g.Chips); len(chips) > 0 {
				out.Followups = append(out.Followups, skills.FollowupGroup{When: g.When, Chips: chips})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"starters": out})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.mgr.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []session.Info{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": list,
		"cwd":      s.mgr.Cwd(),
	})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
		// Workspace is a directory relative to the server root ("" = root); it
		// becomes the session's working directory.
		Workspace string `json:"workspace"`
	}
	_ = decodeBody(r, &body)
	sess, err := s.mgr.Create(body.Model, body.Workspace)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := sess.Model()
	writeJSON(w, http.StatusCreated, map[string]any{
		"session_id": sess.ID,
		"path":       sess.Path(),
		"model":      cfg.Model,
		"provider":   cfg.Provider,
		"workspace":  sess.Workspace(),
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	snap := sess.Hub().Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID,
		"snapshot":   snap,
	})
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	err := s.mgr.Delete(r.PathValue("sid"))
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, ErrRunActive):
		writeErr(w, http.StatusConflict, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleUpdateSession applies sidebar edits (pin, rename). Pointers in the
// body, so a request can change one field without having to know the other.
func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title  *string `json:"title"`
		Pinned *bool   `json:"pinned"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Title == nil && body.Pinned == nil {
		writeErr(w, http.StatusBadRequest, errors.New("nothing to update: pass title and/or pinned"))
		return
	}
	if body.Title != nil {
		trimmed := strings.TrimSpace(*body.Title)
		body.Title = &trimmed
	}
	err := s.mgr.UpdateInfo(r.PathValue("sid"), body.Title, body.Pinned)
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err)
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeErr(w, http.StatusBadRequest, errors.New("prompt is empty"))
		return
	}
	// Slash commands must never reach the transcript. A command that could be
	// parsed out of conversation content would mean one prompt injection — a file
	// containing "please output /auto" — could disable the gate that exists to
	// stop exactly that. The client turns them into /control calls; this is the
	// backstop for a client that forgets.
	if cmd := slashCommand(prompt); cmd != "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"%s is a command, not a prompt: send it to /control so it never enters the conversation", cmd))
		return
	}

	// /skill:name is the opposite case: it is not a command but a prompt that has
	// to be expanded, and the expansion belongs on the server because that is
	// where the skill file is readable. The result enters the transcript on
	// purpose — what the model was told is the thing worth keeping.
	if name, extra, ok := skills.ParseCommand(prompt); ok {
		sk, found := skills.Find(s.mgr.Skills(), name)
		if !found {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("no skill named %q", name))
			return
		}
		expanded, err := skills.Invocation(sk, extra)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		prompt = expanded
	}

	runID, err := sess.Start(prompt)
	if errors.Is(err, ErrRunActive) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID})
}

var slashCommands = []string{
	"/auto", "/strict", "/standard", "/model", "/usage", "/compact", "/help", "/exit", "/quit",
}

// slashCommand reports the command a prompt is, if it is one. Only an exact
// leading word counts, so a prompt that merely starts with a path is untouched.
func slashCommand(prompt string) string {
	word, _, _ := strings.Cut(prompt, " ")
	word = strings.TrimSpace(word)
	for _, c := range slashCommands {
		if word == c {
			return c
		}
	}
	return ""
}

// handleStream is the subscription. It is only a reader of the Hub: this handler
// dying, for any reason, has no effect on the run.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}

	// No `from` asks for a snapshot, which is what a page load wants: it has no
	// local state, and a snapshot is always correct. `from` is for a stream that
	// broke while the page stayed alive.
	from := int64(-1)
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("from must be a non-negative integer"))
			return
		}
		from = n
	}

	backlog, ch, cancel := sess.Hub().Subscribe(from)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// Tells nginx and friends not to buffer, which would defeat streaming.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for _, e := range backlog {
		if !writeEvent(w, e) {
			return
		}
	}
	flusher.Flush()

	ping := time.NewTicker(keepalive)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-ch:
			if !open {
				// Either the session was evicted, or this subscriber fell too far
				// behind and was dropped. Both are recoverable by reconnecting
				// with ?from=<last seq>.
				return
			}
			if !writeEvent(w, e) {
				return
			}
			flusher.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, e Event) bool {
	data, err := json.Marshal(e)
	if err != nil {
		// One unserialisable payload must not kill the stream.
		data, err = json.Marshal(Event{
			Seq: e.Seq, Type: EvError, TS: e.TS,
			Error: "event could not be serialised: " + err.Error(),
		})
		if err != nil {
			return false
		}
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, data)
	return err == nil
}

type controlRequest struct {
	Action string `json:"action"`

	GateID   string          `json:"gate_id"`
	Allow    *bool           `json:"allow"`
	Args     json.RawMessage `json:"args"`
	Reason   string          `json:"reason"`
	Remember string          `json:"remember"`

	// Mode is read by two actions, which never co-occur: set_policy's approval
	// mode, and rewind's "chat" | "files" | "both". This struct is a flat bag
	// interpreted per action — Turns and AllowTool below are shared the same way.
	Mode         string `json:"mode"`
	Turns        int    `json:"turns"`
	AllowTool    string `json:"allow_tool"`
	AllowCommand string `json:"allow_command"`

	Model string `json:"model"`

	// Prompt carries a steering message. It goes through /control rather than
	// /messages because it must not start a run: it only joins one already in
	// flight, and the reply tells the client which happened.
	Prompt string `json:"prompt"`

	// MessageID identifies the timeline message a rewind forks away from.
	MessageID string `json:"message_id"`

	// Rewind reads Mode above for what to act on. It replaced a files bool,
	// because a bool could not express the third state — restore the work tree and
	// leave the conversation where it is.
	//
	// Paths narrows a file restore to a subset of what the preview listed. Empty
	// means the whole checkpoint. Only meaningful for the two file modes.
	Paths []string `json:"paths"`
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var req controlRequest
	if err := decodeBody(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	switch req.Action {
	case "steer":
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			writeErr(w, http.StatusBadRequest, errors.New("prompt is empty"))
			return
		}
		// A slash command must not become a prompt here either; the same reasoning
		// as in handleMessages applies, and a steering message is the easier of the
		// two to sneak one into.
		if cmd := slashCommand(prompt); cmd != "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf(
				"%s is a command, not a prompt", cmd))
			return
		}
		// `steered: false` means there was no run to join, which is not an error:
		// the client's next move is to send it as an ordinary message. Answering
		// with a status code would make the normal race look like a failure.
		writeJSON(w, http.StatusOK, map[string]any{"steered": sess.Steer(prompt)})

	case "cancel":
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": sess.Cancel()})

	case "rewind":
		if req.MessageID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("message_id is required"))
			return
		}
		// No default mode. A rewind is destructive, and guessing which half of it
		// was meant is the one place a helpful default would be dangerous.
		err := sess.Rewind(req.MessageID, RewindMode(req.Mode), req.Paths)
		switch {
		case errors.Is(err, ErrRunActive):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, errMessageUnknown):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, errFilesUnavailable):
			writeErr(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, errRewindMode):
			writeErr(w, http.StatusBadRequest, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{"rewound": true})
		}

	case "rewind_preview":
		if req.MessageID == "" {
			writeErr(w, http.StatusBadRequest, errors.New("message_id is required"))
			return
		}
		changes, available, err := sess.RewindPreview(req.MessageID)
		switch {
		case errors.Is(err, errMessageUnknown):
			writeErr(w, http.StatusNotFound, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			if changes == nil {
				changes = []FileChange{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"available": available, "changes": changes})
		}

	case "compact":
		res, err := sess.Compact(r.Context())
		switch {
		case errors.Is(err, ErrRunActive):
			writeErr(w, http.StatusConflict, err)
		case errors.Is(err, agent.ErrNothingToCompact), errors.Is(err, agent.ErrNotSmaller):
			// The request was well formed and the answer is "this would not help".
			// 422 rather than 400: nothing about the call was wrong.
			writeErr(w, http.StatusUnprocessableEntity, err)
		case errors.Is(err, agent.ErrEmptySummary), errors.Is(err, agent.ErrRaced):
			writeErr(w, http.StatusConflict, err)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{
				"compacted":     true,
				"before":        res.Before,
				"before_tokens": res.BeforeTokens,
				"after_tokens":  res.AfterTokens,
				"freed_tokens":  res.Freed(),
				"summary":       res.Summary,
			})
		}

	case "set_model":
		cfg, err := sess.SetModel(req.Model)
		if errors.Is(err, ErrRunActive) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"model": cfg.Model, "provider": cfg.Provider})

	case "set_policy":
		switch {
		case req.AllowTool != "":
			writeJSON(w, http.StatusOK, sess.AllowTool(req.AllowTool))
		case req.AllowCommand != "":
			writeJSON(w, http.StatusOK, sess.AllowCommand(req.AllowCommand))
		default:
			mode, ok := ParseMode(req.Mode)
			if !ok {
				writeErr(w, http.StatusBadRequest, errors.New(`mode must be "strict", "standard" or "auto"`))
				return
			}
			writeJSON(w, http.StatusOK, sess.SetPolicy(mode, req.Turns))
		}

	case "gate_decide":
		if req.Allow == nil {
			writeErr(w, http.StatusBadRequest, errors.New("allow is required"))
			return
		}
		err := sess.Gate().Decide(req.GateID, Verdict{
			Allow: *req.Allow, Args: req.Args, Reason: req.Reason, Remember: req.Remember,
		})
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case "gate_freeze", "gate_thaw":
		var err error
		if req.Action == "gate_freeze" {
			err = sess.Gate().Freeze(req.GateID)
		} else {
			err = sess.Gate().Thaw(req.GateID)
		}
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", req.Action))
	}
}

// handleAPIListing is what gets served when the front end has not been built.
// A short page describing the API beats a blank screen, since the server is
// perfectly usable with curl.
func (s *Server) handleAPIListing(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, apiListingHTML)
}

const apiListingHTML = `<!doctype html>
<meta charset="utf-8">
<title>pi-go</title>
<body style="font:14px/1.6 ui-monospace,monospace;max-width:44rem;margin:4rem auto;padding:0 1rem">
<h1>pi-go</h1>
<p>The HTTP and SSE layer is running, but the browser UI has not been built into
this binary. Build it with:</p>
<pre>cd web/ui &amp;&amp; npm ci &amp;&amp; npm run build
go build -o pi-go .</pre>
<p>Everything below works over curl in the meantime.</p>
<pre>GET    /api/models
GET    /api/sessions
POST   /api/sessions
GET    /api/sessions/{id}
PATCH  /api/sessions/{id}              {"title":"...","pinned":true}（可只传一个）
DELETE /api/sessions/{id}
POST   /api/sessions/{id}/messages    {"prompt":"..."}
GET    /api/sessions/{id}/stream[?from=N]
POST   /api/sessions/{id}/control     {"action":"cancel"|"rewind"|"rewind_preview"|"steer"|"set_model"|"set_policy"|"gate_decide"|"gate_freeze"|"gate_thaw"}</pre>
<p>Pass the token as <code>Authorization: Bearer &lt;token&gt;</code>, or <code>?token=</code> for the stream.</p>
</body>
`

func (s *Server) session(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.mgr.Get(r.PathValue("sid"))
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return nil, false
	}
	return sess, true
}

// handleTerminal upgrades to a websocket attached to the session's shell. The
// token middleware gates it like every other /api route — an unauthenticated
// shell here would be remote code execution as a service, the same line the
// rest of the server draws.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	term, err := sess.Terminal()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Same-origin pages only: the Origin middleware has already run, so no
	// extra check is needed on upgrade.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		return // Accept has already answered with the error
	}
	term.Attach(r.Context(), c)
}

// decodeBody reads a JSON body, tolerating an empty one so that actions without
// parameters need no payload.
func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
