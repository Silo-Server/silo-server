package database

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
)

// migrationHeartbeatInterval is how often a still-running migration logs that
// it is alive. Long enough not to flood the log, short enough that an admin
// watching `docker logs` sees the server is working, not hung.
var migrationHeartbeatInterval = 20 * time.Second

// migrationStepper is the part of *goose.Provider the logged runner uses.
type migrationStepper interface {
	Status(ctx context.Context) ([]*goose.MigrationStatus, error)
	HasPending(ctx context.Context) (bool, error)
	ApplyVersion(ctx context.Context, version int64, direction bool) (*goose.MigrationResult, error)
}

// applyMigrationsLogged applies pending migrations one at a time so each can be
// logged with its version, name and duration, and so a heartbeat can name the
// migration that is running. Applying each listed version under Goose's lock
// keeps the logged identity accurate when another node applies a migration
// between the status read and the next step.
//
// Long data migrations written in Go should also log their own batch progress
// (rows done, and the total when known), as the subtitle language backfill
// does: the heartbeat can only report elapsed time.
func applyMigrationsLogged(ctx context.Context, runner migrationStepper, logger *slog.Logger, heartbeat time.Duration) error {
	logger.InfoContext(ctx, "checking database migration status")
	stop := startMigrationHeartbeat(heartbeat, func(elapsed string) {
		logger.InfoContext(ctx, "database migration status check still running", "elapsed", elapsed)
	})
	statuses, err := runner.Status(ctx)
	if err != nil {
		stop()
		return fmt.Errorf("reading goose migration status: %w", err)
	}
	// ApplyVersion checks a particular version, so retain Goose's check for
	// missing or out-of-order sources before applying the status snapshot.
	_, err = runner.HasPending(ctx)
	stop()
	if err != nil {
		return fmt.Errorf("checking goose pending migrations: %w", err)
	}
	var pending []*goose.Source
	for _, status := range statuses {
		if status != nil && status.State == goose.StatePending && status.Source != nil {
			pending = append(pending, status.Source)
		}
	}
	if len(pending) == 0 {
		logger.InfoContext(ctx, "database schema is up to date")
		return nil
	}

	logger.InfoContext(ctx, "applying database migrations",
		"pending", len(pending),
		"from_version", pending[0].Version,
		"to_version", pending[len(pending)-1].Version)
	started := time.Now()
	applied := 0
	for i, next := range pending {
		progress := fmt.Sprintf("%d/%d", i+1, len(pending))
		logger.InfoContext(ctx, "applying database migration",
			"version", next.Version,
			"name", migrationName(next),
			"progress", progress)
		stop = startMigrationHeartbeat(heartbeat, func(elapsed string) {
			logger.InfoContext(ctx, "database migration still running",
				"version", next.Version,
				"name", migrationName(next),
				"progress", progress,
				"elapsed", elapsed)
		})
		result, err := runner.ApplyVersion(ctx, next.Version, true)
		stop()
		if errors.Is(err, goose.ErrAlreadyApplied) {
			logger.InfoContext(ctx, "database migration already applied",
				"version", next.Version,
				"name", migrationName(next),
				"progress", progress)
			continue
		}
		if err != nil {
			logger.ErrorContext(ctx, "database migration failed",
				"version", next.Version,
				"name", migrationName(next),
				"progress", progress,
				"error", err)
			return fmt.Errorf("running goose migrations: %w", err)
		}
		applied++
		finished := next
		var duration time.Duration
		if result != nil {
			duration = result.Duration
			if result.Source != nil {
				finished = result.Source
			}
		}
		logger.InfoContext(ctx, "database migration applied",
			"version", finished.Version,
			"name", migrationName(finished),
			"progress", progress,
			"duration", roundMigrationDuration(duration))
	}
	logger.InfoContext(ctx, "database migrations finished",
		"applied", applied,
		"duration", roundMigrationDuration(time.Since(started)))
	return nil
}

func logMigrationRollbackResults(ctx context.Context, logger *slog.Logger, toVersion int64, results []*goose.MigrationResult, err error) int {
	partial, isPartial := errors.AsType[*goose.PartialError](err)
	if isPartial {
		results = partial.Applied
	}
	rolledBack := 0
	for _, result := range results {
		if result == nil || result.Source == nil || result.Error != nil {
			continue
		}
		rolledBack++
		logger.InfoContext(ctx, "database migration rolled back",
			"version", result.Source.Version,
			"name", migrationName(result.Source),
			"duration", roundMigrationDuration(result.Duration))
	}
	if err != nil {
		if isPartial && partial.Failed != nil && partial.Failed.Source != nil {
			logger.ErrorContext(ctx, "database migration rollback failed",
				"version", partial.Failed.Source.Version,
				"name", migrationName(partial.Failed.Source),
				"duration", roundMigrationDuration(partial.Failed.Duration),
				"error", err)
		} else {
			logger.ErrorContext(ctx, "database migration rollback failed",
				"to_version", toVersion, "error", err)
		}
	}
	return rolledBack
}

// startMigrationHeartbeat calls beat with the elapsed time every interval until
// the returned stop function is called. A non-positive interval disables it.
// The caller logs inside beat, so each heartbeat keeps a constant message.
func startMigrationHeartbeat(interval time.Duration, beat func(elapsed string)) func() {
	if interval <= 0 {
		return func() {}
	}
	started := time.Now()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				beat(roundMigrationDuration(time.Since(started)))
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
}

// migrationRollbackCandidates lists the applied migrations newer than
// toVersion, newest first. They are candidates, not a plan: DownTo walks
// application history newest first and stops at the first version at or below
// the target, so with migrations applied out of order (which this provider
// allows) it can undo fewer than these. The results DownTo returns are what
// actually rolled back.
func migrationRollbackCandidates(statuses []*goose.MigrationStatus, toVersion int64) []*goose.Source {
	var plan []*goose.Source
	for _, status := range statuses {
		if status != nil && status.State == goose.StateApplied && status.Source != nil && status.Source.Version > toVersion {
			plan = append(plan, status.Source)
		}
	}
	slices.SortFunc(plan, func(a, b *goose.Source) int { return cmp.Compare(b.Version, a.Version) })
	return plan
}

func migrationNames(sources []*goose.Source) []string {
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, migrationName(source))
	}
	return names
}

func migrationName(source *goose.Source) string {
	if source == nil {
		return ""
	}
	if source.Path == "" {
		// A Go migration registered without a name: fall back to its version.
		return fmt.Sprintf("%d (go)", source.Version)
	}
	return path.Base(source.Path)
}

func roundMigrationDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
