package recommendations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// EmbeddingLockSettingKey is the server setting that stores the embedding
// lock.
const EmbeddingLockSettingKey = embeddingLockSettingKey

// ErrStaleSweepRunning reports an embeddings reset refused because a server
// is refreshing stale profiles, which read embeddings and write taste
// profiles.
var ErrStaleSweepRunning = errors.New("stale profile sweep is running")

// resetEmbeddingsTimeout bounds the reset transaction.
const resetEmbeddingsTimeout = 2 * time.Minute

// EmbeddingsReset counts the rows an embeddings reset deleted.
type EmbeddingsReset struct {
	Embeddings    int64
	TasteProfiles int64
	TasteClusters int64
	CachedRows    int64
}

// ResetEmbeddings deletes the embedding lock, every item embedding, every
// taste profile and cluster, and every profile's cached rows in one
// transaction, so the next embedding run starts a new embedding space with
// whatever model the server is configured for. Global rows (popular, recently
// added, top rated, genre samplers) do not use embeddings and stay.
//
// The reset holds the claims of every job that reads or writes those rows
// (embeddings, taste profiles, the cache) and the stale sweep's lock, so none
// of them interleaves with it on any server; co-watch pairs do not depend on
// embeddings. A busy job or sweep refuses the reset with ErrJobRunning,
// ErrJobRunningElsewhere or ErrStaleSweepRunning. A profile refresh already
// running when the reset commits can still write one taste profile built from
// the old embeddings; the profile's next refresh after re-embedding replaces
// it.
//
// Every item that lost its embedding gets a search index upsert, so its
// search document drops the old vector on the next index sync.
func (w *Worker) ResetEmbeddings(ctx context.Context) (EmbeddingsReset, error) {
	release, err := w.claimJobs(JobEmbeddings, JobTasteProfiles, JobRecommendations)
	if err != nil {
		return EmbeddingsReset{}, err
	}
	defer release()

	lockCtx, cancel := context.WithTimeout(ctx, jobLockTimeout)
	unlock, acquired, err := w.locker.TryLock(lockCtx, staleSweepLock)
	cancel()
	if err != nil {
		return EmbeddingsReset{}, fmt.Errorf("take stale sweep lock: %w", err)
	}
	if !acquired {
		return EmbeddingsReset{}, ErrStaleSweepRunning
	}
	defer unlock()

	ctx, cancel = context.WithTimeout(ctx, resetEmbeddingsTimeout)
	defer cancel()
	res, err := w.engine.repo.ResetEmbeddings(ctx)
	if err != nil {
		return EmbeddingsReset{}, err
	}
	slog.InfoContext(ctx, "recommendation embeddings reset", "component", "recommendations",
		"embeddings", res.Embeddings, "taste_profiles", res.TasteProfiles, "taste_clusters", res.TasteClusters, "cached_rows", res.CachedRows)
	return res, nil
}

// claimJobs claims each named job in order, as runJob does, and returns one
// function that releases them all. When a claim fails it releases the ones
// it took.
func (w *Worker) claimJobs(names ...JobName) (release func(), err error) {
	releases := make([]func(), 0, len(names))
	release = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, name := range names {
		r, err := w.claimJob(name)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, r)
	}
	return release, nil
}

// ResetEmbeddings deletes the embedding lock, every item embedding, taste
// profile, taste cluster and per-profile cache row in one transaction, and
// queues a search index upsert for every item that had an embedding.
func (r *Repo) ResetEmbeddings(ctx context.Context) (EmbeddingsReset, error) {
	var res EmbeddingsReset
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin embeddings reset: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		return res, fmt.Errorf("delete embedding lock: %w", err)
	}
	rows, err := tx.Query(ctx, `DELETE FROM media_item_embeddings RETURNING media_item_id`)
	if err != nil {
		return res, fmt.Errorf("delete embeddings: %w", err)
	}
	itemIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return res, fmt.Errorf("delete embeddings: %w", err)
	}
	res.Embeddings = int64(len(itemIDs))

	for _, step := range []struct {
		what  string
		query string
		count *int64
	}{
		{"taste clusters", `DELETE FROM user_taste_clusters`, &res.TasteClusters},
		{"taste profiles", `DELETE FROM user_taste_profiles`, &res.TasteProfiles},
		// Global rows have no user and do not use embeddings.
		{"cached rows", `DELETE FROM recommendation_cache WHERE user_id IS NOT NULL`, &res.CachedRows},
	} {
		tag, err := tx.Exec(ctx, step.query)
		if err != nil {
			return res, fmt.Errorf("delete %s: %w", step.what, err)
		}
		*step.count = tag.RowsAffected()
	}

	if err := catalog.EnqueueSearchIndexUpserts(ctx, tx, itemIDs); err != nil {
		return res, fmt.Errorf("queue search index updates: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit embeddings reset: %w", err)
	}
	return res, nil
}

// EmbeddingLockConflict explains why the stored embedding lock rejects the
// embedding configuration this server runs, or returns "" when it does not or
// no lock is stored.
func (w *Worker) EmbeddingLockConflict(ctx context.Context) (string, error) {
	lock, err := w.engine.repo.GetEmbeddingLock(ctx)
	if err != nil || lock == nil {
		return "", err
	}
	return DescribeEmbeddingLockConflict(*lock, w.engine.cfg.EmbeddingBaseURL, w.engine.cfg.EmbeddingModel, 0), nil
}

// DescribeEmbeddingLockConflict explains why lock rejects an embedding
// configuration whose model returns sourceDimensions (0 when unknown), or
// returns "" when it does not. A lock recording vectors Silo cannot store is a
// conflict for every configuration. The text never quotes a base URL, which
// can carry credentials.
func DescribeEmbeddingLockConflict(lock EmbeddingLock, baseURL, model string, sourceDimensions int) string {
	err := lock.ValidateConfig(baseURL, model)
	if err == nil && sourceDimensions > 0 {
		err = lock.Validate(baseURL, model, sourceDimensions)
	}
	// Stored vectors of another width conflict whatever the model returns,
	// so this needs no probe of the provider.
	if err == nil && lock.StorageDimensions != 0 && lock.StorageDimensions != CanonicalEmbeddingDimensions {
		return fmt.Sprintf("Embeddings were stored at %d dimensions; this server stores %d. Reset embeddings.", lock.StorageDimensions, CanonicalEmbeddingDimensions)
	}
	if err != nil {
		switch {
		case lock.BaseURL != "" && lock.BaseURL != baseURL:
			return "Embeddings were created with a different provider base URL. Reset embeddings before switching providers."
		case lock.Model != "" && lock.Model != model:
			return fmt.Sprintf("Embeddings were created with model %q, not %q. Reset embeddings before switching models.", lock.Model, model)
		case lock.SourceDimensions != 0 && lock.SourceDimensions != sourceDimensions:
			return fmt.Sprintf("Embeddings were created with %d-dimension vectors, but the model now returns %d. Reset embeddings before switching models.", lock.SourceDimensions, sourceDimensions)
		default:
			return fmt.Sprintf("Embeddings were stored at %d dimensions; this server stores %d. Reset embeddings.", lock.StorageDimensions, CanonicalEmbeddingDimensions)
		}
	}
	if lock.SourceDimensions <= 0 || lock.SourceDimensions > CanonicalEmbeddingDimensions {
		return fmt.Sprintf("The embedding lock records %d-dimension vectors; Silo stores 1 to %d. Reset embeddings and use a model that returns at most %d dimensions.",
			lock.SourceDimensions, CanonicalEmbeddingDimensions, CanonicalEmbeddingDimensions)
	}
	return ""
}

// CacheRefreshedAt returns when the newest cached row was written, or nil
// when nothing is cached.
func (w *Worker) CacheRefreshedAt(ctx context.Context) (*time.Time, error) {
	return w.engine.repo.CacheRefreshedAt(ctx)
}

// CacheRefreshedAt returns the newest cache row's write time, or nil when the
// cache is empty.
func (r *Repo) CacheRefreshedAt(ctx context.Context) (*time.Time, error) {
	var at *time.Time
	if err := r.pool.QueryRow(ctx, `SELECT MAX(created_at) FROM recommendation_cache`).Scan(&at); err != nil {
		return nil, fmt.Errorf("read cache refresh time: %w", err)
	}
	return at, nil
}
