package sections

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestReturningShowsDB pins which series Returning Shows surfaces: a season
// newer than any the profile watched, added inside the lookback window, with a
// present file, newest arrival first. It forces the generic plan Postgres
// caches for a statement a connection keeps running, the plan that made this
// query slow.
func TestReturningShowsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	config.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	id := func(name string) string { return fmt.Sprintf("returning-%s-%d", name, suffix) }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, sql)
		}
	}

	var folderID, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, id("folder")).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`, id("user"), id("user")+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profileID, otherProfileID := id("profile"), id("other-profile")

	// Each series has S1E1 and S2E1. watched is the season this profile
	// watched (0 for none), newAge is how long ago S2E1 was added.
	newest, older := id("newest"), id("older")
	caughtUp, noFile, stale, otherProfile, unwatched := id("caught-up"), id("no-file"), id("stale"), id("other-profile-series"), id("unwatched")
	series := []struct {
		id         string
		watched    int
		watchedBy  string
		newAge     time.Duration
		missingNew bool
	}{
		{newest, 1, profileID, time.Hour, false},
		{older, 1, profileID, 48 * time.Hour, false},
		{caughtUp, 2, profileID, time.Hour, false},
		{noFile, 1, profileID, time.Hour, true},
		{stale, 1, profileID, 60 * 24 * time.Hour, false},
		{otherProfile, 1, otherProfileID, time.Hour, false},
		{unwatched, 0, "", time.Hour, false},
	}
	var seriesIDs []string
	for _, s := range series {
		seriesIDs = append(seriesIDs, s.id)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = ANY($1)`, seriesIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, seriesIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, s := range series {
		s1, s2 := s.id+"-s1e1", s.id+"-s2e1"
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', $1, 'matched', '{}'::text[])`, s.id)
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb, created_at)
			VALUES ($1, $3, 1, 1, 'One', '', 30, 0, NOW() - interval '400 days'), ($2, $3, 2, 1, 'Two', '', 30, 0, NOW() - make_interval(secs => $4))`,
			s1, s2, s.id, s.newAge.Seconds())
		exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path, duration) VALUES ($1, $2, '/returning/' || $1, 1800)`, s1, folderID)
		if s.missingNew {
			exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path, duration, missing_since) VALUES ($1, $2, '/returning/' || $1, 1800, NOW())`, s2, folderID)
		} else {
			exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path, duration) VALUES ($1, $2, '/returning/' || $1, 1800)`, s2, folderID)
		}
		if s.watched > 0 {
			watchedEpisode := fmt.Sprintf("%s-s%de1", s.id, s.watched)
			exec(`INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at) VALUES ($1, $2, $3, $4, NOW() - interval '200 days')`,
				id("history-"+s.id), userID, s.watchedBy, watchedEpisode)
		}
	}
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`, seriesIDs, folderID)

	section := ResolvedSection{ID: id("section"), SectionType: SectionReturningShows, ItemLimit: 20}
	filter := catalog.AccessFilter{AllowedLibraryIDs: []int{folderID}}
	got, err := NewFetcher(pool).FetchOne(ctx, section, nil, []int{folderID}, userID, profileID, filter)
	if err != nil {
		t.Fatalf("FetchOne: %v", err)
	}
	if ids, want := contentIDs(got.Items), []string{newest, older}; strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("row = %v, want %v", ids, want)
	}
}
