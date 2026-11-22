package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

const (
	postRestartSegmentWaitTimeout = 30 * time.Second
	initSegmentWaitTimeout        = 10 * time.Second
	segmentRecoveryNodeComponent  = "transcodenode"
)

// SegmentRecoveryOptions configures OpenSegmentWithRecovery for one HLS
// serving surface.
type SegmentRecoveryOptions struct {
	// SessionID identifies the upstream playback/transcode session in logs.
	SessionID string
	// Component sets the slog "component" attribute (e.g. "api", "jellycompat",
	// or "transcodenode").
	Component string
	// PlaySession, when non-empty, sets the slog "play_session" attribute.
	PlaySession string
	// SegmentNumberName overrides segmentName when parsing the requested
	// segment number (e.g. Jellyfin passes segmentId separately from the
	// container extension, which can contain additional dots).
	SegmentNumberName string
	// Restart resolves and restarts one missing numbered segment under the
	// caller's per-session lifecycle lock.
	Restart func(ctx context.Context, segNum int, seenGeneration time.Time) (SegmentRecoveryTarget, bool, error)
	// CopyLookaheadTimeout, when positive, briefly waits for segNum+1 after a
	// copy-video seek restart so the first resumed playback window is less
	// brittle.
	CopyLookaheadTimeout time.Duration
}

// OpenSegmentWithRecovery opens an HLS segment from session, waiting for an
// active transcode or triggering a locked seek-restart when the segment is not
// yet on disk. Non-numbered segments (such as init.mp4) wait briefly while the
// session is running without triggering a restart.
func OpenSegmentWithRecovery(
	ctx context.Context,
	session *TranscodeSession,
	segmentName string,
	opts SegmentRecoveryOptions,
) (*SegmentLease, error) {
	lease, err := session.OpenSegment(segmentName)
	if !errors.Is(err, ErrSegmentNotFound) {
		return lease, err
	}

	parseName := opts.SegmentNumberName
	if parseName == "" {
		parseName = segmentName
	}
	segNum, parseErr := ParseSegmentNumber(parseName)
	if parseErr != nil {
		if session.IsRunning() {
			// Non-numbered segment (e.g., init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			return session.WaitForOpenSegmentContext(ctx, segmentName, initSegmentWaitTimeout)
		}
		return nil, err
	}

	now := time.Now()
	decision := session.SegmentRecoveryDecision(segNum, now)
	lastProducedAgeMS := int64(-1)
	if !decision.Progress.LastProducedAt.IsZero() {
		lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
	}

	baseLogger := segmentRecoveryBaseLogger(opts, segmentName)
	decisionLogger := baseLogger.With(
		"requested_segment", segNum,
		"produced_head", decision.Progress.ProducedHead,
		"last_requested_segment", decision.Progress.LastRequestedSegment,
		"start_segment_number", decision.Progress.StartSegmentNumber,
		"last_produced_age_ms", lastProducedAgeMS,
		"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
		"restart_on_timeout", decision.RestartOnTimeout,
		"reason", decision.Reason,
	)
	decisionLogger.InfoContext(ctx, "transcode segment missing")

	if decision.Wait {
		decisionLogger.InfoContext(ctx, "transcode segment wait")
		lease, err = session.WaitForOpenSegmentContext(ctx, segmentName, decision.WaitTimeout)
		if !errors.Is(err, ErrSegmentNotFound) {
			return lease, err
		}
		decisionLogger.InfoContext(ctx, "transcode segment wait timeout")
	}

	// If the segment is still missing (timed out, or outside the active encode
	// range), either restart at the exact manifest-derived timeline position or
	// return ErrSegmentNotFound for copy-mode segments outside the current
	// manifest window.
	if !decision.RestartOnTimeout || opts.Restart == nil {
		return nil, err
	}

	target, ok, restartErr := opts.Restart(ctx, segNum, decision.Progress.GenerationStartedAt)
	// A restart skipped because the request ended is not a failure.
	if restartErr != nil && !errors.Is(restartErr, ErrManifestNotReady) && !errors.Is(restartErr, ctx.Err()) {
		logSegmentRestartError(ctx, baseLogger, opts.Component, restartErr)
	}
	if restartErr != nil {
		return nil, restartErr
	}
	if !ok {
		// Copy-mode with an unresolved seek target (ok=false, no error) means
		// the manifest can't place this segment yet. Don't restart at a
		// fabricated position; surface ErrSegmentNotFound so the client retries
		// while the session keeps producing manifest.
		if session.IsCopyVideo() {
			return nil, ErrSegmentNotFound
		}
		return nil, err
	}

	logSegmentSeekRestart(ctx, decisionLogger, opts.Component, target)

	// Throttler + exit monitor re-arm via the session's restart hook.
	lease, err = session.WaitForOpenSegmentContext(ctx, segmentName, postRestartSegmentWaitTimeout)
	if err == nil && opts.CopyLookaheadTimeout > 0 && session.IsCopyVideo() {
		// Copy-mode seeks can resume as soon as the target segment exists, but
		// that sometimes leaves the player one segment away from stalling while
		// FFmpeg catches up. Briefly wait for a single lookahead fragment when
		// available so the first resumed playback window is less brittle.
		suffix := filepath.Ext(segmentName)
		if opts.SegmentNumberName != "" && strings.HasPrefix(segmentName, opts.SegmentNumberName) {
			suffix = strings.TrimPrefix(segmentName, opts.SegmentNumberName)
		}
		nextSegmentName := fmt.Sprintf("seg_%05d%s", segNum+1, suffix)
		if nextSegment, nextErr := session.WaitForOpenSegmentContext(ctx, nextSegmentName, opts.CopyLookaheadTimeout); nextErr == nil {
			_ = nextSegment.Close()
		}
	}
	return lease, err
}

func segmentRecoveryBaseLogger(opts SegmentRecoveryOptions, segmentName string) *slog.Logger {
	logger := slog.Default().With(
		"component", opts.Component,
		"segment", segmentName,
	)
	if opts.PlaySession != "" {
		logger = logger.With("play_session", opts.PlaySession)
	}
	return logger.With(
		"session", opts.SessionID,
		"playback_session_id", opts.SessionID,
	)
}

func logSegmentRestartError(ctx context.Context, logger *slog.Logger, component string, restartErr error) {
	if component == segmentRecoveryNodeComponent {
		logger.ErrorContext(ctx, "restart transcode node at missing segment", "error", restartErr)
		return
	}
	logger.ErrorContext(ctx, "restart transcode at missing segment", "error", restartErr)
}

func logSegmentSeekRestart(ctx context.Context, logger *slog.Logger, component string, target SegmentRecoveryTarget) {
	if component == segmentRecoveryNodeComponent {
		logger.InfoContext(ctx, "transcode node seek restart",
			"seek_seconds", target.SeekSeconds,
			"stream_origin_seconds", target.StreamOriginSeconds,
			"resolved_start_segment", target.StartSegmentNumber,
		)
		return
	}
	logger.InfoContext(ctx, "transcode seek restart",
		"seek_seconds", target.SeekSeconds,
		"stream_origin_seconds", target.StreamOriginSeconds,
		"resolved_start_segment", target.StartSegmentNumber,
	)
}
