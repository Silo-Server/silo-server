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
		// IDs follow the titles, so content-ID order is not episode order either.
		id := episode.series + "-" + strings.ToLower(strings.ReplaceAll(episode.title, " ", ""))
		label[id] = fmt.Sprintf("%s%d.%d", episode.tag, episode.season, episode.number)
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,air_date) VALUES($1,$2,$3,$4,$5,$6::date)`, id, episode.series, episode.season, episode.number, episode.title, episode.airDate); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,'2026-08-01'::timestamptz)`, id, library); err != nil {
			t.Fatal(err)
		}
	}

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, alpha).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) }()
	profile := "10000000-0000-4000-8000-000000000011"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'air date ties')`, profile, userID); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"desc": "A3.11 A3.10 A3.9 B1.4 B1.3 A3.3 A3.2 A3.1",
		"asc":  "A3.1 A3.2 A3.3 A3.9 A3.10 B1.3 B1.4 A3.11",
	}
	executor := QueryExecutor{Pool: pool, Scope: "episode"}
	access := AccessFilter{AllowedLibraryIDs: []int{library, emptyLibrary}}
	for _, field := range []string{querySortReleaseDate, querySortLastAirDate} {
		for _, order := range []string{"desc", "asc"} {
			// One library takes the episode_catalog_entries page, and an unwatched
			// rule for a profile its user-state variant; two libraries take the
			// shared query builder. Cursor pages always use the builder.
			for _, path := range []string{"entries", "user_state", "builder"} {
				t.Run(field+"/"+order+"/"+path, func(t *testing.T) {
					def := QueryDefinition{MediaScope: "episode", LibraryIDs: []int{library}, Sort: QuerySort{Field: field, Order: order}}
					access := access
					switch path {
					case "user_state":
						def.Match = "all"
						def.Groups = []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "watched", Op: "is", Value: false}}}}
						access.UserID = userID
						access.ProfileID = profile
					case "builder":
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

	// The Jellyfin episode list for one series (PremiereDate) breaks ties the
	// same way.
	episodes := NewEpisodeRepository(pool)
	for order, want := range map[string]string{
		BrowseOrderDescending: "A3.11 A3.10 A3.9 A3.3 A3.2 A3.1",
		"":                    "A3.1 A3.2 A3.3 A3.9 A3.10 A3.11",
	} {
		items, _, err := episodes.BrowseEpisodes(ctx, alpha, "", nil, "", BrowseFilters{Sort: BrowseSortReleaseDate, Order: order, Limit: 20}, AccessFilter{AllowedLibraryIDs: []int{library}}, false)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(items))
		for _, item := range items {
			got = append(got, label[item.ContentID])
		}
		if strings.Join(got, " ") != want {
			t.Fatalf("BrowseEpisodes order %q = %s, want %s", order, strings.Join(got, " "), want)
		}
	}
}
