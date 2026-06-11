package notifications

import (
	"context"
	"log/slog"
	"time"
)

const availabilityDetectTimeout = 2 * time.Minute

// AvailabilityDetector turns completed ingest runs into episode_availability
// facts and release events. It runs after matching/reconcile is complete so
// a release is tied to an actual resolved episode, and it never blocks or
// fails the ingest itself.
type AvailabilityDetector struct {
	releases *ReleaseRepository
	settings *Settings
	logger   *slog.Logger
	// nudge wakes the fanout worker after new release events land; may be nil.
	nudge func()
}

// NewAvailabilityDetector creates an AvailabilityDetector.
func NewAvailabilityDetector(releases *ReleaseRepository, settings *Settings) *AvailabilityDetector {
	return &AvailabilityDetector{
		releases: releases,
		settings: settings,
		logger:   slog.Default().With("component", "notifications.availability"),
	}
}

// SetFanoutNudge wires the fanout worker wake signal.
func (d *AvailabilityDetector) SetFanoutNudge(nudge func()) {
	if d != nil {
		d.nudge = nudge
	}
}

// HandleIngestCompleted records newly available episodes for a completed
// ingest scope. fullLibrary distinguishes whole-library scans (set-based
// detection, and the scan that seeds a new library) from subtree/file scans
// (path-bounded detection).
//
// Seeding semantics: a library without a seed marker records availability
// silently — "newly available" means newly released to this server, not newly
// seen by the notifications feature. The marker is written when a full scan
// completes successfully, so the next scan onward emits release events.
func (d *AvailabilityDetector) HandleIngestCompleted(ctx context.Context, libraryID int, fullLibrary bool, scopePaths []string) {
	if d == nil || d.releases == nil {
		return
	}
	// The scan context is done once the scan finishes; detection runs on its
	// own deadline so cancellation of the parent does not drop availability.
	detectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), availabilityDetectTimeout)
	defer cancel()

	seeded, err := d.releases.IsLibrarySeeded(detectCtx, libraryID)
	if err != nil {
		d.logger.Warn("seed state lookup failed", "library_id", libraryID, "error", err)
		return
	}
	emitEvents := seeded && d.settings.ReleaseEventsEnabled(detectCtx)

	var inserted, events int
	if fullLibrary {
		inserted, events, err = d.releases.RecordAvailabilityForLibrary(detectCtx, libraryID, emitEvents)
	} else if seeded {
		inserted, events, err = d.releases.RecordAvailabilityForPaths(detectCtx, libraryID, scopePaths, emitEvents)
	} else {
		// Subtree/file ingest on an unseeded library: record silently but do
		// not seed-mark — only a successful full scan proves the back catalog
		// has been captured.
		inserted, events, err = d.releases.RecordAvailabilityForPaths(detectCtx, libraryID, scopePaths, false)
	}
	if err != nil {
		d.logger.Warn("availability detection failed", "library_id", libraryID, "error", err)
		return
	}

	if fullLibrary && !seeded {
		if err := d.releases.MarkLibrarySeeded(detectCtx, libraryID); err != nil {
			d.logger.Warn("seed marker write failed", "library_id", libraryID, "error", err)
		} else {
			d.logger.Info("library availability seeded",
				"library_id", libraryID, "availability_rows", inserted)
		}
	}

	if inserted > 0 || events > 0 {
		d.logger.Info("availability recorded",
			"library_id", libraryID,
			"full_library", fullLibrary,
			"availability_rows", inserted,
			"release_events", events,
		)
	}
	if events > 0 && d.nudge != nil {
		d.nudge()
	}
}
