package sections

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// nextUpWalkTracer records the next-up walk statements a fetch issues.
type nextUpWalkTracer struct {
	mu    sync.Mutex
	walks []string
}

func (c *nextUpWalkTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "RECURSIVE walk") {
		c.mu.Lock()
		c.walks = append(c.walks, strings.Join(strings.Fields(data.SQL), " "))
		c.mu.Unlock()
	}
	return ctx
}

func (c *nextUpWalkTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *nextUpWalkTracer) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.walks
	c.walks = nil
	return out
}

// TestContinueWatchingCombinedRowKeepsNewestCardsDB pins the combined
// Continue Watching row: in-progress and next-up cards merge newest first and
// the row stops at its item limit. When the in-progress cards fill the row on
// their own, the next-up walk only reads series completed since the oldest of
// them, since anything older could not make the row. A profile showing new
// releases first is the exception: a series finished before all of them leads
// the row when its next episode was just released, so its walk is unbounded.
func TestContinueWatchingCombinedRowKeepsNewestCardsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &nextUpWalkTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	id := func(name string) string { return fmt.Sprintf("cw-combined-%s-%d", name, suffix) }
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
	profileID := id("profile")

	movies := []string{id("movie-1"), id("movie-2"), id("movie-3"), id("movie-4")}
	// rewatch has two episodes in progress; recent was completed between the
	// first and second movie; old was completed before every in-progress card;
	// returning was completed before them too, but its next episode was
	// released after all of them.
	rewatch, recent, old, returning := id("rewatch"), id("recent"), id("old"), id("returning")
	episode := func(series string, n int) string { return fmt.Sprintf("%s-e%d", series, n) }
	allItems := append([]string{rewatch, recent, old, returning}, movies...)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM episode_availability WHERE library_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = ANY($1)`, []string{rewatch, recent, old, returning})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, allItems)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	for _, movie := range movies {
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', '{}'::text[])`, movie)
	}
	for _, series := range []string{rewatch, recent, old, returning} {
		exec(`INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'series', $1, 'matched', '{}'::text[])`, series)
		exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb) VALUES ($1, $3, 1, 1, 'One', '', 30, 0), ($2, $3, 1, 2, 'Two', '', 30, 0)`,
			episode(series, 1), episode(series, 2), series)
		exec(`INSERT INTO media_files (episode_id, media_folder_id, file_path, duration) VALUES ($1, $3, '/cw-combined/' || $1, 1800), ($2, $3, '/cw-combined/' || $2, 1800)`,
			episode(series, 1), episode(series, 2), folderID)
	}
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) SELECT unnest($1::text[]), $2`, allItems, folderID)

	now := time.Now().UTC().Truncate(time.Second)
	progress := []struct {
		item      string
		completed bool
		at        time.Duration
	}{
		{episode(rewatch, 2), false, 30 * time.Minute},
		{movies[0], false, time.Hour},
		{episode(recent, 1), true, 75 * time.Minute},
		{episode(rewatch, 1), false, 90 * time.Minute},
		{movies[1], false, 2 * time.Hour},
		{movies[2], false, 3 * time.Hour},
		{movies[3], false, 4 * time.Hour},
		{episode(old, 1), true, 10 * time.Hour},
		{episode(returning, 1), true, 12 * time.Hour},
	}
	for _, p := range progress {
		position := 600.0
		if p.completed {
			position = 0
		}
		exec(`INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at) VALUES ($1, $2, $3, $4, 1800, $5, $6)`,
			userID, profileID, p.item, position, p.completed, now.Add(-p.at))
	}

	exec(`INSERT INTO episode_availability (library_id, episode_id, series_id, season_number, episode_number, episode_key, available_at, released_at)
		VALUES ($1, $2, $3, 1, 2, 1000002, $4, $4)`, folderID, episode(returning, 2), returning, now.Add(-20*time.Minute))

	provider := pgstore.NewPostgresProvider(pool)
	fetcher := NewFetcher(pool)
	fetcher.StoreProvider = provider
	fetcher.NextUpRepo = catalog.NewNextUpRepository(pool, provider)
	tests := []struct {
		name             string
		newReleasesFirst bool
		limit            int
		want             []string
		boundedWalk      bool
	}{
		{
			// The rewatch series counts once, so three in-progress cards fill
			// the row and the walk stops at the second movie.
			name:        "in-progress cards fill the row",
			limit:       3,
			want:        []string{episode(rewatch, 2), movies[0], episode(recent, 2)},
			boundedWalk: true,
		},
		{
			name:  "row short of its limit",
			limit: 10,
			want:  []string{episode(rewatch, 2), movies[0], episode(recent, 2), movies[1], movies[2], movies[3], episode(old, 2), episode(returning, 2)},
		},
		{
			name:             "new releases first, in-progress cards fill the row",
			newReleasesFirst: true,
			limit:            3,
			want:             []string{episode(returning, 2), episode(rewatch, 2), movies[0]},
		},
		{
			name:             "new releases first, row short of its limit",
			newReleasesFirst: true,
			limit:            10,
			want:             []string{episode(returning, 2), episode(rewatch, 2), movies[0], episode(recent, 2), movies[1], movies[2], movies[3], episode(old, 2)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viewerCtx := access.SetScope(ctx, access.Scope{
				UserID: userID, ProfileID: profileID,
				NextUpMode: NextUpModeCombined, NextUpNewReleasesFirst: tt.newReleasesFirst,
			})
			tracer.take()
			section := ResolvedSection{
				ID:          id("section"),
				SectionType: SectionContinueWatching,
				ItemLimit:   tt.limit,
				Config:      ContinueTypeConfig(ContinueTypeWatching),
			}
			got, err := fetcher.FetchOne(viewerCtx, section, nil, []int{folderID}, userID, profileID, catalog.AccessFilter{AllowedLibraryIDs: []int{folderID}})
			if err != nil {
				t.Fatalf("FetchOne: %v", err)
			}
			if ids := contentIDs(got.Items); strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("row = %v, want %v", ids, tt.want)
			}
			if got.TotalCount != len(tt.want) {
				t.Fatalf("total_count = %d, want %d", got.TotalCount, len(tt.want))
			}
			walks := tracer.take()
			if len(walks) == 0 {
				t.Fatal("fetch issued no next-up walk")
			}
			for _, walk := range walks {
				if bounded := strings.Contains(walk, "uwp.updated_at >= $"); bounded != tt.boundedWalk {
					t.Fatalf("next-up walk bounded = %v, want %v:\n%s", bounded, tt.boundedWalk, walk)
				}
			}
		})
	}
}
