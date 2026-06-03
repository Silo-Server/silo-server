package autoscan

import "time"

// Settings is the global autoscan configuration (singleton row).
type Settings struct {
	Enabled                    bool
	DefaultPollIntervalSeconds int
	DebounceSeconds            int
}

// Connection is an arr server the host can reach: either own credentials, or a
// live reference to a Requests integration (RequestIntegrationID set).
type Connection struct {
	ID                   string
	Name                 string
	Kind                 string
	BaseURL              string // own; empty when linked
	APIKeyRef            string // own; empty when linked
	RequestIntegrationID *string
}

// Source ties a scan_source plugin capability instance to a connection plus the
// host-owned scheduling/bookkeeping state.
type Source struct {
	ID                  string
	InstallationID      int
	CapabilityID        string
	ConnectionID        *string // nil until an operator binds a connection
	Enabled             bool
	PollIntervalSeconds *int    // nil => use settings default
	Marker              *string // opaque; nil on first run
	LastRunAt           *time.Time
	LastError           *string
}
