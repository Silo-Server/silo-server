package autoscan

import (
	"context"
	"fmt"
	"strings"
)

// ResolvedConnection is concrete credentials handed to the plugin.
type ResolvedConnection struct {
	BaseURL string
	APIKey  string
}

// RequestIntegrationLookup resolves a soft-linked Requests integration to its
// base URL and encrypted api-key reference.
type RequestIntegrationLookup interface {
	Get(ctx context.Context, integrationID string) (baseURL, apiKeyRef string, err error)
}

// SecretResolver resolves an encrypted api-key reference to its plaintext value.
type SecretResolver interface {
	Get(ctx context.Context, ref string) (string, error)
}

type ConnectionResolver struct {
	requests RequestIntegrationLookup
	secrets  SecretResolver
}

func NewConnectionResolver(r RequestIntegrationLookup, s SecretResolver) *ConnectionResolver {
	return &ConnectionResolver{requests: r, secrets: s}
}

// Resolve turns a stored Connection into concrete credentials. When the
// connection is linked to a Requests integration the live base URL + key ref are
// read from there; otherwise the connection's own fields are used. The api-key
// ref is then resolved to plaintext via the secrets resolver.
func (cr *ConnectionResolver) Resolve(ctx context.Context, c Connection) (ResolvedConnection, error) {
	baseURL, apiKeyRef := c.BaseURL, c.APIKeyRef
	if c.RequestIntegrationID != nil {
		if cr.requests == nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: linked requests integration %q: no lookup configured", *c.RequestIntegrationID)
		}
		u, ref, err := cr.requests.Get(ctx, *c.RequestIntegrationID)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: linked requests integration %q: %w", *c.RequestIntegrationID, err)
		}
		baseURL, apiKeyRef = u, ref
	}
	apiKey := strings.TrimSpace(apiKeyRef)
	if cr.secrets != nil && apiKey != "" {
		resolved, err := cr.secrets.Get(ctx, apiKey)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: resolve api key: %w", err)
		}
		if resolved = strings.TrimSpace(resolved); resolved != "" {
			apiKey = resolved
		}
	}
	return ResolvedConnection{BaseURL: baseURL, APIKey: apiKey}, nil
}
