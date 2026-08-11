package agent

import "fmt"

// The soft turn cap: a checkpoint, not a wall.
//
// The fixed cap answers "when do we give up". It cannot answer "should we have"
// — a single number serves the tail worst: too small and it cuts real work, too
// large and a run that is merely wandering burns budget to the wall. The measured
// distribution (see -analyze-session with a directory) says where most runs end;
// what it cannot say is whether *this* run, having passed that point, is nearly
// done or has lost the plot.
//
// So instead of a second wall, every SoftTurns turns the loop injects one user
// message telling the model where it stands and what the choice is: finish now,
// or keep working and say what is left. There is no parser on the reply — the
// model's next action *is* the decision. Tool calls continue the run; a plain
// answer ends it exactly as it would have anyway. What the mechanism adds is only
// that the decision is made knowingly, and that the "what is left" sentence lands
// in the transcript, where the next checkpoint — or a person reading it — can
// audit it. That shape is the measured winner over both a fixed cap and an
// unlimited budget (arXiv 2510.16786): the resources go to the runs that
// actually need them.
//
// Extensions need no counter of their own: MaxTurns is the limit on how many
// times this can fire, so a soft cap at or above the hard one never fires at
// all.
//
// The notice rides the steering path (EventSteer) because that is what it is: a
// user-role message landing mid-run at a legal boundary. JSON-mode and web
// consumers already know how to show one, and the transcript stores it like any
// other message — which is the point: the notice is part of the record the run
// is judged from. The [pi-go] prefix marks it as harness-authored, so it is
// never mistaken for something the user typed.

// softCheckpoint returns the notice to inject at the top of turn, or false when
// turn is not a checkpoint. Checkpoints fall on every multiple of SoftTurns
// completed turns; the hard-cap check above it has already returned for any turn
// beyond MaxTurns, so the two never fire together.
func (a *Agent) softCheckpoint(turn int) (string, bool) {
	if a.softTurns <= 0 || turn <= a.softTurns {
		return "", false
	}
	used := turn - 1
	if used%a.softTurns != 0 {
		return "", false
	}
	return fmt.Sprintf("[pi-go] Turn checkpoint: this run has used %d turn(s) (soft cap %d, hard cap %d). "+
		"If the task is done, give the final answer now. If it is not, keep working — and state in one "+
		"sentence what is still missing, so the remaining turns go to it.", used, a.softTurns, a.maxTurns), true
}
