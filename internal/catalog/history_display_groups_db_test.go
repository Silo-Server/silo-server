package catalog

import (
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHistoryDisplayGroupsIncludeSiblingEpisodes(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("hes-series-%d", suffix)
	ep1 := fmt.Sprintf("hes-ep1-%d", suffix)
	ep2 := fmt.Sprintf("hes-ep2-%d", suffix)

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', 'HES Test', true) RETURNING id`,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("hes-user-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", suffix%1_000_000_000_000)
	if _, err := pool.Exec(ctx,
		`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'HES Profile')`,
		profileID, userID,
	); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_history WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		// episodes and episode_libraries cascade from the series row.
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'HES Series', 'matched', '{}'::text[])`,
		seriesID,
	); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for i, epID := range []string{ep1, ep2} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, $3, 'HES Ep')`,
			epID, seriesID, i+1,
		); err != nil {
			t.Fatalf("seed episode %s: %v", epID, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			VALUES ($1, $2, NOW())`,
			epID, folderID,
		); err != nil {
			t.Fatalf("seed episode library %s: %v", epID, err)
		}
	}

	entries := []userstore.WatchHistoryEntry{
		{ID: "newer", MediaItemID: ep2}, {ID: "older", MediaItemID: ep1}, {ID: "movie", MediaItemID: "movie-test"},
	}
	display, err := ResolveHistoryDisplayEntries(ctx, entries, NewEpisodeRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	if len(display) != 2 || display[0].DisplayID != seriesID || display[0].Entry.ID != "newer" {
		t.Fatalf("display = %+v", display)
	}
	groups, err := HistoryDisplayGroups(ctx, display, NewEpisodeRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	members := groups[seriesID]
	slices.Sort(members)
	want := []string{seriesID, ep1, ep2}
	slices.Sort(want)
	if !slices.Equal(members, want) {
		t.Fatalf("series members = %v, want %v", members, want)
	}
	if !slices.Equal(groups["movie-test"], []string{"movie-test"}) {
		t.Fatalf("movie members = %v", groups["movie-test"])
	}
}
