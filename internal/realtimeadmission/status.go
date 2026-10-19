package realtimeadmission

import "github.com/Silo-Server/silo-server/internal/ratelimit"

const unavailableScope = "unavailable"

// RoomLimits apply member first, then account, with no refund on later denial.
type RoomLimits struct {
	Member  ratelimit.WorkLimit `json:"member"`
	Account ratelimit.WorkLimit `json:"account"`
}

// Status describes the startup-selected work backend, not generic HTTP limits.
// Epoch identifies this controller's lifetime; Redis counters can outlive it.
// Shared scope assumes retained Redis counter state and store-window time, and
// is not a wall-clock or failover/data-loss guarantee. Memory is always per node.
type Status struct {
	Version       int                 `json:"version"`
	Backend       string              `json:"backend"`
	Scope         string              `json:"scope"`
	Epoch         string              `json:"epoch"`
	Semantics     string              `json:"semantics"`
	DeadlineMS    int                 `json:"deadline_ms"`
	EventsAccount ratelimit.WorkLimit `json:"events_account"`
	RoomRecovery  RoomLimits          `json:"room_recovery"`
	RoomAction    RoomLimits          `json:"room_action"`
}

func newStatus(backend, epoch string, config configuration) Status {
	scope := unavailableScope
	switch backend {
	case "memory":
		scope = "per_node"
	case "redis":
		scope = "shared"
	}
	return Status{
		Version: 1, Backend: backend, Scope: scope, Epoch: epoch,
		Semantics: "charged_permits_aligned_fixed_windows_no_refunds", DeadlineMS: 250,
		EventsAccount: config.events, RoomRecovery: config.recovery, RoomAction: config.actions,
	}
}

// Status returns a value copy, so callers cannot alter enforcement settings.
func (c *Controller) Status() Status { return c.status }
