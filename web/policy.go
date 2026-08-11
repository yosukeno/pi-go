package web

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/yosukeno/pi-go/agent"
)

// Mode is how much of the agent's work needs a human's eyes.
type Mode string

const (
	// ModeStrict reviews every mutation, including file writes.
	ModeStrict Mode = "strict"
	// ModeStandard reviews bash only. The file tools are already confined to the
	// working directory by the path guard, and their effect is visible as a diff
	// and usually recoverable from git. bash has neither property, which makes it
	// the one tool worth interrupting for by default.
	ModeStandard Mode = "standard"
	// ModeAuto reviews nothing.
	ModeAuto Mode = "auto"
)

// ParseMode validates a mode name coming off the wire.
func ParseMode(s string) (Mode, bool) {
	switch Mode(s) {
	case ModeStrict, ModeStandard, ModeAuto:
		return Mode(s), true
	}
	return "", false
}

// Policy decides which calls can skip review. It is session state, deliberately
// not per-connection: reloading the browser must not silently re-arm a gate the
// user turned off, nor leave the banner off while auto mode is still active.
type Policy struct {
	mu   sync.Mutex
	mode Mode
	// remaining counts down loop turns for a turn-limited auto mode. Turns, not
	// tool calls, because "let it run three more rounds" is the unit people
	// actually think in.
	remaining int
	limited   bool

	// allowTool and allowCmd are session-scoped "always allow" grants.
	allowTool map[string]bool
	allowCmd  map[string]bool
}

func NewPolicy() *Policy {
	return &Policy{
		mode:      ModeStandard,
		allowTool: make(map[string]bool),
		allowCmd:  make(map[string]bool),
	}
}

func (p *Policy) State() PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stateLocked()
}

func (p *Policy) stateLocked() PolicyState {
	return PolicyState{Mode: string(p.mode), RemainingTurns: p.remaining}
}

// Set switches mode. A positive turns argument only means anything for auto and
// makes it expire on its own.
func (p *Policy) Set(mode Mode, turns int) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.remaining, p.limited = 0, false
	if mode == ModeAuto && turns > 0 {
		p.remaining, p.limited = turns, true
	}
	return p.stateLocked()
}

// AllowTool grants a tool a pass for the rest of the session.
func (p *Policy) AllowTool(name string) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowTool[name] = true
	return p.stateLocked()
}

// AllowCommand grants one exact command string a pass for the rest of the
// session.
//
// Exact text, never a prefix. Prefix matching a shell command is not a security
// boundary — `ls; rm -rf ~` starts with `ls` — and its real damage is convincing
// people something is being blocked. An exact string has no syntax left to
// subvert, and it still fixes the fatigue case of the same `go build ./...`
// twenty times in one session.
func (p *Policy) AllowCommand(cmd string) PolicyState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cmd = strings.TrimSpace(cmd); cmd != "" {
		p.allowCmd[cmd] = true
	}
	return p.stateLocked()
}

// TurnStarted spends one turn of a turn-limited auto mode. It reports the mode
// it left when the budget ran out, so the caller can tell the user rather than
// let the gate quietly re-arm.
//
// The budget is spent at the start of a turn and the reversion happens at the
// start of the turn after the last one, so `/auto 3` covers exactly three turns
// rather than two and a half.
func (p *Policy) TurnStarted() (from Mode, state PolicyState, reverted bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mode != ModeAuto || !p.limited {
		return p.mode, p.stateLocked(), false
	}
	if p.remaining > 0 {
		p.remaining--
		return p.mode, p.stateLocked(), false
	}
	p.mode, p.limited = ModeStandard, false
	return ModeAuto, p.stateLocked(), true
}

// Decide reports whether a call may skip review, and which rule said so. The
// rule name is published as an audit trail, so an automatic pass still leaves a
// mark on the timeline.
func (p *Policy) Decide(req agent.GateRequest) (rule string, auto bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.mode == ModeAuto {
		return "policy:auto", true
	}
	if p.allowTool[req.ToolName] {
		return "session:tool:" + req.ToolName, true
	}
	if cmd := commandOf(req.Args); cmd != "" && p.allowCmd[cmd] {
		return "session:command", true
	}
	if !p.reviewsLocked(req.ToolName) {
		return "policy:" + string(p.mode), true
	}
	return "", false
}

// reviewsLocked is the mode matrix.
func (p *Policy) reviewsLocked(tool string) bool {
	switch tool {
	case "read", "ls", "find", "grep":
		// Read-only and confined by the path guard. Reviewing them would train
		// the habit of clicking approve, which is what makes the bash prompt
		// stop meaning anything.
		return false
	case "write", "edit":
		return p.mode == ModeStrict
	default:
		// bash, and anything added later: unknown blast radius, so ask.
		return true
	}
}

// commandOf pulls the command out of bash arguments, for the exact-match grant
// and for the danger highlight.
func commandOf(args json.RawMessage) string {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.Command)
}

// dangerPatterns is a list for highlighting an approval card in red.
//
// It is a prompt for the human, not a control. It never blocks and never changes
// which calls get reviewed; treating it as protection would repeat the mistake
// that rules out a command whitelist. Its one behavioural effect is that a
// matching approval cannot be turned into an "always allow" grant, so a fast
// click cannot open something permanently.
var dangerPatterns = []string{
	"rm -rf", "rm -fr", "sudo ", "dd ", "mkfs", "chmod 777", "chown ",
	"curl | sh", "curl|sh", "wget | sh", "wget|sh", "| sh", "| bash",
	"> /dev/", "shutdown", "reboot", ":(){", "truncate -s 0",
	"git push --force", "git push -f", "git reset --hard", "git clean -fd",
	"npm publish", "docker rm", "kubectl delete",
}

// Danger returns the patterns a call matches.
func Danger(req agent.GateRequest) []string {
	text := commandOf(req.Args)
	if text == "" {
		text = string(req.Args)
	}
	lower := strings.ToLower(text)
	var hits []string
	for _, p := range dangerPatterns {
		if strings.Contains(lower, p) {
			hits = append(hits, strings.TrimSpace(p))
		}
	}
	return hits
}
