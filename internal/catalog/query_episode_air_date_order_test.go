package catalog

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Episodes that share an air date keep their series together, in series title
// order, and follow the season/episode order in the requested direction. The
// titles sort alphabetically in neither episode order, and the series IDs sort
// opposite to the series titles, so neither fallback can pass by accident.
func TestQueryEpisodeAirDateTiesPostgres(t *testing.T) {
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

	stamp := time.Now().UnixNano()
	alpha := fmt.Sprintf("zz-air-date-%d", stamp)
	bravo := fmt.Sprintf("aa-air-date-%d", stamp)
	var library, emptyLibrary int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series','air date ties',true) RETURNING id`).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series','air date ties empty',true) RETURNING id`).Scan(&emptyLibrary); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{alpha, bravo})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{library, emptyLibrary})
	}()
	for _, series := range []struct{ id, title string }{{alpha, "Alpha Show"}, {bravo, "Bravo Show"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'series',$2,'released','{Comedy}')`, series.id, series.title); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, series.id, library); err != nil {
			t.Fatal(err)
		}
	}

	label := map[string]string{}
	for _, episode := range []struct {
		series, tag    string
		season, number int
		title, airDate string
	}{
		{alpha, "A", 3, 1, "Mixer", "2026-08-19"},
		{alpha, "A", 3, 2, "Zero Hour", "2026-08-19"},
		{alpha, "A", 3, 3, "All In", "2026-08-19"},
		{alpha, "A", 3, 9, "Meet the Family", "2026-08-26"},
		{alpha, "A", 3, 10, "Dinner", "2026-08-26"},
		{alpha, "A", 3, 11, "Reunion", "2026-08-30"},
		{bravo, "B", 1, 4, "Qualifying", "2026-08-26"},
		{bravo, "B", 1, 3, "Practice 3", "2026-08-26"},
	} {
		id := fmt.Sprintf("%s-s%02de%02d", episode.series, episode.season, episode.number)
		label[id] = fmt.Sprintf("%s%d.%d", episode.tag, episode.season, episode.number)
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,air_date) VALUES($1,$2,$3,$4,$5,$6::date)`, id, episode.series, episode.season, episode.number, episode.title, episode.airDate); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,'2026-08-01'::timestamptz)`, id, library); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]string{
		"desc": "A3.11 A3.10 A3.9 B1.4 B1.3 A3.3 A3.2 A3.1",
		"asc":  "A3.1 A3.2 A3.3 A3.9 A3.10 B1.3 B1.4 A3.11",
	}
	executor := QueryExecutor{Pool: pool, Scope: "episode"}
	access := AccessFilter{AllowedLibraryIDs: []int{library, emptyLibrary}}
	for _, field := range []string{querySortReleaseDate, querySortLastAirDate} {
		for _, order := range []string{"desc", "asc"} {
			// One library takes the episode_catalog_entries page; two take the
			// shared query builder. Cursor pages always use the builder.
			for _, path := range []string{"entries", "builder"} {
				t.Run(field+"/"+order+"/"+path, func(t *testing.T) {
					def := QueryDefinition{MediaScope: "episode", LibraryIDs: []int{library}, Sort: QuerySort{Field: field, Order: order}}
					if path == "builder" {
						def.LibraryIDs = []int{library, emptyLibrary}
					}
					items, _, _, err := executor.PreviewPage(ctx, def, access, 100, 0, false)
					if err != nil {
						t.Fatal(err)
					}
					offset := make([]string, 0, len(items))
					for _, item := range items {
						offset = append(offset, label[item.ContentID])
					}
					if got := strings.Join(offset, " "); got != want[order] {
						t.Fatalf("offset order = %s, want %s", got, want[order])
					}

					var paged []string
					var cursor *QueryCursor
					for range len(label) {
						page, err := executor.PreviewCursorPage(ctx, def, access, 3, cursor, false)
						if err != nil {
							t.Fatal(err)
						}
						for _, item := range page.Items {
							paged = append(paged, label[item.ContentID])
						}
						if !page.HasMore {
							break
						}
						cursor = page.Next
					}
					if got := strings.Join(paged, " "); got != want[order] {
						t.Fatalf("cursor order = %s, want %s", got, want[order])
					}
				})
			}
		}
	}
}
