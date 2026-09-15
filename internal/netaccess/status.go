package netaccess

import (
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrProviderNotFound reports a provider slug no enabled installation on this
// host declares. The plugin service and the proxy's bearer routes share it so
// the API can tell "unknown provider" from a failed call.
var ErrProviderNotFound = errors.New("network access provider not found")

// Provider states as reported by network_access_provider.v1 plugins.
const (
	StateDisconnected          = "disconnected"
	StateAwaitingAuthorization = "awaiting_authorization"
	StateConnecting            = "connecting"
	StateConnected             = "connected"
	StateError                 = "error"
	// StateUnavailable is host-side: the provider's plugin process is not
	// running on that host, so no status can be read from it. A plugin never
	// reports it.
	StateUnavailable = "unavailable"
)

// Listener is one host listener a provider exposes on the overlay.
type Listener struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
}

// Status is the last status a provider instance on this host reported
// through RuntimeHost.ReportNetworkAccessStatus. It mirrors the SDK's
// NetworkAccessStatus without depending on the generated types so the
// packages that consume it (API handlers, stream URL selection) stay free of
// the plugin SDK. The JSON form is what a proxy node answers the API's
// bearer-authenticated network-access routes with; it carries auth_url and
// error because those routes take the node bearer, unlike the public /health
// report (NodeProviderStatus).
type Status struct {
	InstallationID   int        `json:"installation_id"`
	Provider         string     `json:"provider"`
	State            string     `json:"state"`
	Hostname         string     `json:"hostname,omitempty"`
	Origin           string     `json:"origin,omitempty"`
	Addresses        []string   `json:"addresses,omitempty"`
	Listeners        []Listener `json:"listeners,omitempty"`
	AuthURL          string     `json:"auth_url,omitempty"`
	Error            string     `json:"error,omitempty"`
	ProviderVersion  string     `json:"provider_version,omitempty"`
	DesiredConnected bool       `json:"desired_connected,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at,omitzero"`
}

// Connected reports whether the provider currently serves overlay traffic.
func (s Status) Connected() bool { return s.State == StateConnected }

// StatusCache holds the most recent status of every provider instance
// running in this process. Plugins push on every change; the cache is the
// host's view between pushes and answers the WebSocket origin check with the
// overlay origins currently connected.
type StatusCache struct {
	mu      sync.RWMutex
	entries map[int]Status
	now     func() time.Time
}

// NewStatusCache returns an empty cache.
func NewStatusCache() *StatusCache {
	return &StatusCache{entries: make(map[int]Status), now: time.Now}
}

// Report records a status push. It returns the previous status for the
// installation and whether the state field changed, so the caller can log
// transitions without the cache knowing about logging.
func (c *StatusCache) Report(status Status) (previous Status, changed bool) {
	if c == nil || status.InstallationID == 0 {
		return Status{}, false
	}
	status.UpdatedAt = c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	previous, had := c.entries[status.InstallationID]
	c.entries[status.InstallationID] = status
	return previous, !had || previous.State != status.State
}

// Forget drops the installation's status, for a provider whose process
// stopped: an origin nobody serves must not stay in the allow list.
func (c *StatusCache) Forget(installationID int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, installationID)
	c.mu.Unlock()
}

// Get returns the status of one installation.
func (c *StatusCache) Get(installationID int) (Status, bool) {
	if c == nil {
		return Status{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	status, ok := c.entries[installationID]
	return status, ok
}

// ByProvider returns the status of the provider with the given slug.
func (c *StatusCache) ByProvider(provider string) (Status, bool) {
	if c == nil {
		return Status{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, status := range c.entries {
		if status.Provider == provider {
			return status, true
		}
	}
	return Status{}, false
}

// List returns every cached status ordered by installation id.
func (c *StatusCache) List() []Status {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	out := make([]Status, 0, len(c.entries))
	for _, status := range c.entries {
		out = append(out, status)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].InstallationID < out[j].InstallationID })
	return out
}

// ConnectedOrigins returns the normalized scheme://host[:port] origins of
// every connected provider on this host: the api origin plus each exposed
// listener's origin. Browsers reaching Silo over the overlay send one of
// these as Origin, so the WebSocket handshake accepts them next to the
// configured public URL.
func (c *StatusCache) ConnectedOrigins() []string {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]struct{})
	var out []string
	add := func(raw string) {
		origin, ok := NormalizeOrigin(raw)
		if !ok {
			return
		}
		if _, dup := seen[origin]; dup {
			return
		}
		seen[origin] = struct{}{}
		out = append(out, origin)
	}
	for _, status := range c.entries {
		if !status.Connected() {
			continue
		}
		add(status.Origin)
		for _, listener := range status.Listeners {
			add(listener.Origin)
		}
	}
	sort.Strings(out)
	return out
}

// NormalizeOrigin reduces raw to lowercase scheme://host[:port] and reports
// whether raw was an http(s) origin at all.
func NormalizeOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Host), true
}
