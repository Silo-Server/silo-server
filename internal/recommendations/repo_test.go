package recommendations

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The picker's first page leads with every genre's best candidate, then each
// genre's second, each round in rank order, rather than the top genre's run.
func TestInterleaveByGenreTakesEachGenresBestFirst(t *testing.T) {
	ranked := []tasteSeedCandidate{
		{"drama-1", "Drama"}, {"drama-2", "Drama"}, {"drama-3", "Drama"},
		{"horror-1", "Horror"}, {"drama-4", "Drama"}, {"untagged-1", ""}, {"horror-2", "Horror"},
	}
	got := interleaveByGenre(ranked)
	want := []string{"drama-1", "horror-1", "untagged-1", "drama-2", "horror-2", "drama-3", "drama-4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved = %v, want %v", got, want)
	}
}

// seedRecoMediaItem inserts a minimal embed-eligible media item whose title is
// its content_id. Delegates to seedRecoMediaItemTitled so both the cheap-query
// and EmbedAll tests seed rows that survive ItemRepository.GetByIDs.
func seedRecoMediaItem(t *testing.T, pool *pgxpool.Pool, contentID, mediaType, status string) {
	t.Helper()
	seedRecoMediaItemTitled(t, pool, contentID, mediaType, status, contentID)
}

// seedRecoEmbedding inserts a zero-vector embedding row with an explicit model
// and canonical_text. The vector width matches the migrated column (3072).
func seedRecoEmbedding(t *testing.T, pool *pgxpool.Pool, contentID, model, canonicalText string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text)
		VALUES (
			$1,
			(SELECT array_agg(0.0::real) FROM generate_series(1, 3072))::vector,
			$2,
			$3
		)
	`, contentID, model, canonicalText); err != nil {
		t.Fatalf("seed embedding %s: %v", contentID, err)
	}
}

func cleanupRecoMediaItems(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		// media_item_embeddings and item_people rows cascade on media_items delete.
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
}

func TestRecentCompletedItemIDsGroupsSeriesBeforeLimit(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := t.Context()
	const prefix = "t612-recent-"
	const profile = "66100000-0000-4000-8000-000000000001"
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'anchor test')`, profile, userID); err != nil {
		t.Fatal(err)
	}
	cleanupRecoMediaItems(t, pool, prefix)
	seedRecoMediaItem(t, pool, prefix+"series", "series", "matched")
	seedRecoMediaItem(t, pool, prefix+"movie-a", "movie", "matched")
	seedRecoMediaItem(t, pool, prefix+"movie-b", "movie", "matched")
	for i, id := range []string{prefix + "episode-1", prefix + "episode-2"} {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,'Episode')`, id, prefix+"series", i+1); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{prefix + "episode-1", prefix + "episode-2", prefix + "movie-b", prefix + "movie-a"} {
		// The movies tie; the final order must use canonical ID ascending.
		age := min(i, 2)
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed,updated_at) VALUES($1,$2,$3,true,TIMESTAMPTZ '2026-08-10 12:00:00Z' - $4 * INTERVAL '1 second')`, userID, profile, id, age); err != nil {
			t.Fatal(err)
		}
	}
	got, err := NewRepo(pool).GetRecentCompletedItemIDs(ctx, userID, profile, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{prefix + "series", prefix + "movie-a", prefix + "movie-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recent completed = %v, want %v", got, want)
	}
}
