package config

import (
	"strings"

	"github.com/yosukeno/pi-go/llm"
)

// Provider is an OpenAI-compatible endpoint.
//
// Every provider comes from ~/.pi-go/providers.json (see file.go, including why
// the file is read from the home directory and nowhere else): this fork compiles
// none in, so the file can define any endpoint — a vendor, a local model server,
// a mirror, a gateway — and pi-go refuses to start without one.
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

// No providers or models are compiled in: ~/.pi-go/providers.json is the single
// source of truth (this fork's choice — upstream pi-go ships two built-in
// endpoints so it works with no file at all). Load() refuses to start with an
// empty catalog, so a missing file is an explained error here, not a silent
// fallback to nothing.
var providers = map[string]Provider{}

// catalog lists the models worth exposing. Order matters: the first entry is the
// default unless the file names one explicitly.
//
// ContextWindow is load-bearing beyond display: -context-edit auto takes half of
// it, and the browser's context gauge turns yellow and red at fractions of it. An
// under-declared window therefore makes pi-go clear too early — paying cache misses
// and re-reads — and advise starting a new session while most of the window is
// still free. So the figures in the shipped providers.json are checked against
// the endpoints rather than copied once.
var catalog = []Model{}

// Catalog returns the known models in display order.
func Catalog() []Model { return catalog }

// defaultModel is empty until a config file names one, so that the default stays
// a property of catalog order rather than a second thing to keep in sync with it.
var defaultModel string

// DefaultModel is the configured default, or the first catalog entry. Empty when
// the catalog is empty — Load() refuses that state at startup, so "" here is a
// test-time answer, not a runtime one.
func DefaultModel() string {
	if defaultModel != "" {
		return defaultModel
	}
	if len(catalog) == 0 {
		return ""
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
