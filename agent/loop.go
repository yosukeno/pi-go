// Package agent contains the loop. It is the whole program in one function:
// call the model, run whatever tools it asked for, feed the results back, repeat
// until the model stops asking for tools.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/wangy/pi-go/llm"
	"github.com/wangy/pi-go/tools"
)

// DefaultMaxTurns bounds a single Run. A model stuck in a tool-calling cycle
// would otherwise spend the whole budget unattended.
const DefaultMaxTurns = 50

// DefaultSoftTurns is the CLI's checkpoint interval (see softcap.go). It is a
// flag default only: New does not apply it, so every embedding that constructs
// Config directly — web, tests — keeps the zero value, which is off.
const DefaultSoftTurns = 10

// DefaultToolConcurrency caps how many calls in one batch run at once.
//
// A batch is rarely larger than ten, so eight is not a bottleneck in practice.
// It exists as a ceiling on file descriptors and buffered output: without it, a
// turn that asks for fifty reads would open fifty files and hold fifty truncated
// buffers at once.
const DefaultToolConcurrency = 8

type Agent struct {
	// client is an interface so switching models mid-session is one assignment
	// and the conversation carries over untouched.
	client       llm.Client
	registry     *tools.Registry
	schemas      []llm.ToolSchema
	systemPrompt string
	maxTurns     int
	// softTurns is the checkpoint interval, zero disables it. See softcap.go.
	softTurns int

	// gate is optional. Nil means every call runs, which is what the CLI wants:
	// -p has no human to ask and must stay scriptable.
	gate ToolGate
	// toolExecution can force a whole session sequential regardless of what the
	// individual tools declare.
	toolExecution   tools.ExecutionMode
	toolConcurrency int
	// reviewBudget bounds how long one batch may spend waiting for approvals.
	// Zero means unbounded, which is right when there is no gate.
	reviewBudget time.Duration

	// stagnationThreshold is how many identical tool results in a row count as stagnation.
	// Zero means disabled.
	stagnationThreshold int

	// budgets are run-level limits. Zero means unbounded.
	tokenBudget int64
	costBudget  float64
	timeBudget  time.Duration
	// price is the rate costBudget is measured against, or nil when none was
	// declared. Moves with the model on a switch: see SetPrice.
	price *llm.Price

	// runStartTime tracks when the current run started, for time-budget enforcement.
	runStartTime time.Time

	// messages is the conversation so far, kept across Run calls so a REPL gets
	// multi-turn behaviour for free.
	messages []llm.Message
	usage    llm.Usage
	// delegated is the part of usage that was spent inside subagents. A subset of
	// usage, not a sibling of it: see the rollup in Run.
	delegated llm.Usage
	timing    timingAccum

	// Fixed cost tracking for overhead metrics (RFC B1).
	fixedCostTokens    int64
	systemPromptTokens int64
	toolSchemaTokens   int64

	// lastInput is the provider's own token count for the most recent prompt. It is
	// the only authoritative measure of how full the window is: usage.Input is a
	// running total and grows quadratically with turn count, so it reads far past
	// the window on a long session.
	//
	// Written by the run goroutine beside the usage counters and read under the same
	// rule as Usage — after the run, on the goroutine that owns it.
	lastInput int64
	// measuredThrough is how many messages were in the prompt lastInput measured.
	// Together the two give promptTokens a measured baseline plus an estimated
	// increment, instead of an estimate of the whole history.
	measuredThrough int

	// contextEdit is the clearing policy; a zero Trigger disables it.
	contextEdit ContextEditConfig
	// cleared is the set of tool-result call ids already blanked in the prompt.
	// Carried between turns so a cleared result stays cleared — see editContext on
	// why anything else flaps the prompt prefix. Single-writer, like the counters.
	cleared map[string]bool
	// clearedTokens is how much the most recent pass took out of the prompt. Kept
	// so a composition snapshot can say what the difference between the history and
	// the prompt was: without it the estimate covers text the provider never
	// counted, and the ratio between them stops being a tokenizer ratio.
	clearedTokens int64

	// mu guards steering only: pending and running, nothing else.
	//
	// It deliberately does not guard messages. Steer is called from another
	// goroutine, but it only appends to pending; the run goroutine moves those
	// into messages at a turn boundary, so messages keeps its single writer and
	// the "do not read Messages() mid-run" rule stays true.
	mu      sync.Mutex
	pending []llm.Message
	running bool
}

type Config struct {
	Client       llm.Client
	Registry     *tools.Registry
	SystemPrompt string
	MaxTurns     int
	// SoftTurns turns the run up to MaxTurns into a series of checkpoints: every
	// this many turns the model is told where it stands and either finishes or
	// says what is left (see softcap.go). Zero disables it, and New applies no
	// default — the CLI owns that (DefaultSoftTurns), so embeddings constructing
	// Config directly, web among them, see no behaviour change.
	SoftTurns int
	// Gate is optional; nil disables review entirely.
	Gate ToolGate
	// ToolExecution forces sequential execution for the whole session when set to
	// tools.Sequential. The default respects each tool's own declaration.
	ToolExecution tools.ExecutionMode
	// ToolConcurrency bounds how many calls in one batch run at once.
	ToolConcurrency int
	// ReviewBudget bounds the time one batch may spend waiting for approvals
	// before refusing the rest of its calls unreviewed. Zero means unbounded.
	// Only meaningful alongside a Gate; see reviewBudgetSpent.
	ReviewBudget time.Duration

	// StagnationThreshold is how many identical tool results in a row count as stagnation.
	// Default is 3, meaning three identical results trigger a stop.
	StagnationThreshold int

	// TokenBudget is the maximum total tokens (input+output) allowed for this run.
	// Zero means unbounded. Reaching this limit is treated as failure.
	//
	// Input and Output only: CacheRead is a subset of Input and Reasoning is a subset
	// of Output, so neither is added. See checkBudgets.
	TokenBudget int64

	// CostBudget is the maximum estimated cost allowed for this run, in the same
	// unit Price is declared in. Zero means unbounded. Reaching this limit is
	// treated as failure.
	//
	// Needs Price to do anything. A caller setting this without one gets no ceiling,
	// which is why the CLI refuses that combination up front rather than leaving the
	// agent to ignore it.
	CostBudget float64

	// Price is the per-million-token rate CostBudget is measured against, or nil
	// when the model has no declared price. pi-go ships no built-in prices; see
	// llm.Price for why.
	Price *llm.Price

	// TimeBudget is the maximum wall-clock time allowed for this run.
	// Zero means unbounded. Reaching this limit is treated as failure.
	TimeBudget time.Duration

	// ContextEdit clears the payload of old tool results from the prompt once it
	// grows past ContextEdit.Trigger. A zero Trigger — the zero value — disables it,
	// so every existing caller keeps sending byte-identical prompts until it opts in.
	ContextEdit ContextEditConfig
}

func New(c Config) *Agent {
	if c.MaxTurns <= 0 {
		c.MaxTurns = DefaultMaxTurns
	}
	if c.ToolConcurrency <= 0 {
		c.ToolConcurrency = DefaultToolConcurrency
	}
	schemas := make([]llm.ToolSchema, 0, len(c.Registry.All()))
	for _, t := range c.Registry.All() {
		schemas = append(schemas, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	agent := &Agent{
		client:              c.Client,
		registry:            c.Registry,
		schemas:             schemas,
		systemPrompt:        c.SystemPrompt,
		maxTurns:            c.MaxTurns,
		softTurns:           c.SoftTurns,
		gate:                c.Gate,
		toolExecution:       c.ToolExecution,
		toolConcurrency:     c.ToolConcurrency,
		reviewBudget:        c.ReviewBudget,
		stagnationThreshold: c.StagnationThreshold,
		tokenBudget:         c.TokenBudget,
		costBudget:          c.CostBudget,
		price:               c.Price,
		timeBudget:          c.TimeBudget,
		contextEdit:         c.ContextEdit,
	}

	// Initialize fixed overhead metrics for RFC B1
	// Estimate token costs: rough approximation of 4 chars per token
	agent.systemPromptTokens = int64(len(c.SystemPrompt) / 4)
	agent.toolSchemaTokens = int64(estimateToolSchemaSize(schemas) / 4)
	agent.fixedCostTokens = agent.systemPromptTokens + agent.toolSchemaTokens

	return agent
}

func (a *Agent) Messages() []llm.Message { return a.messages }
func (a *Agent) Model() string           { return a.client.Model() }

// Usage is the accumulated token total across every turn so far.
//
// Only safe to call while no run is in flight: the loop adds to these counters
// from the run goroutine without a lock, and mu deliberately guards steering
// alone. A concurrent caller needs to take that on properly rather than assume
// this is safe — see the note in web/runner.go about the accessor that did.
func (a *Agent) Usage() llm.Usage { return a.usage }

// Delegated is the part of Usage that subagents spent. Same concurrency caveat.
//
// Reported separately because the two numbers are fixed by different decisions: a
// large Usage with a small Delegated means the conversation itself is expensive, and
// the answer is compaction or a cheaper model; the other way round means delegation
// is, and the answer is fewer or narrower tasks. A single total cannot tell them
// apart, and "3–10× the tokens" is the usual reason to look.
func (a *Agent) Delegated() llm.Usage { return a.delegated }

// Timing is the latency summary across every model call so far. Same concurrency
// caveat as Usage.
func (a *Agent) Timing() Timing { return a.timing.summary() }

// promptTokens is how large the prompt about to be sent is: the provider's own
// count for the last one, plus an estimate of everything appended since.
//
// Neither half alone is usable, and the reason is the failure this whole mechanism
// exists to prevent. The measured count is authoritative but always one turn stale,
// and the turn it is stale by is exactly the dangerous one — a batch of tool results
// can add a hundred thousand tokens between two measurements, so a trigger reading
// only the last measurement would sail past the window on the very turn that filled
// it. Estimating the whole history instead would drop the one number that is not a
// guess: the measured total already accounts for the system prompt, the tool
// schemas and the wire envelope, none of which llm.EstimateTokens models.
//
// So: measured baseline, estimated increment. The same split Codex's
// body_after_prefix scope makes, and for the same reason.
//
// The direction of the estimate's error was wrong here for a while, and it matters
// because the trigger is chosen from it. It reads *high*, not low: measured against
// the providers' own counts across 25 real sessions the ratio is 0.98 for ASCII and
// 0.83 for Chinese-heavy text, because both tokenizers were trained on Chinese and a
// two-character word costing six bytes is often one token. So four bytes per token is
// the conservative side on these providers, not the dangerous one — but it is still
// part estimate, which is why the trigger keeps a fifth of the window in hand rather
// than sitting on the ceiling. See ParseContextEdit.
func (a *Agent) promptTokens() int64 {
	if a.contextEdit.Trigger <= 0 {
		return 0 // nobody is asking; do not walk the history
	}
	if a.measuredThrough > len(a.messages) {
		// A caller replaced the history (SetMessages, or a rewind). The baseline no
		// longer describes a prefix of it, so estimate the whole thing.
		return llm.EstimateTokens(a.messages) + a.fixedCostTokens
	}
	return a.lastInput + llm.EstimateTokens(a.messages[a.measuredThrough:])
}

// LastInput is the provider's token count for the most recent prompt, or 0 before
// any call has returned. Same concurrency caveat as Usage.
//
// Distinct from Usage().Input on purpose, and the difference is the whole point:
// this one is how full the window is, that one is what the session has cost. Both
// front ends already tracked their own copy of this for their gauges; having it on
// the agent is what lets the measurement written to the transcript come from the
// same place the gauges read.
func (a *Agent) LastInput() int64 { return a.lastInput }

// forceClear runs a clearing pass that the provider has already justified,
// returning the smaller prompt and whether it is actually smaller than the one that
// was rejected.
//
// Three parts of the normal policy are overridden, and each for the same reason —
// they exist to avoid paying for clearing that might not be needed, and the
// rejection settles that question:
//
//   - The trigger, because it is compared against a figure that is part measured
//     and part estimated. The provider's count is neither.
//   - ClearAtLeast, which exists so a small saving does not buy a cache miss. A
//     cache miss is not the alternative here; a dead session is.
//   - Keep, reduced to the single most recent result. One rather than zero because
//     clearing the result the model is about to reason about would make the retry
//     answer a question whose evidence had just been removed. Zero is not even
//     expressible: editContext reads a non-positive Keep as its default, so the
//     guard is there rather than here — which is why the property is tested through
//     a forced pass rather than by inspecting this value.
//
// ExcludeTools is *not* overridden. It is the caller saying "never clear this", and
// a mechanism that discards an explicit instruction the moment it becomes
// inconvenient is worse than one that reports it could not help. If the exclusion
// is what makes the prompt too large, the rejection is returned and says so.
//
// ok is false when the pass freed nothing new, which is also what bounds this: the
// frozen set only grows, so a second overflow in the same run cannot loop — the
// second pass finds nothing to add and the error is returned.
func (a *Agent) forceClear() ([]llm.Message, bool) {
	before := len(a.cleared)
	cfg := ContextEditConfig{
		Trigger:      1,
		Keep:         1,
		ClearAtLeast: 1,
		ExcludeTools: a.contextEdit.ExcludeTools,
	}
	prompt, edit := editContext(a.messages, cfg, a.cleared, math.MaxInt64)
	if edit.cleared == nil || len(edit.cleared) <= before {
		return nil, false
	}
	a.cleared = edit.cleared
	a.clearedTokens = edit.ClearedTokens + edit.ClearedArgTokens
	return prompt, true
}

// ClearedFromPrompt is how many estimated tokens context editing removed from the
// prompt LastInput describes. Zero when clearing is off or did nothing. Same
// concurrency caveat as Usage.
//
// It exists so a composition snapshot can compare like with like: Compose measures
// the whole history, LastInput measured a prompt that clearing had shrunk, and the
// ratio of those two is only a tokenizer ratio once this is subtracted.
func (a *Agent) ClearedFromPrompt() int64 { return a.clearedTokens }

// OverheadTokens is the fixed per-request cost: system prompt plus tool schemas,
// estimated at four characters per token. Unlike Usage it is safe to read from
// any goroutine — fixedCostTokens is written once in New and never mutated
// (SetClient does not touch it), so there is nothing to race with.
func (a *Agent) OverheadTokens() int64 { return a.fixedCostTokens }

// SetMessages restores a previous conversation, e.g. from a session file.
//
// The measured baseline is reset with it: lastInput described a prompt this history
// is not a continuation of, so promptTokens has to estimate from scratch until the
// next call reports a real number. Leaving it would let a resumed session read as
// whatever the previous one last measured.
func (a *Agent) SetMessages(m []llm.Message) {
	a.messages = m
	a.lastInput, a.measuredThrough = 0, 0
}

// SetUsage restores the cost totals of a resumed session, so reopening a
// transcript does not zero the counters. Same single-writer caveat as Usage:
// call it before any run starts.
func (a *Agent) SetUsage(u, delegated llm.Usage) { a.usage, a.delegated = u, delegated }

// SetClient switches models between turns. The history stays as-is: every
// provider here speaks the same wire format, and thinking blocks are not
// replayed, so there is nothing model-specific left in the transcript to
// translate.
func (a *Agent) SetClient(c llm.Client) { a.client = c }

// SetPrice re-points the rate a cost budget is measured against, which a model
// switch has to do for the same reason SetContextEdit exists: the figure belongs to
// the model, not to the run, and carrying the old one over would measure the new
// model's tokens at the old model's rate.
//
// nil is a legitimate argument — it means the new model has no declared price — and
// it disables the comparison rather than treating the model as free. A caller that
// has a cost budget and does not want it silently dropped has to refuse the switch
// itself; the agent cannot, because by the time it is told, the client has changed.
// The CLI does refuse: see the /model command.
//
// Same rule as SetClient: between runs only, on the goroutine that owns the agent.
func (a *Agent) SetPrice(p *llm.Price) { a.price = p }

// CostBudget is the run's spend ceiling, and Price is the rate it is measured
// against. Both are read by the CLI to decide whether a model switch would silently
// remove a ceiling the user asked for.
func (a *Agent) CostBudget() float64 { return a.costBudget }

// Price is the rate currently in effect, or nil when the model has no declared one.
func (a *Agent) Price() *llm.Price { return a.price }

// SetContextEdit re-points the clearing policy, which a model switch has to do:
// "auto" is a fraction of the model's window, and the catalogue spans 262K to 1M.
// Left alone, moving from a 262K model to a 1M one would keep clearing at a fifth of
// the new window for no reason, and moving the other way would keep a trigger the new
// window cannot reach.
//
// Same rule as SetClient: between runs only, on the goroutine that owns the agent.
// The already-cleared set is deliberately kept — those results are gone from the
// prompt whatever the new threshold is, and restoring them would flap the prefix.
func (a *Agent) SetContextEdit(cfg ContextEditConfig) { a.contextEdit = cfg }

// ContextEditTrigger is the prompt size at which clearing begins, or 0 when it is
// off. Safe to read from any goroutine: it is only assigned between runs, by the
// goroutine that owns the agent.
//
// It exists for the interfaces rather than for the loop. A gauge drawn against fixed
// percentages of the window went permanently amber once the trigger moved to four
// fifths, because clearing holds occupancy just below it — so the thing a warning
// colour has to be measured against is this, not a constant.
func (a *Agent) ContextEditTrigger() int64 { return a.contextEdit.Trigger }

// estimateToolSchemaSize estimates the wire-format size of tool schemas.
// This replicates the conversion done in llm/convert.go:toWireTools.
func estimateToolSchemaSize(schemas []llm.ToolSchema) int {
	if len(schemas) == 0 {
		return 0
	}

	// Rough estimation: each tool becomes a JSON object with type, function, name, description, parameters
	// This is conservative and accounts for JSON overhead
	totalSize := 0
	for _, s := range schemas {
		// Approximate size calculation based on string lengths
		// {"type":"function","function":{"name":"...","description":"...","parameters":{...}}}
		nameSize := len(s.Name)
		descSize := len(s.Description)

		// JSON structure overhead + field names + parameter size (simplified)
		toolSize := 50 + nameSize + descSize + estimateParameterSize(s.InputSchema)
		totalSize += toolSize
	}
	return totalSize
}

// estimateParameterSize roughly estimates the JSON size of parameter schemas.
func estimateParameterSize(params map[string]any) int {
	if params == nil {
		return 30 // for "parameters":{}
	}

	// Basic estimation: count property names and assume some average size
	// This is intentionally conservative
	size := 40 // base overhead for parameters object
	for key, val := range params {
		size += len(key) + 10 // key + JSON overhead
		if str, ok := val.(string); ok {
			size += len(str)
		} else if m, ok := val.(map[string]any); ok {
			size += estimateParameterSize(m)
		}
	}
	return size
}

// Steer queues a message for delivery inside the run that is currently in
// flight. It reports whether there was one to queue.
//
// The message arrives after the current turn's tool calls have finished and
// before the next model call — the earliest point at which adding to the
// conversation is legal, since a tool_use must be answered by its tool_result and
// nothing else. This is what makes "no, do it the other way" possible without
// cancelling and losing the turn.
//
// A false return means the caller should start a new run instead. Checking
// Active() first would be a race; the answer has to come from the same lock that
// accepts the message.
func (a *Agent) Steer(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running {
		return false
	}
	a.pending = append(a.pending, llm.UserText(text))
	return true
}

// takePending hands the queued messages to the run goroutine.
func (a *Agent) takePending() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.pending
	a.pending = nil
	return out
}

func (a *Agent) hasPending() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.pending) > 0
}

// stopRunning closes the run to further steering and returns anything that was
// accepted but never delivered.
//
// There is always a window for that: a message can be accepted microseconds
// before the loop hits its turn limit or a transport error. Returning it rather
// than dropping it silently is the difference between the user being told and the
// user watching their message vanish.
func (a *Agent) stopRunning() []llm.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.running = false
	out := a.pending
	a.pending = nil
	return out
}

// Run appends the prompt and drives the loop until the model stops calling
// tools. The returned channel is closed after EventAgentEnd; the caller must
// drain it, which is what keeps the loop and its consumer in lockstep without
// any buffering policy.
func (a *Agent) Run(ctx context.Context, prompt string) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		a.run(ctx, prompt, out)
	}()
	return out
}

func (a *Agent) run(ctx context.Context, prompt string, out chan<- Event) {
	a.messages = append(a.messages, llm.UserText(prompt))

	a.mu.Lock()
	a.running = true
	a.mu.Unlock()

	// Initialize tracking for this run
	a.runStartTime = time.Now()
	var stagnationHistory []string

	// finish is the single exit: it closes the run to steering and reports
	// anything accepted but not delivered, so no terminal path can drop a message
	// without saying so.
	finish := func(e Event) {
		for _, m := range a.stopRunning() {
			e.Undelivered = append(e.Undelivered, m.Text())
		}
		// Stamped here rather than at each call site so that no exit path — turn
		// limit, budget, stagnation, transport error — can forget it.
		e.Timing = a.timing.summary()
		a.emitFinal(out, e)
	}

	a.emit(ctx, out, Event{Kind: EventAgentStart})

	var stop llm.StopReason
	for turn := 1; ; turn++ {
		// Check budgets
		if reason, detail := a.checkBudgets(); reason != "" {
			finish(Event{
				Kind: EventAgentEnd, EndReason: reason, Usage: a.usage,
				Err: fmt.Errorf("budget exceeded: %s", detail),
			})
			return
		}

		if turn > a.maxTurns {
			finish(Event{
				Kind: EventAgentEnd, EndReason: EndTurnLimit, Usage: a.usage,
				Err: fmt.Errorf("stopped after %d turns without a final answer", a.maxTurns),
			})
			return
		}

		// The soft cap is a checkpoint, not an exit: one message, and the loop
		// continues. It lands before steering so that when both arrive at the
		// same boundary, the human's words are the fresher ones.
		if notice, ok := a.softCheckpoint(turn); ok {
			a.messages = append(a.messages, llm.UserText(notice))
			a.emit(ctx, out, Event{Kind: EventSteer, Text: notice})
		}

		// Steering lands here: after the previous turn's tool results, before the
		// next model call. Any earlier and it would come between a tool_use and its
		// tool_result, which the API rejects.
		for _, m := range a.takePending() {
			a.messages = append(a.messages, m)
			a.emit(ctx, out, Event{Kind: EventSteer, Text: m.Text()})
		}

		// Context editing runs here: after steering has landed, immediately before the
		// request is built, on the history exactly as it will be sent. a.messages is
		// not touched — prompt is a view for this one call, so the transcript, the
		// session file and every audit keep the original output.
		prompt, edit := editContext(a.messages, a.contextEdit, a.cleared, a.promptTokens())
		if edit.cleared != nil {
			a.cleared = edit.cleared
		}
		// Overwritten rather than accumulated: it describes the gap between the
		// history and the one prompt about to be sent, which is exactly the prompt
		// LastInput will report on.
		a.clearedTokens = edit.ClearedTokens + edit.ClearedArgTokens

		turnStart := Event{Kind: EventTurnStart, Turn: turn}
		if edit.Cleared() {
			turnStart.ContextEdit = &edit
		}
		a.emit(ctx, out, turnStart)

		// Recorded before the call so promptTokens knows which messages the count it
		// is about to receive covers.
		a.measuredThrough = len(a.messages)

		resp, err := a.client.Stream(ctx, a.systemPrompt, prompt, a.schemas, func(d llm.Delta) {
			switch d.Kind {
			case llm.DeltaText:
				a.emit(ctx, out, Event{Kind: EventTextDelta, Text: d.Text})
			case llm.DeltaThinking:
				a.emit(ctx, out, Event{Kind: EventThinkingDelta, Text: d.Text})
			case llm.DeltaToolCallStart:
				// Name-bearing and fragment-less: it lets a consumer open a
				// preview card as early as possible, before any arguments arrive.
				a.emit(ctx, out, Event{Kind: EventToolArgs, ToolCallID: d.ToolID, ToolName: d.ToolName})
			case llm.DeltaToolCallArgs:
				a.emit(ctx, out, Event{Kind: EventToolArgs, ToolCallID: d.ToolID, Text: d.Text})
			}
		})
		if err != nil {
			// One failure is recoverable without the model's help: the prompt did
			// not fit. Clearing normally waits for a threshold, and a threshold is a
			// guess — it is compared against a number that is part measured and part
			// estimated, so it can be crossed without anyone noticing. The provider's
			// rejection is not a guess, so it gets to force a pass.
			//
			// This is the difference between a failed turn and a dead session. The
			// history only grows, so without this the next prompt in the same session
			// is larger again: the first overflow made every later message in that
			// session fail too, and 400 is not in llm/retry.go's retryable set, so
			// nothing else was going to intervene. Recovering here is what makes the
			// failure a hiccup rather than "start a new session".
			//
			// No event announces this. A second EventTurnStart for the same turn
			// would be wrong — web/hub.go mints the turn's message id there, on the
			// stated contract that one turn produces one assistant message — and the
			// right home is a new event kind, which is a change to the wire contract
			// rather than to this file. It is not silent in the end: editContext
			// reports the whole frozen set on every pass, not just what it added, so
			// the next turn's turn_start carries the larger figure in both front ends.
			if llm.IsContextOverflow(err) {
				if retryPrompt, ok := a.forceClear(); ok {
					prompt = retryPrompt
					// nil observer: the first attempt streamed nothing (an overflow is
					// rejected before generation), but a consumer that had already been
					// handed fragments must not receive a second copy of them.
					resp, err = a.client.Stream(ctx, a.systemPrompt, prompt, a.schemas, nil)
				}
			}
		}
		if err != nil {
			// A transport failure is the one thing the loop cannot hand back to
			// the model, so it ends the run.
			//
			// Still an overflow here means the branch above did not save it: either
			// clearing is off, or the frozen set had nothing left to add. Reported
			// apart from a transport error because the two are fixed by opposite
			// actions — this one by compacting or starting a session, that one by
			// waiting — and both are 400s on the same provider.
			reason := EndTransportError
			if llm.IsContextOverflow(err) {
				reason = EndContextOverflow
			}
			finish(Event{
				Kind: EventAgentEnd, EndReason: reason,
				StopReason: llm.StopError, Usage: a.usage, Err: err,
			})
			return
		}

		a.messages = append(a.messages, resp.Message)
		a.usage.Input += resp.Usage.Input
		a.usage.Output += resp.Usage.Output
		a.usage.CacheRead += resp.Usage.CacheRead
		a.usage.Reasoning += resp.Usage.Reasoning
		a.lastInput = resp.Usage.Input
		a.timing.add(resp.Timing)
		// This turn's own usage rides along, not the accumulated total: it is the
		// only signal that says how full the context window currently is. Timing is
		// per-call for the same reason: an average is for the end of the run.
		event := Event{
			Kind: EventMessage, Message: resp.Message,
			Usage: resp.Usage, CallTiming: resp.Timing,
		}

		// Calculate overhead metrics for RFC B1
		if resp.Usage.Input > 0 {
			event.OverheadMetrics = &OverheadMetrics{
				FixedCostTokens:  a.fixedCostTokens,
				TotalInputTokens: resp.Usage.Input,
				OverheadRatio:    float64(a.fixedCostTokens) / float64(resp.Usage.Input),
			}
		}

		a.emit(ctx, out, event)
		stop = resp.StopReason

		if stop == llm.StopAborted {
			break
		}

		calls := resp.Message.ToolCalls()
		if len(calls) == 0 {
			// The model is finished — unless the user queued something while it
			// was finishing. Breaking with a message still in the queue would
			// swallow it, and it is exactly the message someone typed because they
			// saw where the answer was heading.
			if a.hasPending() {
				continue
			}
			break
		}

		// Every tool_result for one assistant message must ride in a single user
		// message, in the same order as the tool_use blocks. Pre-allocating and
		// writing by index gives that ordering for free and needs no mutex, since
		// each goroutine owns one slot.
		//
		// Every tool_use must get a result even when the run is cancelled
		// mid-batch: an assistant message with an unpaired tool_use is rejected
		// by the API on the next request, which would make the session
		// unresumable.
		results := make([]llm.Block, len(calls))
		// delegated collects tokens spent by subagents, one slot per call for the
		// same reason results has one: each goroutine owns its own, so no lock is
		// needed. It is folded into the run's totals below, after the batch, on this
		// goroutine — the usage counters have a single writer by design (see the note
		// on Usage), and adding to them from a batch goroutine would be a race that
		// -race would catch and a reader would not.
		delegated := make([]llm.Usage, len(calls))
		if a.parallelBatch(calls) {
			// Review the whole batch before running any of it, one call at a
			// time. The gate is a person: reviewing concurrently means three
			// approval cards appearing at once, in an order nobody chose, each
			// with its own countdown. Execution still overlaps — that is what the
			// parallel batch is for — but the questions are asked in call order.
			//
			// Asking one at a time means the waits add up, so the phase gets a
			// budget: see reviewBudgetSpent for why the run cannot be allowed to
			// spend its whole life here.
			reviews := make([]reviewedCall, len(calls))
			started := time.Now()
			for i, call := range calls {
				if a.reviewBudgetSpent(started, i) {
					reviews[i] = settledCall(call, fmt.Sprintf(
						"Tool call %q was not executed: the batch spent its whole approval budget (%s) waiting on "+
							"earlier calls in the same batch. Re-issue it, or ask the user to approve sooner.",
						call.Name, a.reviewBudget))
					continue
				}
				reviews[i] = a.review(ctx, call, turn, stop)
			}
			a.runBatchParallel(ctx, reviews, results, delegated, out)
		} else {
			// A sequential batch interleaves review and execution on purpose. It
			// is the better order here: approving the second command after seeing
			// what the first one did beats approving both blind.
			for i, call := range calls {
				results[i] = a.runToolCall(ctx, a.review(ctx, call, turn, stop), &delegated[i], out)
			}
		}
		// A subagent's tokens are the parent's tokens: they were spent on the
		// parent's behalf, and without this a token budget would bound only the work
		// the agent did not delegate. The cost is one turn of latency — the budget is
		// checked at the top of the loop, so a child's spending lands on the next
		// check rather than this one, which is the price of keeping a single writer.
		// Counted twice on purpose, into two different questions. a.usage answers
		// "what has this run cost, all in", which is what a budget has to bound.
		// a.delegated answers "how much of that was spent by someone else", which is
		// the only way to tell an expensive conversation from an expensive delegation
		// after the fact — and the two are fixed by very different decisions.
		for _, u := range delegated {
			a.usage.Input += u.Input
			a.usage.Output += u.Output
			a.usage.CacheRead += u.CacheRead
			a.usage.Reasoning += u.Reasoning

			a.delegated.Input += u.Input
			a.delegated.Output += u.Output
			a.delegated.CacheRead += u.CacheRead
			a.delegated.Reasoning += u.Reasoning
		}
		a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: results})
		// The turn is now complete on disk terms: the assistant message and its
		// results can persist together, never one without the other.
		a.emit(ctx, out, Event{Kind: EventToolResults, Message: a.messages[len(a.messages)-1]})

		// Check for stagnation after tool results
		if a.stagnationThreshold > 0 && len(results) > 0 {
			// Compute hash of this turn's tool results
			resultHash := a.hashToolResults(results)
			stagnationHistory = append(stagnationHistory, resultHash)

			// Check if we're stagnating
			if stagnant, reason := a.detectStagnation(stagnationHistory); stagnant {
				finish(Event{
					Kind: EventAgentEnd, EndReason: EndStagnation, Usage: a.usage,
					Err: fmt.Errorf("stagnation detected: %s", reason),
				})
				return
			}
		}

		if ctx.Err() != nil {
			stop = llm.StopAborted
			break
		}
	}

	finish(Event{
		Kind: EventAgentEnd, EndReason: endReasonForStop(stop),
		StopReason: stop, Usage: a.usage,
	})
}

// parallelBatch reports whether this batch may overlap. Following pi: one
// sequential tool in the batch serializes all of it.
func (a *Agent) parallelBatch(calls []llm.Block) bool {
	if len(calls) < 2 || a.toolExecution == tools.Sequential {
		return false
	}
	for _, c := range calls {
		t, ok := a.registry.Get(c.Name)
		// An unknown tool fails instantly, but treat it as sequential anyway
		// rather than guessing about a tool we know nothing about.
		if !ok || t.ExecutionMode() == tools.Sequential {
			return false
		}
	}
	return true
}

// runBatchParallel executes already-reviewed calls with a bounded number in
// flight.
//
// A plain semaphore rather than errgroup: the only feature needed is the limit,
// and errgroup's error propagation is actively wrong here. Cancelling siblings
// when one call fails would leave tool_use blocks without results.
func (a *Agent) runBatchParallel(
	ctx context.Context, reviews []reviewedCall, results []llm.Block,
	delegated []llm.Usage, out chan<- Event,
) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, a.toolConcurrency)
	for i, rv := range reviews {
		wg.Add(1)
		go func(i int, rv reviewedCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = a.runToolCall(ctx, rv, &delegated[i], out)
		}(i, rv)
	}
	wg.Wait()
}

// reviewBudgetSpent reports whether a batch has spent its approval budget and
// the calls still queued should be refused unreviewed.
//
// Reviewing one call at a time makes the waits additive, and each one can run to
// the gate's own timeout. Eight unanswered calls at five minutes apiece outlast a
// thirty-minute run, and then the run's own deadline kills everything: every
// call comes back "aborted", the turn is lost, and nothing explains why.
//
// Spending the budget instead refuses the remainder with a reason the model can
// act on and leaves the run alive. The bound is the budget plus at most one gate
// timeout, since a review already under way is allowed to finish.
//
// i > 0 because the first call must always get its question asked: a budget that
// can refuse everything unreviewed is just a broken gate.
func (a *Agent) reviewBudgetSpent(started time.Time, i int) bool {
	return a.reviewBudget > 0 && i > 0 && time.Since(started) >= a.reviewBudget
}

// settledCall builds a call that was decided without running, carrying the text
// the model will read as its result.
func settledCall(call llm.Block, msg string) reviewedCall {
	return reviewedCall{call: call, args: call.Input, settled: true, output: msg, isError: true}
}

// reviewedCall is one tool call after review: either a decision that settles it
// without running anything, or a tool plus the arguments to run it with.
type reviewedCall struct {
	call llm.Block
	tool tools.Tool
	// args are what will execute and what tool_start reports. The gate's rewrite
	// lands here, which is why the event cannot be emitted before review.
	args json.RawMessage

	// settled marks a call decided during review: an unknown tool, arguments that
	// may be truncated, a refusal, or a cancellation. output and isError carry the
	// result the model will see.
	settled bool
	output  string
	isError bool
}

// review decides whether and how a call will run, without emitting anything and
// without touching the filesystem.
//
// Splitting this out from execution is what lets a parallel batch ask its
// approval questions one at a time while still running the work concurrently.
// It also means the phase that can block on a human is the phase that does no
// I/O, which is easier to reason about.
//
// Nothing here is fatal: an unknown tool, malformed arguments, or a refusal all
// become text the model can read and react to. Turning these into hard errors is
// the classic way to make an agent brittle.
func (a *Agent) review(ctx context.Context, call llm.Block, turn int, stop llm.StopReason) reviewedCall {
	rv := reviewedCall{call: call, args: call.Input}
	if len(rv.args) == 0 {
		rv.args = json.RawMessage("{}")
	}
	settle := func(msg string) reviewedCall {
		rv.settled, rv.output, rv.isError = true, msg, true
		return rv
	}

	if stop == llm.StopMaxTokens {
		// The response was cut off mid-stream, so the arguments of every tool
		// call in it may be silently incomplete. None are safe to run.
		return settle(fmt.Sprintf("Tool call %q was not executed: the response hit the output token limit, so its "+
			"arguments may be truncated. Re-issue the call with complete arguments.", call.Name))
	}
	if ctx.Err() != nil {
		return settle("Operation aborted")
	}

	tool, ok := a.registry.Get(call.Name)
	if !ok {
		return settle(fmt.Sprintf("Tool %q not found", call.Name))
	}
	rv.tool = tool

	// Validate before the gate, not after. A call with a missing field cannot run
	// whatever the human says, and asking someone to approve it wastes the one
	// thing the gate is spending: their attention.
	if err := tools.ValidateArgs(tool, rv.args); err != nil {
		return settle(fmt.Sprintf("Tool %q was not executed: %s", call.Name, err))
	}

	// A gate that needs to tell the UI a call is awaiting approval does so through
	// its own channel; the loop only consumes the verdict.
	if a.gate != nil {
		d := a.gate.Review(ctx, GateRequest{
			CallID: call.ID, ToolName: call.Name, Args: rv.args, Turn: turn,
		})
		if !d.Allow {
			reason := d.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			// A refusal is information for the model, not a fatal error. It can
			// explain itself, try another approach, or ask the user. Ending the
			// run instead would make one click cost the whole turn.
			return settle(reason)
		}
		if d.Args != nil {
			rv.args = d.Args
		}
	}
	return rv
}

// runToolCall executes a reviewed call and reports it, returning its result
// block. It never returns an error: failures are results.
//
// tool_start is emitted here rather than during review so that it carries the
// arguments that actually execute, including any the reviewer rewrote.
// delegatedUsage reports what a tool spent on the model's behalf inside another
// process. Only the subagent tool has any, and it reports it in its details rather
// than in its text: the model reads the answer, the accounting is the harness's
// business.
func delegatedUsage(details any) llm.Usage {
	d, ok := details.(tools.SubagentDetails)
	if !ok {
		return llm.Usage{}
	}
	return llm.Usage{Input: d.InputTok, Output: d.OutTok}
}

func (a *Agent) runToolCall(ctx context.Context, rv reviewedCall, delegated *llm.Usage, out chan<- Event) llm.Block {
	a.emit(ctx, out, Event{
		Kind: EventToolStart, ToolCallID: rv.call.ID,
		ToolName: rv.call.Name, ToolArgs: string(rv.args),
	})

	output, isError := rv.output, rv.isError
	// details are for interfaces only and never enter the conversation. A tool may
	// supply them alongside an error, which is how a failed command still reports
	// its exit code to the UI.
	var details any

	switch {
	case rv.settled:
	case ctx.Err() != nil:
		// Reviewing the batch may have taken a while — a whole gate timeout, in the
		// worst case. Work nobody is waiting for should not be started.
		output, isError = "Operation aborted", true
	default:
		res, err := a.execute(ctx, rv, out)
		details = res.Details
		// Recorded even when the call failed: a subagent that crashed after four
		// turns still spent those tokens, and a budget that ignored them would be
		// wrong in the direction that costs money.
		if delegated != nil {
			*delegated = delegatedUsage(details)
		}
		if err != nil {
			output, isError = err.Error(), true
		} else {
			output = res.Text
		}
	}

	a.emit(ctx, out, Event{
		Kind: EventToolEnd, ToolCallID: rv.call.ID, ToolName: rv.call.Name,
		ToolOutput: output, ToolDetails: details, IsError: isError,
	})
	return llm.Block{
		Type:      llm.BlockToolResult,
		ToolUseID: rv.call.ID,
		Text:      output,
		IsError:   isError,
		Details:   marshalDetails(details),
	}
}

// execute runs the tool, forwarding output as it appears when the tool can
// produce it incrementally.
//
// The type assertion is the whole mechanism: a tool that does not implement
// StreamingTool is called exactly as before, so adding this changed nothing for
// six of the seven built-ins.
//
// Fragments go through emit, which means they can be dropped if the consumer has
// stopped reading — the right trade for output that is only ever a preview. The
// settled output rides on tool_end, so a consumer that missed every fragment
// still ends up correct.
func (a *Agent) execute(ctx context.Context, rv reviewedCall, out chan<- Event) (tools.Result, error) {
	st, ok := rv.tool.(tools.StreamingTool)
	if !ok {
		return rv.tool.Execute(ctx, rv.args)
	}
	return st.ExecuteStreaming(ctx, rv.args, func(p tools.Partial) {
		a.emit(ctx, out, Event{
			Kind: EventToolPartial, ToolCallID: rv.call.ID,
			ToolName: rv.call.Name, Text: p.Text, ToolFrame: p.Frame,
		})
	})
}

// marshalDetails renders a tool's structured payload for the transcript.
//
// Details ride in the message so that they survive to the session file: a diff
// and an exit code are the parts of a transcript worth looking at afterwards, and
// before this they existed only in the live event stream. They still never reach
// the model — see llm.Block.Details.
//
// A payload that will not marshal is dropped rather than reported. It is display
// data; losing a rendering is not worth failing a tool call that already
// succeeded.
func marshalDetails(details any) json.RawMessage {
	if details == nil {
		return nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return raw
}

// emitFinal delivers the terminating event with a plain blocking send.
//
// It must not go through emit: once ctx is cancelled, emit's second select is a
// coin flip between delivering and dropping, and dropping this particular event
// means the consumer never learns the run ended. A UI would sit there showing a
// turn in progress forever after a single cancel. Blocking is safe because the
// contract is that callers drain the channel, and this is the last event on it.
func (a *Agent) emitFinal(out chan<- Event, e Event) { out <- e }

// emit sends an event without risking a permanently blocked goroutine if the
// consumer has stopped reading. The non-blocking attempt comes first so that a
// consumer which is still draining after a Ctrl-C still receives the event
// instead of losing a coin flip against ctx.Done.
func (a *Agent) emit(ctx context.Context, out chan<- Event, e Event) {
	select {
	case out <- e:
		return
	default:
	}
	select {
	case out <- e:
	case <-ctx.Done():
	}
}

// hashToolResults computes a hash of tool results for stagnation detection.
// It hashes tool names, arguments, and results to detect when the same actions
// produce the same outcomes repeatedly.
func (a *Agent) hashToolResults(results []llm.Block) string {
	h := sha256.New()
	for _, r := range results {
		// Include tool name, input args, and output text
		fmt.Fprintf(h, "%s|%s|%s|%v", r.ToolUseID, r.Name, r.Text, r.IsError)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// detectStagnation checks if the agent is stuck in a loop by looking at
// recent tool result hashes. It returns true if the same result pattern
// repeats threshold times consecutively.
func (a *Agent) detectStagnation(history []string) (stagnant bool, reason string) {
	if len(history) < a.stagnationThreshold {
		return false, ""
	}

	// Check if the last N results are identical
	lastHash := history[len(history)-1]
	consecutiveCount := 0

	// Count backwards from the most recent result
	for i := len(history) - 1; i >= 0; i-- {
		if history[i] == lastHash {
			consecutiveCount++
			if consecutiveCount >= a.stagnationThreshold {
				return true, fmt.Sprintf("%d identical tool results in a row", consecutiveCount)
			}
		} else {
			// Pattern broken
			break
		}
	}

	return false, ""
}

// checkBudgets verifies that token, cost, and time budgets haven't been exceeded.
// It returns the EndReason to stop with and a detail string for the message, or a
// zero EndReason when every budget still has room.
//
// The reason is returned rather than derived from the detail text because the
// caller's only other option is matching prose, and which budget was hit changes
// what a driver should do about it: a token budget is refilled by spending more, a
// time budget by waiting.
func (a *Agent) checkBudgets() (EndReason, string) {
	// Input + Output, and deliberately not + Reasoning.
	//
	// Reasoning is a subset of Output (llm.Usage says so), so adding it charged
	// thinking tokens twice. That was the bug this line used to have, and it was the
	// wrong direction in a way worth naming: a thinking-heavy run hit its ceiling
	// early, so the budget looked like it was working while actually stopping runs
	// that had room left. CacheRead is a subset of Input and was already, correctly,
	// left out — so the pattern was known here and applied to only one of the two.
	if a.tokenBudget > 0 {
		totalTokens := a.usage.Input + a.usage.Output
		if totalTokens >= a.tokenBudget {
			return EndTokenBudget, fmt.Sprintf("token budget exceeded: %d/%d", totalTokens, a.tokenBudget)
		}
	}

	// The nil check is load-bearing in the plain sense — Cost is a method on a value
	// and a.price is a pointer, so dropping it panics — and it is worth saying that
	// this is the *only* thing it does. Substituting a zero Price for nil would not
	// stop a run either: a zero rate makes every cost zero, and zero never exceeds a
	// positive budget. The two are indistinguishable from inside this function.
	//
	// So the guarantee that an unpriced run cannot proceed uncapped does not live
	// here. It lives in checkCostBudget, which refuses to start at all. This function
	// only declines to invent a number, which is the most it can do once a run is
	// already going.
	if a.costBudget > 0 && a.price != nil {
		spent := a.price.Cost(a.usage)
		if spent >= a.costBudget {
			// %g rather than a fixed number of decimal places: the unit is whatever the
			// user's prices were written in (llm.Price declares no currency), so pi-go
			// has no basis for deciding that two digits after the point is the right
			// precision, and a rate quoted in fractions of a cent needs more.
			return EndCostBudget, fmt.Sprintf("cost budget exceeded: %g/%g", spent, a.costBudget)
		}
	}

	// Check time budget
	if a.timeBudget > 0 {
		elapsed := time.Since(a.runStartTime)
		if elapsed >= a.timeBudget {
			return EndTimeBudget, fmt.Sprintf("time budget exceeded: %v/%v", elapsed, a.timeBudget)
		}
	}

	return "", ""
}
