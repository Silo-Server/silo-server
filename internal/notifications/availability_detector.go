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

// AvailabilityKinds selects which content kinds an ingest scope covers.
// Each kind keeps its own seed marker and silent-seeding semantics.
type AvailabilityKinds struct {
	Episodes bool
	Movies   bool
}

// availabilityKindOps abstracts the per-kind recording calls so episode and
// movie passes share one detection flow; seed state is kind-keyed in the
// repository itself.
type availabilityKindOps struct {
	kind             string
	recordForLibrary func(ctx context.Context, libraryID int, emitEvents bool) (int, int, error)
	recordForPaths   func(ctx context.Context, libraryID int, scopePaths []string, emitEvents bool) (int, int, error)
}

// HandleIngestCompleted records newly available content for a completed
// ingest scope. fullLibrary distinguishes whole-library scans (set-based
// detection, and the scan that seeds a new library) from subtree/file scans
// (path-bounded detection).
//
// Seeding semantics: a library without a seed marker records availability
// silently — "newly available" means newly released to this server, not newly
// seen by the notifications feature. The marker is written when a full scan
// completes successfully, so the next scan onward emits release events.
func (d *AvailabilityDetector) HandleIngestCompleted(ctx context.Context, libraryID int, fullLibrary bool, scopePaths []string, kinds AvailabilityKinds) {
	if d == nil || d.releases == nil {
		return
	}
	// The scan context is done once the scan finishes; detection runs on its
	// own deadline so cancellation of the parent does not drop availability.
	detectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), availabilityDetectTimeout)
	defer cancel()

	if kinds.Episodes {
		d.runKind(detectCtx, libraryID, fullLibrary, scopePaths, availabilityKindOps{
			kind:             EventKindEpisode,
			recordForLibrary: d.releases.RecordAvailabilityForLibrary,
			recordForPaths:   d.releases.RecordAvailabilityForPaths,
		})
	}
	if kinds.Movies {
		d.runKind(detectCtx, libraryID, fullLibrary, scopePaths, availabilityKindOps{
			kind:             EventKindMovie,
			recordForLibrary: d.releases.RecordMovieAvailabilityForLibrary,
			recordForPaths:   d.releases.RecordMovieAvailabilityForPaths,
		})
	}
}

func (d *AvailabilityDetector) runKind(ctx context.Context, libraryID int, fullLibrary bool, scopePaths []string, ops availabilityKindOps) {
	seeded, err := d.releases.IsContentSeeded(ctx, libraryID, ops.kind)
	if err != nil {
		d.logger.Warn("seed state lookup failed",
			"library_id", libraryID, "kind", ops.kind, "error", err)
		return
	}
	emitEvents := seeded && d.settings.ReleaseEventsEnabled(ctx)

	var inserted, events int
	if fullLibrary {
		inserted, events, err = ops.recordForLibrary(ctx, libraryID, emitEvents)
	} else if seeded {
		inserted, events, err = ops.recordForPaths(ctx, libraryID, scopePaths, emitEvents)
	} else {
		// Subtree/file ingest on an unseeded library: record silently but do
		// not seed-mark — only a successful full scan proves the back catalog
		// has been captured.
		inserted, events, err = ops.recordForPaths(ctx, libraryID, scopePaths, false)
	}
	if err != nil {
		d.logger.Warn("availability detection failed",
			"library_id", libraryID, "kind", ops.kind, "error", err)
		return
	}

	if fullLibrary && !seeded {
		if err := d.releases.MarkContentSeeded(ctx, libraryID, ops.kind); err != nil {
			d.logger.Warn("seed marker write failed",
				"library_id", libraryID, "kind", ops.kind, "error", err)
		} else {
			d.logger.Info("library availability seeded",
				"library_id", libraryID, "kind", ops.kind, "availability_rows", inserted)
		}
	}

	if inserted > 0 || events > 0 {
		d.logger.Info("availability recorded",
			"library_id", libraryID,
			"kind", ops.kind,
			"full_library", fullLibrary,
			"availability_rows", inserted,
			"release_events", events,
		)
	}
	if events > 0 && d.nudge != nil {
		d.nudge()
	}
}
