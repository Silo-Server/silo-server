package metadata

import (
	"context"
	"sort"
	"sync"
)

// Built-in host metadata providers are registered as data: one reserved
// plugin_installations row (kind='builtin', plugin_id='silo.builtin') carries
// ordinary plugin_capabilities rows, so the chain store, seeding, defaults
// endpoint, and admin UI all work unchanged. This file is the single
// in-process registration point mapping those capability ids to provider
// constructors: buildProviders returns the registered provider instead of a
// gRPC PluginProvider when the resolved entry's installation is a builtin.
//
// Providers self-register from their package's init (e.g. internal/metadata/nfo)
// to avoid an import cycle — this package defines the Provider interfaces the
// builtin packages implement. cmd/silo blank-imports each builtin package.
// Activating a future builtin (e.g. xattr) is a registration call plus a
// capability row in a migration.

// IdentityHintProvider is the narrow contract a built-in local provider
// implements to contribute trusted identity hints (tmdb/imdb/tvdb ids parsed
// from curated sidecar files) before Phase-1 search. Hints anchor candidate
// selection through the trusted-hint machinery, unlocking remote enrichment
// by ID even when remote search-by-title returns nothing.
type IdentityHintProvider interface {
	IdentityHints(ctx context.Context, query SearchQuery) map[string]string
}

var (
	builtinProvidersMu sync.RWMutex
	builtinProviders   = map[string]func() Provider{}
)

// RegisterBuiltinProvider registers the constructor for a built-in provider
// under its plugin_capabilities capability_id. Later registrations for the
// same capability id replace earlier ones.
func RegisterBuiltinProvider(capabilityID string, construct func() Provider) {
	// Registration only happens from package init(); empty id or nil constructor
	// is a programmer error that would silently disable a provider, so fail loud.
	if capabilityID == "" || construct == nil {
		panic("metadata: RegisterBuiltinProvider requires a non-empty capabilityID and non-nil constructor")
	}
	builtinProvidersMu.Lock()
	builtinProviders[capabilityID] = construct
	builtinProvidersMu.Unlock()
}

// BuiltinCapabilityIDs returns the registered builtin capability ids, sorted.
func BuiltinCapabilityIDs() []string {
	builtinProvidersMu.RLock()
	ids := make([]string, 0, len(builtinProviders))
	for id := range builtinProviders {
		ids = append(ids, id)
	}
	builtinProvidersMu.RUnlock()
	sort.Strings(ids)
	return ids
}

// builtinProvider constructs the registered in-process provider for a builtin
// capability id.
func builtinProvider(capabilityID string) (Provider, bool) {
	builtinProvidersMu.RLock()
	construct, ok := builtinProviders[capabilityID]
	builtinProvidersMu.RUnlock()
	if !ok {
		return nil, false
	}
	return construct(), true
}
