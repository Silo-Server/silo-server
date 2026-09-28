package netaccess

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
)

// IngressTokenBytes is the entropy of one ingress token.
const IngressTokenBytes = 32

// Ingress identifies the plugin instance behind a validated ingress token.
type Ingress struct {
	InstallationID int
	Provider       string
}

type registryEntry struct {
	token    []byte
	provider string
}

// Registry maps ingress tokens to the resident plugin installation that
// received them. The resident supervisor issues a fresh token every time it
// starts a network access provider and revokes it when the process stops, so
// a token outlives neither the plugin process nor the host process (the
// registry is in memory only). Lookups compare in constant time.
type Registry struct {
	mu      sync.RWMutex
	entries map[int]registryEntry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[int]registryEntry)}
}

// Issue generates a new token for the installation, replacing any previous
// one, and returns its wire form. provider is the provider slug the
// installation's capability declares; it becomes Path.Provider for requests
// carrying the token.
func (r *Registry) Issue(installationID int, provider string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("netaccess: registry is nil")
	}
	if installationID <= 0 {
		return "", fmt.Errorf("netaccess: installation id %d is not a persisted installation", installationID)
	}
	if provider == "" {
		return "", fmt.Errorf("netaccess: provider is required")
	}
	raw := make([]byte, IngressTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("netaccess: generate ingress token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	r.mu.Lock()
	r.entries[installationID] = registryEntry{token: []byte(token), provider: provider}
	r.mu.Unlock()
	return token, nil
}

// Revoke forgets the installation's token if it is still the one given, and
// reports whether it did. A stop that races a replacing start therefore never
// removes the token the new process was just issued. Requests still carrying
// a revoked token are rejected until a new one is issued.
func (r *Registry) Revoke(installationID int, token string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[installationID]
	if !ok || subtle.ConstantTimeCompare([]byte(token), entry.token) != 1 {
		return false
	}
	delete(r.entries, installationID)
	return true
}

// IngressToken returns the installation's current token for GetHostInfo.
func (r *Registry) IngressToken(installationID int) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[installationID]
	if !ok {
		return "", false
	}
	return string(entry.token), true
}

// Provider returns the provider slug registered for the installation.
func (r *Registry) Provider(installationID int) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[installationID]
	if !ok {
		return "", false
	}
	return entry.provider, true
}

// Lookup resolves a presented token. Every registered token is compared in
// constant time so the response time does not reveal which one was close.
func (r *Registry) Lookup(token string) (Ingress, bool) {
	if r == nil || token == "" {
		return Ingress{}, false
	}
	presented := []byte(token)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var (
		match Ingress
		found int
	)
	for installationID, entry := range r.entries {
		if subtle.ConstantTimeCompare(presented, entry.token) == 1 {
			match = Ingress{InstallationID: installationID, Provider: entry.provider}
			found = 1
		}
	}
	return match, found == 1
}
