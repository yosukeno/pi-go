// Package config resolves a model name into an endpoint plus a credential.
//
// Credentials come from the environment only. There is no key file and no
// fallback: a secret that lives in one place is a secret you can rotate in one
// place, and a process that cannot find a key on disk cannot leak one either.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/wangy/pi-go/llm"
)

// Resolved is everything llm.New needs, plus the two figures the harness reads off
// the model rather than the endpoint: ContextWindow and Price.
type Resolved struct {
	Model         string
	Provider      string
	BaseURL       string
	APIKey        string
	MaxTokens     int64
	ContextWindow int
	// Price is the declared per-million-token rate, or nil when nobody declared one
	// — which is the case for every built-in. Only -cost-budget reads it, and it
	// refuses to run rather than treating nil as free. See llm.Price.
	Price *llm.Price
}

// Resolve looks up a model by id or alias and attaches its provider's endpoint
// and key. An empty name selects the default model.
func Resolve(name string) (Resolved, error) {
	if name == "" {
		name = DefaultModel()
	}
	m, ok := lookup(name)
	if !ok {
		return Resolved{}, fmt.Errorf("unknown model %q; known models: %s", name, strings.Join(names(), ", "))
	}
	p := providers[m.Provider]

	// A provider with no key_env needs no credential; the client then omits the
	// Authorization header rather than sending "Bearer ".
	var key string
	if p.KeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.KeyEnv))
		if key == "" {
			return Resolved{}, fmt.Errorf("%s is not set; export it to use %s (provider %q)", p.KeyEnv, m.ID, p.Name)
		}
	}

	baseURL := p.BaseURL
	// The env override only moves a built-in default. A base_url the user wrote
	// in their file is pinned: environment variables are ambient and often
	// occupied by other clients, so they must not reroute an explicit choice.
	if !p.pinnedBaseURL {
		if override := strings.TrimSpace(os.Getenv(p.BaseURLEnv)); override != "" {
			baseURL = override
		}
	}
	return Resolved{
		Model:         m.ID,
		Provider:      p.Name,
		BaseURL:       strings.TrimSuffix(baseURL, "/"),
		APIKey:        key,
		MaxTokens:     m.MaxTokens,
		ContextWindow: m.ContextWindow,
		Price:         m.Price,
	}, nil
}

// Configured reports whether a provider's key is present, for listing models
// without attempting a call. A provider with no key_env needs no credential and
// is always configured.
func Configured(provider string) bool {
	p, ok := providers[provider]
	if !ok {
		return false
	}
	if p.KeyEnv == "" {
		return true
	}
	return strings.TrimSpace(os.Getenv(p.KeyEnv)) != ""
}

// KeyEnv is the environment variable a provider reads its key from.
func KeyEnv(provider string) string { return providers[provider].KeyEnv }

func names() []string {
	out := make([]string, 0, len(catalog))
	for _, m := range catalog {
		out = append(out, m.ID)
	}
	return out
}
