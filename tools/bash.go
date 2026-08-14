package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// DefaultBashTimeout caps runaway commands. pi leaves this open-ended, but an
// unattended server agent needs a ceiling more than it needs the flexibility.
const DefaultBashTimeout = 120 * time.Second

type Bash struct {
	Cwd string
	// Timeout overrides DefaultBashTimeout when non-zero.
	Timeout time.Duration
	// Guard, when non-nil, refuses commands that would reach outside an isolated
	// worktree. Nil in the terminal and web paths, where bash is unrestricted —
	// which is the documented existing behaviour, not an oversight.
	Guard *Guard
}

type bashArgs struct {
	Command string `json:"command" required:"true" description:"Bash command to execute"`
	// Workdir is where to run, relative to the session's directory or absolute.
	// Optional; empty means the session's own directory, which is what every call
	// did before this existed.
	Workdir string  `json:"workdir,omitempty" description:"Directory to run in, relative or absolute. Defaults to the working directory"`
	Timeout float64 `json:"timeout,omitempty" description:"Timeout in seconds"`
}

func (*Bash) Name() string { return "bash" }

// Description states that the shell does not persist, which is the one thing about
// this tool a model cannot discover and is likely to assume the opposite of: the
// bash tool most models have seen documented keeps one session alive across calls,
// so `cd` there carries over. Here every call is a new `bash -c`, and a model acting
// on the other assumption runs its next command in the wrong directory and gets a
// plausible wrong answer rather than an error.
//
// Chaining is mentioned in the same breath because it is the remedy, not because
// long chains are wanted: output is tail-truncated and one exit code is reported for
// the whole string, so a chain trades attribution for a round trip. Saying "chain
// what depends on the same directory" gets the fix without inviting `a && b && c &&
// d` where the first failure is the one that scrolls off.
//
// workdir is named before the chaining remedy, because for the "run it over there"
// case it is the better answer and the model will otherwise reach for `cd x && …`,
// which it has to get syntactically right, which the guard then has to parse back
// out of the command text, and which spends the exit code and the output budget on
// the `cd`. Codex's shell tool has the same parameter for the same reason. The
// chaining sentence stays because `cd` is still the only answer for the rest of what
// does not persist: environment variables, an activated virtualenv, a shell function.
func (*Bash) Description() string {
	return fmt.Sprintf(
		"Execute a bash command in the working directory. Returns combined stdout and stderr. "+
			"Output is truncated to the last %d lines or %dKB, whichever is hit first; when truncated, the full "+
			"output is written to a temp file whose path is reported. Optionally provide a timeout in seconds (default %.0f). "+
			"Each call is a fresh shell: cd and environment variables do not carry over. To run somewhere else, "+
			"pass workdir rather than prefixing cd; for anything else that does not persist, chain it into the one "+
			"command (. venv/bin/activate && pytest).",
		MaxLines, MaxBytes/1024, DefaultBashTimeout.Seconds())
}

// workdir resolves the requested directory to an absolute path, or returns an
// error the model can act on.
//
// Deliberately *not* run through resolve(): that is the path guard, and bash has
// never had one. Confining workdir while `cd /etc` in the command string still works
// would be a restriction that reads like a boundary without being one, and it would
// refuse the legitimate case this parameter is for — running a build in a checkout
// beside the session's directory. The real boundary for bash is the container, which
// is stated in the type comment and in the Guard's.
//
// What it does check is that the directory exists and is a directory, because the
// alternative is bash failing to start with "chdir: no such file or directory" and
// an exit code of -1: a message about the harness, not about the mistake. The error
// names the resolved path, not the argument, since a relative one is exactly the
// case where the model's mental model of the base directory is what went wrong.
func (t *Bash) workdir(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return t.Cwd, nil
	}
	dir := requested
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(t.Cwd, dir)
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workdir %s does not exist (resolved from %q against %s): %w",
			dir, requested, t.Cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workdir %s is a file, not a directory", dir)
	}
	return dir, nil
}

// ExecutionMode is Sequential, which is a deliberate divergence from pi (whose
// bash runs in parallel like everything else).
//
// Three reasons. Commands that matter here are already internally parallel
// (`go build`, `go test`, `make -j`), so overlapping them contends on the build
// cache instead of saving time. Two commands writing one output file corrupt it
// and nothing here can detect that. And under the approval gate, N parallel bash
// calls mean N approval prompts at once.
//
// Because a sequential call overlaps nothing in its batch, this also stops a read
// from racing a command that rewrites the file being read. That still holds now that
// a sequential tool no longer serializes its whole batch (agent.segments): what the
// promise was ever about is this call running alone, not its siblings running one at
// a time. And it is load-bearing here rather than decorative — bash writes through
// arbitrary shell commands, which never pass the per-path lock the file tools share,
// so being alone is the only protection there is.
func (*Bash) ExecutionMode() ExecutionMode { return Sequential }

// InputSchema keeps command as the only required field. Codex declares its
// equivalent of workdir required — a consequence of the strict-schema rule that
// every property must be listed, not a judgement that a caller should always supply
// one — and requiring it here would mean every single call carrying a redundant "."
// and every existing transcript becoming invalid against the schema.
func (*Bash) InputSchema() map[string]any {
	return object([]string{"command"}, map[string]any{
		"command": prop("string", "Bash command to execute"),
		"workdir": prop("string", "Directory to run in, relative to the working directory or "+
			"absolute. Defaults to the working directory; use this instead of a cd prefix."),
		"timeout": prop("number", "Timeout in seconds"),
	})
}

// Schema returns the JSON schema for bash tool using reflection.
func (*Bash) Schema() map[string]any {
	return GenerateSchema(reflect.TypeOf(bashArgs{}))
}

// Execute runs the command without an observer. It delegates so that there is a
// single implementation to get right; see StreamingTool.
func (t *Bash) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	return t.ExecuteStreaming(ctx, raw, nil)
}

// ExecuteStreaming runs the command, reporting output as it appears.
//
// bash is the only built-in that implements this, and the reason is `go test`:
// a command that takes a minute used to show nothing at all until it finished, so
// there was no way to tell a slow test suite from a hung one.
func (t *Bash) ExecuteStreaming(ctx context.Context, raw json.RawMessage, onPartial func(Partial)) (Result, error) {
	var a bashArgs
	if err := unmarshal(raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Command) == "" {
		return Result{}, fmt.Errorf("command must not be empty")
	}
	// Checked before anything runs, and before the timeout is even resolved: a
	// refused command must leave no trace at all.
	if err := t.Guard.Check(a.Command); err != nil {
		return Result{}, err
	}
	dir, err := t.workdir(a.Workdir)
	if err != nil {
		return Result{}, err
	}
	// The guard has to see workdir as well as the command, and this is the part of
	// adding the parameter that is not free. Its `cd` check works by parsing an
	// absolute target back out of the command text; a workdir field reaches the same
	// place without ever appearing there, so leaving it unchecked would turn a new
	// convenience into a hole in the one containment a subagent has.
	if err := t.Guard.CheckDir(dir, a.Workdir); err != nil {
		return Result{}, err
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = DefaultBashTimeout
	}
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout * float64(time.Second))
	}

	// CommandContext ties the child process to the cancellation chain, so Ctrl-C
	// on the agent kills the command instead of orphaning it.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command string is passed as a single argv element to bash -c, never
	// interpolated into a larger shell string, so there is nothing to escape.
	cmd := exec.CommandContext(runCtx, "bash", "-c", a.Command)
	cmd.Dir = dir
	// Kill the whole process group, not just bash.
	//
	// The default behaviour signals the direct child only, so `sleep 45` inside
	// `bash -c` survives, gets reparented to init, and keeps running after the
	// agent thinks it stopped. For a coding agent that is the common case, not an
	// edge one: cancelling a turn has to stop the `go test` it launched.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	// Give the group a moment to die on its own, then stop waiting on pipes a
	// stubborn grandchild might still hold open.
	cmd.WaitDelay = 2 * time.Second
	sink := newPartialSink(onPartial)
	cmd.Stdout = sink
	cmd.Stderr = sink

	started := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(started)
	// Flush before rendering, so the last lines are not held back by coalescing.
	// Run has returned, so nothing else will write to the sink.
	sink.close()

	output, note, details := t.render(sink.String())
	details.Command = a.Command
	// Recorded as the resolved absolute path rather than what the model typed, and
	// only when it differs from the session's directory: "." and "" describe the
	// default, and a card announcing the default on every call says nothing.
	if dir != t.Cwd {
		details.Workdir = dir
	}
	details.DurationMS = elapsed.Milliseconds()
	details.ExitCode = cmd.ProcessState.ExitCode() // -1 when the process never ran

	// Details travel with the error too: a failed command's exit code and output
	// are exactly what the UI needs to show.
	fail := func(status string) (Result, error) {
		return Result{Details: details}, errors.New(withStatus(output, note, status))
	}
	switch {
	case ctx.Err() != nil:
		return fail("Command aborted")
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		details.TimedOut = true
		return fail(fmt.Sprintf("Command timed out after %.0f seconds", timeout.Seconds()))
	case runErr != nil:
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			return fail(fmt.Sprintf("Command exited with code %d", exit.ExitCode()))
		}
		return fail(runErr.Error())
	}

	if output == "" {
		return Result{Text: "(no output)", Details: details}, nil
	}
	return Result{Text: output + note, Details: details}, nil
}

// render truncates from the tail and spills the full output to a temp file when
// it does not fit, so nothing is silently lost.
func (t *Bash) render(raw string) (output, note string, details BashDetails) {
	tr := TruncateTail(raw)
	details.TotalLines = tr.TotalLines
	if !tr.Truncated {
		return tr.Content, "", details
	}
	details.Truncated = true

	start := tr.TotalLines - tr.OutputLines + 1
	full, spilled := spill(raw)
	rest := "The rest could not be saved, so only the lines above are available."
	if spilled {
		details.FullOutputPath = full
		rest = "Full output: " + full
	}
	limit := ""
	if tr.By == "bytes" {
		limit = fmt.Sprintf(" (%s limit)", formatSize(MaxBytes))
	}
	return tr.Content, fmt.Sprintf("\n\n[Showing lines %d-%d of %d%s. %s]",
		start, tr.TotalLines, tr.TotalLines, limit, rest), details
}

// spill writes the whole output to a temp file, reporting a path only when all of
// it arrived.
//
// Every error here used to be discarded and the path handed over regardless, so a
// full disk produced a note promising the complete output at a file holding
// nothing — or holding half of it, which is worse, because the model can read that
// one and get a plausible answer from a fragment it was told was whole. The note
// is the only thing in the result that says output was truncated at all, which
// makes it the last place allowed to be wrong.
//
// Close is checked as carefully as the write: a buffered flush fails there, so
// ignoring it is precisely how a partial file passes for a complete one. The byte
// count needs no separate check, because io.Writer must report an error when it
// writes less than it was given — the same kind of contractual guarantee the
// context-editing code leans on for encoding/json, and stated here for the same
// reason, since a future switch to a buffered writer would end it silently.
//
// Honest note on coverage: the tests drive the creation failure (temp directory
// unusable) but not this branch. Making a write to a healthy temp file fail needs
// either a full disk or a swappable os.CreateTemp, and this package has no seam of
// that kind — the one configuration hook in the project, config.PathEnv, exists for
// real users and not for tests. So the check is here on the contract's authority,
// not on a test's.
//
// A file created but not completed is removed rather than left behind. Nothing
// points at it, and the next person looking through /tmp should not find a
// plausible-looking pi-go log that is quietly missing its middle.
func spill(raw string) (path string, ok bool) {
	f, err := os.CreateTemp("", "pi-go-bash-*.log")
	if err != nil {
		return "", false
	}
	_, werr := f.WriteString(raw)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		return "", false
	}
	return f.Name(), true
}

func withStatus(output, note, status string) string {
	if output == "" {
		return status
	}
	return output + note + "\n\n" + status
}
