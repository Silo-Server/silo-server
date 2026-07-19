package watchsync

import (
	"fmt"
	"sort"
	"sync"
)

const providerSourcePlugin = "plugin"

type sourcedProvider interface {
	ProviderSource() string
}

type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

func (r *Registry) Register(provider Provider) error {
	if provider == nil {
		return fmt.Errorf("watchsync provider is nil")
	}

	key := provider.Key()
	if key == "" {
		return fmt.Errorf("watchsync provider key is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[key]; exists {
		return fmt.Errorf("watchsync provider %q already registered", key)
	}
	r.providers[key] = provider
	return nil
}

// ReplacePluginProviders atomically replaces providers discovered from enabled
// plugin installations while preserving built-in providers. A plugin may not
// shadow a built-in key, and duplicate plugin keys reject the entire reload.
func (r *Registry) ReplacePluginProviders(providers []Provider) error {
	if r == nil {
		return fmt.Errorf("watchsync registry is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	next := make(map[string]Provider, len(r.providers)+len(providers))
	for key, provider := range r.providers {
		if sourced, ok := provider.(sourcedProvider); ok && sourced.ProviderSource() == providerSourcePlugin {
			continue
		}
		next[key] = provider
	}
	for _, provider := range providers {
		if provider == nil || provider.Key() == "" {
			return fmt.Errorf("watchsync plugin provider and key are required")
		}
		if _, exists := next[provider.Key()]; exists {
			return fmt.Errorf("watchsync plugin provider key %q conflicts with another provider", provider.Key())
		}
		next[provider.Key()] = provider
	}
	r.providers = next
	return nil
}

func (r *Registry) Get(key string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	provider, ok := r.providers[key]
	return provider, ok
}

func (r *Registry) List() []ProviderSummary {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	summaries := make([]ProviderSummary, 0, len(r.providers))
	for key, provider := range r.providers {
		summaries = append(summaries, ProviderSummary{
			Key:          key,
			DisplayName:  provider.DisplayName(),
			Capabilities: provider.Capabilities(),
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Key < summaries[j].Key
	})
	return summaries
}
