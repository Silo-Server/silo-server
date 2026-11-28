package catalog

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
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

// TestSupersededPastCompletedHistoryCapPostgres pins #5090: an episode left in
// progress long ago, then superseded by a later episode completed before more
// than supersededProgressMaxRows newer completions, still leaves Continue
// Watching. The old newest-first walk stopped at the row cap and kept it.
func TestSupersededPastCompletedHistoryCapPostgres(t *testing.T) {
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
	const prefix = "cwcap-5090"
	cleanup := func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE profile_id = $1`, prefix+"-profile")
		_, _ = pool.Exec(bg, `DELETE FROM user_profiles WHERE id = $1`, prefix+"-profile")
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE username = $1`, prefix)
		_, _ = pool.Exec(bg, `DELETE FROM episodes WHERE content_id LIKE 'cwcap-%'`)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id LIKE 'cwcap-%'`)
	}
	cleanup()
	t.Cleanup(cleanup)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	profileID := prefix + "-profile"
	if _, err := pool.Exec(ctx, `
 INSERT INTO media_items(content_id,type,title) VALUES ('cwcap-series','series','Series'), ('cwcap-other','series','Other');
 INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES
 ('cwcap-e1','cwcap-series',1,1,'One'),
 ('cwcap-e2','cwcap-series',1,2,'Two'),
 ('cwcap-o1','cwcap-other',1,1,'Other one'),
 ('cwcap-o2','cwcap-other',1,2,'Other two');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'cap')`, profileID, userID); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Date(2023, 9, 20, 12, 0, 0, 0, time.UTC)
	fill := supersededProgressMaxRows + 500
	if _, err := pool.Exec(ctx, `
 INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,completed,updated_at) VALUES
 ($1,$2,'cwcap-e1',600,false,$3),
 ($1,$2,'cwcap-e2',0,true,$3::timestamptz + interval '1 day'),
 ($1,$2,'cwcap-o2',600,false,$3::timestamptz + interval '2 days'),
 ($1,$2,'cwcap-o1',0,true,$3::timestamptz + interval '3 days')`, userID, profileID, startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
 INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,completed,updated_at)
 SELECT $1,$2,'cwcap-fill-'||n,0,true,$3::timestamptz + interval '10 days' + n * interval '1 minute'
 FROM generate_series(1,$4::int) AS n;`, userID, profileID, startedAt, fill); err != nil {
		t.Fatal(err)
	}

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	inProgress, err := store.ListProgress(ctx, profileID, "in_progress", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inProgress) != 2 {
		t.Fatalf("in-progress rows = %v, want cwcap-o2 and cwcap-e1", inProgress)
	}
	counting := &countingItemsLister{UserStore: store}
	cache := NewCompletedProgressCache()
	filter := NewContinueWatchingProgressFilter(pool)
	superseded, err := filter.SupersededEpisodeProgressIDsCached(ctx, counting, profileID, inProgress, cache)
	if err != nil {
		t.Fatal(err)
	}
	// A second Continue Watching page with the same cache reads no item twice.
	read := counting.items
	again, err := filter.SupersededEpisodeProgressIDsCached(ctx, counting, profileID, inProgress[1:], cache)
	if err != nil {
		t.Fatal(err)
	}
	if counting.items != read {
		t.Fatalf("second page read %d more items, want 0", counting.items-read)
	}
	if _, ok := again["cwcap-e1"]; !ok || len(again) != 1 {
		t.Fatalf("second page superseded = %v, want only cwcap-e1", again)
	}
	// cwcap-e1 is superseded by cwcap-e2, finished a day later and followed by
	// more completions than the cap. cwcap-o2 is in progress after its earlier
	// episode was finished, so it stays.
	if _, ok := superseded["cwcap-e1"]; !ok || len(superseded) != 1 {
		t.Fatalf("superseded = %v, want only cwcap-e1", superseded)
	}
}

type countingItemsLister struct {
	userstore.UserStore
	items int
}

func (c *countingItemsLister) ListProgressByMediaItems(ctx context.Context, profileID string, ids []string) (map[string]userstore.WatchProgress, error) {
	c.items += len(ids)
	return c.UserStore.ListProgressByMediaItems(ctx, profileID, ids)
}
