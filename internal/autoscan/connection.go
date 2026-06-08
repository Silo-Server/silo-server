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
// base URL and (already-decrypted) api key.
type RequestIntegrationLookup interface {
	Get(ctx context.Context, integrationID string) (baseURL, apiKey string, err error)
}

type ConnectionResolver struct {
	requests RequestIntegrationLookup
}

func NewConnectionResolver(r RequestIntegrationLookup) *ConnectionResolver {
	return &ConnectionResolver{requests: r}
}

// Resolve turns a stored Connection into concrete credentials. When the
// connection is linked to a Requests integration the live base URL + key are
// read from there (the Requests repo decrypts it); otherwise the connection's
// own fields are used (the autoscan repo already decrypted api_key_ref on read).
// Either way the returned key is plaintext — there is no longer a separate
// secret-resolution step.
func (cr *ConnectionResolver) Resolve(ctx context.Context, c Connection) (ResolvedConnection, error) {
	baseURL, apiKey := c.BaseURL, c.APIKeyRef
	// A pointer-to-empty-string request_integration_id is NOT a live link (it can
	// arise from a both-NULL orphan or a stripped link). Treat empty/whitespace as
	// "no link" so we don't call requests.Get("") and use the connection's own
	// fields instead.
	if c.RequestIntegrationID != nil && strings.TrimSpace(*c.RequestIntegrationID) != "" {
		if cr.requests == nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: linked requests integration %q: no lookup configured", *c.RequestIntegrationID)
		}
		u, key, err := cr.requests.Get(ctx, *c.RequestIntegrationID)
		if err != nil {
			return ResolvedConnection{}, fmt.Errorf("autoscan: linked requests integration %q: %w", *c.RequestIntegrationID, err)
		}
		baseURL, apiKey = u, key
	}
	return ResolvedConnection{BaseURL: baseURL, APIKey: strings.TrimSpace(apiKey)}, nil
}
