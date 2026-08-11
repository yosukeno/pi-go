package config

import (
	"strings"

	"github.com/yosukeno/pi-go/llm"
)

// Provider is an OpenAI-compatible endpoint.
//
// The two below are compiled in, verified against the live service, and are what
// pi-go uses with no configuration at all — a constant that is right beats a file
// that can be wrong. What a file adds is the case a constant cannot cover: an
// endpoint only you have. A local model server, a mirror, a gateway. Without one,
// pi-go is usable with exactly two vendors and no amount of correctness in the
// built-ins fixes that. See file.go, including why the file is read from the home
// directory and nowhere else.
type Provider struct {
	Name    string
	BaseURL string
	// KeyEnv is the only place a credential is read from. Empty means the
	// endpoint needs no credential at all — a local model server, say — and the
	// client then sends no Authorization header.
	KeyEnv string
	// BaseURLEnv allows pointing at a proxy or a mirror without a rebuild. It
	// only moves a *built-in* default: a base_url written in the user's file is
	// pinned, because an ambient environment variable — quite possibly set for
	// some other client — must not reroute an explicit choice.
	BaseURLEnv string
	// pinnedBaseURL marks a base_url that came from the user's file.
	pinnedBaseURL bool
}

// Model is one entry in the catalog. MaxTokens caps output; ContextWindow is what
// the gauges read against and what agent.ParseContextEdit halves to get a clearing
// threshold, so it now constrains what the model is actually sent rather than being
// informational as it was before context editing existed.
type Model struct {
	ID            string
	Provider      string
	Aliases       []string
	ContextWindow int
	MaxTokens     int64
	// SubagentModel is the model a read-only subagent runs instead of this one, or
	// "" to inherit it. Only ever set from the user's config file: see modelFile.
	SubagentModel string
	// Subagent marks the model as eligible for the explore-subagent pool: parallel
	// read-only children are balanced across every marked model, one provider at a
	// time. Only ever set from the user's config file: see modelFile.
	Subagent bool
	// Price is the per-million-token rate, or nil when nobody has declared one.
	//
	// Never set for a built-in, and that is not an omission waiting to be filled in:
	// see llm.Price for why pi-go declines to assert what a model costs, and why the
	// two built-in subscription plans have no per-token cost to assert. nil is what
	// makes -cost-budget refuse rather than silently pass, so it has to stay
	// distinguishable from a declared rate of zero.
	Price *llm.Price
}

var providers = map[string]Provider{
	// Kimi for Coding subscription endpoint. The pay-as-you-go platform
	// (api.moonshot.cn/v1) rejects these keys with 401, so it is not a fallback.
	"kimi": {
		Name:       "kimi",
		BaseURL:    "https://api.kimi.com/coding/v1",
		KeyEnv:     "KIMI_API_KEY",
		BaseURLEnv: "KIMI_BASE_URL",
	},
	// GLM Coding Plan endpoint. The generic /api/paas/v4 also accepts the key but
	// bills differently, so the coding path is the correct one for this plan.
	"zhipu": {
		Name:       "zhipu",
		BaseURL:    "https://open.bigmodel.cn/api/coding/paas/v4",
		KeyEnv:     "ZHIPU_API_KEY",
		BaseURLEnv: "ZHIPU_BASE_URL",
	},
}

// catalog lists the models worth exposing. Order matters: the first entry is the
// default.
//
// glm-5.2 leads because the kimi coding endpoint currently blackholes streaming
// requests for k3 (non-streaming answers fine; verified 2026-08). pi-go always
// streams, so a default of k3 hangs on the first prompt.
// ContextWindow is load-bearing beyond display: -context-edit auto takes half of
// it, and the browser's context gauge turns yellow and red at fractions of it. An
// under-declared window therefore makes pi-go clear too early — paying cache misses
// and re-reads — and advise starting a new session while most of the window is
// still free. So these are checked against the endpoints rather than copied once.
//
// glm-5.2 was declared 200,000 here and that was wrong by a factor of five. Both
// the vendor's own documentation and the model card state a 1M-token context, and a
// probe against pi-go's actual endpoint (the coding plan, not the general API)
// accepted and billed a 400,013-token prompt with finish_reason "stop" — so it is
// certainly more than 200,000. MaxTokens is left at 16384, which is an output cap
// rather than a window: the model permits 131,072, but a coding agent that emitted
// that in one turn has gone wrong, and on kimi the output cap is charged against
// the same limit as the prompt ("prompt tokens + max_tokens exceeds the model
// specification").
//
// The kimi figures were confirmed exactly by the same probe: kimi-for-coding
// rejected the same body with "Your request exceeded model token limit: 262144".
var catalog = []Model{
	{ID: "glm-5.2", Provider: "zhipu", Aliases: []string{"glm", "zhipu"}, ContextWindow: 1_048_576, MaxTokens: 16384},
	{ID: "k3", Provider: "kimi", Aliases: []string{"kimi-k3", "kimi"}, ContextWindow: 1_048_576, MaxTokens: 16384},
	{ID: "k3-256k", Provider: "kimi", ContextWindow: 262_144, MaxTokens: 16384},
	{ID: "kimi-for-coding", Provider: "kimi", Aliases: []string{"k2.7"}, ContextWindow: 262_144, MaxTokens: 16384},
	{ID: "kimi-for-coding-highspeed", Provider: "kimi", Aliases: []string{"k2.7-fast"}, ContextWindow: 262_144, MaxTokens: 16384},
}

// Catalog returns the known models in display order.
func Catalog() []Model { return catalog }

// defaultModel is empty until a config file names one, so that the built-in
// default stays a property of catalog order rather than a second thing to keep in
// sync with it.
var defaultModel string

// DefaultModel is the configured default, or the first catalog entry.
func DefaultModel() string {
	if defaultModel != "" {
		return defaultModel
	}
	return catalog[0].ID
}

// lookup resolves an id or alias, case-insensitively.
func lookup(name string) (Model, bool) {
	for _, m := range catalog {
		if strings.EqualFold(m.ID, name) {
			return m, true
		}
		for _, a := range m.Aliases {
			if strings.EqualFold(a, name) {
				return m, true
			}
		}
	}
	return Model{}, false
}

// SetCatalogForTest replaces the catalogue. It exists so that tests in other
// packages can describe the behaviour they mean — "a model with a subagent mapping"
// — instead of depending on whichever real models happen to be listed today.
//
// Exported reluctantly and named to say so. The alternative was making every
// catalogue reader take a parameter, which is a large change to a small number of
// call sites that all want the same global answer.
func SetCatalogForTest(ms []Model) { catalog = ms }
