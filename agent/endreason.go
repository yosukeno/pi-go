package agent

import "github.com/wangy/pi-go/llm"

// EndReason is the harness's reason for ending a run.
//
// # Why this is not llm.StopReason
//
// llm.StopReason is the provider's word for why one *reply* ended: end_turn,
// tool_use, max_tokens. It belongs to the protocol layer and it is the provider's
// vocabulary, so it cannot name the things this loop decides for itself — a turn
// cap, a budget, a stagnation verdict. Those three ended a run with nothing set but
// Err, so on the wire they arrived as an absent stop_reason and a sentence of
// English prose.
//
// That is the same shape this project already rejected once, one layer down: before
// llm.APIError kept its structure, a non-2xx was flattened to a string, and "the
// prompt is too long" and "the key is wrong" — both 400 on the same provider,
// needing opposite responses — could only be told apart by matching prose. The fix
// there was to keep the structure. This is that fix at the harness level.
//
// # Who needs it
//
// A person does not: the terminal prints Err and that sentence is better than any
// enum. A *driver* does — an outer script that starts the next run when this one
// ran out of allowance and stops when the provider is down. Without a machine
// -readable reason that script has to grep the error text, which means every reword
// of a message is a breaking change to something nobody knew was an interface.
//
// Every EndReason is registered in dispositions below, and a test requires it, so
// adding one forces an answer to "what should a driver do about this" rather than
// letting it inherit a default silently.
type EndReason string

const (
	// EndCompleted is the model answering with no tool calls and nothing queued:
	// the only reason that means the work is done rather than interrupted.
	EndCompleted EndReason = "completed"

	// EndTurnLimit is -max-turns. The work is unfinished by definition — the loop
	// stopped counting, not the model.
	EndTurnLimit EndReason = "turn_limit"

	// EndTokenBudget, EndCostBudget and EndTimeBudget are -token-budget,
	// -cost-budget and -time-budget. Kept apart because the response differs: a
	// token or cost budget is refilled by deciding to spend more, a time budget by
	// waiting, and a driver deciding whether to start another run needs to know
	// which one it just hit.
	//
	// EndCostBudget can only occur when the active model has a declared price, since
	// a run without one refuses to start rather than proceeding uncapped. See
	// llm.Price and Agent.checkBudgets.
	EndTokenBudget EndReason = "token_budget"
	EndCostBudget  EndReason = "cost_budget"
	EndTimeBudget  EndReason = "time_budget"

	// EndStagnation is the same tool results N times in a row. It is not a budget:
	// nothing was exhausted, so re-running the identical prompt reproduces it.
	EndStagnation EndReason = "stagnation"

	// EndContextOverflow is a prompt the provider refused as too large *after*
	// clearing had already been forced and had nothing left to free (see
	// forceClear). Distinct from EndTransportError because the two 400s are fixed by
	// opposite actions — this one by compacting or starting a fresh session, that
	// one by waiting.
	EndContextOverflow EndReason = "context_overflow"

	// EndTransportError is any other failed model call. The loop hands tool failures
	// back to the model to correct; it cannot hand back a call that never returned.
	EndTransportError EndReason = "transport_error"

	// EndMaxTokens is the model's own output cap ending its reply with no tool calls
	// to follow. The answer is cut off mid-sentence, which is why it is not
	// EndCompleted even though the loop exited by the same path.
	EndMaxTokens EndReason = "max_tokens"

	// EndAborted is a cancelled context: Ctrl-C, a web run timeout, a shutdown.
	// Someone or something outside the task stopped it on purpose.
	EndAborted EndReason = "aborted"
)

// AllEndReasons is every reason the loop can end a run with.
//
// It exists because Go cannot enumerate the constants above, so without a list
// written down there is nothing for a test to iterate and the dispositions table
// could quietly fall behind. Kept adjacent to the constants for the one reason that
// makes that work: adding a reason means editing this block, and the list is the next
// thing in it.
//
// TestEveryEndReasonHasADisposition checks this against dispositions in both
// directions, so an entry here without a disposition and a disposition without an
// entry here both fail.
var AllEndReasons = []EndReason{
	EndCompleted,
	EndTurnLimit,
	EndTokenBudget,
	EndCostBudget,
	EndTimeBudget,
	EndStagnation,
	EndContextOverflow,
	EndTransportError,
	EndMaxTokens,
	EndAborted,
}

// Disposition is what an unattended driver should do about an EndReason.
//
// It exists because the obvious reading of the reasons — "anything that is not
// completed means try again" — is wrong for three of them in three different ways,
// and each of those wrong answers is a loop that burns money without progressing.
// Encoding the judgement once here is cheaper than every caller rediscovering it.
type Disposition string

const (
	// DispositionDone: the work finished. Stop.
	DispositionDone Disposition = "done"

	// DispositionContinue: an allowance ran out partway through. The transcript is
	// intact and a fresh run reading it can pick up, so starting one is progress
	// rather than repetition.
	DispositionContinue Disposition = "continue"

	// DispositionIntervene: work remains, but repeating this run unchanged fails the
	// same way. Something about the inputs has to change first — a different prompt,
	// a compaction, a human. A driver that treats this as Continue spins.
	DispositionIntervene Disposition = "intervene"

	// DispositionHalt: the run ended for a reason outside the task. Retrying is not
	// wrong so much as uninformed: nothing here says the condition has cleared, and
	// an aborted run was aborted on purpose.
	DispositionHalt Disposition = "halt"
)

// dispositions is exhaustive over EndReason, enforced by
// TestEveryEndReasonHasADisposition.
//
// The table is explicit rather than a switch with a default for the same reason
// contextedit.go's re-run cost table is: a default silently absorbs a reason nobody
// classified, and here the absorbing value would be whatever the author of the
// default happened to think was harmless.
var dispositions = map[EndReason]Disposition{
	EndCompleted: DispositionDone,

	// The four exhaustion reasons. A fresh run is the right move for all of them,
	// and it is the same move: read the transcript, keep going.
	EndTurnLimit:   DispositionContinue,
	EndTokenBudget: DispositionContinue,
	EndCostBudget:  DispositionContinue,
	EndTimeBudget:  DispositionContinue,
	// Same treatment for a different cause: the reply was truncated, so the next run
	// is finishing a sentence rather than resuming a task. Continue either way.
	EndMaxTokens: DispositionContinue,

	// Not Continue, and this is the distinction the whole type exists for. The
	// stagnation check fires on identical tool results, so a run started from the
	// same history does the same thing and trips it again at the same turn.
	EndStagnation: DispositionIntervene,
	// Also not Continue: clearing already ran and freed nothing, so the next prompt
	// is the same size. The mechanism that helps is compaction, which is manual on
	// purpose (see compact.go), so this one genuinely needs a decision from outside.
	EndContextOverflow: DispositionIntervene,

	EndTransportError: DispositionHalt,
	EndAborted:        DispositionHalt,
}

// Disposition classifies r. An unregistered reason is DispositionHalt.
//
// Halt is the cautious default in the one direction that matters: an unattended
// driver that halts on something it does not understand costs a stalled job, while
// one that continues costs an unbounded number of runs. Registration is still
// required by test — this default is the floor under a mistake, not a substitute for
// answering the question.
func (r EndReason) Disposition() Disposition {
	if d, ok := dispositions[r]; ok {
		return d
	}
	return DispositionHalt
}

// endReasonForStop maps a provider stop reason onto the harness reason, for the
// paths that leave the loop through its normal exit rather than through a finish()
// of their own.
//
// Only three provider reasons can reach that exit. tool_use cannot: a reply with
// tool calls keeps the loop going, and one without them reports end_turn. The
// zero value cannot either, because every break follows an assignment from a
// successful call — it falls to EndCompleted only so that a future break added
// before the first call gets the harmless answer rather than an empty string on the
// wire.
func endReasonForStop(stop llm.StopReason) EndReason {
	switch stop {
	case llm.StopAborted:
		return EndAborted
	case llm.StopMaxTokens:
		return EndMaxTokens
	default:
		return EndCompleted
	}
}
