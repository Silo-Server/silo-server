package recommendations

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// keyedLocker refuses the keys another server holds and grants the rest.
type keyedLocker struct {
	mu       sync.Mutex
	held     map[int64]bool
	keys     []int64
	released int
}

func (l *keyedLocker) TryLock(_ context.Context, key int64) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, key)
	if l.held[key] {
		return nil, false, nil
	}
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.released++
	}, true, nil
}

func (l *keyedLocker) snapshot() (keys []int64, released int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.keys), l.released
}

func assertNothingRunning(t *testing.T, w *Worker) {
	t.Helper()
	for _, name := range []JobName{JobEmbeddings, JobTasteProfiles, JobCowatch, JobRecommendations} {
		if w.IsRunning(name) {
			t.Fatalf("%s is still marked running", name)
		}
	}
}

func TestResetEmbeddingsRefusesWhileAJobRunsHere(t *testing.T) {
	locker := &keyedLocker{}
	w := newJobTestWorker(locker, nil)
	// No engine: a reset that went ahead would dereference it.
	w.setRunning(JobTasteProfiles, true)

	_, err := w.ResetEmbeddings(t.Context())
	if !errors.Is(err, ErrJobRunning) {
		t.Fatalf("reset while taste profiles run = %v, want ErrJobRunning", err)
	}
	keys, released := locker.snapshot()
	if !slices.Equal(keys, []int64{embeddingsJobLock}) || released != 1 {
		t.Fatalf("locks taken %#x, released %d; want only the embeddings lock, released", keys, released)
	}
	if w.IsRunning(JobEmbeddings) || !w.IsRunning(JobTasteProfiles) {
		t.Fatal("the reset kept a claim, or dropped the running job's flag")
	}
}

func TestResetEmbeddingsRefusesWhileAnotherServerRunsAJob(t *testing.T) {
	locker := &keyedLocker{held: map[int64]bool{cacheJobLock: true}}
	w := newJobTestWorker(locker, nil)

	_, err := w.ResetEmbeddings(t.Context())
	if !errors.Is(err, ErrJobRunningElsewhere) {
		t.Fatalf("reset while another server caches = %v, want ErrJobRunningElsewhere", err)
	}
	keys, released := locker.snapshot()
	if !slices.Equal(keys, []int64{embeddingsJobLock, tasteProfilesJobLock, cacheJobLock}) || released != 2 {
		t.Fatalf("locks taken %#x, released %d", keys, released)
	}
	assertNothingRunning(t, w)
}

func TestResetEmbeddingsRefusesWhileProfilesAreSwept(t *testing.T) {
	locker := &keyedLocker{held: map[int64]bool{staleSweepLock: true}}
	w := newJobTestWorker(locker, nil)

	_, err := w.ResetEmbeddings(t.Context())
	if !errors.Is(err, ErrStaleSweepRunning) {
		t.Fatalf("reset during a stale sweep = %v, want ErrStaleSweepRunning", err)
	}
	keys, released := locker.snapshot()
	if !slices.Equal(keys, []int64{embeddingsJobLock, tasteProfilesJobLock, cacheJobLock, staleSweepLock}) || released != 3 {
		t.Fatalf("locks taken %#x, released %d", keys, released)
	}
	assertNothingRunning(t, w)
}

// Saved embedding settings that wait for a restart refuse a reset before it
// claims anything: this server would re-embed with the old model and lock it
// again. Matching settings let the reset go on to its claims.
func TestResetEmbeddingsRefusesWhileSavedSettingsWaitForARestart(t *testing.T) {
	running := config.RecommendationsConfig{EmbeddingModel: "old", EmbeddingBaseURL: "http://embed:11434"}
	for _, tc := range []struct {
		name  string
		saved config.RecommendationsConfig
		want  error
	}{
		{"new model", config.RecommendationsConfig{EmbeddingModel: "new", EmbeddingBaseURL: running.EmbeddingBaseURL}, ErrEmbeddingSettingsPendingRestart},
		{"new base URL", config.RecommendationsConfig{EmbeddingModel: running.EmbeddingModel, EmbeddingBaseURL: "http://other:11434"}, ErrEmbeddingSettingsPendingRestart},
		{"active", running, ErrStaleSweepRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locker := &keyedLocker{held: map[int64]bool{staleSweepLock: true}}
			w := newJobTestWorker(locker, nil)
			w.engine = &Engine{cfg: running}
			w.WithSavedConfig(func(context.Context) (config.RecommendationsConfig, error) { return tc.saved, nil })

			_, err := w.ResetEmbeddings(t.Context())
			if !errors.Is(err, tc.want) {
				t.Fatalf("reset = %v, want %v", err, tc.want)
			}
			if keys, _ := locker.snapshot(); errors.Is(tc.want, ErrEmbeddingSettingsPendingRestart) && len(keys) != 0 {
				t.Fatalf("a refused reset took locks %#x", keys)
			}
			assertNothingRunning(t, w)
		})
	}
}

func TestResetEmbeddingsHoldsEveryClaimWhileItRuns(t *testing.T) {
	locker := &keyedLocker{}
	w := newJobTestWorker(locker, nil)
	w.engine = closedPoolEngine(t)

	_, err := w.ResetEmbeddings(t.Context())
	if err == nil || errors.Is(err, ErrJobRunning) || errors.Is(err, ErrJobRunningElsewhere) || errors.Is(err, ErrStaleSweepRunning) {
		t.Fatalf("reset against a failing database = %v, want a database error", err)
	}
	keys, released := locker.snapshot()
	if !slices.Equal(keys, []int64{embeddingsJobLock, tasteProfilesJobLock, cacheJobLock, staleSweepLock}) || released != 4 {
		t.Fatalf("locks taken %#x, released %d", keys, released)
	}
	assertNothingRunning(t, w)
	// A cron run of any of those jobs can start again afterwards.
	if err := w.runJob(job{name: JobEmbeddings, timeout: time.Minute, run: func(context.Context) (jobResult, error) {
		return embeddingsResult{}, nil
	}}, false); err != nil {
		t.Fatalf("embeddings after the reset: %v", err)
	}
}

func TestDescribeEmbeddingLockConflict(t *testing.T) {
	lock := EmbeddingLock{BaseURL: "https://user:pw@embed.example", Model: "model-a", SourceDimensions: 768, StorageDimensions: CanonicalEmbeddingDimensions}
	for _, tc := range []struct {
		name    string
		lock    EmbeddingLock
		baseURL string
		model   string
		dims    int
		want    string
	}{
		{"matching", lock, lock.BaseURL, "model-a", 768, ""},
		{"matching, dimensions unknown", lock, lock.BaseURL, "model-a", 0, ""},
		{"other base URL", lock, "http://other", "model-a", 768, "different provider base URL"},
		{"other model", lock, lock.BaseURL, "model-b", 768, `model "model-a", not "model-b"`},
		{"other dimensions", lock, lock.BaseURL, "model-a", 1024, "768-dimension vectors, but the model now returns 1024"},
		{"unstorable lock", EmbeddingLock{BaseURL: "http://ollama", Model: "qwen3-embedding:latest", SourceDimensions: 4096}, "http://ollama", "qwen3-embedding:latest", 0, "records 4096-dimension vectors; Silo stores 1 to 3072"},
		{"zero-dimension lock", EmbeddingLock{BaseURL: "http://ollama", Model: "m"}, "http://ollama", "m", 0, "records 0-dimension vectors"},
		{"other storage", EmbeddingLock{Model: "m", SourceDimensions: 768, StorageDimensions: 1536}, "", "m", 768, "stored at 1536 dimensions"},
		{"other storage, dimensions unknown", EmbeddingLock{Model: "m", SourceDimensions: 768, StorageDimensions: 1536}, "", "m", 0, "stored at 1536 dimensions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeEmbeddingLockConflict(tc.lock, tc.baseURL, tc.model, tc.dims)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Fatalf("conflict = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "pw@") || strings.Contains(got, "embed.example") || strings.Contains(got, "http://other") {
				t.Fatalf("conflict quotes a base URL: %q", got)
			}
		})
	}
}

func TestEmbeddingLockConflictUsesTheRunningConfigPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := t.Context()
	w := newJobTestWorker(&keyedLocker{}, nil)
	w.engine = &Engine{repo: NewRepo(pool)}
	w.engine.cfg.EmbeddingBaseURL = "http://ollama:11434"
	w.engine.cfg.EmbeddingModel = "model-b"

	if _, err := pool.Exec(ctx, `DELETE FROM server_settings WHERE key = $1`, embeddingLockSettingKey); err != nil {
		t.Fatal(err)
	}
	if conflict, err := w.EmbeddingLockConflict(ctx); err != nil || conflict != "" {
		t.Fatalf("no lock: %q, %v", conflict, err)
	}
	if err := w.engine.repo.SetEmbeddingLock(ctx, EmbeddingLock{BaseURL: "http://ollama:11434", Model: "model-a", SourceDimensions: 768}); err != nil {
		t.Fatal(err)
	}
	if conflict, err := w.EmbeddingLockConflict(ctx); err != nil || !strings.Contains(conflict, `"model-a", not "model-b"`) {
		t.Fatalf("other model: %q, %v", conflict, err)
	}
}

// resetFixture seeds two embedded items, a lock, a profile's taste profile,
// cluster and cached rows, and a global cached row.
type resetFixture struct {
	itemIDs    []string
	userID     int
	globalType string
}

func seedResetFixture(t *testing.T, ctx context.Context, repo *Repo) resetFixture {
	t.Helper()
	pool := repo.pool
	f := resetFixture{globalType: "test_global_" + uuid.NewString()}
	for i := range 2 {
		id := "reset-" + uuid.NewString()
		seedRecoMediaItemTitled(t, pool, id, "movie", "matched", "Reset Fixture "+id)
		f.itemIDs = append(f.itemIDs, id)
		vector := make([]float32, 8)
		vector[i] = 1
		if err := repo.UpsertEmbedding(ctx, id, vector, "model-a", "text"); err != nil {
			t.Fatalf("seed embedding: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, f.itemIDs)
		_, _ = pool.Exec(context.Background(), `DELETE FROM catalog_search_index_events WHERE content_id = ANY($1)`, f.itemIDs)
	})
	if err := repo.SetEmbeddingLock(ctx, EmbeddingLock{BaseURL: "http://ollama", Model: "model-a", SourceDimensions: 8}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		"embeddings-reset-"+uuid.NewString()).Scan(&f.userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, f.userID)
	})
	taste := make([]float32, CanonicalEmbeddingDimensions)
	taste[0] = 1
	if err := repo.UpsertTasteProfile(ctx, f.userID, "p", taste, map[string]int{"watched": 2}, "", time.Now()); err != nil {
		t.Fatalf("seed taste profile: %v", err)
	}
	if err := repo.UpsertTasteClusters(ctx, f.userID, "p", []TasteCluster{{ClusterIdx: 0, Embedding: taste, Label: "Drama", MemberCount: 2, TotalWeight: 1}}); err != nil {
		t.Fatalf("seed taste clusters: %v", err)
	}
	items := []ScoredItem{{MediaItemID: f.itemIDs[0], Score: 1}}
	expires := cacheExpiry(time.Now())
	if err := repo.UpsertRecommendationCache(ctx, f.userID, "p", RecTypeForYouMain, "", items, expires); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, f.globalType, "", items, expires); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, f.globalType)
	})
	return f
}

// useMeilisearchProvider makes search index writes queue events for the
// test's duration.
func useMeilisearchProvider(t *testing.T, ctx context.Context, repo *Repo) {
	t.Helper()
	var original *string
	_ = repo.pool.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, catalog.SearchSettingProvider).Scan(&original)
	t.Cleanup(func() {
		if original == nil {
			_, _ = repo.pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, catalog.SearchSettingProvider)
			return
		}
		_, _ = repo.pool.Exec(context.Background(), `UPDATE server_settings SET value = $2 WHERE key = $1`, catalog.SearchSettingProvider, *original)
	})
	if _, err := repo.pool.Exec(ctx, `
		INSERT INTO server_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, catalog.SearchSettingProvider, catalog.SearchProviderMeilisearch); err != nil {
		t.Fatal(err)
	}
}

// The reset deletes every embedding, the lock, taste profiles, clusters and
// per-profile cache rows together, keeps global rows, and queues a search
// update for every item that lost its vector.
func TestResetEmbeddingsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := t.Context()
	repo := NewRepo(pool)
	f := seedResetFixture(t, ctx, repo)
	useMeilisearchProvider(t, ctx, repo)
	var lastEvent int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM catalog_search_index_events`).Scan(&lastEvent); err != nil {
		t.Fatal(err)
	}
	before := countRows(t, repo.pool, `SELECT COUNT(*) FROM media_item_embeddings`)

	res, err := repo.ResetEmbeddings(ctx)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if res.Embeddings != int64(before) || res.TasteProfiles < 1 || res.TasteClusters < 1 || res.CachedRows < 1 {
		t.Fatalf("reset counts = %+v, want %d embeddings and the seeded taste and cache rows", res, before)
	}
	if lock, err := repo.GetEmbeddingLock(ctx); err != nil || lock != nil {
		t.Fatalf("lock after reset = %+v, %v", lock, err)
	}
	for _, table := range []string{"media_item_embeddings", "user_taste_profiles", "user_taste_clusters"} {
		if n := countRows(t, repo.pool, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Fatalf("%s has %d rows after reset", table, n)
		}
	}
	if n := countRows(t, repo.pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id IS NOT NULL`); n != 0 {
		t.Fatalf("%d per-profile cache rows after reset", n)
	}
	if n := countRows(t, repo.pool, `SELECT COUNT(*) FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, f.globalType); n != 1 {
		t.Fatalf("global cache row count after reset = %d, want 1", n)
	}
	if n := countRows(t, repo.pool, `
		SELECT COUNT(DISTINCT content_id) FROM catalog_search_index_events
		WHERE id > $1 AND action = 'upsert' AND content_id = ANY($2)`, lastEvent, f.itemIDs); n != len(f.itemIDs) {
		t.Fatalf("search upserts queued for %d of %d reset items", n, len(f.itemIDs))
	}
}

// A failure inside the reset leaves everything as it was.
func TestResetEmbeddingsRollsBackPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	snapshotEmbeddingLock(t, pool)
	ctx := t.Context()
	repo := NewRepo(pool)
	f := seedResetFixture(t, ctx, repo)

	// The last delete fails, after the lock, embeddings and taste rows are
	// already deleted inside the transaction.
	fn := "test_fail_reset_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION `+fn+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'reset blocked by test'; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER `+fn+` BEFORE DELETE ON recommendation_cache FOR EACH STATEMENT EXECUTE FUNCTION `+fn+`()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+fn+` ON recommendation_cache`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`()`)
	})

	if _, err := repo.ResetEmbeddings(ctx); err == nil || !strings.Contains(err.Error(), "reset blocked by test") {
		t.Fatalf("reset = %v, want the injected failure", err)
	}
	if lock, err := repo.GetEmbeddingLock(ctx); err != nil || lock == nil || lock.Model != "model-a" {
		t.Fatalf("lock after a failed reset = %+v, %v", lock, err)
	}
	if n := countRows(t, repo.pool, `SELECT COUNT(*) FROM media_item_embeddings WHERE media_item_id = ANY($1)`, f.itemIDs); n != len(f.itemIDs) {
		t.Fatalf("%d of %d embeddings left after a failed reset", n, len(f.itemIDs))
	}
	for _, table := range []string{"user_taste_profiles", "user_taste_clusters", "recommendation_cache"} {
		if n := countRows(t, repo.pool, `SELECT COUNT(*) FROM `+table+` WHERE user_id = $1`, f.userID); n == 0 {
			t.Fatalf("%s lost the profile's rows in a failed reset", table)
		}
	}
}

func TestCacheRefreshedAtPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	repo := NewRepo(pool)
	recType := "test_global_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, recType)
	})
	if err := repo.UpsertRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, recType, "", []ScoredItem{{MediaItemID: "x", Score: 1}}, cacheExpiry(time.Now())); err != nil {
		t.Fatal(err)
	}
	var written time.Time
	if err := pool.QueryRow(ctx, `SELECT created_at FROM recommendation_cache WHERE user_id IS NULL AND rec_type = $1`, recType).Scan(&written); err != nil {
		t.Fatal(err)
	}
	at, err := repo.CacheRefreshedAt(ctx)
	if err != nil || at == nil || at.Before(written) {
		t.Fatalf("cache refreshed at = %v, %v; want at least %v", at, err, written)
	}
}
