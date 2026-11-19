package recommendations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

var errExternalDisabled = errors.New("outside-title generation disabled")

// StopExternalGeneration cancels the current run after a config reload turns
// either recommendation switch off. Retention runs outside this context.
func (w *Worker) StopExternalGeneration() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancelExternal != nil {
		w.cancelExternal(errExternalDisabled)
	}
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

	generationCtx, cancel := context.WithCancelCause(ctx)
	// Register before reading the switches: a reload either cancels this
	// context or the read below already sees the disabled setting.
	w.mu.Lock()
	w.cancelExternal = cancel
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.cancelExternal = nil
		w.mu.Unlock()
		cancel(nil)
	}()
	cfg := w.currentConfig()
	res.Enabled = cfg.Enabled && cfg.ExternalEnabled
	if res.Enabled {
		res.ExternalPoolCounts, err = e.BuildExternalPool(generationCtx, ExternalPoolOptions{MaxTitles: cfg.ExternalMaxTitles, held: held})
		// A failed write on the pinned connection may be the first sign its
		// session closed. Only rebuild from partial results while we own it.
		if err != nil && held != nil && generationCtx.Err() == nil && !errors.Is(err, errJobLockLost) {
			err = errors.Join(err, held.Check(generationCtx))
		}
		if generationCtx.Err() == nil && !errors.Is(err, errJobLockLost) {
			var rowsErr error
			res.externalRowsResult, rowsErr = w.rebuildAllExternalRows(generationCtx, held)
			err = errors.Join(err, rowsErr)
		}
		if errors.Is(context.Cause(generationCtx), errExternalDisabled) && errors.Is(err, context.Canceled) {
			err = nil
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
		lock, unlock, acquired, err := w.tryProfileLock(ctx, held, p.UserID, p.ProfileID)
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
		profileCtx, cancel := context.WithTimeout(ctx, profileRefreshTimeout)
		built, err := w.rebuildProfileExternalRows(profileCtx, lock, p.UserID, p.ProfileID, nil)
		cancel()
		unlock()
		res.RowsBuilt += built.Written
		res.RowsDeleted += built.Deleted
		res.RowsRefused += built.Refused
		if errors.Is(err, errJobLockLost) {
			res.RowsRemaining = len(profiles) - i
			return res, err
		}
		if err != nil {
			res.RowFailures++
			slog.WarnContext(ctx, "rebuilding a profile's outside rows failed", "component", "recommendations", "user_id", p.UserID, "profile_id", p.ProfileID, "error", err)
		}
		// A signal refresh that arrived while this job held the profile lock
		// left a durable stale mark. Queue it as soon as the lock is free.
		var stale bool
		if err := w.engine.pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM user_taste_profiles WHERE user_id = $1 AND profile_id = $2 AND stale_at IS NOT NULL
		)`, p.UserID, p.ProfileID).Scan(&stale); err != nil {
			slog.WarnContext(ctx, "checking stale profile after outside row rebuild failed", "component", "recommendations", "user_id", p.UserID, "profile_id", p.ProfileID, "error", err)
		} else if stale {
			w.RequestProfileRefresh(ctx, p.UserID, p.ProfileID)
		}
	}
	return res, nil
}

// externalPurgeTimeout bounds the deleted-profile check after a rebuild.
const externalPurgeTimeout = 10 * time.Second

// errProfilePurgeCheck marks a failed deleted-profile check, which a refresh
// must not treat as done, apart from a failed rebuild of the rows themselves.
var errProfilePurgeCheck = errors.New("check profile still exists")

// rebuildProfileExternalRows rebuilds rows on the session holding the
// profile's lock. A profile deleted after the rebuild read it, and purged
// before the rebuild wrote, would keep the rows written; like a refresh, it
// checks again after the last write and purges them. The check has its own
// deadline and runs after a failed rebuild too: one that wrote the movie
// row and then failed or timed out on the series row left a row behind.
func (w *Worker) rebuildProfileExternalRows(ctx context.Context, lock clusterLock, userID int, profileID string, signals *tasteSignals) (ExternalRowsBuilt, error) {
	built, err := w.engine.rebuildExternalRows(ctx, lock, userID, profileID, signals)
	if err != nil && lock != nil && ctx.Err() == nil {
		err = errors.Join(err, lock.Check(ctx))
	}
	purgeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), externalPurgeTimeout)
	defer cancel()
	if purgeErr := w.engine.purgeIfProfileDeleted(purgeCtx, userID, profileID); purgeErr != nil {
		err = errors.Join(err, fmt.Errorf("%w: %w", errProfilePurgeCheck, purgeErr))
	}
	return built, err
}

// PruneExternalTitles applies outside-title retention: titles whose last
// detail fetch is externalRetentionDetail old, and titles and sources nothing
// has referenced for externalRetentionUnreferenced. It calls neither TMDB nor
// the embedding provider, so it runs whether or not the feature is enabled.
func (e *Engine) PruneExternalTitles(ctx context.Context) (ExternalRetentionResult, error) {
	return pruneExternalTitles(ctx, e.repo)
}

func pruneExternalTitles(ctx context.Context, repo *Repo) (ExternalRetentionResult, error) {
	now, err := repo.Now(ctx)
	if err != nil {
		return ExternalRetentionResult{}, err
	}
	res, err := repo.PruneExternalTitles(ctx, now.Add(-externalRetentionDetail), now.Add(-externalRetentionUnreferenced))
	if err != nil {
		return res, err
	}
	if res.Titles > 0 || res.Sources > 0 {
		slog.InfoContext(ctx, "outside titles pruned", "component", "recommendations", "titles", res.Titles, "sources", res.Sources, "links", res.Links)
	}
	return res, nil
}

// externalRetentionInterval is how often RunExternalRetention prunes.
const externalRetentionInterval = 24 * time.Hour

// RunExternalRetention applies outside-title retention now and then daily,
// until ctx ends. It is for a server started with recommendations off: such
// a server has no Worker, so nothing else would delete the TMDB data an
// earlier configuration stored. Every API server runs it; the deletes are
// idempotent.
func RunExternalRetention(ctx context.Context, pool *pgxpool.Pool) {
	repo := NewRepo(pool)
	ticker := time.NewTicker(externalRetentionInterval)
	defer ticker.Stop()
	for {
		if _, err := pruneExternalTitles(ctx, repo); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "outside-title retention failed", "component", "recommendations", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
