package catalog

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ListNewest must return exactly the head of the full UniqueTargets ordering,
// whether a bounded window answers or it falls back to the full query.
func TestRecentTVListNewestMatchesFullQuery(t *testing.T) {
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

	prefix := fmt.Sprintf("recent-tv-newest-%d", time.Now().UnixNano())
	id := func(format string, args ...any) string { return prefix + "-" + fmt.Sprintf(format, args...) }
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folderA, folderB int
	for _, folder := range []*int{&folderA, &folderB} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, id("folder-%p", folder)).Scan(folder); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{folderA, folderB})
	})

	// 60 shows with bursts, gap-chained trickles, and lone arrivals; some
	// episodes also land in a second folder, some files are missing, and a few
	// shows have no available episode at all.
	rng := rand.New(rand.NewPCG(1588, 2))
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var seriesIDs []string
	for s := range 60 {
		seriesID := id("s%02d", s)
		seriesIDs = append(seriesIDs, seriesID)
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', $2, 'matched', '{}')`, seriesID, fmt.Sprintf("Show %02d", s))
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, seriesID, folderA, base.Add(time.Duration(rng.IntN(90*24))*time.Hour))
		if s%15 == 7 {
			continue // no episodes: a bare series event
		}
		at := base.Add(time.Duration(rng.IntN(90*24)) * time.Hour)
		for e := range 4 + rng.IntN(20) {
			switch rng.IntN(4) {
			case 0:
				at = at.Add(time.Duration(3+rng.IntN(72)) * time.Hour) // new event
			case 1:
				at = at.Add(time.Duration(60+rng.IntN(60)) * time.Minute) // chained trickle
			default:
				at = at.Add(time.Duration(1+rng.IntN(10)) * time.Minute) // burst
			}
			episodeID := id("s%02d-e%02d", s, e)
			exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, $3, $4, 'Episode')`, episodeID, seriesID, 1+e/10, e)
			folders := []int{folderA}
			if rng.IntN(10) == 0 {
				folders = append(folders, folderB)
			}
			for i, folder := range folders {
				exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, episodeID, folder, at.Add(time.Duration(i)*time.Minute))
				missing := rng.IntN(12) == 0
				exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $3, CASE WHEN $4 THEN now() ELSE NULL END)`,
					episodeID, folder, fmt.Sprintf("/%s/%d", episodeID, folder), missing)
			}
		}
	}

	repo := NewRecentTVRepository(pool)
	accesses := map[string]AccessFilter{
		"open":       {},
		"restricted": {AllowedContentIDs: seriesIDs[:40]},
	}
	for name, access := range accesses {
		for _, folders := range [][]int{{folderA}, {folderA, folderB}} {
			full, _, _, err := repo.List(ctx, RecentTVQuery{LibraryIDs: folders, Access: access, Limit: 1000, UniqueTargets: true, SkipTotal: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, limit := range []int{1, 3, 8, 20, 200} {
				t.Run(fmt.Sprintf("%s/folders=%d/limit=%d", name, len(folders), limit), func(t *testing.T) {
					q := RecentTVQuery{LibraryIDs: folders, Access: access, Limit: limit}
					got, hasMore, err := repo.ListNewest(ctx, q)
					if err != nil {
						t.Fatal(err)
					}
					want := full[:min(limit, len(full))]
					sameEvents := len(got) == len(want)
					for i := range got {
						sameEvents = sameEvents && got[i].EventID == want[i].EventID
					}
					if !equalRecentTVTargets(got, want) || !sameEvents || hasMore != (len(full) > limit) {
						t.Fatalf("ListNewest = %+v (more %v)\nwant %+v (more %v)", got, hasMore, want, len(full) > limit)
					}
				})
			}
		}
	}

	// Small limits must be answered by a bounded window, not the fallback.
	if _, found, err := repo.listNewestBounded(ctx, RecentTVQuery{LibraryIDs: []int{folderA}, Limit: 3}); err != nil || !found {
		t.Fatalf("bounded window did not answer: found %v, err %v", found, err)
	}
}

// A bounded window's cutoff can fall inside an event. The event must still be
// grouped from the show's full history: here X1 arrives before the cutoff and
// X2 after it, and together they are one series card.
func TestRecentTVListNewestGroupsEventsAcrossCutoff(t *testing.T) {
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

	prefix := fmt.Sprintf("recent-tv-cutoff-%d", time.Now().UnixNano())
	id := func(format string, args ...any) string { return prefix + "-" + fmt.Sprintf(format, args...) }
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	addEpisode := func(series, episode string, number int, at time.Time) {
		t.Helper()
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', 'Cutoff Show', 'matched', '{}') ON CONFLICT DO NOTHING`, series)
		exec(`INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, series, folder, at)
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, $3, 'Episode')`, episode, series, number)
		exec(`INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, episode, folder, at)
		exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path) VALUES ($1, $2, $3)`, episode, folder, "/"+episode)
	}

	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seriesX := id("x")
	addEpisode(seriesX, id("x1"), 1, now.Add(-100*time.Minute))
	addEpisode(seriesX, id("x2"), 2, now)
	// Eight lone arrivals between X1 and X2 put the limit-1 cutoff (the 8th
	// newest row) after X1.
	for i := range 8 {
		addEpisode(id("y%d", i), id("y%d-1", i), 1, now.Add(-time.Duration(10+10*i)*time.Minute))
	}

	repo := NewRecentTVRepository(pool)
	q := RecentTVQuery{LibraryIDs: []int{folder}, Limit: 1}
	bounded, found, err := repo.listNewestBounded(ctx, q)
	if err != nil || !found {
		t.Fatalf("bounded window did not answer: found %v, err %v", found, err)
	}
	if bounded[0].ContentID != seriesX || bounded[0].Type != recentTVTypeSeries {
		t.Fatalf("newest target = %+v, want the %s series event", bounded[0], seriesX)
	}
	full, _, _, err := repo.List(ctx, RecentTVQuery{LibraryIDs: []int{folder}, Limit: 1, UniqueTargets: true, SkipTotal: true})
	if err != nil || len(full) != 1 || full[0].EventID != bounded[0].EventID {
		t.Fatalf("full = %+v, err %v; bounded event %q", full, err, bounded[0].EventID)
	}
}
