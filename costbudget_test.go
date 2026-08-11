package main

import (
	"strings"
	"testing"

	"github.com/wangy/pi-go/config"
	"github.com/wangy/pi-go/llm"
)

// The behaviour this replaces, stated as what it was: -cost-budget was defined,
// documented in two languages, and plumbed all the way into the agent, where nothing
// read it. Setting it bought a number that read back as a safeguard and did nothing.
//
// So the guard is the feature, and it has to refuse rather than warn. A warning on
// stderr is fine for a bad subagent_model — that degrades to inheriting the parent's
// model and the session works — but a spend ceiling has no degraded mode: carrying
// on unbounded is the precise opposite of the request, on exactly the runs nobody is
// watching.
func TestCostBudgetRefusesWithoutAPrice(t *testing.T) {
	cfg := config.Resolved{Model: "glm-5.2", Provider: "zhipu"} // Price nil, like every built-in.

	err := checkCostBudget(2.5, cfg)
	if err == nil {
		t.Fatal("a cost budget was accepted for a model with no declared price")
	}
	msg := err.Error()
	for _, want := range []string{
		"glm-5.2",        // which model is missing a price
		"providers.json", // where to declare it
		"-token-budget",  // the alternative that needs no price
		"per million",    // the unit of the number to write
		"subscription",   // why none ships built in
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q; a refusal that does not say what to do next is a dead end.\ngot: %s", want, msg)
		}
	}
}

func TestCostBudgetAcceptsADeclaredPrice(t *testing.T) {
	cfg := config.Resolved{
		Model: "local-model", Provider: "local",
		Price: &llm.Price{Input: 0.5, Output: 1.5},
	}
	if err := checkCostBudget(2.5, cfg); err != nil {
		t.Errorf("a priced model was refused: %v", err)
	}
}

// A declared rate of zero is an answer, not a missing one. A local model server
// costs nothing per token and is entitled to a ceiling it will never reach; the
// alternative is refusing the one configuration where the answer is certain.
func TestCostBudgetAcceptsAFreeModel(t *testing.T) {
	cfg := config.Resolved{Model: "ollama", Provider: "local", Price: &llm.Price{}}
	if err := checkCostBudget(2.5, cfg); err != nil {
		t.Errorf("a model declared free was refused: %v", err)
	}
}

// No budget, no requirement. Every existing command line has to keep working: the
// guard only fires for someone who asked for a ceiling.
func TestNoCostBudgetNeedsNoPrice(t *testing.T) {
	cfg := config.Resolved{Model: "glm-5.2", Provider: "zhipu"}
	for _, budget := range []float64{0, -1} {
		if err := checkCostBudget(budget, cfg); err != nil {
			t.Errorf("checkCostBudget(%g) = %v, want nil", budget, err)
		}
	}
}

// Every built-in must stay unpriced, which is what makes the refusal the default
// path rather than a corner case. If a price is ever added here, this test is the
// place that forces the decision to be deliberate — see llm.Price for the argument
// against.
func TestNoBuiltInModelShipsAPrice(t *testing.T) {
	for _, m := range config.Catalog() {
		if m.Price != nil {
			t.Errorf("built-in model %q ships a price %+v", m.ID, *m.Price)
		}
	}
}
