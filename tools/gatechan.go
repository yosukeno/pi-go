package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// EnvGateFD tells a child that an approval channel exists and where it starts.
//
// The channel is two inherited file descriptors rather than stdin and stdout, and
// both alternatives were tried on paper first. stdout is the public event contract
// from -mode json: putting private messages on it would mean a script reading a
// subagent's stream sees frames that are none of its business, and the guarantee
// that every line is a wire event would stop being true. stdin is worse — a
// non-terminal stdin is read to completion as the prompt, so a gate message there
// would arrive as part of the task.
//
// Descriptors have their own cost: they are a Unix mechanism, so on a platform
// without ExtraFiles a subagent runs ungated. That is recorded in the tool's
// refusal rather than hidden; see wireGate.
const EnvGateFD = "PI_GO_GATE_FD"

// gateRequest and gateVerdict are the two lines of the private protocol. Field
// names are short because nothing renders them; this is a pipe between two copies
// of the same binary, not an interface anyone else consumes.
type gateRequest struct {
	CallID string          `json:"call_id"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args,omitempty"`
}

type gateVerdict struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// wireGate attaches the approval channel to cmd and starts serving it.
//
// Returns a function to run after the child has been waited for, which closes the
// parent's ends. The ordering matters in both directions: the parent must close its
// copy of the child's write end or the reader never sees EOF, and it must not close
// the verdict writer until the child is gone or a call in flight gets a broken pipe
// instead of an answer.
func (s *Subagent) wireGate(cmd *exec.Cmd, subagentID string) (func(), error) {
	if s.Review == nil {
		return func() {}, nil
	}
	// Child writes requests here, parent reads.
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	// Parent writes verdicts here, child reads.
	verR, verW, err := os.Pipe()
	if err != nil {
		reqR.Close()
		reqW.Close()
		return nil, err
	}
	// ExtraFiles[0] is fd 3 in the child, ExtraFiles[1] is fd 4.
	cmd.ExtraFiles = append(cmd.ExtraFiles, reqW, verR)
	cmd.Env = append(cmd.Env, EnvGateFD+"=3")

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serveGate(reqR, verW, subagentID)
	}()

	return func() {
		// The child's ends first: our copies keep the pipe open and the server
		// goroutine would block on a read that can never complete.
		reqW.Close()
		verR.Close()
		// Closing the read end unblocks the server if the child died without
		// closing its side.
		reqR.Close()
		<-done
		verW.Close()
	}, nil
}

// serveGate answers the child's requests one at a time.
//
// Serial by construction, because the loop on the other side is one goroutine
// asking one question and waiting. That is also what the timeout arithmetic
// assumes: a child has at most one call awaiting approval, so N children can hold
// at most N gate timeouts between them.
func (s *Subagent) serveGate(req io.Reader, verdicts io.Writer, subagentID string) {
	sc := bufio.NewScanner(req)
	sc.Buffer(make([]byte, 0, 8*1024), MaxBytes)
	enc := json.NewEncoder(verdicts)
	for sc.Scan() {
		var q gateRequest
		if err := json.Unmarshal(sc.Bytes(), &q); err != nil {
			// A malformed request is refused rather than ignored: the child is
			// blocked waiting for an answer, and silence would hang it until the
			// subagent timeout.
			_ = enc.Encode(gateVerdict{Reason: "the approval request could not be read"})
			continue
		}
		d := s.Review(context.Background(), Approval{
			Subagent: subagentID, CallID: q.CallID, Tool: q.Tool, Args: q.Args,
		})
		if err := enc.Encode(gateVerdict{Allow: d.Allow, Reason: d.Reason}); err != nil {
			return // the child is gone
		}
	}
}

// FDGate is the child's side: it asks the parent instead of asking a human.
//
// Constructed only when EnvGateFD is set, which happens only when the parent had a
// gate to delegate to. Everywhere else it is nil and the child runs exactly as
// `pi-go -p` always has.
type FDGate struct {
	requests *os.File
	verdicts *bufio.Reader
	enc      *json.Encoder
}

// OpenFDGate returns the child's gate, or nil when this process was not given one.
func OpenFDGate() *FDGate {
	fd := os.Getenv(EnvGateFD)
	if fd != "3" {
		return nil
	}
	// Named for what they are, since these names show up in error messages.
	req := os.NewFile(3, "pi-go gate requests")
	ver := os.NewFile(4, "pi-go gate verdicts")
	if req == nil || ver == nil {
		return nil
	}
	return &FDGate{requests: req, verdicts: bufio.NewReader(ver), enc: json.NewEncoder(req)}
}

// Ask sends one request and waits for the answer.
//
// Every failure is a refusal. An unattended gate must fail closed, and a broken
// pipe here means the parent is gone — continuing to act while nobody can say no
// is the exact situation the gate exists to prevent.
func (g *FDGate) Ask(callID, tool string, args json.RawMessage) (allow bool, reason string) {
	if err := g.enc.Encode(gateRequest{CallID: callID, Tool: tool, Args: args}); err != nil {
		return false, "could not reach the parent agent for approval, so this call was refused"
	}
	line, err := g.verdicts.ReadString('\n')
	if err != nil {
		return false, "the parent agent did not answer, so this call was refused"
	}
	var v gateVerdict
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &v); err != nil {
		return false, "the parent agent's answer could not be read, so this call was refused"
	}
	if !v.Allow && v.Reason == "" {
		v.Reason = "the parent agent refused this call"
	}
	return v.Allow, v.Reason
}

// GateFDError explains why a platform cannot carry the channel. Kept as a value so
// the message is identical wherever it surfaces.
var GateFDError = fmt.Errorf("passing an approval channel to a subagent needs inherited " +
	"file descriptors, which this platform does not support")
