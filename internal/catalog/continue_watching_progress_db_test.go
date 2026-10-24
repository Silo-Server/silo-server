package catalog

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestSupersededCutoffIgnoresInProgressMoviesPostgres pins that only episodes
// set the completed-walk cutoff: a movie left unfinished long ago can be
// superseded by nothing, and letting it pull the cutoff back made every
// Continue Watching load read the profile's whole recent completed history.
func TestSupersededCutoffIgnoresInProgressMoviesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id LIKE 'cwcut-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE 'cwcut-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, `
 INSERT INTO media_items(content_id,type,title) VALUES
 ('cwcut-movie','movie','Movie'),
 ('cwcut-series','series','Series');
 INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES
 ('cwcut-e1','cwcut-series',1,1,'One'),
 ('cwcut-e2','cwcut-series',1,2,'Two');`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	episodeAt := now.Add(-24 * time.Hour)
	entries := []userstore.WatchProgress{
		{MediaItemID: "cwcut-e1", UpdatedAt: episodeAt.Format(time.RFC3339)},
		{MediaItemID: "cwcut-movie", UpdatedAt: now.Add(-100 * 24 * time.Hour).Format(time.RFC3339)},
	}
	store := &stubSinceLister{stubProgressLister: stubProgressLister{entries: []userstore.WatchProgress{
		{MediaItemID: "cwcut-e2", UpdatedAt: now.Format(time.RFC3339)},
	}}}

	superseded, err := NewContinueWatchingProgressFilter(pool).SupersededEpisodeProgressIDs(ctx, store, "p1", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(store.sinceAt) != 1 || !store.sinceAt[0].Equal(episodeAt) {
		t.Fatalf("completed walk cutoffs = %v, want the episode's %v", store.sinceAt, episodeAt)
	}
	if _, ok := superseded["cwcut-e1"]; !ok || len(superseded) != 1 {
		t.Fatalf("superseded = %v, want only cwcut-e1", superseded)
	}

	// With only a movie in progress there is nothing to supersede and no walk.
	store.sinceAt = nil
	superseded, err = NewContinueWatchingProgressFilter(pool).SupersededEpisodeProgressIDs(ctx, store, "p1", entries[1:])
	if err != nil {
		t.Fatal(err)
	}
	if len(superseded) != 0 || len(store.sinceAt) != 0 {
		t.Fatalf("movie-only: superseded = %v, walks = %v; want none", superseded, store.sinceAt)
	}
}

// TestSupersededSpecialsNeedALaterSpecialPostgres pins that specials, which
// have no airing position, leave Continue Watching only for a later special:
// finishing a regular episode afterwards keeps a half-watched special.
func TestSupersededSpecialsNeedALaterSpecialPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cleanup := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id LIKE 'cwspecial-%'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE 'cwspecial-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := pool.Exec(ctx, `
 INSERT INTO media_items(content_id,type,title) VALUES ('cwspecial-series','series','Series');
 INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES
 ('cwspecial-s5','cwspecial-series',0,5,'Special Five'),
 ('cwspecial-s6','cwspecial-series',0,6,'Special Six'),
 ('cwspecial-e1','cwspecial-series',1,1,'One');`); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Hour).Format(time.RFC3339)
	inProgress := []userstore.WatchProgress{{MediaItemID: "cwspecial-s5", UpdatedAt: startedAt.Format(time.RFC3339)}}
	filter := NewContinueWatchingProgressFilter(pool)

	for _, tc := range []struct {
		name, completed string
		want            int
	}{
		{"regular episode finished later", "cwspecial-e1", 0},
		{"later special finished later", "cwspecial-s6", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &stubSinceLister{stubProgressLister: stubProgressLister{entries: []userstore.WatchProgress{
				{MediaItemID: tc.completed, UpdatedAt: finishedAt},
			}}}
			superseded, err := filter.SupersededEpisodeProgressIDs(ctx, store, "p1", inProgress)
			if err != nil {
				t.Fatal(err)
			}
			if len(superseded) != tc.want {
				t.Fatalf("superseded = %v, want %d entries", superseded, tc.want)
			}
		})
	}
}
