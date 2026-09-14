package netaccess

import (
	"maps"
	"strings"
	"time"
)

// NodeProviderStatus is what a node reports about one network access provider
// running beside it: the subset of Status a remote reader needs to decide
// whether clients on that provider's overlay can reach the node, and where.
// It travels in the node's /health response and is stored verbatim on the
// node's row, so it carries only stable, non-secret fields — never the auth
// URL or the error text.
type NodeProviderStatus struct {
	State    string `json:"state"`
	Origin   string `json:"origin,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// UpdatedAt is when the node last heard from the provider, on the node's
	// clock. Display data; nothing routes on it.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// NodeNetworkAccess is a node's last provider report keyed by provider slug.
// A nil or empty map means the node reports no providers.
type NodeNetworkAccess map[string]NodeProviderStatus

// ConnectedOrigin returns the normalized scheme://host[:port] origin clients on
// the provider's overlay should use to reach the node, and whether there is
// one: the provider must be connected and must have reported a well-formed
// http(s) origin. Anything else is "not reachable this way", which callers
// turn into their API-relative fallback rather than into a guess.
func (m NodeNetworkAccess) ConnectedOrigin(provider string) (string, bool) {
	if len(m) == 0 || provider == "" {
		return "", false
	}
	status, ok := m[provider]
	if !ok || !status.Connected() {
		return "", false
	}
	return NormalizeOrigin(status.Origin)
}

// Connected reports whether the provider currently serves overlay traffic.
func (s NodeProviderStatus) Connected() bool { return s.State == StateConnected }

// Normalized returns a copy with provider keys and string fields trimmed and
// entries with an empty provider dropped, so a report is stored and compared
// in one canonical form regardless of how the node spelled it. nil in, nil
// out; an empty result is also nil so "reports none" has one representation.
func (m NodeNetworkAccess) Normalized() NodeNetworkAccess {
	if len(m) == 0 {
		return nil
	}
	out := make(NodeNetworkAccess, len(m))
	for provider, status := range m {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			continue
		}
		status.State = strings.TrimSpace(status.State)
		status.Origin = strings.TrimSpace(status.Origin)
		status.Hostname = strings.TrimSpace(status.Hostname)
		out[provider] = status
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Clone returns an independent copy, nil for nil.
func (m NodeNetworkAccess) Clone() NodeNetworkAccess {
	if m == nil {
		return nil
	}
	return maps.Clone(m)
}

// NodeNetworkAccess renders the cache as the per-provider report a node puts
// on its /health response: one entry per provider instance running in this
// process, keyed by provider slug. This is the shape the API server stores on
// the node's row and hands stream URL selection.
func (c *StatusCache) NodeNetworkAccess() NodeNetworkAccess {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.entries) == 0 {
		return nil
	}
	out := make(NodeNetworkAccess, len(c.entries))
	for _, status := range c.entries {
		if status.Provider == "" {
			continue
		}
		out[status.Provider] = NodeProviderStatus{
			State:     status.State,
			Origin:    status.Origin,
			Hostname:  status.Hostname,
			UpdatedAt: status.UpdatedAt,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// HostStatusReport is what a proxy node answers the API's bearer-authenticated
// GET /network-access/status with: the full status (auth URL and error text
// included, since the caller holds the node bearer) of every provider
// instance running beside it. A provider whose process is not running is
// listed as StateUnavailable with the reason in Error.
type HostStatusReport struct {
	Providers []Status `json:"providers"`
}
