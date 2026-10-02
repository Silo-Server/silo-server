package handlers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEpisodeCompletionUserStates(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("state-episodes-%d", time.Now().UnixNano())
	series, season, empty, episode1, episode2, orphan := prefix+"-series", prefix+"-season", prefix+"-empty", prefix+"-ep1", prefix+"-ep2", prefix+"-orphan"
	var folderID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, []string{series, empty})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folderID)
	})
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Series'),($2,'series','Empty')`, []any{series, empty}},
		{`INSERT INTO seasons (content_id,series_id,season_number) VALUES ($1,$2,1)`, []any{season, series}},
		{`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title)
    VALUES ($1,$4,$5,1,1,'One'),($2,$4,$5,1,2,'Two'),($3,$4,$5,1,3,'Unavailable')`, []any{episode1, episode2, orphan, series, season}},
		{`INSERT INTO episode_libraries (episode_id,media_folder_id) VALUES ($1,$3),($2,$3)`, []any{episode1, episode2, folderID}},
	} {
		if _, err := pool.Exec(t.Context(), stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		checkEpisodeCompletionUserStates(t, pool, newProfileTestStore(t), "profile-1", "other-profile", series, season, empty, episode1, episode2)
	})
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	otherProfileID := fmt.Sprintf("00000000-0000-4000-8001-%012d", time.Now().UnixNano()%1_000_000_000_000)
	for _, id := range []string{profileID, otherProfileID} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Completion fixture')`, id, userID); err != nil {
			t.Fatal(err)
		}
	}
	store, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("postgres", func(t *testing.T) {
		checkEpisodeCompletionUserStates(t, pool, store, profileID, otherProfileID, series, season, empty, episode1, episode2)
	})
}

// Hiding optional completion support exercises the original ID/progress path
// against exactly the same store and catalog after each watch-state change.
type completionFallbackStore struct{ userstore.UserStore }

type failingCompletionStore struct{ userstore.UserStore }

func (s failingCompletionStore) SeriesCompletion(context.Context, string, []string) (map[string]bool, error) {
	return nil, errors.New("completion query unavailable")
}

func (s failingCompletionStore) SeasonCompletion(context.Context, string, []string) (map[string]bool, error) {
	return nil, errors.New("completion query unavailable")
}

func checkEpisodeCompletionUserStates(t *testing.T, pool *pgxpool.Pool, store userstore.UserStore, profileID, otherProfileID, series, season, empty, episode1, episode2 string) {
	t.Helper()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.SetProgressAt(t.Context(), profileID, episode1, 100, 100, true, at); err != nil {
		t.Fatal(err)
	}
	if err := store.AddFavorite(t.Context(), profileID, series); err != nil {
		t.Fatal(err)
	}
	if err := store.AddToWatchlist(t.Context(), profileID, season); err != nil {
		t.Fatal(err)
	}
	items := []*models.MediaItem{nil, {ContentID: series, Type: "series"}, {ContentID: season, Type: "season"}, {ContentID: empty, Type: "series"}, {ContentID: episode1, Type: "episode"}, {ContentID: series, Type: "series"}}
	check := func(wantCompleted bool) {
		t.Helper()
		states, err := resolveItemUserStates(t.Context(), store, profileID, catalog.NewEpisodeRepository(pool), items)
		if err != nil {
			t.Fatal(err)
		}
		original, err := resolveItemUserStates(t.Context(), completionFallbackStore{store}, profileID, catalog.NewEpisodeRepository(pool), items)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(states, original) {
			t.Fatalf("completion optimization changed user states: got %#v, want %#v", states, original)
		}
		failed, err := resolveItemUserStates(t.Context(), failingCompletionStore{store}, profileID, catalog.NewEpisodeRepository(pool), items)
		if err != nil || !reflect.DeepEqual(failed, original) {
			t.Fatalf("completion query failure changed fallback: %v", err)
		}
		if completionStore, ok := store.(userstore.EpisodeParentCompletionStore); ok {
			// Call the capability directly as well: a SQL error must not let the
			// handler's fallback hide a broken optimized query from this test.
			seriesFlags, err := completionStore.SeriesCompletion(t.Context(), profileID, []string{series, empty, series, "unknown"})
			if err != nil || len(seriesFlags) != 3 || seriesFlags[series] != wantCompleted || seriesFlags[empty] || seriesFlags["unknown"] {
				t.Fatalf("series completion flags = %v, error = %v", seriesFlags, err)
			}
			seasonFlags, err := completionStore.SeasonCompletion(t.Context(), profileID, []string{season, "unknown"})
			if err != nil || len(seasonFlags) != 2 || seasonFlags[season] != wantCompleted || seasonFlags["unknown"] {
				t.Fatalf("season completion flags = %v, error = %v", seasonFlags, err)
			}
			for _, load := range []func(context.Context, string, []string) (map[string]bool, error){completionStore.SeriesCompletion, completionStore.SeasonCompletion} {
				flags, err := load(t.Context(), profileID, nil)
				if err != nil || len(flags) != 0 {
					t.Fatalf("empty completion = %v, error = %v", flags, err)
				}
			}
		}
		if len(states) != 4 {
			t.Fatalf("got %d states, want 4", len(states))
		}
		if states[series].Played != wantCompleted || states[season].Played != wantCompleted {
			t.Fatalf("series/season completion = %v/%v, want %v", states[series].Played, states[season].Played, wantCompleted)
		}
		if states[empty].Played || !states[episode1].Played {
			t.Fatal("empty-series or leaf completion changed")
		}
		if !states[series].IsFavorite || !states[season].InWatchlist {
			t.Fatal("favorites/watchlist state changed")
		}
	}
	check(false)
	if err := store.AddHistory(t.Context(), userstore.WatchHistoryEntry{ProfileID: profileID, MediaItemID: episode2, WatchedAt: at.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceTrakt}); err != nil {
		t.Fatal(err)
	}
	check(true)
	if err := store.RemoveHistoryItems(t.Context(), profileID, []string{episode2}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := store.SetProgressAt(t.Context(), profileID, episode2, 100, 100, true, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(true)
	// Membership changes must take effect immediately, including an episode
	// with no media-file row. Completion uses membership, not file presence.
	if _, err := pool.Exec(t.Context(), `INSERT INTO episode_libraries (episode_id,media_folder_id)
		SELECT content_id, (SELECT media_folder_id FROM episode_libraries WHERE episode_id=$2 LIMIT 1)
		FROM episodes WHERE series_id=$1 AND episode_number=3`, series, episode1); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err := pool.Exec(t.Context(), `DELETE FROM episode_libraries WHERE episode_id IN
		(SELECT content_id FROM episodes WHERE series_id=$1 AND episode_number=3)`, series); err != nil {
		t.Fatal(err)
	}
	check(true)
	other, err := resolveItemUserStates(t.Context(), store, otherProfileID, catalog.NewEpisodeRepository(pool), items)
	if err != nil {
		t.Fatal(err)
	}
	if other[series].Played || other[series].IsFavorite || other[season].InWatchlist {
		t.Fatal("state leaked across profiles")
	}
}
