package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/wangy/pi-go/llm"
)

// Context editing: dropping the payload of old tool results from the prompt while
// leaving the transcript whole.
//
// The shape follows Anthropic's clear_tool_uses_20250919 strategy, because that is
// the one design here with a published contract and defaults rather than a blog
// post: clear the oldest tool results once the prompt crosses a token threshold,
// keep the most recent few intact, keep the tool *call* visible and blank only its
// result, replace each cleared result with placeholder text so the model knows it
// was removed, and refuse to act unless the clearing frees enough to be worth the
// cache it invalidates.
//
// Why mechanical rather than a summary: the only controlled comparison available
// (Lindenbauer et al., "The Complexity Trap", DL4C@NeurIPS'25, SWE-bench Verified
// across five model configurations) found that simply omitting old observations
// halves cost against the raw agent while matching — sometimes slightly beating —
// the solve rate of LLM summarisation. It is also the cheaper mechanism by every
// measure that matters here: no extra model call, no summary that can be wrong in
// ways no test can catch, and behaviour a unit test can pin exactly.
//
// # This is a view, not an edit
//
// The transcript is never touched. Editing happens on a copy built for one request,
// which is the same division Anthropic draws — their strategy runs server-side and
// the docs are explicit that the client keeps its full, unmodified history. Here it
// means the session file, `-resume`, the web diff view and every audit keep the
// original output while the model sees the placeholder.
// There is deliberately no DefaultContextEditTrigger constant. Anthropic's default
// is a fixed 100,000, and one used to sit here justified as "half of the default
// model's 200K window" — a derivation that stopped being true when glm-5.2's
// catalogue entry was corrected to 1M, at which point the constant named a tenth of
// the window instead of a half. It never had a caller either: ParseContextEdit
// resolves "auto" from the live window precisely because copying the absolute number
// is a different policy on a catalogue spanning 262K to 1M.
//
// Stated rather than silently absent, because the obvious repair to a missing
// default is to add one back, and a hard-coded trigger is the thing to avoid here.
const (
	// DefaultContextEditKeep is Anthropic's default: how many of the most recent
	// tool results survive untouched. Three is aggressive and is theirs; the working
	// set of a coding turn is usually the last read and the last command.
	DefaultContextEditKeep = 3

	// DefaultContextEditClearAtLeast is the floor on how much one pass must free.
	//
	// Anthropic leaves this unset by default but documents what it is for: clearing
	// invalidates the cached prompt prefix, so a pass that frees a little buys a
	// cache miss for nothing. pi-go sets it, because the cost of that miss is worse
	// here — both of its providers cache implicitly with no API to edit a cached
	// prefix, and on Kimi a miss is billed at roughly ten times the hit rate
	// (Zhipu's own docs put its discount at about half, so the penalty differs by
	// provider by a factor of five). This value is a guess pending real session
	// data; it is the parameter most worth calibrating first.
	DefaultContextEditClearAtLeast = 8_000
)

// AutoTriggerNumerator and AutoTriggerDenominator are the fraction of the model's
// window "auto" clears at: four fifths.
//
// Exported because the browser's context gauge has to colour its bands against this
// same number — with clearing on, occupancy settles just below the trigger, so a
// gauge using fixed percentages would sit permanently in its warning colour and stop
// meaning anything.
const (
	AutoTriggerNumerator   = 4
	AutoTriggerDenominator = 5
)

// ParseContextEdit resolves the -context-edit flag against the model's window.
//
// "auto" scales with the window rather than taking Anthropic's literal 100,000:
// their default is fixed at a number that happens to be half of the 200K window
// their models have, and pi-go's catalogue spans 262K to 1M, where a fixed 100,000
// would start clearing at a tenth of the window.
//
// # Why four fifths and not their half
//
// Half was theirs, and it is too early here for two measured reasons and one
// structural one.
//
// The estimate this is compared against errs *high*: measured against the
// providers' own counts across 25 real sessions, the ratio runs 0.98 for ASCII and
// 0.83 for Chinese-heavy text (see session.Composition.Calibration). So a trigger at
// half the window in estimated tokens fires at roughly 42%–49% of it measured —
// clearing would begin while more than half the window is genuinely free.
//
// And clearing is not free. It rewrites part of the prompt, so the cached prefix is
// invalidated: on Kimi a cache miss is billed at roughly ten times the hit rate, on
// Zhipu about twice. Paying that to reclaim room nobody needed yet is pure loss, and
// the model may re-read files it could still see.
//
// The structural reason is the one that changed. When half was chosen, running out
// of context was fatal and permanent: the provider answered 400, retry.go did not
// retry 4xx, and the history did not shrink — so the same session failed on its next
// message forever. Being early was cheap insurance against a cliff. That cliff is
// gone; a rejection now forces a clearing pass and retries once (see forceClear), so
// a late trigger costs one wasted call instead of a session.
//
// # What the remaining fifth is for
//
// Three things, and they are why this is not nine tenths. The provider's own output
// counts against the same limit on Kimi ("prompt tokens + max_tokens exceeds the
// model specification"), so MaxTokens has to fit in the margin. The number compared
// against the trigger is part measured and part estimated (see Agent.promptTokens),
// so it is not to be trusted within a few percent. And clearing happens *before* a
// call, while the turn it starts can still add tool output to the history.
//
// On every catalogue entry today the margin is 3× to 12× MaxTokens; a test in the
// root package asserts that, because the margin is the assumption a new model with a
// large output cap and a small window would break.
//
// A model that is not in the catalogue has no window to take a fraction of, so auto
// disables rather than guessing: the run still finishes either way, and silence beats
// a made-up threshold.
func ParseContextEdit(spec string, window int) (ContextEditConfig, error) {
	switch spec {
	case "off", "0":
		return ContextEditConfig{}, nil
	case "", "auto":
		if window <= 0 {
			return ContextEditConfig{}, nil
		}
		return ContextEditConfig{
			Trigger: int64(window) * AutoTriggerNumerator / AutoTriggerDenominator,
		}, nil
	}
	n, err := strconv.ParseInt(spec, 10, 64)
	if err != nil || n < 0 {
		return ContextEditConfig{}, fmt.Errorf(
			"-context-edit %q: want a token count, \"auto\" (four fifths of the model's window) or \"off\"", spec)
	}
	if n == 0 {
		return ContextEditConfig{}, nil
	}
	return ContextEditConfig{Trigger: n}, nil
}

// ContextEditConfig is one session's clearing policy. A zero Trigger disables
// clearing entirely, which is the default everywhere it is not set on purpose.
type ContextEditConfig struct {
	// Trigger is the prompt size in tokens at which clearing begins. Zero disables.
	Trigger int64
	// Keep is how many of the most recent tool results survive untouched. Zero
	// means DefaultContextEditKeep — never "keep none", because a working set of
	// zero would clear the result the model is about to reason about.
	Keep int
	// ClearAtLeast is the minimum tokens a pass must free before it is applied at
	// all. Zero means DefaultContextEditClearAtLeast.
	ClearAtLeast int64
	// ExcludeTools names tools whose results are never cleared.
	ExcludeTools []string
}

// ContextEdit reports what one pass did, for the interfaces and for the log. It
// mirrors the applied_edits Anthropic returns, which is the minimum needed to
// answer "why did the context suddenly get smaller".
type ContextEdit struct {
	// ClearedResults is how many tool results are blanked in the prompt just sent.
	ClearedResults int `json:"cleared_results"`
	// ClearedTokens is roughly how many tokens that freed from tool results.
	ClearedTokens int64 `json:"cleared_tokens"`
	// ClearedArgs is how many tool calls had a payload argument blanked, and
	// ClearedArgTokens is what that freed. See payloadArgs for which arguments
	// qualify and why most do not.
	//
	// These two are disjoint from ClearedResults and ClearedTokens, not a subset of
	// them: a result and its call are different bytes in the prompt, so the pairs
	// add. Said explicitly because this codebase has three fields of the opposite
	// kind — Usage.CacheRead inside Input, Stats.Delegated inside Usage,
	// Composition as a snapshot rather than a delta — and a reader who has met
	// those is right to check before adding anything here.
	ClearedArgs      int   `json:"cleared_args,omitempty"`
	ClearedArgTokens int64 `json:"cleared_arg_tokens,omitempty"`
	// PromptTokens is the size that triggered it, before clearing.
	PromptTokens int64 `json:"prompt_tokens"`
	// cleared is the set of call ids now blanked. Carried so the next turn can
	// blank the same ones again; see the monotonicity note in editContext.
	cleared map[string]bool
}

// Cleared reports whether the pass did anything at all. A call whose arguments
// were blanked without its result qualifying cannot happen today — the two are
// decided together, see editContext — but callers should not have to rely on that
// to ask the question.
func (c ContextEdit) Cleared() bool { return c.ClearedResults > 0 || c.ClearedArgs > 0 }

// candidate is one clearable tool result, located by where it sits, together with
// the call that produced it.
type candidate struct {
	msg, blk int
	// useMsg and useBlk locate the paired tool_use. Needed because a payload
	// argument lives on the call, which is in an earlier message than its result.
	// -1 when the call did not survive in the history.
	useMsg, useBlk int
	callID         string
	tool           string
	tokens         int64
	lines          int
	bytes          int
	// newInput is the call's arguments with their payload blanked, precomputed so
	// the token arithmetic that gates clearing and the rewrite that performs it can
	// never disagree. Nil when this call has no payload worth taking out.
	newInput  json.RawMessage
	argTokens int64
	argBytes  int
}

// editContext returns the history to send and what was cleared, leaving the input
// untouched.
//
// # Monotonicity, and why it needs state
//
// frozen is the set of results a previous pass already cleared, and every one of
// them is cleared again whatever the current size says. Without that, a pass that
// brings the prompt back under the trigger would let the next turn restore the
// original text, and the turn after that clear it again — flapping the prompt
// prefix and paying a cache miss on every cycle. Anthropic gets this for free:
// their docs describe a cleared result's fate as frozen once seen, and identical
// replacement text every time so the bytes stay stable. This is that rule, made
// explicit because pi-go has to carry the set itself.
//
// The placeholder is therefore a pure function of the original's shape, never of
// the clock or of how many passes have run.
func editContext(msgs []llm.Message, cfg ContextEditConfig, frozen map[string]bool, promptTokens int64) ([]llm.Message, ContextEdit) {
	if cfg.Trigger <= 0 {
		return msgs, ContextEdit{}
	}
	keep := cfg.Keep
	if keep <= 0 {
		keep = DefaultContextEditKeep
	}
	floor := cfg.ClearAtLeast
	if floor <= 0 {
		floor = DefaultContextEditClearAtLeast
	}
	excluded := make(map[string]bool, len(cfg.ExcludeTools))
	for _, name := range cfg.ExcludeTools {
		excluded[name] = true
	}

	cands, newestTodo := clearable(msgs, excluded)

	// Everything already cleared stays cleared, in place, whatever the size is now.
	chosen := make(map[string]bool)
	for _, c := range cands {
		if frozen[c.callID] {
			chosen[c.callID] = true
		}
	}

	// A superseded task list is cleared regardless of the trigger and regardless of
	// the keep window, because it is not an event that happened but a state that has
	// been replaced. Only the newest one is the plan, and that one is never cleared
	// even when it is the oldest thing in the history — it is the only record of
	// what the work was that outlives a compaction boundary.
	for _, c := range cands {
		if c.tool == todoToolName && c.callID != newestTodo {
			chosen[c.callID] = true
		}
	}

	// New clearing only once the prompt is over the trigger, oldest first, sparing
	// the most recent `keep` results.
	//
	// The pinned task list is left out of this arithmetic entirely rather than merely
	// skipped: it is not part of the working set the keep window is protecting, so
	// letting it occupy one of those slots would silently shrink the window by one.
	var added int64
	if promptTokens >= cfg.Trigger {
		evictable := make([]candidate, 0, len(cands))
		for _, c := range cands {
			if c.callID != newestTodo {
				evictable = append(evictable, c)
			}
		}
		limit := len(evictable) - keep
		for i := 0; i < limit; i++ {
			c := evictable[i]
			if chosen[c.callID] {
				continue
			}
			chosen[c.callID] = true
			added += c.tokens
		}
	}

	// The floor gates escalation, not the frozen set: refusing to re-clear what is
	// already cleared would restore it and cause exactly the flapping the frozen set
	// exists to prevent.
	if added > 0 && added < floor {
		for _, c := range cands {
			if !frozen[c.callID] && c.tool != todoToolName {
				delete(chosen, c.callID)
			}
		}
		added = 0
	}
	if len(chosen) == 0 {
		return msgs, ContextEdit{}
	}

	edited, stat := apply(msgs, cands, chosen)
	stat.PromptTokens = promptTokens
	stat.cleared = chosen
	return edited, stat
}

// todoToolName is the task list, which this file has to know about by name because
// its results are state rather than events. See the superseded rule in editContext.
const todoToolName = "todo"

// payloadArgs names the argument fields that carry a payload rather than a
// description of the call, per tool.
//
// Anthropic's clear_tool_inputs defaults to false and their reason is sound: the
// record that a call happened, and against what, is what lets the model decide
// whether to re-issue it. That reason holds for read, ls, find, grep and bash,
// where the arguments *are* the description — a path, a glob, a pattern, a command
// line. Blanking those would remove the only thing worth keeping.
//
// It does not hold for write and edit. There the large argument is not a
// description of the call, it is the payload — and it is the one payload
// guaranteed to exist somewhere else, because the tool's whole purpose was to put
// it on disk. Reading the file back is both cheaper and more truthful than
// carrying a copy that stops matching the file the moment anything else edits it.
//
// This was measured, not guessed. Across the 100 sessions this project had
// accumulated, two were 83% and 96% tool-call arguments — 42,328 estimated tokens
// of write content in one of them — and clearing could touch none of it, because
// results were all it looked at. Those two sessions are neither of the shapes the
// composition report was built to tell apart: not tool output, not conversation.
//
// edit's oldText is included deliberately rather than incidentally. After a
// successful edit that text is by definition no longer in the file, so it can
// never match again; keeping it preserves the one string in the call that has
// become unusable. A *failed* edit is different and is already spared, because
// clearable skips results that are errors — there, oldText is exactly what the
// model needs to correct.
var payloadArgs = map[string][]string{
	"write": {"content"},
	"edit":  {"edits", "oldText", "newText"},
}

// minPayloadBytes is the size a single field must exceed before it is worth
// blanking. Below it the placeholder is comparable to the payload, so the one
// certain effect is a disturbed prompt prefix — the cost ClearAtLeast exists to
// avoid, at field granularity.
//
// The exact value is not load-bearing: the measurement above says write content is
// the whole of this problem in practice (edit arguments totalled 243 tokens across
// all 100 sessions), and write content is never near this size.
const minPayloadBytes = 256

// clearArgs returns the call's arguments with their payload fields blanked, and
// whether anything changed.
//
// Structure and types survive exactly: an edits array stays an array of objects
// with both string fields present. Two reasons. The history is a record of what the
// model asked for and a reshaped record is a false one; and a provider that
// validated historical tool_use arguments against the schema would reject anything
// else, which is not a bet worth taking for a few bytes.
//
// The result is a pure function of the input, which the frozen set requires: the
// placeholder carries the blanked field's own size and nothing about the clock or
// how many passes have run, and encoding/json orders map keys, so the same call
// blanked on two turns marshals to identical bytes.
func clearArgs(in json.RawMessage, fields []string) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(in, &obj); err != nil {
		// Not a JSON object. Models do send malformed arguments — pi-go wraps
		// unparseable tool input as a string rather than dropping the call — and one
		// that could not be read is left byte-for-byte as it arrived: rewriting what
		// we did not understand is how a record stops being evidence.
		//
		// This branch is belt to the structure's braces, not the thing that makes it
		// true: encoding/json leaves the destination empty when it errors (verified
		// against truncated objects, trailing garbage, two concatenated objects and a
		// bare string), so the loop below would find no fields and report no change
		// anyway. Kept because the guarantee should be stated where it is relied on,
		// and a lenient decoder is a plausible future edit that would silently end it.
		return in, false
	}
	changed := false
	for _, f := range fields {
		raw, ok := obj[f]
		if !ok {
			continue
		}
		var next json.RawMessage
		var did bool
		if f == "edits" {
			next, did = blankEdits(raw)
		} else {
			next, did = blankString(raw)
		}
		if did {
			obj[f] = next
			changed = true
		}
	}
	if !changed {
		return in, false
	}
	out, err := json.Marshal(obj)
	if err != nil || len(out) >= len(in) {
		return in, false
	}
	return out, true
}

// blankString replaces one string field with a size marker, or reports that it was
// not worth it.
func blankString(raw json.RawMessage) (json.RawMessage, bool) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || len(s) <= minPayloadBytes {
		return raw, false
	}
	out, err := json.Marshal(argPlaceholder(len(s)))
	if err != nil {
		return raw, false
	}
	return out, true
}

// blankEdits walks the edits array, blanking both string fields of each
// replacement. The array's length survives, so "how many replacements" is still
// answerable from the call.
func blankEdits(raw json.RawMessage) (json.RawMessage, bool) {
	var ops []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &ops); err != nil {
		return raw, false
	}
	changed := false
	for _, op := range ops {
		for _, f := range []string{"oldText", "newText"} {
			if v, ok := op[f]; ok {
				if next, did := blankString(v); did {
					op[f], changed = next, true
				}
			}
		}
	}
	if !changed {
		return raw, false
	}
	out, err := json.Marshal(ops)
	if err != nil {
		return raw, false
	}
	return out, true
}

// argPlaceholder is what stands in for a blanked argument. Deliberately terse:
// unlike a result, which appears once, this text can repeat for every replacement
// in an edit, and the sentence explaining what to do about it belongs on the
// paired result where it is said once. See placeholder.
func argPlaceholder(n int) string { return fmt.Sprintf("[%s cleared]", humanBytes(n)) }

// reRunCost is what it costs to get a cleared result back. It exists because the
// placeholder ends in advice, and advice a model will act on has to be true for the
// tool it is attached to.
//
// Anthropic's strategy says one thing to every cleared result. That works when the
// only clearable results are reads. pi-go has tools whose calls are not free to
// repeat, and a survey of this project's own 100 transcripts found real ones in the
// history: `git add -A && git commit`, `git cherry-pick <sha> && go test`, and a
// heredoc that overwrites ~/.pi-go/providers.json. Telling a model to call those
// again is not a re-read, it is a second side effect.
type reRunCost int

const (
	// reRunFree: calling again re-derives the answer from current state, and the
	// only cost is the call. This is the case Anthropic's wording was written for.
	reRunFree reRunCost = iota
	// reRunOnDisk: the call's effect *is* a file, so reading beats repeating —
	// repeating would write content the model no longer holds, or fail to match.
	reRunOnDisk
	// reRunRepeatsEffect: calling again does whatever it did again. What that means
	// is knowable only from the command, which is still in the arguments directly
	// above, so the placeholder states the fact and leaves the judgement there.
	reRunRepeatsEffect
	// reRunPaysAgain: the answer cost another agent's entire run.
	reRunPaysAgain
)

// reRun classifies every tool that can reach a placeholder.
//
// todo is deliberately absent: its newest result is pinned and never cleared, and a
// superseded one is a stale state that nobody should be invited to restore. A tool
// missing from this table gets the cautious wording, and a test requires every
// registered tool to be listed, so adding one forces the decision rather than
// silently inheriting "call it again".
var reRun = map[string]reRunCost{
	"read":     reRunFree,
	"ls":       reRunFree,
	"find":     reRunFree,
	"grep":     reRunFree,
	"write":    reRunOnDisk,
	"edit":     reRunOnDisk,
	"bash":     reRunRepeatsEffect,
	"subagent": reRunPaysAgain,
}

// advice is the closing sentence of a placeholder: what this particular tool's
// output costs to get back.
//
// An unclassified tool gets the cautious sentence rather than the encouraging one,
// and the lookup is explicit rather than leaning on the zero value — reRunFree
// being zero would otherwise make "not in the table" silently mean "free to
// repeat", which is the one default that can turn an eviction into an action.
func advice(tool string) string {
	cost, known := reRun[tool]
	if !known {
		return "Only call it again if repeating the call is safe."
	}
	switch cost {
	case reRunFree:
		return "Call the tool again if you still need it."
	case reRunOnDisk:
		return "The change is on disk; read the file to see its current content."
	case reRunRepeatsEffect:
		return "Calling it again would repeat whatever the command did, " +
			"so re-run it only if repeating it is safe."
	case reRunPaysAgain:
		return "Getting this back means delegating again, which costs another full run."
	}
	return "Only call it again if repeating the call is safe."
}

// clearable lists the tool results eligible for clearing, oldest first, and names
// the newest task list.
//
// Two kinds are skipped, and only one of them is Anthropic's:
//
//   - Tools in ExcludeTools, which is their own escape hatch.
//   - Failed results. Theirs does not spare these; pi-go does, because
//     self-correction from an error string is this project's signature behaviour —
//     the whole timeline UI is built around linking a repair to the failure it
//     repairs. Errors are also short, so sparing them costs almost nothing.
func clearable(msgs []llm.Message, excluded map[string]bool) (out []candidate, newestTodo string) {
	type callSite struct {
		name     string
		msg, blk int
	}
	calls := make(map[string]callSite)
	for i, m := range msgs {
		for j, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				calls[b.ID] = callSite{name: b.Name, msg: i, blk: j}
			}
		}
	}
	for i, m := range msgs {
		for j, b := range m.Content {
			if b.Type != llm.BlockToolResult || b.IsError || b.Text == "" {
				continue
			}
			call, paired := calls[b.ToolUseID]
			if excluded[call.name] {
				continue
			}
			if call.name == todoToolName {
				newestTodo = b.ToolUseID
			}
			c := candidate{
				msg: i, blk: j, useMsg: -1, useBlk: -1,
				callID: b.ToolUseID, tool: call.name,
				tokens: int64(len(b.Text)) / llm.BytesPerToken,
				lines:  countLines(b.Text), bytes: len(b.Text),
			}
			// The payload half. Folded into c.tokens rather than tracked beside it,
			// so the trigger and the ClearAtLeast floor weigh what clearing this
			// call actually frees. Without that a write whose result reads
			// "Successfully wrote 42000 bytes" looks like a ten-token saving while
			// being the largest thing in the history.
			if fields, ok := payloadArgs[call.name]; ok && paired {
				in := msgs[call.msg].Content[call.blk].Input
				if next, changed := clearArgs(in, fields); changed {
					c.useMsg, c.useBlk = call.msg, call.blk
					c.newInput = next
					c.argBytes = len(in) - len(next)
					c.argTokens = int64(c.argBytes) / llm.BytesPerToken
					c.tokens += c.argTokens
				}
			}
			out = append(out, c)
		}
	}
	// Already in message order, but stated rather than assumed: "oldest first" is
	// the whole eviction order and a repaired transcript can interleave blocks.
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].msg != out[b].msg {
			return out[a].msg < out[b].msg
		}
		return out[a].blk < out[b].blk
	})
	return out, newestTodo
}

// apply builds the edited copy.
//
// Copy-on-write per message: a message with nothing cleared is shared with the
// input, so an unedited history costs no allocation. Structure is preserved
// exactly — same messages, same blocks, same order, same ids, and Details
// untouched — because a tool_use whose tool_result went missing is rejected by the
// API on the next request, which would make the session unresumable.
func apply(msgs []llm.Message, cands []candidate, chosen map[string]bool) ([]llm.Message, ContextEdit) {
	var stat ContextEdit
	out := make([]llm.Message, len(msgs))
	copy(out, msgs)

	// Copy-on-write per message, tracked by index rather than by a pre-grouped map
	// because a call and its result are in different messages and either may be
	// shared with the input. Touching one message twice has to be safe.
	touched := make(map[int]bool)
	blocksOf := func(mi int) []llm.Block {
		if !touched[mi] {
			blocks := make([]llm.Block, len(msgs[mi].Content))
			copy(blocks, msgs[mi].Content)
			out[mi].Content = blocks
			touched[mi] = true
		}
		return out[mi].Content
	}

	for _, c := range cands {
		if !chosen[c.callID] {
			continue
		}
		// c.tokens carries the payload half so the floor could weigh it; the report
		// splits the two back out, because "the output went" and "the content you
		// wrote went" are different things for a reader to act on.
		stat.ClearedResults++
		stat.ClearedTokens += c.tokens - c.argTokens
		blocksOf(c.msg)[c.blk].Text = placeholder(c)

		if c.newInput != nil && c.useMsg >= 0 {
			blocksOf(c.useMsg)[c.useBlk].Input = c.newInput
			stat.ClearedArgs++
			stat.ClearedArgTokens += c.argTokens
		}
	}
	return out, stat
}

// placeholder is what the model reads in place of the output.
//
// It has to be a pure function of the original's shape: the same result cleared on
// two consecutive turns must produce byte-identical text, or the prompt prefix
// changes every turn and the cache never settles.
//
// The wording follows the dialect the tools already speak — read ends a truncated
// file with "Use offset=N to continue", bash names the temp file holding the rest —
// so "this was here and you can get it back" is not a new idea the model has to
// learn. The tool call itself is still directly above with its arguments intact
// (Anthropic's clear_tool_inputs defaults to false for the same reason), so the
// placeholder does not restate the path.
func placeholder(c candidate) string {
	// The closing sentence is per-tool, because it is advice a model acts on. See
	// reRun for the four cases and the survey that produced them. Until then one
	// sentence went to every cleared result, which was wrong for three of the nine
	// tools: every cleared write told the model to call write again.
	args := ""
	if c.argBytes > 0 {
		args = fmt.Sprintf(" Its arguments were removed too (%s).", humanBytes(c.argBytes))
	}
	return fmt.Sprintf("[%d lines / %s of %s output removed to fit the context window.%s %s]",
		c.lines, humanBytes(c.bytes), c.tool, args, advice(c.tool))
}

func countLines(s string) int {
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%dKB", n/1024)
}
