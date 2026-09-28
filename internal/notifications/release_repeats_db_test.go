package notifications

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRepeatedFromOtherLibraries pins which events a server channel treats
// as a second copy: content another library made available before the event.
func TestRepeatedFromOtherLibraries(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	nonce := time.Now().UnixNano()
	prefix := fmt.Sprintf("repeat-%d", nonce)
	hd, uhd, deleted := 700000+int(nonce%90000), 700000+int(nonce%90000)+1, 700000+int(nonce%90000)+2
	series := prefix + "-series"
	earlier := time.Now().Add(-48 * time.Hour).UTC()
	later := time.Now().Add(-time.Hour).UTC()
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, table := range []string{"release_events", "episode_availability", "movie_availability", "item_availability"} {
			_, _ = pool.Exec(cleanup, `DELETE FROM `+table+` WHERE library_id = ANY($1)`, []int{hd, uhd, deleted})
		}
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{hd, uhd})
	})

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	addEpisode := func(library, episode int, at time.Time) {
		t.Helper()
		exec(`INSERT INTO episode_availability (library_id, episode_id, series_id, season_number, episode_number, episode_key, created_at)
			VALUES ($1, $2, $3, 1, $4, $5, $6)`,
			library, fmt.Sprintf("%s-e%d", prefix, episode), series, episode, EpisodeKey(1, episode), at)
	}
	episodeEvent := func(id string, library, episode int, at time.Time) {
		t.Helper()
		exec(`INSERT INTO release_events (id, library_id, kind, series_id, episode_id, season_number, episode_number, episode_key, available_at, dedupe_key, created_at)
			VALUES ($1, $2, 'episode', $3, $4, 1, $5, $6, $7, $8, $7)`,
			prefix+id, library, series, fmt.Sprintf("%s-e%d", prefix, episode), episode, EpisodeKey(1, episode), at,
			EpisodeDedupeKey(library, series, EpisodeKey(1, episode)))
	}
	movieEvent := func(id string, library int, item string, at time.Time) {
		t.Helper()
		exec(`INSERT INTO movie_availability (library_id, item_id, created_at) VALUES ($1, $2, $3)`, library, item, at)
		exec(`INSERT INTO release_events (id, library_id, kind, item_id, available_at, dedupe_key, created_at)
			VALUES ($1, $2, 'movie', $3, $4, $5, $4)`,
			prefix+id, library, item, at, ItemDedupeKey(EventKindMovie, library, item))
	}

	audiobookEvent := func(id string, library int, item string, at time.Time) {
		t.Helper()
		exec(`INSERT INTO item_availability (library_id, item_id, kind, created_at) VALUES ($1, $2, 'audiobook', $3)`, library, item, at)
		exec(`INSERT INTO release_events (id, library_id, kind, item_id, available_at, dedupe_key, created_at)
			VALUES ($1, $2, 'audiobook', $3, $4, $5, $4)`,
			prefix+id, library, item, at, ItemDedupeKey(EventKindAudiobook, library, item))
	}

	exec(`INSERT INTO media_folders (id, type, name) VALUES ($1, 'mixed', $3 || '-hd'), ($2, 'mixed', $3 || '-4k')`, hd, uhd, prefix)

	// E1 reached the HD library first; its 4K copy arrives later.
	addEpisode(hd, 1, earlier)
	episodeEvent("-e1-hd", hd, 1, earlier)
	addEpisode(uhd, 1, later)
	episodeEvent("-e1-4k", uhd, 1, later)
	// E2's earlier copy was in a library that has since been deleted; its
	// availability row remains but no longer counts.
	addEpisode(deleted, 2, earlier)
	addEpisode(uhd, 2, later)
	episodeEvent("-e2-4k", uhd, 2, later)
	// The movie's 4K copy came first; the HD copy is the repeat.
	movieEvent("-m-4k", uhd, prefix+"-movie", earlier)
	movieEvent("-m-hd", hd, prefix+"-movie", later)
	// Flat kinds other than movies use item_availability.
	audiobookEvent("-a-1", hd, prefix+"-book", earlier)
	audiobookEvent("-a-2", uhd, prefix+"-book", later)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	all := []string{prefix + "-e1-hd", prefix + "-e1-4k", prefix + "-e2-4k", prefix + "-m-4k", prefix + "-m-hd", prefix + "-a-1", prefix + "-a-2"}
	repeated, err := NewReleaseRepository(pool).RepeatedFromOtherLibraries(ctx, tx, all)
	if err != nil {
		t.Fatalf("RepeatedFromOtherLibraries: %v", err)
	}
	var got []string
	for id := range repeated {
		got = append(got, id)
	}
	slices.Sort(got)
	want := []string{prefix + "-a-2", prefix + "-e1-4k", prefix + "-m-hd"}
	if !slices.Equal(got, want) {
		t.Fatalf("repeated = %v, want %v", got, want)
	}

	// The sweep keeps the first copy of each title, in feed order.
	events := make([]ReleaseEvent, len(all))
	for i, id := range all {
		events[i] = ReleaseEvent{ID: id}
	}
	worker := &serverChannelWorker{releases: NewReleaseRepository(pool)}
	kept, err := worker.dropLibraryRepeats(ctx, tx, events)
	if err != nil {
		t.Fatalf("dropLibraryRepeats: %v", err)
	}
	var keptIDs []string
	for _, event := range kept {
		keptIDs = append(keptIDs, event.ID)
	}
	if want := []string{prefix + "-e1-hd", prefix + "-e2-4k", prefix + "-m-4k", prefix + "-a-1"}; !slices.Equal(keptIDs, want) {
		t.Fatalf("kept %v, want %v", keptIDs, want)
	}
}
