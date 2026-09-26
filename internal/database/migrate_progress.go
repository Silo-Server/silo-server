package database

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
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
	UpByOne(ctx context.Context) (*goose.MigrationResult, error)
}

// applyMigrationsLogged applies pending migrations one at a time so each can be
// logged with its version, name and duration, and so a heartbeat can name the
// migration that is running. Goose applies pending versions in ascending
// order, including out-of-order ones, so the pending list read up front names
// the migration each step runs.
//
// Long data migrations written in Go should also log their own batch progress
// (rows done, and the total when known), as the subtitle language backfill
// does: the heartbeat can only report elapsed time.
func applyMigrationsLogged(ctx context.Context, runner migrationStepper, logger *slog.Logger, heartbeat time.Duration) error {
	statuses, err := runner.Status(ctx)
	if err != nil {
		return fmt.Errorf("reading goose migration status: %w", err)
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
		stop := startMigrationHeartbeat(heartbeat, func(elapsed string) {
			logger.InfoContext(ctx, "database migration still running",
				"version", next.Version,
				"name", migrationName(next),
				"progress", progress,
				"elapsed", elapsed)
		})
		result, err := runner.UpByOne(ctx)
		stop()
		if errors.Is(err, goose.ErrNoNextVersion) {
			// Another node applied the rest while this one waited for the lock.
			break
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

func migrationName(source *goose.Source) string {
	if source == nil || source.Path == "" {
		return ""
	}
	return path.Base(source.Path)
}

func roundMigrationDuration(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
