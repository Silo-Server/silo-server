// Package progresssync owns immutable full-replacement progress snapshots.
// It deliberately has no incremental checkpoint or HTTP activation.
package progresssync

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	MaxPageSize              = 200
	MaxSnapshotItems         = 100000
	MaxSnapshotBytes   int64 = 64 << 20
	MaxActiveSnapshots       = 2
	SnapshotTTL              = 15 * time.Minute
	ReplayRetention          = 24 * time.Hour
	AdmissionTimeout         = 30 * time.Second
)

var (
	ErrInvalid         = errors.New("invalid progress snapshot input")
	ErrNotFound        = errors.New("progress snapshot not found")
	ErrResetRequired   = errors.New("progress snapshot reset required")
	ErrRequestConflict = errors.New("progress snapshot request changed")
	ErrQuota           = errors.New("too many active progress snapshots")
	ErrTooLarge        = errors.New("progress snapshot exceeds admission bounds")
)

type Identity struct {
	UserID    int
	ProfileID string
}

// Entry is a domain-owned projection in seconds, matching existing progress reads.
// No sequence, credential, or delivery acknowledgement is included.
type Entry struct {
	MediaItemID     string    `json:"media_item_id"`
	PositionSeconds float64   `json:"position_seconds"`
	DurationSeconds float64   `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Visibility must authorize the account/profile and return its nonempty effective
// access digest and the visible subset of IDs, using this transaction for reads.
// It is called with no IDs to authorize empty snapshots as well. There is no
// allow-all default; production wiring must use the owning viewer-access policy.
// A caller must additionally verify current request/PIN authority before and after
// invoking this package. No policy implementation or route is activated here.
type Visibility func(context.Context, pgx.Tx, Identity, []string) (digest string, visible map[string]bool, err error)

type Snapshot struct {
	ID string
	Identity
	RequestID      string
	InstallationID string
	Generation     string
	AccessDigest   string
	PageSize       int
	CapturedAt     time.Time
	ExpiresAt      time.Time
	ItemCount      int
	ByteCount      int64
}

// Position is storage continuation, not a public token. The transport must sign
// every field together with operation and mode; numeric legacy cursors do not fit.
type Position struct {
	SnapshotID     string
	InstallationID string
	Generation     string
	AccessDigest   string
	Identity
	PageSize int
	After    int
}
type Page struct {
	Snapshot Snapshot
	Items    []Entry
	Next     *Position
}
