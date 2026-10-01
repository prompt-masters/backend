package ai

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
)

// ProviderConfig is what an adapter needs to build a client.
type ProviderConfig struct {
	APIKey APIKey
	// BaseURL overrides the provider's endpoint; tests point it at an
	// httptest server.
	BaseURL    string
	HTTPClient *http.Client
	Pricing    Pricing
}

// Factory builds a client for one provider.
type Factory func(ProviderConfig) (AIClient, error)

// Registry builds clients by provider name, which is what lets configuration
// swap providers without code changes.
type Registry struct {
	factories map[string]Factory
}

func NewRegistry(factories map[string]Factory) *Registry {
	return &Registry{factories: maps.Clone(factories)}
}

// New builds the named provider's client.
func (r *Registry) New(name string, cfg ProviderConfig) (AIClient, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("unknown AI provider %q (known: %v)", name, slices.Sorted(maps.Keys(r.factories)))
	}
	return f(cfg)
}

// Clients builds one client for every provider cfg uses.
func (r *Registry) Clients(cfg Config, httpClient *http.Client) (map[string]AIClient, error) {
	clients := make(map[string]AIClient)
	for _, name := range cfg.ProviderNames() {
		p := cfg.Providers[name]
		c, err := r.New(name, ProviderConfig{APIKey: p.APIKey, BaseURL: p.BaseURL, HTTPClient: httpClient, Pricing: cfg.Pricing})
		if err != nil {
			return nil, err
		}
		clients[name] = c
	}
	return clients, nil
}
