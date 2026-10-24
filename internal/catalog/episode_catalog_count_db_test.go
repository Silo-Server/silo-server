package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// Episode counts come from episode_catalog_entries while cursor pages list the
// general episode relation, so a smart collection's count must name exactly
// the episodes its pages show, under every rule and access limit the faster
// count supports.
func TestEpisodeCatalogCountsMatchCursorPagesPostgres(t *testing.T) {
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

	f := seedEpisodeCountLibrary(t, pool)
	library, disabled, id := f.library, f.disabled, f.id

	viewer := AccessFilter{UserID: f.userID, ProfileID: f.profile, AllowedLibraryIDs: []int{library, disabled}}
	unwatched := []QueryRule{{Field: "watched", Op: "is", Value: false}}
	two := 2
	for _, tc := range []struct {
		name   string
		rules  []QueryRule
		limit  *int
		access func(AccessFilter) AccessFilter
		want   int
	}{
		{name: "unwatched", rules: unwatched, want: 6},
		{name: "watched", rules: []QueryRule{{Field: "watched", Op: "is", Value: true}}, want: 2},
		{name: "in progress", rules: []QueryRule{{Field: "in_progress", Op: "is", Value: true}}, want: 1},
		{name: "watched recently", rules: []QueryRule{{Field: "last_watched", Op: "in_last", Value: "30d"}}, want: 2},
		{name: "genre", rules: []QueryRule{{Field: "genre", Op: "is", Value: "Comedy"}}, want: 5},
		{name: "item limit", limit: &two, want: 2},
		{name: "unwatched name prefix", rules: unwatched, access: func(a AccessFilter) AccessFilter { a.NamePrefix = "A"; return a }, want: 1},
		{name: "unwatched allowed items", rules: unwatched, access: func(a AccessFilter) AccessFilter {
			a.AllowedContentIDs = []string{id("apple"), id("eggplant"), id("fig")}
			return a
		}, want: 2},
		{name: "unwatched rating limit", rules: unwatched, access: func(a AccessFilter) AccessFilter {
			a.MaturityLimits = access.MaturityLimits{MaxContentRating: "TV-14"}
			return a
		}, want: 3},
		{name: "unwatched disabled library", rules: unwatched, access: func(a AccessFilter) AccessFilter { a.DisabledLibraryIDs = []int{disabled}; return a }, want: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := QueryDefinition{MediaScope: "episode", LibraryIDs: []int{library}, Limit: tc.limit, Match: "all"}
			if len(tc.rules) > 0 {
				def.Groups = []QueryGroup{{Match: "all", Rules: tc.rules}}
			}
			access := viewer
			if tc.access != nil {
				access = tc.access(access)
			}
			executor := &QueryExecutor{Pool: pool, Scope: "episode"}
			if _, _, ok, err := executor.episodeCatalogCountQuery(def, access); err != nil || !ok {
				t.Fatalf("episode_catalog_entries count unavailable: ok=%v err=%v", ok, err)
			}
			count, err := executor.Count(ctx, def, access)
			if err != nil {
				t.Fatal(err)
			}

			listed, total := 0, -1
			var cursor *QueryCursor
			for range 10 {
				page, err := executor.PreviewCursorPage(ctx, def, access, 3, cursor, cursor == nil)
				if err != nil {
					t.Fatal(err)
				}
				if cursor == nil {
					total = page.Total
				}
				listed += len(page.Items)
				if !page.HasMore {
					break
				}
				cursor = page.Next
			}
			if count != tc.want || listed != tc.want || total != tc.want {
				t.Fatalf("count = %d, cursor total = %d, cursor pages listed %d; want %d", count, total, listed, tc.want)
			}
		})
	}
}

// episodeCountLibrary is the library seedEpisodeCountLibrary builds.
type episodeCountLibrary struct {
	library, disabled, userID int
	profile                   string
	id                        func(title string) string
}

// seedEpisodeCountLibrary builds two series and a podcast in one library, one
// episode also in a second library, and a profile with watched, finished,
// half-watched, and hidden episodes.
func seedEpisodeCountLibrary(t *testing.T, pool *pgxpool.Pool) episodeCountLibrary {
	t.Helper()
	ctx := t.Context()
	prefix := fmt.Sprintf("episode-count-%d", time.Now().UnixNano())
	var library, disabled int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, prefix+"-disabled").Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	profile := "10000000-0000-4000-8000-000000000012"
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{library, disabled})
	})
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'episode counts')`, profile, userID); err != nil {
		t.Fatal(err)
	}

	alpha, bravo, podcast := prefix+"-alpha", prefix+"-bravo", prefix+"-podcast"
	for _, parent := range []struct {
		id, kind, genre, rating string
		age                     int
	}{
		{alpha, "series", "Comedy", "TV-PG", 10},
		{bravo, "series", "Drama", "TV-MA", 17},
		// Not a TV series: its episodes reach episode_catalog_entries but
		// never a catalog page.
		{podcast, "podcast", "Comedy", "TV-PG", 10},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age) VALUES($1,$2,$1,'released',ARRAY[$3],$4,$5)`, parent.id, parent.kind, parent.genre, parent.rating, parent.age); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, parent.id, library); err != nil {
			t.Fatal(err)
		}
	}
	id := func(title string) string { return prefix + "-" + title }
	for i, episode := range []struct {
		series, title string
		alsoDisabled  bool
	}{
		{alpha, "apple", false},
		{alpha, "banana", false},
		{alpha, "cherry", false},
		{alpha, "date", false},
		{alpha, "eggplant", false},
		{bravo, "fig", false},
		{bravo, "grape", false},
		{bravo, "apricot", true},
		{podcast, "avocado", false},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,$3,$4)`, id(episode.title), episode.series, i+1, episode.title); err != nil {
			t.Fatal(err)
		}
		libraries := []int{library}
		if episode.alsoDisabled {
			libraries = append(libraries, disabled)
		}
		for _, folder := range libraries {
			if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, id(episode.title), folder); err != nil {
				t.Fatal(err)
			}
		}
	}
	// apple is watched; cherry finished through progress; banana is half
	// watched; date was watched before the profile hid its history.
	for _, stmt := range []struct {
		sql  string
		item string
	}{
		{`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,completed,watched_at) VALUES($1||'-h',$2,$3,$1,true,NOW() - INTERVAL '1 day')`, id("apple")},
		{`INSERT INTO user_watch_history(id,user_id,profile_id,media_item_id,completed,watched_at) VALUES($1||'-h',$2,$3,$1,true,NOW() - INTERVAL '2 days')`, id("date")},
		{`INSERT INTO user_history_hidden_items(user_id,profile_id,media_item_id,hidden_before) VALUES($2,$3,$1,NOW() - INTERVAL '1 day')`, id("date")},
		{`INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($2,$3,$1,0,1000,true)`, id("cherry")},
		{`INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed) VALUES($2,$3,$1,300,1000,false)`, id("banana")},
	} {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.item, userID, profile); err != nil {
			t.Fatal(err)
		}
	}
	return episodeCountLibrary{library: library, disabled: disabled, userID: userID, profile: profile, id: id}
}
