package recommendations

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
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

	existing, err := repo.RecommendableItemIDs(ctx, []string{movieA, movieC, episodes[0], series})
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
	if _, err := w.doCowatch(ctx, fakeClusterLock{}); err != nil {
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
	if _, err := w.doCowatch(ctx, fakeClusterLock{}); err != nil {
		t.Fatal(err)
	}
	got = cowatchPairsFor(t, pool, prefix)
	if len(got) != 2 || got[x+">"+y] != 1 || got[y+">"+x] != 1 {
		t.Fatalf("pairs after the second run = %v, want only x and y paired", got)
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

// The watched set is the same read with one query, as on the Postgres user
// store, or walked from the store by keyset across pages, as on a store
// outside Postgres: every title completed or at least half watched, episodes
// for their series, and every book read at least half way, leaving out rows
// hidden from the history since their last progress and other profiles' rows.
func TestWatchedItemIDSetQueryMatchesStoreWalkPostgres(t *testing.T) {
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
	at := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	seedCompletion(t, pool, userID, profile, episode, at)
	progress := func(profile, itemID string, position, duration float64, updatedAt time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_watch_progress(user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
			VALUES ((SELECT user_id FROM user_profiles WHERE id = $1), $1, $2, $3, $4, $5)`, profile, itemID, position, duration, updatedAt); err != nil {
			t.Fatal(err)
		}
	}
	hide := func(itemID string, hiddenBefore time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items(user_id, profile_id, media_item_id, hidden_before) VALUES ($1, $2, $3, $4)`, userID, profile, itemID, hiddenBefore); err != nil {
			t.Fatal(err)
		}
	}
	progress(profile, prefix+"half", 50, 100, at)
	progress(profile, prefix+"under-half", 49.99, 100, at)
	progress(profile, prefix+"no-duration", 50, 0, at)
	seedCompletion(t, pool, userID, profile, prefix+"hidden", at)
	hide(prefix+"hidden", at.Add(time.Hour))
	seedCompletion(t, pool, userID, profile, prefix+"rewatched", at.Add(2*time.Hour))
	hide(prefix+"rewatched", at.Add(time.Hour))
	sibling := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id, user_id, name) VALUES ($1, $2, 'sibling')`, sibling, userID); err != nil {
		t.Fatal(err)
	}
	seedCompletion(t, pool, userID, sibling, prefix+"sibling", at)
	// The sibling hiding a title the profile watched leaves it in the profile's set.
	if _, err := pool.Exec(ctx, `INSERT INTO user_history_hidden_items(user_id, profile_id, media_item_id, hidden_before) VALUES ($1, $2, $3, $4)`, userID, sibling, prefix+"item-00000", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	otherUser, otherProfile := newTasteTestAccount(t, pool, prefix+"other")
	seedCompletion(t, pool, otherUser, otherProfile, prefix+"other-account", at)

	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type, name) VALUES ('ebooks', $1) RETURNING id`, prefix+"books").Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	book := func(itemID string, read float64) {
		t.Helper()
		seedRecoMediaItem(t, pool, itemID, "ebook", "matched")
		if _, err := pool.Exec(ctx, `
			WITH file AS (INSERT INTO media_files(media_folder_id, file_path, content_id) VALUES ($1, $2, $3) RETURNING id)
			INSERT INTO ebook_reader_progress(user_id, profile_id, content_id, file_id, location, progress, updated_at)
			SELECT $4, $5, $3, id, 'loc', $6, $7 FROM file`, folderID, "/"+itemID+".epub", itemID, userID, profile, read, at); err != nil {
			t.Fatal(err)
		}
	}
	book(prefix+"book-half", 0.5)
	book(prefix+"book-under-half", 0.49)
	book(prefix+"book-hidden", 0.95)
	hide(prefix+"book-hidden", at.Add(time.Hour))

	want := map[string]struct{}{series: {}, prefix + "half": {}, prefix + "rewatched": {}, prefix + "book-half": {}}
	for i := range rows {
		if i%3 != 2 {
			want[fmt.Sprintf("%sitem-%05d", prefix, i)] = struct{}{}
		}
	}
	store := pgstore.NewPostgresProvider(pool)
	for name, reader := range map[string]*SignalReader{
		"query": NewSignalReader(NewRepo(pool), store),
		"walk":  NewSignalReader(NewRepo(pool), store).WithUserStoreOutsidePostgres(true),
	} {
		got, err := reader.WatchedItemIDSet(ctx, userID, profile)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got, want) {
			t.Fatalf("%s: watched set has %d items, want %d; extra %v, missing %v", name, len(got), len(want), setDifference(got, want), setDifference(want, got))
		}
	}
}

// setDifference lists the keys of a missing from b, sorted.
func setDifference(a, b map[string]struct{}) []string {
	var out []string
	for id := range a {
		if _, ok := b[id]; !ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}
