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

func startReadyTranscodePipeline(
	ctx context.Context,
	pipeline *AutoTranscodePipeline,
	timeout time.Duration,
	start transcodeStarter,
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
		failedDevice := session.Opts().HWDevice
		_ = session.Close()
		if wasRunning {
			return nil, fmt.Errorf("transcode did not become ready: %w", err)
		}

		nextAvailable := pipeline.AdvanceAfterFailure(failedDevice)
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
