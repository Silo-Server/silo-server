package recommendations

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func seedCompletion(t *testing.T, pool *pgxpool.Pool, userID int, profile, itemID string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, completed, updated_at)
		VALUES ($1, $2, $3, true, $4)`, userID, profile, itemID, at); err != nil {
		t.Fatal(err)
	}
}

// Anchors are titles still in the catalog: a deleted movie and the episodes
// of a deleted series are passed over on both the SQL path and the user
// store path, which pages past them.
func TestRecentCompletedItemIDsSkipDeletedItemsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	prefix := "tlive-anchor-" + uuid.NewString()[:8] + "-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)

	movieA, movieB, movieC, series := prefix+"movie-a", prefix+"movie-b", prefix+"movie-c", prefix+"series"
	for _, id := range []string{movieA, movieB, movieC} {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
	}
	seedRecoMediaItem(t, pool, series, "series", "matched")
	episodes := []string{prefix + "s1e1", prefix + "s1e2"}
	for i, ep := range episodes {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, $3, 'Episode')`, ep, series, i+1); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	seedCompletion(t, pool, userID, profile, movieC, base)
	seedCompletion(t, pool, userID, profile, episodes[1], base.Add(-time.Minute))
	seedCompletion(t, pool, userID, profile, episodes[0], base.Add(-2*time.Minute))
	seedCompletion(t, pool, userID, profile, movieB, base.Add(-3*time.Minute))
	seedCompletion(t, pool, userID, profile, movieA, base.Add(-4*time.Minute))

	repo := NewRepo(pool)
	storePath := NewSignalReader(repo, pgstore.NewPostgresProvider(pool))
	before, err := storePath.RecentCompletedItemIDs(ctx, userID, profile, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{movieC, series, movieB}; !slices.Equal(before, want) {
		t.Fatalf("anchors before the deletes = %v, want %v", before, want)
	}

	// Deleting the series cascades its episodes, so their progress no
	// longer rolls up to it.
	if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movieC, series}); err != nil {
		t.Fatal(err)
	}
	want := []string{movieB, movieA}
	sqlPath, err := repo.GetRecentCompletedItemIDs(ctx, userID, profile, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sqlPath, want) {
		t.Fatalf("SQL path anchors = %v, want %v", sqlPath, want)
	}
	viaStore, err := storePath.RecentCompletedItemIDs(ctx, userID, profile, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(viaStore, want) {
		t.Fatalf("store path anchors = %v, want %v", viaStore, want)
	}

	existing, err := repo.ExistingItemIDs(ctx, []string{movieA, movieC, episodes[0], series})
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 1 {
		t.Fatalf("existing = %v, want only %s", existing, movieA)
	}
	if _, ok := existing[movieA]; !ok {
		t.Fatalf("existing = %v, want %s", existing, movieA)
	}
}

func cowatchPairsFor(t *testing.T, pool *pgxpool.Pool, prefix string) map[string]float64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT item_id, similar_item_id, jaccard_score
		FROM   item_cowatch
		WHERE  item_id LIKE $1 OR similar_item_id LIKE $1`, prefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[string]float64{}
	for rows.Next() {
		var item, similar string
		var score float64
		if err := rows.Scan(&item, &similar, &score); err != nil {
			t.Fatal(err)
		}
		pairs[item+">"+similar] = score
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return pairs
}

// The co-watch job never pairs a deleted item, and each run leaves only the
// pairs it wrote: pairs for items deleted since, and pairs that fell below
// the shared-watcher floor, are gone after the next run.
func TestCowatchSkipsDeletedItemsAndPrunesStalePairsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	prefix := "tcowatch-" + uuid.NewString()[:8] + "-"
	cleanupRecoMediaItems(t, pool, prefix)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM item_cowatch WHERE item_id LIKE $1 OR similar_item_id LIKE $1`, prefix+"%")
	})

	x, y, z, dead := prefix+"x", prefix+"y", prefix+"z", prefix+"dead"
	for _, id := range []string{x, y, z, dead} {
		seedRecoMediaItem(t, pool, id, "movie", "matched")
	}
	type watcher struct {
		userID  int
		profile string
	}
	watchers := make([]watcher, DefaultMinWatchers)
	at := time.Now().Add(-time.Hour)
	for i := range watchers {
		userID, profile := newTasteTestAccount(t, pool, prefix)
		watchers[i] = watcher{userID, profile}
		for _, id := range []string{x, y, z, dead} {
			seedCompletion(t, pool, userID, profile, id, at)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, dead); err != nil {
		t.Fatal(err)
	}
	// A pair an earlier run left for an item deleted since.
	if _, err := pool.Exec(ctx, `
		INSERT INTO item_cowatch(item_id, similar_item_id, jaccard_score, cowatch_count, updated_at)
		VALUES ($1, $2, 0.9, 9, NOW() - INTERVAL '2 days')`, x, prefix+"merged-away"); err != nil {
		t.Fatal(err)
	}

	w := &Worker{engine: &Engine{repo: NewRepo(pool)}}
	if _, err := w.doCowatch(ctx); err != nil {
		t.Fatal(err)
	}
	got := cowatchPairsFor(t, pool, prefix)
	want := map[string]float64{x + ">" + y: 1, x + ">" + z: 1, y + ">" + x: 1, y + ">" + z: 1, z + ">" + x: 1, z + ">" + y: 1}
	if len(got) != len(want) {
		t.Fatalf("pairs = %v, want %v", got, want)
	}
	for pair, score := range want {
		if got[pair] != score {
			t.Fatalf("pairs = %v, want %v", got, want)
		}
	}

	// z loses watchers until too few accounts share it; its pairs go.
	for _, wt := range watchers[:3] {
		if _, err := pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`, wt.userID, z); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.doCowatch(ctx); err != nil {
		t.Fatal(err)
	}
	got = cowatchPairsFor(t, pool, prefix)
	if len(got) != 2 || got[x+">"+y] != 1 || got[y+">"+x] != 1 {
		t.Fatalf("pairs after the second run = %v, want only x and y paired", got)
	}
}

// pgvector rejects an hnsw.ef_search above 1000; a deep candidate pull
// clamps to it instead of failing the scan.
func TestHNSWCandidateScanClampsEfSearchPostgres(t *testing.T) {
	newEngineTestPool(t)
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	// One connection, so loading the vector library below applies to the
	// scan's connection and pgvector validates the setting.
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `SELECT '[1,2]'::vector`); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rejected := tx.Exec(ctx, `SELECT set_config('hnsw.ef_search', '1200', true)`)
	_ = tx.Rollback(ctx)
	if rejected == nil {
		t.Fatal("pgvector accepted hnsw.ef_search 1200; the clamp is no longer needed")
	}

	var got string
	err = NewRepo(pool).withHNSWCandidateScan(ctx, 1200, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SHOW hnsw.ef_search`).Scan(&got)
	})
	if err != nil {
		t.Fatalf("candidate scan for 1200: %v", err)
	}
	if got != "1000" {
		t.Fatalf("hnsw.ef_search = %s, want 1000", got)
	}
}

// The cluster read without embeddings answers every other field the full
// read does.
func TestGetTasteClusterMetaOmitsOnlyEmbeddingsPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	userID, profile := newTasteTestAccount(t, pool, "tcluster-meta-")
	repo := NewRepo(pool)
	clusters := []TasteCluster{
		{ClusterIdx: 0, Embedding: axisVector(0, nil), DominantGenres: []string{"Drama", "Crime"}, Label: "Drama", MemberCount: 4, TotalWeight: 2.5},
		{ClusterIdx: 1, Embedding: axisVector(1, nil), DominantGenres: []string{"Comedy"}, Label: "Comedy", MemberCount: 2, TotalWeight: 1.25},
	}
	if err := repo.UpsertTasteClusters(ctx, userID, profile, clusters); err != nil {
		t.Fatal(err)
	}
	full, err := repo.GetTasteClusters(ctx, userID, profile)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := repo.GetTasteClusterMeta(ctx, userID, profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 2 || len(meta) != 2 {
		t.Fatalf("clusters: full %d, meta %d; want 2 each", len(full), len(meta))
	}
	for i := range full {
		if len(full[i].Embedding) == 0 || meta[i].Embedding != nil {
			t.Fatalf("cluster %d: full embedding %d dims, meta embedding %v; want only the full read to carry it", i, len(full[i].Embedding), meta[i].Embedding)
		}
		full[i].Embedding = nil
		if fmt.Sprint(full[i]) != fmt.Sprint(meta[i]) {
			t.Fatalf("cluster %d: meta %+v, want %+v", i, meta[i], full[i])
		}
	}
}

// The watched set walks the user store by keyset across pages: every row
// at least half watched counts, episodes for their series.
func TestWatchedItemIDSetAcrossPagesPostgres(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	prefix := "twatched-keyset-" + uuid.NewString()[:8] + "-"
	cleanupRecoMediaItems(t, pool, prefix)
	userID, profile := newTasteTestAccount(t, pool, prefix)
	series := prefix + "series"
	seedRecoMediaItem(t, pool, series, "series", "matched")
	episode := prefix + "episode"
	if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Pilot')`, episode, series); err != nil {
		t.Fatal(err)
	}

	// 2,500 rows, ten per second so page edges fall inside updated_at ties:
	// every third row completed, every third half watched, the rest barely
	// started.
	const rows = 2500
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
		SELECT $1, $2, $3 || lpad(i::text, 5, '0'),
		       CASE i % 3 WHEN 1 THEN 60 WHEN 2 THEN 10 ELSE 0 END,
		       100,
		       i % 3 = 0,
		       TIMESTAMPTZ '2026-08-10 00:00:00Z' - (i / 10) * INTERVAL '1 second'
		FROM generate_series(0, $4 - 1) AS i`, userID, profile, prefix+"item-", rows); err != nil {
		t.Fatal(err)
	}
	seedCompletion(t, pool, userID, profile, episode, time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC))

	got, err := NewSignalReader(NewRepo(pool), pgstore.NewPostgresProvider(pool)).WatchedItemIDSet(ctx, userID, profile)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{series: {}}
	for i := range rows {
		if i%3 != 2 {
			want[fmt.Sprintf("%sitem-%05d", prefix, i)] = struct{}{}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("watched set has %d items, want %d", len(got), len(want))
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Fatalf("watched set misses %s", id)
		}
	}
}
