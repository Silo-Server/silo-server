package mediasample

import (
	"context"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// HardwareResolver turns the playback.hw_accel and playback.hw_device
// settings into the backend a Runner decodes on. Consumers that sample in
// the background, such as credits detection, keep one for the life of the
// process. The backend is kept per configured pair, since resolving "auto"
// probes the host, until the playback probe cache is invalidated. A
// resolution to no hardware, which "auto" gives when a smoke probe fails, is
// asked again after HardwareRetryInterval, so a GPU that recovers takes the
// work back.
type HardwareResolver struct {
	// Resolve, Generation, and Now stand in for
	// playback.ResolveHWAccelWithFFmpegContext, playback.HWProbeGeneration,
	// and time.Now; tests replace them.
	Resolve    func(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) string
	Generation func() uint64
	Now        func() time.Time

	mu                 sync.Mutex
	accel, device      string
	resolved           bool
	resolvedAccel      string
	resolvedFrom       string
	resolvedDevice     string
	resolvedFFmpegAt   string
	resolvedGeneration uint64
	// retryAt, when set, is when a resolution to no hardware expires.
	retryAt time.Time
	// reported holds the accelerators whose failed attempt has been reported,
	// so a backend that always fails says why once.
	reported map[string]bool
}

// HardwareRetryInterval is how long a resolution to no hardware stands. It is
// longer than playback's own negative probe expiry, which it relies on to
// probe again, because every resolution walks the host's devices and logs
// its verdict, and background runs follow one another closely.
const HardwareRetryInterval = 5 * time.Minute

// NewHardwareResolver returns a resolver for the configured playback.hw_accel
// and playback.hw_device values.
func NewHardwareResolver(accel, device string) *HardwareResolver {
	return &HardwareResolver{
		Resolve:    playback.ResolveHWAccelWithFFmpegContext,
		Generation: playback.HWProbeGeneration,
		Now:        time.Now,
		accel:      accel,
		device:     device,
	}
}

// Set applies changed playback.hw_accel and playback.hw_device values to the
// next resolution.
func (h *HardwareResolver) Set(accel, device string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.accel, h.device = accel, device
}

// FirstFailure reports whether accel's failed attempt is its first since the
// resolver was made, so a caller can log the first one loudly.
func (h *HardwareResolver) FirstFailure(accel string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reported[accel] {
		return false
	}
	if h.reported == nil {
		h.reported = map[string]bool{}
	}
	h.reported[accel] = true
	return true
}

// Backend returns the resolved accelerator, for Runner.HWAccel, and the
// configured device value, for Runner.HWDevice, which the runner resolves to
// one device per attempt.
func (h *HardwareResolver) Backend(ctx context.Context, ffmpegPath string) (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	generation := h.Generation()
	now := h.Now()
	current := h.resolved && h.resolvedFrom == h.accel && h.resolvedDevice == h.device &&
		h.resolvedFFmpegAt == ffmpegPath && h.resolvedGeneration == generation &&
		(h.retryAt.IsZero() || now.Before(h.retryAt))
	if !current {
		accel := h.Resolve(ctx, h.accel, ffmpegPath, h.device)
		// A probe the caller's context cut short is not a verdict.
		if ctx.Err() != nil {
			return accel, h.device
		}
		h.resolvedAccel, h.resolvedFrom, h.resolvedDevice, h.resolvedFFmpegAt = accel, h.accel, h.device, ffmpegPath
		h.resolvedGeneration = generation
		h.retryAt = time.Time{}
		// Only "auto" resolves a configured accelerator to none.
		if accel == playback.HWAccelNone && h.accel != playback.HWAccelNone {
			h.retryAt = now.Add(HardwareRetryInterval)
		}
		h.resolved = true
	}
	return h.resolvedAccel, h.device
}
