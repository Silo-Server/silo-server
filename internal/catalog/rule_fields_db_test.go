package catalog

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// The title, decade, runtime, rating, newest-episode and "not in the last"
// rules run against PostgreSQL: LIKE wildcards in a title match literally, a
// fractional bound compares against an integer column, a title without the
// date matches neither relative operator, and a show's last watched date is
// its most recently watched episode.
func TestRuleFieldsFilterCatalogDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("rule-fields-%d", time.Now().UnixNano())

	var movies, shows, userID int
	for _, lib := range []struct {
		id   *int
		kind string
	}{{&movies, "movies"}, {&shows, "tv"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`,
			lib.kind, prefix+"-"+lib.kind).Scan(lib.id); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	profile := prefix + "-profile"
	id := func(name string) string { return prefix + "-" + name }
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, shows})
	})

	recent := time.Now().UTC().AddDate(0, 0, -10).Format("2006-01-02")
	batchEquivExec(t, pool, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'viewer')`, profile, userID)
	batchEquivExec(t, pool, `
		INSERT INTO media_items (content_id, type, title, genres, year, runtime, rating_tmdb, rating_rt_critic, release_date) VALUES
			($1, 'movie', 'The Empire Strikes Back', '{}', 1980, 124, 8.4, 94, '1980-05-21'),
			($2, 'movie', '100% Wolf', '{}', 2020, 96, 5.9, 20, $4::date),
			($3, 'movie', 'Undated', '{}', 0, 0, NULL, NULL, NULL)`,
		id("empire"), id("wolf"), id("undated"), recent)
	batchEquivExec(t, pool, `
		INSERT INTO media_items (content_id, type, title, genres, latest_episode_added_at, last_air_date_at) VALUES
			($1, 'series', 'Fresh Show', '{}', NOW() - INTERVAL '2 days', CURRENT_DATE - 2),
			($2, 'series', 'Stale Show', '{}', NOW() - INTERVAL '400 days', CURRENT_DATE - 400)`,
		id("fresh"), id("stale"))
	for _, movie := range []string{"empire", "wolf", "undated"} {
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id(movie), movies)
	}
	for _, show := range []string{"fresh", "stale"} {
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, id(show), shows)
		batchEquivExec(t, pool, `INSERT INTO seasons (content_id, series_id, season_number) VALUES ($1 || '-s1', $1, 1)`, id(show))
		batchEquivExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title) VALUES ($1 || '-e1', $1, $1 || '-s1', 1, 1, 'Pilot')`, id(show))
		batchEquivExec(t, pool, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1 || '-e1', $2)`, id(show), shows)
	}
	// Only Fresh Show's episode was finished, three days ago. Stale Show's
	// episode and 100% Wolf were started yesterday but not finished, which
	// dates neither them nor the show.
	batchEquivExec(t, pool, `INSERT INTO user_watch_history (id, user_id, profile_id, media_item_id, watched_at, completed) VALUES
		($1 || '-history', $2, $3, $4, NOW() - INTERVAL '3 days', TRUE),
		($1 || '-unfinished', $2, $3, $5, NOW() - INTERVAL '1 day', FALSE),
		($1 || '-unfinished-movie', $2, $3, $6, NOW() - INTERVAL '1 day', FALSE)`,
		prefix, userID, profile, id("fresh")+"-e1", id("stale")+"-e1", id("wolf"))

	executor := &QueryExecutor{Pool: pool}
	viewer := AccessFilter{UserID: userID, ProfileID: profile}
	tests := []struct {
		name  string
		scope string
		rule  QueryRule
		want  []string
	}{
		{"title contains", "movie", QueryRule{Field: "title", Op: "contains", Value: "WOLF"}, []string{"wolf"}},
		{"title percent is literal", "movie", QueryRule{Field: "title", Op: "contains", Value: "%"}, []string{"wolf"}},
		{"title begins with", "movie", QueryRule{Field: "title", Op: "begins_with", Value: "the "}, []string{"empire"}},
		{"title ends with", "movie", QueryRule{Field: "title", Op: "ends_with", Value: "back"}, []string{"empire"}},
		{"title not contains", "movie", QueryRule{Field: "title", Op: "not_contains", Value: "wolf"}, []string{"empire", "undated"}},
		{"title is", "movie", QueryRule{Field: "title", Op: "is", Value: "undated"}, []string{"undated"}},
		{"decade", "movie", QueryRule{Field: "decade", Op: "is", Value: 1980.0}, []string{"empire"}},
		{"runtime fractional bound", "movie", QueryRule{Field: "runtime", Op: "lt", Value: 100.5}, []string{"wolf"}},
		{"rt critic fractional bound", "movie", QueryRule{Field: "rating_rt_critic", Op: "gte", Value: 90.5}, []string{"empire"}},
		{"tmdb rating range", "movie", QueryRule{Field: "rating_tmdb", Op: "between", Value: []any{5.0, 6.0}}, []string{"wolf"}},
		{"released in the last", "movie", QueryRule{Field: "release_date", Op: "in_last", Value: "30d"}, []string{"wolf"}},
		{"released not in the last", "movie", QueryRule{Field: "release_date", Op: "not_in_last", Value: "30d"}, []string{"empire"}},
		{"episode added in the last", "series", QueryRule{Field: "latest_episode_added", Op: "in_last", Value: "7d"}, []string{"fresh"}},
		{"episode added not in the last", "series", QueryRule{Field: "latest_episode_added", Op: "not_in_last", Value: "7d"}, []string{"stale"}},
		{"aired not in the last", "series", QueryRule{Field: "last_air_date", Op: "not_in_last", Value: "1m"}, []string{"stale"}},
		{"show watched in the last", "series", QueryRule{Field: "last_watched", Op: "in_last", Value: "7d"}, []string{"fresh"}},
		{"show not watched in the last", "series", QueryRule{Field: "last_watched", Op: "not_in_last", Value: "7d"}, []string{"stale"}},
		{"unfinished title not watched", "movie", QueryRule{Field: "last_watched", Op: "in_last", Value: "7d"}, []string{}},
		// The episode fast path leaves title to the generic executor, whose
		// placeholders follow the episode relation's library arguments.
		{"episode title", "episode", QueryRule{Field: "title", Op: "is", Value: "pilot"}, []string{"fresh-e1", "stale-e1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := ApplySmartCollectionItemLimit(QueryDefinition{
				MediaScope: tt.scope,
				LibraryIDs: []int{movies, shows},
				Match:      "all",
				Groups:     []QueryGroup{{Match: "all", Rules: []QueryRule{tt.rule}}},
				Sort:       QuerySort{Field: "title", Order: "asc"},
			}.Normalize())
			items, total, err := executor.Preview(ctx, def, viewer, 50)
			if err != nil {
				t.Fatalf("Preview: %v", err)
			}
			var got []string
			for _, item := range items {
				got = append(got, item.ContentID)
			}
			want := make([]string, len(tt.want))
			for i, name := range tt.want {
				want[i] = id(name)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) || total != len(want) {
				t.Fatalf("got %v (total %d), want %v", got, total, want)
			}
		})
	}
}
