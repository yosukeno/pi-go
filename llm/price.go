package llm

// Price is what one model charges per million tokens.
//
// # There are no built-in prices, and that is a decision
//
// pi-go ships no price for any model, for the same reason it ships no
// subagent_model mapping: what a model costs is a claim about someone's billing
// arrangement, and pi-go has no basis for making one on the user's behalf. Both
// built-in providers are subscription plans — Kimi for Coding and the GLM Coding
// Plan — where per-token cost is not merely unknown but *not a quantity that
// exists*. A number invented for them would not be an approximation, it would be
// fiction, and a spend ceiling computed from fiction is worse than no ceiling
// because it reads as a safeguard.
//
// So prices come from the user's config file or not at all, and the flag that needs
// one refuses to run without it rather than doing nothing. See config.priceFile and
// agent.Agent.checkBudgets.
//
// # The unit is deliberately not a currency
//
// Fields are "per million tokens" in whatever unit the user wrote them in. pi-go
// never prints a currency symbol and never converts: -cost-budget is compared
// against the same unit its prices were declared in, and that is the whole
// contract. Asserting USD would be a second unfounded claim on top of the first —
// both built-in vendors bill in CNY — and a wrong currency label on a correct
// number is the kind of error nobody re-reads.
type Price struct {
	// Input is the rate for prompt tokens that were *not* served from cache.
	Input float64
	// Output is the rate for completion tokens.
	Output float64
	// CacheRead is the rate for prompt tokens served from the provider's cached
	// prefix. Zero is meaningful and common: it means cached reads are free under
	// this arrangement, not that the rate is unknown.
	//
	// It matters more here than it looks. On kimi a cache miss is billed at roughly
	// ten times the hit rate and on zhipu about twice, so a session with good prefix
	// reuse and one without differ in cost by far more than they differ in tokens —
	// which is exactly the difference a ceiling is being asked to catch.
	CacheRead float64
}

// perMillion converts a token count at a per-million rate.
const perMillion = 1_000_000.0

// Cost estimates what u was billed at these rates.
//
// # Both of Usage's subset fields are traps, and they are handled here
//
// CacheRead is part of Input, not an addition to it, so the cached portion is
// subtracted before the input rate is applied and charged at the cache rate
// instead. Adding them would bill the cached tokens twice, at full price and again
// at the cache price — the exact arithmetic error the Usage doc comment exists to
// warn about, and the reason this function lives beside that type rather than in
// the package that happens to need it.
//
// Reasoning is part of Output and is therefore *not* referenced at all. It is named
// in this comment only because its absence looks like an omission: a reader
// checking that every Usage field is accounted for should find the answer here
// rather than conclude thinking tokens are free. (The token budget in agent got
// this one wrong in the other direction and added it.)
//
// # Nonsense reports are clamped rather than trusted
//
// Every count is a quantity of tokens, so none of them can be negative and the
// cached part cannot exceed the prompt. A provider reporting otherwise is reporting
// something impossible, and the clamping is not tidiness: a negative charge would
// *buy room* under a ceiling, which is the one direction of error a spend limit
// cannot tolerate. Erring towards charging more is safe here — it stops a run early;
// erring towards charging less removes the limit.
func (p Price) Cost(u Usage) float64 {
	input, output := nonNegative(u.Input), nonNegative(u.Output)
	cached := nonNegative(u.CacheRead)
	if cached > input {
		// The subset relationship inverted. Treated as "all of it was cached" rather
		// than allowing a negative uncached remainder.
		cached = input
	}
	return (float64(input-cached)*p.Input +
		float64(cached)*p.CacheRead +
		float64(output)*p.Output) / perMillion
}

func nonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// Zero reports whether every rate is zero, which is what a free endpoint looks
// like: a local model server costs nothing per token and is entitled to say so.
//
// Distinct from "no price declared", which is a nil *Price. The difference is the
// whole point — one is an answer and the other is the absence of one, and only the
// second may block a spend ceiling.
func (p Price) Zero() bool { return p.Input == 0 && p.Output == 0 && p.CacheRead == 0 }
