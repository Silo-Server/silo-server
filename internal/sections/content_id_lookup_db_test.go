package sections

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// Rows look titles and episodes up by content ID with one statement for every
// list length, so a new length never prepares new SQL, and a list longer than
// the 65,535 parameters Postgres binds still answers: a personal collection
// rail sends every member.
func TestContentIDLookupsKeepOneStatementPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &nextUpStatementTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := "id-lookup-" + uuid.NewString()[:8] + "-"
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix+"lib").Scan(&library); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, library)
	})
	// The second movie and episode are in the library but never asked for.
	movie, series, episode := prefix+"movie", prefix+"series", prefix+"episode"
	for _, item := range [][2]string{{movie, "movie"}, {prefix + "other-movie", "movie"}, {series, "series"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status) VALUES ($1, $2, $1, 'matched')`, item[0], item[1]); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, item[0], library, time.Now()); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	for i, id := range []string{episode, prefix + "other-episode"} {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview, runtime, rating_imdb) VALUES ($1, $2, 1, $3, 'Episode', '', 0, 0)`, id, series, i+1); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
	}

	fetcher := NewFetcher(pool)
	statements := map[string][]string{}
	for _, missing := range []int{0, 70_000} {
		ids := []string{movie, episode}
		for i := range missing {
			ids = append(ids, fmt.Sprintf("%smissing-%05d", prefix, i))
		}
		tracer.take()
		items, err := fetcher.fetchItemsByContentIDs(ctx, ids, &library, nil, catalog.AccessFilter{})
		if err != nil || !slices.Equal(contentIDs(items), []string{movie}) {
			t.Fatalf("items by %d IDs = %v, %v; want the movie", len(ids), contentIDs(items), err)
		}
		statements["items"] = append(statements["items"], tracer.take()...)
		episodes, _, err := fetcher.fetchEpisodeTargetsByContentIDs(ctx, ids, &library, nil, catalog.AccessFilter{})
		if err != nil || !slices.Equal(contentIDs(episodes), []string{episode}) {
			t.Fatalf("episodes by %d IDs = %v, %v; want the episode", len(ids), contentIDs(episodes), err)
		}
		statements["episodes"] = append(statements["episodes"], tracer.take()...)
	}
	for lookup, sql := range statements {
		if len(sql) != 2 || sql[0] != sql[1] {
			t.Errorf("%s lookups ran %q; want one statement, the same for 2 and 70,002 IDs", lookup, sql)
		}
	}
}
