package main

import (
	"testing"

	"github.com/yosukeno/pi-go/agent"
	"github.com/yosukeno/pi-go/config"
)

// "auto" is four fifths of the model's window, and the fifth it leaves has to cover
// three things: the provider's own output (on kimi, prompt + max_tokens is charged
// against one limit), the error in an estimate that is only part measured, and the
// growth of the turn that clearing is about to allow.
//
// The output half of that is checkable, and it is the assumption a future catalogue
// entry would break — a model with a large output cap and a small window. This test
// lives in the root package because it is the only one that imports both the rule and
// the catalogue; agent deliberately knows nothing about the model list.
func TestAutoTriggerLeavesRoomForEveryModelsOutput(t *testing.T) {
	for _, m := range config.Catalog() {
		cfg, err := agent.ParseContextEdit("auto", m.ContextWindow)
		if err != nil {
			t.Errorf("%s: %v", m.ID, err)
			continue
		}
		margin := int64(m.ContextWindow) - cfg.Trigger
		if margin <= m.MaxTokens {
			t.Errorf("%s: window %d leaves a margin of %d above the trigger, "+
				"which does not clear MaxTokens %d — clearing would begin only after the "+
				"prompt plus the reply could no longer fit",
				m.ID, m.ContextWindow, margin, m.MaxTokens)
		}
	}
}

// The trigger scales with the window rather than being a constant, which is the whole
// reason ParseContextEdit takes the window at all. A fixed number would clear a 1M
// model at a tenth of its window and be unreachable on a small one.
func TestAutoTriggerScalesWithEachModelsWindow(t *testing.T) {
	seen := map[int64]bool{}
	for _, m := range config.Catalog() {
		cfg, _ := agent.ParseContextEdit("auto", m.ContextWindow)
		want := int64(m.ContextWindow) * agent.AutoTriggerNumerator / agent.AutoTriggerDenominator
		if cfg.Trigger != want {
			t.Errorf("%s: trigger = %d, want %d (%d/%d of %d)",
				m.ID, cfg.Trigger, want,
				agent.AutoTriggerNumerator, agent.AutoTriggerDenominator, m.ContextWindow)
		}
		seen[cfg.Trigger] = true
	}
	// The catalogue spans two window sizes, so the triggers must not all be equal —
	// that would mean the scaling had been replaced by a constant.
	if len(seen) < 2 {
		t.Errorf("every model resolved to the same trigger (%v); the rule is not scaling", seen)
	}
}
