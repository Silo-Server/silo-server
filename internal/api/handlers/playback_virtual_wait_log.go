package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

// virtualResolveWaitLog observes one blocking wait on the virtual candidate
// resolve/fetch path. A request that spends seconds inside a provider listing,
// an AltMount import hold, a retry sleep, or an ffprobe previously produced no
// line between the stages of the resolve, so a slow start could not be
// attributed to fetch or probe. enter logs once when the wait begins; finish
// leaves a debug line for ordinary waits and a warn once the wait crosses
// warnAfter. It is observational only: it never cancels, shortens, or retries
// the wait.
//
// warnAfter must be an existing budget or timeout from the same path (for
// example virtualStartProbeBudget for a start-path probe or
// virtualPendingHoldCap for the AltMount hold). No new tuning knob is added.
type virtualResolveWaitLog struct {
	ctx        context.Context //nolint:containedctx // One wait cycle carries the caller's request context to tag its log lines.
	kind       string
	on         string
	contentID  string
	fileID     int
	virtualURI string
	bound      time.Duration
	warnAfter  time.Duration
	started    time.Time
	extra      []any
}

// startVirtualResolveWaitLog starts one wait-cycle observation. kind names the
// work being awaited (fetch, probe, sleep); on names the exact step so a log
// line distinguishes a provider listing from an import hold or an ffprobe.
// virtualURI is sanitized before it is logged.
func startVirtualResolveWaitLog(ctx context.Context, kind, on, contentID string, fileID int, virtualURI string, bound, warnAfter time.Duration, extra ...any) *virtualResolveWaitLog {
	if ctx == nil {
		ctx = context.Background()
	}
	wait := &virtualResolveWaitLog{
		ctx:        ctx,
		kind:       kind,
		on:         on,
		contentID:  contentID,
		fileID:     fileID,
		virtualURI: virtualURI,
		bound:      bound,
		warnAfter:  warnAfter,
		started:    time.Now(),
		extra:      extra,
	}
	attrs := wait.attrs("bound_ms", max(0, bound.Milliseconds()))
	if bound <= 0 {
		attrs = append(attrs, "bound_exhausted", true)
	}
	slog.InfoContext(ctx, "virtual resolve wait started", attrs...)
	return wait
}

// finish records the wait outcome exactly once. It logs at warn when the wait
// reached warnAfter and at debug otherwise, so a normal fast path stays quiet.
func (w *virtualResolveWaitLog) finish(err error) {
	if w == nil {
		return
	}
	elapsed := time.Since(w.started)
	attrs := w.attrs("waited_ms", elapsed.Milliseconds())
	if err != nil {
		attrs = append(attrs, virtualEvidenceLogKeyError, logredact.SanitizeURLError(err))
	}
	if w.warnAfter > 0 && elapsed >= w.warnAfter {
		slog.WarnContext(w.ctx, "virtual resolve wait exceeded threshold", attrs...)
		return
	}
	slog.DebugContext(w.ctx, "virtual resolve wait finished", attrs...)
}

func (w *virtualResolveWaitLog) attrs(extra ...any) []any {
	attrs := []any{
		virtualEvidenceLogKeyComponent, virtualEvidenceLogValueAPI,
		"wait_kind", w.kind,
		"wait_on", w.on,
	}
	if w.contentID != "" {
		attrs = append(attrs, contentIDKey, w.contentID)
	}
	if w.fileID > 0 {
		attrs = append(attrs, virtualEvidenceLogKeyFileID, w.fileID)
	}
	if w.virtualURI != "" {
		attrs = append(attrs, "virtual_uri", logredact.SanitizeURL(w.virtualURI))
	}
	attrs = append(attrs, w.extra...)
	attrs = append(attrs, extra...)
	return attrs
}
