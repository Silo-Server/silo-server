package watchsync

import "time"

// ListItemState is the per-connection, per-list shadow record tracking whether
// an item is present locally and/or remotely, used to drive incremental
// export/removal reconciliation. It serves both the favorites and watchlist
// lists, discriminated by ListKind.
type ListItemState struct {
	ID                  string
	ConnectionID        string
	ListKind            ListKind
	MediaItemID         string
	ProviderItemKey     string
	Kind                string
	Title               string
	Year                int
	RemotePresent       bool
	LocalPresent        bool
	LastSeenRemoteAt    *time.Time
	LastSeenLocalAt     *time.Time
	LastExportedAt      *time.Time
	LastRemovedRemoteAt *time.Time
	LastRemovedLocalAt  *time.Time
	LastError           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	// TitleIDs are, on a watchlist title row, the prefixed IDs ("tmdb:550",
	// "imdb:tt0137523", "tvdb:121361") the title has held. Writes add to them.
	TitleIDs []string
}
