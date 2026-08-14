package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yosukeno/pi-go/llm"
)

// FileName is the user's provider and model configuration.
//
// It lives in the home directory and is read from nowhere else. That is a security
// decision, not a convenience one, and it has a specific precedent: CVE-2026-21852
// in Claude Code, where a repository could ship a settings file that pointed the
// tool's base URL at an attacker's endpoint, and the API key was sent there when
// the project was opened — before the user was asked to trust anything. A file that
// says "which host receives your credential" must not be something a `git clone`
// can deliver. The home directory is already as trusted as the binary; a working
// directory is not.
//
// So there is deliberately no per-project provider config, and no merge of one.
// pi-go warns if it finds this file inside a working directory, because the only
// two explanations are a mistake and an attempt.
const FileName = "providers.json"

// PathEnv overrides the config location, for tests and for anyone keeping their
// configuration somewhere unusual. Still not read from the working directory: this
// is the user's own environment, which a cloned repository does not write.
const PathEnv = "PIGO_CONFIG"

// fileConfig is the on-disk shape.
//
// Providers and models are separate because they nest: a provider is a host plus
// the name of the variable holding its credential, and models belong to one. Both
// merge onto the built-ins rather than replacing them, so a file can add a local
// endpoint without restating the catalog, and pi-go still works with no file at
// all.
type fileConfig struct {
	// Default overrides which model is used when none is named.
	Default string `json:"default,omitempty"`
	// Providers are keyed by the name models refer to. A key matching a built-in
	// replaces it, which is how a corporate gateway gets used without pretending to
	// be a different provider.
	Providers map[string]providerFile `json:"providers,omitempty"`
	// Models are appended, except that an id matching a built-in replaces it.
	Models []modelFile `json:"models,omitempty"`
}

type providerFile struct {
	BaseURL string `json:"base_url"`
	// KeyEnv is the *name* of an environment variable, never a credential. See
	// validate: a non-empty value that does not look like a variable name is
	// refused rather than tried, so a pasted key fails with an explanation
	// instead of ending up in an error message. Empty means the endpoint needs
	// no credential at all (a local server, say).
	KeyEnv     string `json:"key_env"`
	BaseURLEnv string `json:"base_url_env,omitempty"`
}

type modelFile struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Aliases       []string `json:"aliases,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxTokens     int64    `json:"max_tokens,omitempty"`
	// SubagentModel is the model a read-only subagent runs instead of this one.
	//
	// Read-only delegation is the case where a smaller model usually suffices —
	// finding where something is implemented is not the same work as writing it —
	// and it is also the case where latency is most visible, because the parent's
	// turn is blocked until the answer comes back. Left empty, a subagent inherits
	// its parent's model, which is the existing behaviour.
	//
	// No built-in mapping ships with pi-go. Which model is the cheaper sibling of
	// which is a pricing claim, and pi-go has no basis for making one on the user's
	// behalf.
	SubagentModel string `json:"subagent_model,omitempty"`
	// Subagent marks the model for the explore-subagent pool. Marked models share
	// parallel read-only children least-in-flight first, so a batch of subagents
	// spreads across providers instead of all landing on the parent's.
	Subagent bool `json:"subagent,omitempty"`
	// Price is what this model charges per million tokens. Optional, and only
	// -cost-budget reads it.
	//
	// A pointer so that "declared as free" and "not declared" stay different things:
	// a local model server legitimately costs nothing, while a model nobody priced
	// must not be silently treated as free by a flag whose entire job is to stop
	// spending. See llm.Price for why none of these ship built in.
	Price *priceFile `json:"price,omitempty"`
}

// priceFile is the on-disk shape of a rate.
//
// Rates are per *million* tokens because that is how every vendor publishes them,
// so a user transcribing a price page does not have to move a decimal point six
// places and does not have to wonder whether pi-go wanted per-token. The unit of
// the number itself is whatever the user is billed in; pi-go never names a currency
// and never converts. See llm.Price.
type priceFile struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	// CacheRead defaults to 0, which reads as "cached prompt tokens are free here".
	// That is a real arrangement and a reasonable default, but it is also the
	// optimistic one, so validatePrice requires it to be no larger than Input —
	// a cache that cost more than a miss would mean the fields were swapped.
	CacheRead float64 `json:"cache_read,omitempty"`
}

// envName is the conventional shape of an environment variable name. Used to
// refuse a credential pasted into key_env.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Path is where the configuration is read from.
func Path() (string, error) {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-go", FileName), nil
}

// Load merges the user's configuration into the catalog.
//
// Returns warnings rather than printing them, so the caller decides where
// diagnostics go — in json mode they must not reach stdout.
//
// This fork compiles in no providers or models, so the file is the only source:
// a missing file, or one that declares no models, is an error that says where to
// create the file and what a minimal one looks like. (Upstream treats a missing
// file as the normal case because it ships built-ins; with an empty built-in
// catalog, "carry on" would mean a later catalog[0] panic.)
//
// A malformed file *is* an error. The alternative, carrying on regardless, would
// mean a typo silently changes which endpoint receives the credential, and the
// user would have no way to notice.
func Load() (warnings []string, err error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no provider configuration: create %s "+
				"(set %s to point elsewhere). No models are compiled in, so there is "+
				"nothing to run without it. Minimal file:\n%s",
				path, PathEnv, minimalConfig)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f fileConfig
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// Strict: an unrecognised key is far more likely to be a misspelling that
	// silently does nothing than a forward-compatible extension, and the cost of the
	// former is a setting the user believes is in effect.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ps, ms, def, warnings, err := merge(providers, catalog, f)
	if err != nil {
		return warnings, fmt.Errorf("%s: %w", path, err)
	}
	if len(ms) == 0 {
		return warnings, fmt.Errorf("%s declares no models: add at least one entry "+
			"to \"models\" — with nothing compiled in, the file is the only source", path)
	}
	providers, catalog = ps, ms
	if def != "" {
		defaultModel = def
	}
	return warnings, nil
}

// minimalConfig is the example Load points a first-time user at: one provider,
// one model, every field spelled the way the decoder expects.
const minimalConfig = `{
  "providers": {
    "zhipu": { "base_url": "https://open.bigmodel.cn/api/coding/paas/v4", "key_env": "ZHIPU_API_KEY" }
  },
  "models": [
    { "id": "glm-5.2", "provider": "zhipu", "context_window": 1048576, "max_tokens": 16384 }
  ]
}`

// WarnIfInWorkingDir reports a configuration file found somewhere it will not be
// read from.
//
// Ignoring it silently would be safe but unhelpful in both directions: a user who
// put it in the wrong place would see their settings do nothing, and a repository
// that shipped one to redirect the credential would go unremarked.
func WarnIfInWorkingDir(dir string) string {
	if dir == "" {
		return ""
	}
	for _, rel := range []string{FileName, filepath.Join(".pi-go", FileName)} {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		home, _ := Path()
		return fmt.Sprintf("ignoring %s: provider configuration is only read from %s, "+
			"because a file that decides where your API key is sent must not arrive with a "+
			"checkout", p, home)
	}
	return ""
}

// lostFields names the fields the built-in entry had and the replacement does not.
//
// Only the ones whose absence changes behaviour, and each for a stated reason:
//
//   - context_window drives -context-edit auto (half of it) and the browser's
//     context gauge (fractions of it), so a missing one disables both.
//   - max_tokens caps output, and zero would send no cap at all.
//   - aliases are how the model is reachable from -model; losing them silently
//     breaks a command line that used to work.
//   - subagent_model and subagent select the explore-subagent pool.
//
// Provider is deliberately not checked: pointing an id at a different provider is
// the whole reason someone re-declares a built-in model, not an accident.
func lostFields(old, new Model) []string {
	var out []string
	if old.ContextWindow != 0 && new.ContextWindow == 0 {
		out = append(out, "context_window")
	}
	if old.MaxTokens != 0 && new.MaxTokens == 0 {
		out = append(out, "max_tokens")
	}
	if len(old.Aliases) > 0 && len(new.Aliases) == 0 {
		out = append(out, "aliases")
	}
	if old.SubagentModel != "" && new.SubagentModel == "" {
		out = append(out, "subagent_model")
	}
	if old.Subagent && !new.Subagent {
		out = append(out, "subagent")
	}
	if old.Price != nil && new.Price == nil {
		out = append(out, "price")
	}
	return out
}

// toLLM converts a declared rate, or returns nil when none was declared.
//
// A method on the pointer so the nil case is handled once here rather than at the
// call site: nil in means nil out, and nil is the value -cost-budget refuses on.
func (p *priceFile) toLLM() *llm.Price {
	if p == nil {
		return nil
	}
	return &llm.Price{Input: p.Input, Output: p.Output, CacheRead: p.CacheRead}
}

// validatePrice refuses a rate that cannot be what the user meant.
//
// Refusing rather than warning, unlike a bad subagent_model reference. The
// difference is what the field is for: an unusable subagent mapping degrades to
// inheriting the parent's model and the session still works, whereas a wrong price
// is silently wrong for the entire run and the only thing reading it is a spend
// ceiling. There is no degraded mode for "your ceiling is off by a factor of ten".
func validatePrice(id string, p *priceFile) error {
	if p == nil {
		return nil
	}
	for _, f := range []struct {
		name string
		v    float64
	}{{"input", p.Input}, {"output", p.Output}, {"cache_read", p.CacheRead}} {
		if f.v < 0 {
			return fmt.Errorf("model %q: price.%s is negative", id, f.name)
		}
		// NaN and +Inf survive JSON only as the results of arithmetic, but a rate that
		// is not a real number would make every comparison against a ceiling false,
		// which is the one failure mode that looks exactly like "no ceiling was set".
		if f.v != f.v || f.v > 1e12 {
			return fmt.Errorf("model %q: price.%s is not a usable rate", id, f.name)
		}
	}
	if p.CacheRead > p.Input {
		return fmt.Errorf("model %q: price.cache_read (%g) is higher than price.input (%g); "+
			"a cached read costs less than a miss, so these look swapped",
			id, p.CacheRead, p.Input)
	}
	return nil
}

// merge folds a file onto the built-ins. Pure, so the rules are testable without
// touching a filesystem or package state.
func merge(baseProviders map[string]Provider, baseModels []Model, f fileConfig) (
	map[string]Provider, []Model, string, []string, error) {

	ps := make(map[string]Provider, len(baseProviders)+len(f.Providers))
	for k, v := range baseProviders {
		ps[k] = v
	}
	var warnings []string

	for name, pf := range f.Providers {
		if err := validateProvider(name, pf); err != nil {
			return nil, nil, "", warnings, err
		}
		if _, replaced := ps[name]; replaced {
			warnings = append(warnings, fmt.Sprintf(
				"provider %q from the config file replaces the built-in one", name))
		}
		ps[name] = Provider{
			Name: name, BaseURL: pf.BaseURL, KeyEnv: pf.KeyEnv, BaseURLEnv: pf.BaseURLEnv,
			pinnedBaseURL: true,
		}
	}

	ms := make([]Model, len(baseModels))
	copy(ms, baseModels)
	index := func(id string) int {
		for i, m := range ms {
			if strings.EqualFold(m.ID, id) {
				return i
			}
		}
		return -1
	}
	for _, mf := range f.Models {
		if strings.TrimSpace(mf.ID) == "" {
			return nil, nil, "", warnings, fmt.Errorf("a model entry has no id")
		}
		if _, ok := ps[mf.Provider]; !ok {
			return nil, nil, "", warnings, fmt.Errorf(
				"model %q names provider %q, which is neither built in nor defined in this file",
				mf.ID, mf.Provider)
		}
		if err := validatePrice(mf.ID, mf.Price); err != nil {
			return nil, nil, "", warnings, err
		}
		m := Model{
			ID: mf.ID, Provider: mf.Provider, Aliases: mf.Aliases,
			ContextWindow: mf.ContextWindow, MaxTokens: mf.MaxTokens,
			SubagentModel: mf.SubagentModel, Subagent: mf.Subagent,
			Price: mf.Price.toLLM(),
		}
		if i := index(mf.ID); i >= 0 {
			// Replacing a built-in model is announced, like replacing a built-in
			// provider is. It was not, and that silence had a cost: glm-5.2's window
			// was corrected in the catalogue from 200,000 to 1,048,576, and a config
			// file that re-declared the id kept the old figure with nothing said —
			// which quietly kept -context-edit auto clearing at a tenth of the real
			// window and the browser gauge going red at a sixth of it.
			//
			// The dropped fields are named rather than just counted, because the
			// replacement is wholesale: a field the file omits does not fall back to
			// the built-in, it is gone. That is the part nobody expects, and the
			// warning is the only place it can be said.
			if dropped := lostFields(ms[i], m); len(dropped) > 0 {
				warnings = append(warnings, fmt.Sprintf(
					"model %q from the config file replaces the built-in one, dropping %s "+
						"(the whole entry is replaced, not merged field by field)",
					mf.ID, strings.Join(dropped, ", ")))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"model %q from the config file replaces the built-in one", mf.ID))
			}
			ms[i] = m
		} else {
			ms = append(ms, m)
		}
	}

	// Checked after everything is merged, because a subagent model may be defined
	// later in the file than the model that names it. A bad reference is dropped
	// with a warning rather than refused: the parent model still works, and losing
	// a session over an optional field would be the wrong trade.
	known := func(id string) bool {
		for _, m := range ms {
			if strings.EqualFold(m.ID, id) {
				return true
			}
		}
		return false
	}
	for i := range ms {
		if ms[i].SubagentModel == "" {
			continue
		}
		if !known(ms[i].SubagentModel) {
			warnings = append(warnings, fmt.Sprintf(
				"model %q names subagent_model %q, which is not a known model; subagents "+
					"will inherit %s instead", ms[i].ID, ms[i].SubagentModel, ms[i].ID))
			ms[i].SubagentModel = ""
		}
	}

	def := strings.TrimSpace(f.Default)
	if def != "" && !known(def) {
		return nil, nil, "", warnings, fmt.Errorf("default names %q, which is not a known model", def)
	}
	return ps, ms, def, warnings, nil
}

func validateProvider(name string, p providerFile) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("a provider has an empty name")
	}
	// Empty key_env is legitimate: the endpoint needs no credential. A non-empty
	// value must be a variable *name* — deliberately not echoed on failure,
	// because the most likely reason it fails this check is that someone pasted
	// the credential itself.
	if p.KeyEnv != "" && !envName.MatchString(p.KeyEnv) {
		return fmt.Errorf("provider %q: key_env must be the name of an environment "+
			"variable holding the key, not the key itself", name)
	}
	if p.BaseURLEnv != "" && !envName.MatchString(p.BaseURLEnv) {
		return fmt.Errorf("provider %q: base_url_env must be an environment variable name", name)
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return fmt.Errorf("provider %q: base_url %q is not a URL", name, p.BaseURL)
	}
	// Scheme before host, because the scheme is the part that decides whether the
	// credential is protected, and because it produces the better message: a bare
	// "api.example/v1" parses as a path with no scheme, and "add https://" is more
	// use than "no host".
	switch u.Scheme {
	case "https":
	case "http":
		// The credential travels in a header on every request, so plaintext to a
		// remote host means handing it to anything on the path. Loopback is exempted
		// because a local model server is the main reason to write this file at all,
		// and there is no network to observe.
		if !loopback(u.Hostname()) {
			what := p.KeyEnv
			if what == "" {
				what = "the conversation"
			}
			return fmt.Errorf("provider %q: base_url %q is plain http to a remote host, "+
				"which would send %s in cleartext; use https, or a loopback address for a "+
				"local server", name, p.BaseURL, what)
		}
	default:
		return fmt.Errorf("provider %q: base_url %q must start with https:// (or http:// "+
			"for a loopback address)", name, p.BaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("provider %q: base_url %q names no host", name, p.BaseURL)
	}
	return nil
}

func loopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return strings.HasSuffix(strings.ToLower(host), ".localhost")
}

// SubagentModel is the model a read-only subagent should run for a given parent
// model, or "" to inherit.
//
// Falls back to inheriting when the mapped model's provider has no key, because
// the alternative is a subagent that cannot start for a reason the parent did not
// choose and cannot see. The parent's own model is known to work — it is running.
func SubagentModel(parent string) string {
	m, ok := lookup(parent)
	if !ok || m.SubagentModel == "" {
		return ""
	}
	sub, ok := lookup(m.SubagentModel)
	if !ok || !Configured(sub.Provider) {
		return ""
	}
	return sub.ID
}

// SubagentPool is the set of models parallel explore subagents are balanced
// across, in catalog order: every model marked "subagent": true whose provider
// has a key. An empty pool means subagents inherit the parent's model, which is
// the behaviour with no file at all.
//
// Order is the tie-break for equal in-flight counts, so the file's writing
// order is the preference order. The key check happens here rather than at pick
// time because an unconfigured provider never becomes usable mid-session: the
// environment does not change under a running process.
func SubagentPool() []Model {
	var out []Model
	for _, m := range catalog {
		if m.Subagent && Configured(m.Provider) {
			out = append(out, m)
		}
	}
	return out
}
