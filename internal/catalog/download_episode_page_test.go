package catalog

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"reflect"
	"testing"
)

func TestDownloadEpisodePagesSQLBoundsAndAvailability(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Private connection-local copies permit equal numbering without changing the
	// real catalog's uniqueness constraints or any shared fixture rows.
	exec(`CREATE TEMP TABLE episodes (LIKE public.episodes INCLUDING DEFAULTS)`)
	exec(`CREATE TEMP TABLE episode_libraries (LIKE public.episode_libraries INCLUDING DEFAULTS)`)
	for _, v := range []struct {
		id, series string
		s, e       int
		available  bool
	}{{"special", "series", 0, 1, true}, {"b", "series", 1, 1, true}, {"a", "series", 1, 1, true}, {"hidden", "series", 1, 2, false}, {"last", "series", 2, 1, true}, {"foreign", "other", 0, 1, true}} {
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title)VALUES($1,$2,$3,$4,$1)`, v.id, v.series, v.s, v.e)
		if v.available {
			exec(`INSERT INTO episode_libraries(episode_id,media_folder_id)VALUES($1,1)`, v.id)
		}
	}
	repo := NewEpisodeRepository(pool)
	var after *EpisodePagePosition
	got := []string{}
	for {
		rows, more, err := repo.ListDownloadEpisodesPage(t.Context(), "series", nil, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 2 {
			t.Fatalf("unbounded result %d", len(rows))
		}
		for _, r := range rows {
			got = append(got, r.ContentID)
		}
		if !more {
			break
		}
		if len(rows) == 0 {
			t.Fatal("empty continuation")
		}
		last := rows[len(rows)-1]
		after = &EpisodePagePosition{SeasonNumber: last.SeasonNumber, EpisodeNumber: last.EpisodeNumber, ContentID: last.ContentID}
	}
	if !reflect.DeepEqual(got, []string{"special", "a", "b", "last"}) {
		t.Fatalf("order/availability %v", got)
	}
	rows, more, err := repo.ListDownloadEpisodesPage(t.Context(), "series", new(0), nil, 2)
	if err != nil || more || len(rows) != 1 || rows[0].ContentID != "special" {
		t.Fatalf("specials %v %v %v", rows, more, err)
	}
	rows, more, err = repo.ListDownloadEpisodesPage(t.Context(), "series", new(1), nil, 1)
	if err != nil || !more || len(rows) != 1 || rows[0].ContentID != "a" {
		t.Fatalf("season %v %v %v", rows, more, err)
	}
	legacy, err := repo.ListBySeries(t.Context(), "series")
	if err != nil || len(legacy) != 4 {
		t.Fatalf("bridge %v %v", legacy, err)
	}
	rows, more, err = repo.ListDownloadEpisodesPage(t.Context(), "missing", nil, nil, 2)
	if err != nil || more || rows == nil || len(rows) != 0 {
		t.Fatalf("empty %v %v %v", rows, more, err)
	}
	// A poisonous row after the selected page proves the database limit runs
	// before model scanning, rather than slicing an already materialized series.
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,created_at)VALUES('poison','series',9,1,'Poison','infinity')`)
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id)VALUES('poison',1)`)
	rows, _, err = repo.ListDownloadEpisodesPage(t.Context(), "series", nil, nil, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("bounded scan %v %v", rows, err)
	}
	if _, err := repo.ListBySeries(t.Context(), "series"); err == nil {
		t.Fatal("unbounded scan unexpectedly accepted poison timestamp")
	}
}
func TestDownloadEpisodePageRejectsInvalidBounds(t *testing.T) {
	repo := &EpisodeRepository{}
	for _, limit := range []int{0, 201} {
		if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", nil, nil, limit); err == nil {
			t.Fatal("accepted limit")
		}
	}
	if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", new(-1), nil, 1); err == nil {
		t.Fatal("accepted negative season")
	}
	if _, _, err := repo.ListDownloadEpisodesPage(t.Context(), "s", new(0), &EpisodePagePosition{SeasonNumber: 1, ContentID: "id"}, 1); err == nil {
		t.Fatal("accepted wrong season cursor")
	}
}
