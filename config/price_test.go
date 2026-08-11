package config

import (
	"strings"
	"testing"

	"github.com/wangy/pi-go/llm"
)

// A declared price reaches the resolved model. Nothing else in the file has to
// change for it: it is an optional field on an entry that otherwise looks the same.
func TestMergeCarriesADeclaredPrice(t *testing.T) {
	ps, ms := baseFixture()
	f := fileConfig{Models: []modelFile{{
		ID: "small", Provider: "vendor", ContextWindow: 32_000,
		Price: &priceFile{Input: 0.5, Output: 1.5, CacheRead: 0.05},
	}}}
	_, gotMs, _, _, err := merge(ps, ms, f)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var got *Model
	for i := range gotMs {
		if gotMs[i].ID == "small" {
			got = &gotMs[i]
		}
	}
	if got == nil {
		t.Fatal("the model was not merged")
	}
	if got.Price == nil {
		t.Fatal("Price is nil after declaring one")
	}
	if got.Price.Input != 0.5 || got.Price.Output != 1.5 || got.Price.CacheRead != 0.05 {
		t.Errorf("Price = %+v, want the declared rates", *got.Price)
	}
}

// The distinction the pointer exists for. A model nobody priced must stay
// distinguishable from one declared free, because only the first may block a spend
// ceiling — and every built-in is the first kind.
func TestAnUndeclaredPriceIsNilNotZero(t *testing.T) {
	ps, ms := baseFixture()
	f := fileConfig{Models: []modelFile{
		{ID: "unpriced", Provider: "vendor", ContextWindow: 1000},
		{ID: "free", Provider: "vendor", ContextWindow: 1000, Price: &priceFile{}},
	}}
	_, gotMs, _, _, err := merge(ps, ms, f)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	byID := map[string]Model{}
	for _, m := range gotMs {
		byID[m.ID] = m
	}
	if byID["unpriced"].Price != nil {
		t.Error("a model with no price block got a Price; that would read as free")
	}
	p := byID["free"].Price
	if p == nil {
		t.Fatal("an explicit empty price block produced nil; free is an answer")
	}
	if !p.Zero() {
		t.Errorf("an empty price block produced %+v, want all zero", *p)
	}
	// And every built-in is unpriced, which is what makes the CLI's refusal the
	// default path rather than an edge case.
	for _, m := range Catalog() {
		if m.Price != nil {
			t.Errorf("built-in model %q ships a price; see llm.Price for why none should", m.ID)
		}
	}
}

func TestPriceValidationRefusesWhatCannotBeMeant(t *testing.T) {
	cases := []struct {
		name string
		p    priceFile
		want string
	}{
		{"negative input", priceFile{Input: -1}, "negative"},
		{"negative output", priceFile{Output: -1}, "negative"},
		{"negative cache", priceFile{Input: 1, CacheRead: -1}, "negative"},
		{
			// The likely transcription error: a price page lists the cache rate first, so
			// the two get swapped. Caught because a cached read is cheaper than a miss by
			// definition, and a silently inverted pair would under-report every cached
			// session — the sessions a ceiling is most needed for.
			"cache dearer than input", priceFile{Input: 0.05, CacheRead: 0.5}, "swapped",
		},
		{"absurd rate", priceFile{Input: 1e13}, "not a usable rate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps, ms := baseFixture()
			f := fileConfig{Models: []modelFile{{
				ID: "m", Provider: "vendor", ContextWindow: 1000, Price: &tc.p,
			}}}
			_, _, _, _, err := merge(ps, ms, f)
			if err == nil {
				t.Fatalf("merge accepted price %+v", tc.p)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Equal is fine: some arrangements bill a cached read at the full rate.
func TestPriceValidationAllowsACacheRateEqualToInput(t *testing.T) {
	ps, ms := baseFixture()
	f := fileConfig{Models: []modelFile{{
		ID: "m", Provider: "vendor", ContextWindow: 1000,
		Price: &priceFile{Input: 0.5, Output: 1, CacheRead: 0.5},
	}}}
	if _, _, _, _, err := merge(ps, ms, f); err != nil {
		t.Errorf("merge refused an equal cache rate: %v", err)
	}
}

// Replacing a model is wholesale, so a re-declaration that omits the price loses it
// — and has to say so, for the same reason dropping context_window does. That
// silence has already cost this project once, with glm-5.2's window.
func TestReplacingAModelNamesADroppedPrice(t *testing.T) {
	ps, ms := baseFixture()
	ms[0].Price = &llm.Price{Input: 1, Output: 2}
	f := fileConfig{Models: []modelFile{{
		ID: "big", Provider: "vendor", Aliases: []string{"b"},
		ContextWindow: 200_000, MaxTokens: 8192,
	}}}
	_, gotMs, _, warns, err := merge(ps, ms, f)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if gotMs[0].Price != nil {
		t.Error("the price survived a wholesale replacement that omitted it")
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "price") {
		t.Errorf("warnings = %v, want the dropped price named", warns)
	}
}
