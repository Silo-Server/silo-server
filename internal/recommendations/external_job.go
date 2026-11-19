package recommendations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// externalJobTimeout bounds one outside-title run: its TMDB and embedding
// budgets, then every profile's rows.
const externalJobTimeout = 3 * time.Hour

// LiveConfig returns the recommendation settings as this server runs them
// now, including changes applied since startup.
type LiveConfig func() config.RecommendationsConfig

// WithLiveConfig makes the outside-title switch and title cap follow settings
// changes without a restart, and returns the worker. Without it they keep the
// values the engine was built with.
func (w *Worker) WithLiveConfig(live LiveConfig) *Worker {
	if w != nil {
		w.liveConfig = live
	}
	return w
}

func (w *Worker) currentConfig() config.RecommendationsConfig {
	if w.liveConfig != nil {
		return w.liveConfig()
	}
	return w.engine.cfg
}

// ExternalEnabled reports whether outside-title recommendations are on:
// recommendations are enabled and recommendations.external.enabled is set.
func (w *Worker) ExternalEnabled() bool {
	if w == nil || w.engine == nil {
		return false
	}
	cfg := w.currentConfig()
	return cfg.Enabled && cfg.ExternalEnabled
}

// TriggerExternal starts an outside-title run unless one is already running
// on this server or another.
func (w *Worker) TriggerExternal() error {
	return w.runJob(w.jobFor(JobExternal), true)
}

// ExternalStatus is what admin status reports about outside titles.
type ExternalStatus struct {
	Enabled bool
	// Titles counts the outside titles stored; Rows the profile rows.
	Titles int
	Rows   int
}

// ExternalStatus reports whether outside titles are on and what is stored.
func (w *Worker) ExternalStatus(ctx context.Context) (ExternalStatus, error) {
	status := ExternalStatus{Enabled: w.ExternalEnabled()}
	var err error
	if status.Titles, err = w.engine.repo.CountExternalTitles(ctx); err != nil {
		return status, err
	}
	if err := w.engine.pool.QueryRow(ctx, `SELECT count(*) FROM external_recommendation_rows`).Scan(&status.Rows); err != nil {
		return status, fmt.Errorf("count outside rows: %w", err)
	}
	return status, nil
}

// externalResult is one outside-title run's result data.
type externalResult struct {
	// Enabled reports whether the feature was on, so the run did more than
	// prune.
	Enabled bool `json:"enabled"`
	// Pruned* count what retention deleted; PruneFailed marks a run whose
	// retention failed.
	PrunedTitles  int64 `json:"pruned_titles"`
	PrunedSources int64 `json:"pruned_sources"`
	PrunedLinks   int64 `json:"pruned_links"`
	PruneFailed   bool  `json:"prune_failed,omitempty"`
	ExternalPoolCounts
	// TitlesStored counts the outside titles stored after the run.
	TitlesStored int `json:"titles_stored"`
	externalRowsResult
}

// externalRowsResult counts the rebuild of every profile's outside rows.
type externalRowsResult struct {
	RowProfiles int `json:"row_profiles"`
	RowsBuilt   int `json:"rows_built"`
	RowsDeleted int `json:"rows_deleted"`
	// RowsRefused counts rows not written because the embedding lock
	// changed while they were built.
	RowsRefused int `json:"rows_refused"`
	// RowFailures counts profiles whose rows could not be rebuilt; they keep
	// their previous rows.
	RowFailures int `json:"row_failures"`
	// RowsBusy counts profiles another server was refreshing; they are
	// marked stale, and their refresh rebuilds their rows.
	RowsBusy      int `json:"rows_busy"`
	RowsRemaining int `json:"rows_remaining"`
}

func (r externalResult) failures() int {
	n := r.RowFailures
	if r.PruneFailed {
		n++
	}
	return n
}

// doExternal runs the outside-title job: retention always, then, while the
// feature is on, the pool (TMDB lists and details, vectors) and every
// profile's rows. With the feature off it calls neither TMDB nor the
// embedding provider.
func (w *Worker) doExternal(ctx context.Context, held clusterLock) (externalResult, error) {
	var res externalResult
	e := w.engine
	pruned, err := e.PruneExternalTitles(ctx)
	if err != nil {
		res.PruneFailed = true
		slog.WarnContext(ctx, "outside-title retention failed", "component", "recommendations", "error", err)
	}
	res.PrunedTitles, res.PrunedSources, res.PrunedLinks = pruned.Titles, pruned.Sources, pruned.Links

	res.Enabled = w.ExternalEnabled()
	if res.Enabled {
		cfg := w.currentConfig()
		var poolErr error
		res.ExternalPoolCounts, poolErr = e.BuildExternalPool(ctx, ExternalPoolOptions{MaxTitles: cfg.ExternalMaxTitles, held: held})
		// Rows are built from whatever the pool stored, so a pool run that
		// stopped early (TMDB failing, a provider limit) still refreshes
		// them; a lost lock or an expired run does not.
		if poolErr != nil && (errors.Is(poolErr, errJobLockLost) || ctx.Err() != nil) {
			return res, poolErr
		}
		res.externalRowsResult, err = w.rebuildAllExternalRows(ctx, held)
		if err == nil {
			err = poolErr
		}
	}
	if n, countErr := e.repo.CountExternalTitles(ctx); countErr == nil {
		res.TitlesStored = n
	}
	return res, err
}

// rebuildAllExternalRows rebuilds every profile's outside rows from stored
// data, under each profile's refresh lock, as the cache job builds its rows.
func (w *Worker) rebuildAllExternalRows(ctx context.Context, held clusterLock) (externalRowsResult, error) {
	var res externalRowsResult
	profiles, err := w.engine.repo.externalRowSubjects(ctx)
	if err != nil {
		return res, err
	}
	res.RowProfiles = len(profiles)
	for i, p := range profiles {
		if err := ctx.Err(); err != nil {
			res.RowsRemaining = len(profiles) - i
			return res, fmt.Errorf("outside rows stopped after %d of %d profiles: %w", i, len(profiles), err)
		}
		unlock, acquired, err := w.tryProfileLock(ctx, held, p.UserID, p.ProfileID)
		if errors.Is(err, errJobLockLost) {
			res.RowsRemaining = len(profiles) - i
			return res, err
		}
		if err != nil {
			res.RowFailures++
			slog.WarnContext(ctx, "outside rows could not lock a profile", "component", "recommendations", "user_id", p.UserID, "profile_id", p.ProfileID, "error", err)
			continue
		}
		if !acquired {
			res.RowsBusy++
			w.markBusyProfileStale(ctx, p.UserID, p.ProfileID)
			continue
		}
		built, err := w.rebuildProfileExternalRows(ctx, p.UserID, p.ProfileID)
		unlock()
		res.RowsBuilt += built.Written
		res.RowsDeleted += built.Deleted
		res.RowsRefused += built.Refused
		if err != nil {
			res.RowFailures++
			slog.WarnContext(ctx, "rebuilding a profile's outside rows failed", "component", "recommendations", "user_id", p.UserID, "profile_id", p.ProfileID, "error", err)
		}
	}
	return res, nil
}

// externalPurgeTimeout bounds the deleted-profile check after a rebuild.
const externalPurgeTimeout = 10 * time.Second

// rebuildProfileExternalRows is RebuildExternalRows for a caller holding the
// profile's lock. A profile deleted after the rebuild read it, and purged
// before the rebuild wrote, would keep the rows written; like a refresh, it
// checks again after the last write and purges them. The check has its own
// deadline and runs after a failed rebuild too: one that wrote the movie
// row and then failed or timed out on the series row left a row behind.
func (w *Worker) rebuildProfileExternalRows(ctx context.Context, userID int, profileID string) (ExternalRowsBuilt, error) {
	built, err := w.engine.RebuildExternalRows(ctx, userID, profileID)
	purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), externalPurgeTimeout)
	defer cancel()
	if purgeErr := w.engine.purgeIfProfileDeleted(purgeCtx, userID, profileID); purgeErr != nil {
		err = errors.Join(err, fmt.Errorf("check profile still exists: %w", purgeErr))
	}
	return built, err
}
