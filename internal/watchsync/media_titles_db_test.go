package watchsync

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GetMediaTitles reads a movie's own title and year, and an episode's title
// with its series' title and year (an episode's year is its series').
func TestGetMediaTitlesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := uuid.NewString()
	movie, series, episode := "titles-movie-"+suffix, "titles-series-"+suffix, "titles-episode-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,year,genres) VALUES ($1,'movie','17 Again',2009,'{}'),($2,'series','Breaking Bad',2008,'{}')`, movie, series); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id IN ($1,$2)`, movie, series) }()
	if _, err := pool.Exec(ctx, `INSERT INTO episodes (content_id,series_id,season_number,episode_number,title,air_date) VALUES ($1,$2,1,1,'Pilot','2010-01-20')`, episode, series); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE content_id=$1`, episode) }()

	titles, err := NewPostgresRepository(pool, nil).GetMediaTitles(ctx, []string{movie, episode, "titles-missing-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]MediaTitles{
		movie:   {Kind: "movie", Title: "17 Again", Year: 2009},
		episode: {Kind: "episode", Title: "Pilot", Year: 2008, SeriesTitle: "Breaking Bad", SeriesYear: 2008},
	}
	if len(titles) != len(want) || titles[movie] != want[movie] || titles[episode] != want[episode] {
		t.Fatalf("titles = %+v, want %+v", titles, want)
	}
}
