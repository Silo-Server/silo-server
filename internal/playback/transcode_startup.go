package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// TranscodeStartFunc launches one FFmpeg attempt. StartTranscode is the
// production implementation; callers substitute their own seams.
type TranscodeStartFunc func(context.Context, TranscodeOpts) (*TranscodeSession, error)

// TranscodeStartupRetry selects the single legacy retry a startup takes when
// its automatic pipeline is not enabled and FFmpeg exits before its first
// manifest. An enabled pipeline never takes it: its last stage is software.
// When the hardware decoder rejected the source, every policy except
// TranscodeStartupRetryNone walks NewSoftwareDecodeRetryPipeline instead.
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
	// A fresh start stops waiting when its request ends: the caller holds the
	// session lifecycle lock for the whole wait and discards the transport once
	// its request is gone, so waiting on only delays the client's retry. A
	// reconstruct serves every segment request waiting on it, so each attempt's
	// launch and wait outlive the one that triggered it. StartTranscode returns
	// a canceled context's error for some recipes (an HEVC target, a capped
	// VAAPI encode, burned-in text subtitles), which would fail the rebuild
	// for all of them.
	attemptCtx := ctx
	if reconstruct {
		attemptCtx = context.WithoutCancel(ctx)
	}
	attempt := pipeline.Current()
	legacyRetryUsed := false
	for {
		session, err := startup.start(attemptCtx, attempt)
		if err != nil {
			// Validation, directory, and exec failures are not hardware
			// failures; another path cannot fix them.
			return nil, err
		}

		_, err = session.WaitForGenerationManifestContext(attemptCtx, startup.timeout())
		if err == nil {
			pipeline.RememberSuccess()
			return session, nil
		}
		if attemptCtx.Err() != nil {
			// A fresh start's requester left (a reconstruct's context never
			// ends); this says nothing about the device, so the pipeline
			// neither advances nor remembers a result.
			wasRunning := session.IsRunning()
			failedDevice := session.Opts().HWDevice
			_ = session.Close()
			return nil, &TranscodeStartupError{Err: err, WasRunning: wasRunning, FailedDevice: failedDevice}
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
			} else if decodeRetry := legacyDecodeRetryPipeline(startup.LegacyRetry, pipeline, legacyRetryUsed, attempt, err); decodeRetry.Enabled() {
				// The decoder rejected the source, so repeating the GPU
				// decode fails again. Walk CPU decode instead, as auto would.
				pipeline = decodeRetry
				attempt = pipeline.Current()
				legacyRetryUsed = true
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

// hardwareDecodeUnsupportedMarkers are FFmpeg startup errors that mean the
// GPU's decoder cannot take this source at all, so running the same command
// again fails the same way. NVDEC prints the first one when the card has no
// decoder for the codec, profile or bit depth (HEVC on a Maxwell GM204, for
// example) even though NVENC encodes fine.
var hardwareDecodeUnsupportedMarkers = []string{
	"Hardware is lacking required capabilities",
}

// ErrHardwareDecodeUnsupported marks an FFmpeg failure whose diagnostics say
// the hardware decoder cannot decode the source. errors.Is finds it on the
// startup errors TranscodeSession and PrepareFile return.
var ErrHardwareDecodeUnsupported = errors.New("hardware decoder cannot decode the source")

// IsHardwareDecodeUnsupported reports whether a startup error says the
// hardware decoder cannot decode this source.
func IsHardwareDecodeUnsupported(err error) bool {
	return errors.Is(err, ErrHardwareDecodeUnsupported)
}

// classifyFFmpegFailure marks err with ErrHardwareDecodeUnsupported when
// stderr carries a decoder rejection, leaving its message unchanged. Callers
// pass FFmpeg's whole retained stderr, not the excerpt an error message keeps,
// so earlier output cannot push the rejection out of view.
func classifyFFmpegFailure(err error, stderr string) error {
	if err == nil {
		return nil
	}
	for _, marker := range hardwareDecodeUnsupportedMarkers {
		if strings.Contains(stderr, marker) {
			return &hardwareDecodeUnsupportedError{err: err}
		}
	}
	return err
}

type hardwareDecodeUnsupportedError struct{ err error }

func (e *hardwareDecodeUnsupportedError) Error() string { return e.err.Error() }

func (e *hardwareDecodeUnsupportedError) Unwrap() error { return e.err }

func (e *hardwareDecodeUnsupportedError) Is(target error) bool {
	return target == ErrHardwareDecodeUnsupported
}

// NewSoftwareDecodeRetryPipeline returns the fallback for a startup whose
// hardware decoder rejected the source (see IsHardwareDecodeUnsupported): CPU
// decode with the configured hardware encoder, then CPU decode and encode.
// It is the same order hw_accel=auto walks after its first stage, so an
// explicit acceleration keeps its GPU encoder when the upload works and still
// reaches software when it does not. The pipeline keeps its own cache, so an
// explicit setting never changes what hw_accel=auto prefers.
//
// The returned pipeline is disabled (Enabled reports false) when the cause is
// any other failure, the attempt already decoded on the CPU, it carries a
// tone-map recipe (whose filter graph names its executor), it copies video,
// or it did not use a hardware backend.
func NewSoftwareDecodeRetryPipeline(failed TranscodeOpts, cause error) *AutoTranscodePipeline {
	if !IsHardwareDecodeUnsupported(cause) ||
		failed.SoftwareVideoDecode ||
		failed.ToneMapMode != "" ||
		strings.EqualFold(failed.TargetCodecVideo, "copy") ||
		!isHardwareTranscodeBackend(failed.HWAccel) {
		return &AutoTranscodePipeline{base: failed}
	}
	retry := failed
	retry.SoftwareVideoDecode = true
	return newResolvedAutoTranscodePipeline(retry, newAutoTranscodePipelineCache())
}

// legacyDecodeRetryPipeline returns the CPU-decode fallback a startup outside
// the automatic pipeline takes in place of its legacy retry, or a disabled
// pipeline when policy allows no retry, the retry was taken, or cause is not a
// hardware decoder rejection.
func legacyDecodeRetryPipeline(policy TranscodeStartupRetry, pipeline *AutoTranscodePipeline, legacyRetryUsed bool, failed TranscodeOpts, cause error) *AutoTranscodePipeline {
	if policy == TranscodeStartupRetryNone || pipeline.Enabled() || legacyRetryUsed {
		return &AutoTranscodePipeline{base: failed}
	}
	return NewSoftwareDecodeRetryPipeline(failed, cause)
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
