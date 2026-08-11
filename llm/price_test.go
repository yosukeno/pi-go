package llm

import "testing"

// The subset trap, stated as arithmetic. CacheRead is part of Input, so a naive
// Input*rate + CacheRead*cacheRate bills the cached tokens twice — once at full
// price and again at the cache price.
func TestCostChargesCachedTokensOnceAtTheCacheRate(t *testing.T) {
	p := Price{Input: 10, Output: 30, CacheRead: 1}
	// 1M prompt tokens of which 800K came from cache, 100K out.
	u := Usage{Input: 1_000_000, CacheRead: 800_000, Output: 100_000}

	// 200K uncached at 10 + 800K cached at 1 + 100K out at 30, per million.
	want := 0.2*10 + 0.8*1 + 0.1*30
	if got := p.Cost(u); !closeEnough(got, want) {
		t.Errorf("Cost = %g, want %g", got, want)
	}

	// The wrong version, spelled out so the difference is visible rather than
	// asserted: it would be 1.0*10 + 0.8*1 + 0.1*30, over three times the answer.
	if wrong := 1.0*10 + 0.8*1 + 0.1*30; closeEnough(want, wrong) {
		t.Fatal("the test case cannot tell the two formulas apart")
	}
}

// Reasoning is a subset of Output and must not be added. A run that thinks a lot
// would otherwise be billed for its thinking twice.
func TestCostIgnoresReasoningBecauseOutputAlreadyContainsIt(t *testing.T) {
	p := Price{Input: 10, Output: 30}
	quiet := Usage{Input: 1_000_000, Output: 100_000}
	thinky := Usage{Input: 1_000_000, Output: 100_000, Reasoning: 90_000}

	if p.Cost(quiet) != p.Cost(thinky) {
		t.Errorf("Cost differs by Reasoning alone: %g vs %g; Reasoning is inside Output",
			p.Cost(quiet), p.Cost(thinky))
	}
}

// A provider reporting a cached count larger than the prompt is reporting nonsense.
// The uncached remainder must not go negative, because a negative charge would buy
// room under a ceiling — the one direction of error a spend limit cannot tolerate.
func TestCostNeverGoesNegativeOnANonsenseReport(t *testing.T) {
	p := Price{Input: 10, Output: 30, CacheRead: 1}
	for _, u := range []Usage{
		{Input: 1000, CacheRead: 5000},  // subset relationship inverted
		{Input: 1000, CacheRead: -5000}, // negative count
		{Input: -1000, Output: -1000},   // both negative
	} {
		if got := p.Cost(u); got < 0 {
			t.Errorf("Cost(%+v) = %g, want it floored at or above zero", u, got)
		}
	}
}

// A declared rate of zero is an answer — a local model server costs nothing — and
// has to stay distinguishable from "nobody declared a rate", which is a nil *Price
// and is what makes -cost-budget refuse.
func TestZeroIsADeclaredRateNotAMissingOne(t *testing.T) {
	if !(Price{}).Zero() {
		t.Error("an all-zero Price does not report itself as free")
	}
	if (Price{CacheRead: 0.01}).Zero() {
		t.Error("a Price with a cache rate reports itself as free")
	}
	// The distinction itself is a pointer, so there is nothing to assert here beyond
	// naming it: a free model has a Price whose Zero is true, a model nobody priced
	// has no Price at all.
	var undeclared *Price
	if undeclared != nil {
		t.Error("nil is not nil")
	}
}

func TestCostOfNothingIsNothing(t *testing.T) {
	if got := (Price{Input: 10, Output: 30}).Cost(Usage{}); got != 0 {
		t.Errorf("Cost of an empty Usage = %g, want 0", got)
	}
}

func closeEnough(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
