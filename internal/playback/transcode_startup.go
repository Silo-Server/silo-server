package playback

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type transcodeStarter func(context.Context, TranscodeOpts) (*TranscodeSession, error)

// StartReadyTranscode starts FFmpeg and waits for its first playable manifest.
// Automatic hardware selection retries progressively safer pipelines only when
// FFmpeg exits during startup; a merely slow running process is not duplicated.
func StartReadyTranscode(
	ctx context.Context,
	opts TranscodeOpts,
	timeout time.Duration,
) (*TranscodeSession, error) {
	return startReadyTranscode(ctx, opts, timeout, StartTranscode)
}

func startReadyTranscode(
	ctx context.Context,
	opts TranscodeOpts,
	timeout time.Duration,
	start transcodeStarter,
) (*TranscodeSession, error) {
	return startReadyTranscodePipeline(ctx, NewAutoTranscodePipeline(ctx, opts), timeout, start)
}

// StartReconstructTranscode rebuilds a lost generation on an enabled automatic
// pipeline. As on a fresh start, a process that exits before its first
// manifest is replaced by the next safer path. A reconstruct differs in two
// ways: a failed attempt keeps the output directory, which still holds segments
// the client may request, and a process that is slow but still running is kept,
// because segment requests already wait for a reconstructed process.
func StartReconstructTranscode(
	ctx context.Context,
	pipeline *AutoTranscodePipeline,
	timeout time.Duration,
) (*TranscodeSession, error) {
	return startReconstructTranscodePipeline(ctx, pipeline, timeout, StartTranscode)
}

func startReadyTranscodePipeline(
	ctx context.Context,
	pipeline *AutoTranscodePipeline,
	timeout time.Duration,
	start transcodeStarter,
) (*TranscodeSession, error) {
	return runTranscodeStartup(ctx, pipeline, timeout, start, false)
}

func startReconstructTranscodePipeline(
	ctx context.Context,
	pipeline *AutoTranscodePipeline,
	timeout time.Duration,
	start transcodeStarter,
) (*TranscodeSession, error) {
	return runTranscodeStartup(ctx, pipeline, timeout, start, true)
}

// runTranscodeStartup starts pipeline attempts until one produces a manifest.
// reconstruct applies the reconstruct rules described on
// StartReconstructTranscode.
func runTranscodeStartup(
	ctx context.Context,
	pipeline *AutoTranscodePipeline,
	timeout time.Duration,
	start transcodeStarter,
	reconstruct bool,
) (*TranscodeSession, error) {
	attempt := pipeline.Current()
	playbackSessionID := strings.TrimSpace(attempt.PlaybackSessionID)
	if playbackSessionID == "" {
		playbackSessionID = attempt.SessionID
	}
	legacyRetryUsed := false
	for {
		session, err := start(ctx, attempt)
		if err != nil {
			return nil, err
		}

		if _, err = session.WaitForManifest(timeout); err == nil {
			pipeline.RememberSuccess()
			return session, nil
		}

		wasRunning := session.IsRunning()
		if wasRunning && reconstruct {
			slog.WarnContext(ctx, "reconstructed transcode slow to produce a manifest",
				"component", "playback",
				"playback_session_id", playbackSessionID,
				"error", err,
			)
			return session, nil
		}

		failedDevice := session.Opts().HWDevice
		nextAvailable := false
		if !wasRunning {
			nextAvailable = pipeline.AdvanceAfterFailure(failedDevice)
			if nextAvailable {
				attempt = pipeline.Current()
			} else if !legacyRetryUsed {
				retryAccel := StartupRetryHWAccel(attempt)
				if retryAccel != attempt.HWAccel {
					legacyRetryUsed = true
					attempt.HWAccel = retryAccel
					attempt.AvoidHWDevice = failedDevice
					nextAvailable = true
				}
			}
		}
		if reconstruct && nextAvailable {
			// The next attempt writes into this directory.
			_ = session.CloseProcess()
		} else {
			_ = session.Close()
		}
		if !nextAvailable {
			return nil, fmt.Errorf("transcode did not become ready: %w", err)
		}

		slog.WarnContext(ctx, "transcode path failed during startup; trying safer path",
			"component", "playback",
			"playback_session_id", playbackSessionID,
			"failed_device", failedDevice,
			"next_hw_accel", attempt.HWAccel,
			"next_software_decode", attempt.SoftwareVideoDecode,
			"error", err,
		)
	}
}
