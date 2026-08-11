package agent

import (
	"testing"

	"github.com/wangy/pi-go/tools"
)

func emptyRegistry() *tools.Registry { return tools.NewRegistry() }

// "auto" scales with the window instead of taking Anthropic's literal 100,000.
// Their default is fixed because their models all have roughly the same window;
// pi-go's catalogue runs from 262K to 1M, where a fixed number would mean two
// completely different policies.
//
// Four fifths rather than their half, which this test pins because it is a number
// with reasons behind it and not a preference: the estimate being compared errs high
// (0.98 ASCII / 0.83 Chinese against the providers' own counts), clearing costs a
// cache miss billed at up to ten times the hit rate, and an overflow stopped being
// fatal once forceClear started retrying it. See ParseContextEdit.
func TestParseContextEditAutoIsFourFifthsOfTheWindow(t *testing.T) {
	cases := map[int]int64{
		262_144:   209_715, // kimi-for-coding
		1_048_576: 838_860, // glm-5.2 and k3
		200_000:   160_000, // a hypothetical smaller window still scales
	}
	for window, want := range cases {
		got, err := ParseContextEdit("auto", window)
		if err != nil {
			t.Fatalf("window %d: %v", window, err)
		}
		if got.Trigger != want {
			t.Errorf("window %d: Trigger = %d, want %d", window, got.Trigger, want)
		}
		// The property behind the number: clearing must start while there is still
		// room to work in, and it must not start while most of the window is free.
		if got.Trigger >= int64(window) {
			t.Errorf("window %d: trigger %d is not below the ceiling", window, got.Trigger)
		}
		if got.Trigger <= int64(window)/2 {
			t.Errorf("window %d: trigger %d is at or below half, which is what this moved away from",
				window, got.Trigger)
		}
	}
	// The default flag value is "auto", but an empty spec has to mean the same:
	// every caller that forgets to set it should get the intended policy.
	if got, _ := ParseContextEdit("", 262_144); got.Trigger != 209_715 {
		t.Errorf("empty spec: Trigger = %d, want the same as auto", got.Trigger)
	}
}

// A model outside the catalogue has no window to take a fraction of, so auto
// disables rather than inventing a threshold. Clearing too early costs re-reads and
// cache misses, and the run finishes either way, so doing nothing is the safe reading.
func TestParseContextEditAutoWithNoKnownWindowDisables(t *testing.T) {
	got, err := ParseContextEdit("auto", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Trigger != 0 {
		t.Errorf("Trigger = %d with an unknown window, want 0 (disabled)", got.Trigger)
	}
}

func TestParseContextEditExplicitValues(t *testing.T) {
	if got, _ := ParseContextEdit("40000", 200_000); got.Trigger != 40_000 {
		t.Errorf("Trigger = %d, want the number given", got.Trigger)
	}
	// Three spellings of off, because "0" is what someone types by analogy with
	// -token-budget and it would be hostile to reject it.
	for _, spec := range []string{"off", "0"} {
		if got, err := ParseContextEdit(spec, 200_000); err != nil || got.Trigger != 0 {
			t.Errorf("%q: got %+v, %v; want disabled", spec, got, err)
		}
	}
}

// A rejected spec must name the alternatives: the whole point of erroring instead
// of silently disabling is that a typo should not look like a working setting.
func TestParseContextEditRejectsNonsense(t *testing.T) {
	for _, spec := range []string{"yes", "half", "-1", "1e5", "100k"} {
		_, err := ParseContextEdit(spec, 200_000)
		if err == nil {
			t.Errorf("%q was accepted", spec)
			continue
		}
		for _, want := range []string{"auto", "off"} {
			if !contains(err.Error(), want) {
				t.Errorf("%q: error does not offer %q: %v", spec, want, err)
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A model switch has to move the threshold with it. Left alone, going from a 262K
// model to a 1M one keeps clearing at a fifth of the new window; going the other
// way leaves a trigger the new window cannot reach, so clearing silently stops.
func TestSetContextEditFollowsAModelSwitch(t *testing.T) {
	small, _ := ParseContextEdit("auto", 262_144)
	a := New(Config{Client: &fakeClient{}, Registry: emptyRegistry(), ContextEdit: small})
	if a.contextEdit.Trigger != 209_715 {
		t.Fatalf("Trigger = %d, want 209715", a.contextEdit.Trigger)
	}

	big, _ := ParseContextEdit("auto", 1_048_576)
	a.SetContextEdit(big)
	if a.contextEdit.Trigger != 838_860 {
		t.Errorf("after the switch: Trigger = %d, want 838860", a.contextEdit.Trigger)
	}
}

// The already-cleared set survives a switch. Those results are gone from the prompt
// whatever the new threshold is, and restoring them would flap the prefix and cost a
// cache miss for nothing.
func TestSetContextEditKeepsWhatWasAlreadyCleared(t *testing.T) {
	a := New(Config{Client: &fakeClient{}, Registry: emptyRegistry()})
	a.cleared = map[string]bool{"call-1": true}
	a.SetContextEdit(ContextEditConfig{Trigger: 999_999})
	if !a.cleared["call-1"] {
		t.Error("a model switch un-cleared a result")
	}
}
