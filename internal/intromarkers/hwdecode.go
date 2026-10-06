package intromarkers

import (
	"context"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

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
	accel, device := e.hardware.Backend(ctx, e.config.FFmpegPath)
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
	if e.hardware.FirstFailure(accel) {
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
