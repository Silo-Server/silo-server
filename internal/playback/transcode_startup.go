package playback

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// TranscodeStartFunc launches one FFmpeg attempt. StartTranscode is the
// production implementation; callers substitute their own seams.
type TranscodeStartFunc func(context.Context, TranscodeOpts) (*TranscodeSession, error)

// TranscodeStartupRetry selects the single legacy retry a startup takes when
// its automatic pipeline is not enabled and FFmpeg exits before its first
// manifest. An enabled pipeline never takes it: its last stage is software.
type TranscodeStartupRetry uint8

const (
	// TranscodeStartupRetryNone fails on the first early exit.
	TranscodeStartupRetryNone TranscodeStartupRetry = iota
	// TranscodeStartupRetryOtherDevice retries one clean generation on the
	// acceleration StartupRetryHWAccel returns, preferring another configured
	// render device. It is the native local transport's policy.
	TranscodeStartupRetryOtherDevice
	// TranscodeStartupRetryAccelChange retries only when StartupRetryHWAccel
	// changes the acceleration (VideoToolbox to software). It is the transcode
	// node's policy.
	TranscodeStartupRetryAccelChange
)

// TranscodeStartup configures StartReadyTranscode and StartReconstructTranscode.
type TranscodeStartup struct {
	// Timeout bounds each attempt's wait for its first manifest. Zero uses
	// ManifestStartupTimeout.
	Timeout time.Duration
	// Start launches one attempt. Nil uses StartTranscode.
	Start TranscodeStartFunc
	// LegacyRetry applies only when the pipeline is not enabled.
	LegacyRetry TranscodeStartupRetry
}

// TranscodeStartupError reports a startup whose FFmpeg process never produced
// its first manifest. A StartTranscode error is returned unchanged instead, so
// errors.As distinguishes a readiness failure from a spawn failure.
type TranscodeStartupError struct {
	// Err is the final attempt's readiness error.
	Err error
	// WasRunning is true when the final attempt was still running at the
	// deadline (fresh start only; the process has been closed).
	WasRunning bool
	// FailedDevice is the concrete device the final attempt ran on.
	FailedDevice string
}

func (e *TranscodeStartupError) Error() string {
	return fmt.Sprintf("transcode did not become ready: %v", e.Err)
}

func (e *TranscodeStartupError) Unwrap() error { return e.Err }

// StartReadyTranscode starts FFmpeg for a fresh generation and returns only
// after its first playable manifest. When FFmpeg exits before that manifest,
// an enabled pipeline moves to its next safer path, and a disabled one takes
// startup.LegacyRetry. A process still running at the deadline is closed and
// the start fails; it is never duplicated by a second encoder. Every failed
// attempt is closed, output directory included.
func StartReadyTranscode(ctx context.Context, pipeline *AutoTranscodePipeline, startup TranscodeStartup) (*TranscodeSession, error) {
	return runTranscodeStartup(ctx, pipeline, startup, false)
}

// StartReconstructTranscode rebuilds a lost generation in its existing output
// directory. It differs from StartReadyTranscode in three ways:
//   - With no fallback available (a disabled pipeline whose legacy retry does
//     not apply) it starts once and returns without waiting, because segment
//     requests already wait for a reconstructed process.
//   - A process still running at the deadline is kept and returned.
//   - A failed attempt followed by another is stopped with CloseProcess,
//     keeping segments the client may still request; only the final failure
//     closes the session and its directory.
func StartReconstructTranscode(ctx context.Context, pipeline *AutoTranscodePipeline, startup TranscodeStartup) (*TranscodeSession, error) {
	if !pipeline.Enabled() {
		if _, retryable := legacyStartupRetry(startup.LegacyRetry, pipeline.Current(), ""); !retryable {
			return startup.start(ctx, pipeline.Current())
		}
	}
	return runTranscodeStartup(ctx, pipeline, startup, true)
}

func (startup TranscodeStartup) start(ctx context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
	if startup.Start == nil {
		return StartTranscode(ctx, opts)
	}
	return startup.Start(ctx, opts)
}

func (startup TranscodeStartup) timeout() time.Duration {
	if startup.Timeout <= 0 {
		return ManifestStartupTimeout
	}
	return startup.Timeout
}

// runTranscodeStartup starts attempts until one produces a manifest. See
// StartReconstructTranscode for the reconstruct rules.
func runTranscodeStartup(ctx context.Context, pipeline *AutoTranscodePipeline, startup TranscodeStartup, reconstruct bool) (*TranscodeSession, error) {
	attempt := pipeline.Current()
	legacyRetryUsed := false
	for {
		session, err := startup.start(ctx, attempt)
		if err != nil {
			// Validation, directory, and exec failures are not hardware
			// failures; another path cannot fix them.
			return nil, err
		}

		_, err = session.WaitForGenerationManifest(startup.timeout())
		if err == nil {
			pipeline.RememberSuccess()
			return session, nil
		}

		wasRunning := session.IsRunning()
		if wasRunning && reconstruct {
			slog.WarnContext(ctx, "reconstructed transcode slow to produce a manifest",
				"component", "playback",
				"playback_session_id", attempt.SessionID,
				"error", err,
			)
			return session, nil
		}

		failedDevice := session.Opts().HWDevice
		next := false
		if !wasRunning {
			if pipeline.AdvanceAfterFailure(failedDevice) {
				attempt = pipeline.Current()
				next = true
			} else if !pipeline.Enabled() && !legacyRetryUsed {
				attempt, next = legacyStartupRetry(startup.LegacyRetry, attempt, failedDevice)
				legacyRetryUsed = next
			}
		}
		if reconstruct && next {
			// The next attempt writes into this directory.
			_ = session.CloseProcess()
		} else {
			_ = session.Close()
		}
		if !next {
			return nil, &TranscodeStartupError{Err: err, WasRunning: wasRunning, FailedDevice: failedDevice}
		}

		slog.WarnContext(ctx, "transcode exited during startup; trying a safer path",
			"component", "playback",
			"playback_session_id", attempt.SessionID,
			"failed_device", failedDevice,
			"next_hw_accel", attempt.HWAccel,
			"next_software_decode", attempt.SoftwareVideoDecode,
			"error", err,
		)
	}
}

// legacyStartupRetry returns the single retry main takes outside the
// automatic pipeline, and whether policy allows one for failed.
func legacyStartupRetry(policy TranscodeStartupRetry, failed TranscodeOpts, failedDevice string) (TranscodeOpts, bool) {
	retry := failed
	switch policy {
	case TranscodeStartupRetryOtherDevice:
		retry.HWAccel = StartupRetryHWAccel(failed)
		retry.AvoidHWDevice = failedDevice
		return retry, true
	case TranscodeStartupRetryAccelChange:
		retryAccel := StartupRetryHWAccel(failed)
		if retryAccel == failed.HWAccel {
			return failed, false
		}
		retry.HWAccel = retryAccel
		return retry, true
	default:
		return failed, false
	}
}
