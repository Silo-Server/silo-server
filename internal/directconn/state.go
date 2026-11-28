package directconn

import "context"

// DirectState answers playback questions about one device under the planner's
// read rule: one store read per request, map lookups afterwards.
type DirectState interface {
	// Device returns the device's enrollment and the API host's live path for
	// it. A device without a live enrollment returns ok=false.
	Device(ctx context.Context, userID int, deviceID string) (DeviceDirect, bool, error)
	// PathGeneration reports the API host's current path generation for the
	// device, or 0 when no live path exists.
	PathGeneration(userID int, deviceID string) uint64
	// PathObservation reports, in one observation, whether this replica's
	// engine is running and the device's path generation. A caller that needs
	// both must use it: PathGeneration alone answers 0 for "no engine here" and
	// for "engine here, no live path", and two separate calls can straddle an
	// engine stop or restart.
	PathObservation(userID int, deviceID string) (engineRunning bool, generation uint64)
	// Enrolled reports whether the device holds a live enrollment.
	Enrolled(ctx context.Context, userID int, deviceID string) (bool, error)
}

// DeviceDirect is one device's direct-connection state.
type DeviceDirect struct {
	Policy         string // "automatic" or "direct_only"
	APIReady       bool   // engine up and the API host's confirmation for the device is live
	ConnectionKey  string // "direct:" + HMAC of the peer's public IP, empty when unresolved
	PathGeneration uint64 // random per path, never 0 when APIReady
}
