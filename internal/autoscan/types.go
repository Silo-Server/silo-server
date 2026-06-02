package autoscan

import "time"

// Settings is the global autoscan configuration (singleton row).
type Settings struct {
	Enabled             bool      `json:"enabled"`
	PollIntervalMinutes int       `json:"poll_interval_minutes"`
	DebounceSeconds     int       `json:"debounce_seconds"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// PathRewrite is an optional prefix translation from an arr path to a Silo path.
type PathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Source is an autoscan-enabled Radarr/Sonarr instance. Kind/BaseURL/APIKeyRef/Name
// are read from request_integrations; the rest live in autoscan_sources.
type Source struct {
	IntegrationID string        `json:"integration_id"`
	Kind          string        `json:"kind"`
	Name          string        `json:"name"`
	BaseURL       string        `json:"-"`
	APIKeyRef     string        `json:"-"`
	Enabled       bool          `json:"enabled"`
	PathRewrites  []PathRewrite `json:"path_rewrites"`
	LastPollAt    *time.Time    `json:"last_poll_at,omitempty"`
}

// SourceUpdate is the admin-editable subset of a source.
type SourceUpdate struct {
	Enabled      bool          `json:"enabled"`
	PathRewrites []PathRewrite `json:"path_rewrites"`
}
