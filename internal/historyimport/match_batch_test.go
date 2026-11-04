package historyimport

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type countingQuerier struct {
	q lookupQuerier
	n int
}

func (c *countingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	return c.q.Query(ctx, sql, args...) //nolint:rowserrcheck // the caller checks the rows
}

func (c *countingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n++
	return c.q.QueryRow(ctx, sql, args...)
}

// TestMatchAllAgreesWithMatchDB: MatchAllOn finds what Match finds for
// each record, by every lookup the matcher makes (a movie by its ids, an
// ambiguous one, a series, an episode by its own ids, by its series' ids
// and numbers, and by its series' match), and misses what Match misses,
// in a bounded number of reads whatever the number of records.
func TestMatchAllAgreesWithMatchDB(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	exec := func(sql string, args ...any) pgconn.CommandTag {
		t.Helper()
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		return tag
	}
	id := func(s string) string { return s + "-" + sfx }
	exec(`INSERT INTO media_items (content_id, type, title, year, tmdb_id, imdb_id, status) VALUES
		($1, 'movie', 'One', 2001, $2, $3, 'matched'), ($4, 'movie', 'Twin A', 2002, $5, '', 'matched'), ($6, 'movie', 'Twin B', 2002, $5, '', 'matched')`,
		id("movie"), id("tmdb-1"), id("tt-1"), id("twin-a"), id("tmdb-twin"), id("twin-b"))
	exec(`INSERT INTO media_items (content_id, type, title, year, tvdb_id, tmdb_id, status) VALUES ($1, 'series', 'Show', 2010, $2, $3, 'matched')`,
		id("series"), id("tvdb-s"), id("tmdb-s"))
	exec(`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, tvdb_id) VALUES
		($1, $2, 1, 1, 'Pilot', $3), ($4, $2, 1, 2, 'Second', '')`, id("ep1"), id("series"), id("tvdb-e1"), id("ep2"))

	records := []Record{
		{Kind: KindMovie, TMDBID: id("tmdb-1")},
		{Kind: KindMovie, TMDBID: id("missing"), IMDbID: id("tt-1")},
		{Kind: KindMovie, TMDBID: id("tmdb-twin")},
		{Kind: KindSeries, TVDBID: id("tvdb-s")},
		{Kind: KindSeries, TVDBID: id("missing"), TMDBID: id("tmdb-s"), PreferTMDB: true},
		{Kind: KindEpisode, TVDBID: id("tvdb-e1")},
		{Kind: KindEpisode, SeriesTVDBID: id("tvdb-s"), SeasonNumber: 1, EpisodeNumber: 2},
		{Kind: KindEpisode, SeriesTMDBID: id("tmdb-s"), SeasonNumber: 1, EpisodeNumber: 1},
		{Kind: KindEpisode, SeriesTVDBID: id("tvdb-s"), SeasonNumber: 3, EpisodeNumber: 9},
		{Kind: KindEpisode, TVDBID: id("missing"), SeasonNumber: 1, EpisodeNumber: 1},
	}
	for i := range 5000 {
		records = append(records, Record{Kind: KindMovie, TMDBID: fmt.Sprintf("none-%d-%s", i, sfx)},
			Record{Kind: KindEpisode, TVDBID: fmt.Sprintf("none-e-%d", i), SeriesTVDBID: fmt.Sprintf("none-s-%d", i), SeasonNumber: 1, EpisodeNumber: i + 1})
	}
	counted := &countingQuerier{q: tx}
	all, err := MatchAllOn(ctx, counted, records)
	if err != nil {
		t.Fatal(err)
	}
	if counted.n > 24 {
		t.Fatalf("%d records read %d times", len(records), counted.n)
	}
	one := NewMatcherOn(tx)
	for i, r := range records {
		want, _, err := one.Match(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if (want == nil) != (all[i] == nil) || want != nil && *want != *all[i] {
			t.Errorf("record %d (%+v): MatchAllOn = %+v, Match = %+v", i, r, all[i], want)
		}
		if i < 8 && (i == 2) != (want == nil) {
			t.Errorf("record %d (%+v): fixture expects a match only where one row has its id, got %+v", i, r, want)
		}
	}
}
