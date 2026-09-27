package intromarkers

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// hardwareDecoder holds the hardware credits tail passes decode keyframes
// on: the playback.hw_accel and playback.hw_device settings, with the
// backend they resolve to, as chapter thumbnails resolve it. The backend is
// kept per configured pair, since resolving "auto" probes the host, until the
// playback probe cache is invalidated. A resolution to no hardware, which
// "auto" gives when a smoke probe fails, is asked again after
// hardwareRetryInterval, so a GPU that recovers takes the passes back.
//
// The decoder shapes no artifact: hardware and software keyframe statistics
// agree closely enough to classify alike, so a tail pass keeps its config
// hash whichever decoded it.
type hardwareDecoder struct {
	// resolve, generation, and now stand in for
	// playback.ResolveHWAccelWithFFmpegContext, playback.HWProbeGeneration,
	// and time.Now; tests replace them.
	resolve    func(ctx context.Context, hwAccel, ffmpegPath, hwDevice string) string
	generation func() uint64
	now        func() time.Time

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
	// reported holds the accelerators whose failed attempt has been logged
	// at warn level, so a backend that always fails says why once.
	reported map[string]bool
}

// hardwareRetryInterval is how long a resolution to no hardware stands. It is
// longer than playback's own negative probe expiry, which it relies on to
// probe again, because every resolution walks the host's devices and logs
// its verdict, and tail passes run back to back.
const hardwareRetryInterval = 5 * time.Minute

func newHardwareDecoder(accel, device string) *hardwareDecoder {
	return &hardwareDecoder{
		resolve:    playback.ResolveHWAccelWithFFmpegContext,
		generation: playback.HWProbeGeneration,
		now:        time.Now,
		accel:      accel,
		device:     device,
	}
}

// set applies changed playback.hw_accel and playback.hw_device values to the
// next tail pass.
func (h *hardwareDecoder) set(accel, device string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.accel, h.device = accel, device
}

// reportFailure reports whether accel's failed attempt is its first since
// the process started.
func (h *hardwareDecoder) reportFailure(accel string) bool {
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

// backend returns the resolved accelerator and the configured device value,
// which the runner resolves to one device per attempt.
func (h *hardwareDecoder) backend(ctx context.Context, ffmpegPath string) (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	generation := h.generation()
	now := h.now()
	current := h.resolved && h.resolvedFrom == h.accel && h.resolvedDevice == h.device &&
		h.resolvedFFmpegAt == ffmpegPath && h.resolvedGeneration == generation &&
		(h.retryAt.IsZero() || now.Before(h.retryAt))
	if !current {
		accel := h.resolve(ctx, h.accel, ffmpegPath, h.device)
		// A probe the caller's context cut short is not a verdict.
		if ctx.Err() != nil {
			return accel, h.device
		}
		h.resolvedAccel, h.resolvedFrom, h.resolvedDevice, h.resolvedFFmpegAt = accel, h.accel, h.device, ffmpegPath
		h.resolvedGeneration = generation
		h.retryAt = time.Time{}
		// Only "auto" resolves a configured accelerator to none.
		if accel == playback.HWAccelNone && h.accel != playback.HWAccelNone {
			h.retryAt = now.Add(hardwareRetryInterval)
		}
		h.resolved = true
	}
	return h.resolvedAccel, h.device
}

// hardwareAttempts is the attempt plan of a tail pass on accel: hardware
// first and software after a hardware failure when accel decodes in
// hardware, and one software attempt otherwise. Like every analysis run,
// neither has a timeout of its own.
func hardwareAttempts(accel string) []mediasample.Attempt {
	if !mediasample.SupportsHardwareDecode(accel) {
		return nil
	}
	return []mediasample.Attempt{{Hardware: true}, {}}
}

// tailRunner returns the runner of a tail pass and sets req's attempts:
// a request with a video output decodes it on the configured hardware when
// the host has any the runner supports (VAAPI, QSV, or VideoToolbox). A
// hardware attempt that fails because an output finds no stream ends the
// run without falling back or counting as a hardware failure.
func (e *ChromaprintExtractor) tailRunner(ctx context.Context, req *mediasample.Request) mediasample.Runner {
	runner := analysisRunner(e.config)
	if req.Stats == nil || e.hardware == nil {
		return runner
	}
	accel, device := e.hardware.backend(ctx, e.config.FFmpegPath)
	if req.Attempts = hardwareAttempts(accel); req.Attempts != nil {
		runner.HWAccel, runner.HWDevice = accel, device
		runner.Fallback = func(attempt mediasample.Attempt, failure mediasample.AttemptError) bool {
			if !attempt.Hardware {
				return true
			}
			// An output that finds no stream is the input's fault, not the
			// GPU's: software would fail the same way, and the caller's
			// retry without audio runs on hardware again.
			if failure.Cause() == mediasample.ReasonNoStream {
				return false
			}
			e.logHardwareFailure(ctx, accel, failure)
			return true
		}
	}
	return runner
}

// logHardwareFailure logs why a hardware tail attempt failed before the
// software attempt runs: at warn level the first time per accelerator, since
// a backend that keeps failing doubles the cost of every pass, and at debug
// level after that.
func (e *ChromaprintExtractor) logHardwareFailure(ctx context.Context, accel string, failure mediasample.AttemptError) {
	level := slog.LevelDebug
	if e.hardware.reportFailure(accel) {
		level = slog.LevelWarn
	}
	e.logger.Log(ctx, level, "credits tail hardware decode failed; using software",
		"decoder", failure.Decoder,
		"reason", failure.Reason,
		"error", failure.Err,
		"stderr_tail", failure.StderrTail)
}

// logTailDecoder logs which decoder produced a tail pass. A pass planned on
// hardware that software produced had its hardware attempt fail.
func (e *ChromaprintExtractor) logTailDecoder(ctx context.Context, candidate Candidate, req mediasample.Request, result mediasample.Result) {
	e.logger.DebugContext(ctx, "credits tail sampled",
		"file_id", candidate.FileID,
		"decoder", result.Decoder,
		"hardware_fallback", len(req.Attempts) > 1 && result.Decoder == "software",
		"keyframes", len(result.Frames))
}
