package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

// TestApplyMaturityLimitsAdvisoryAge pins the advisory predicate: it admits a
// title with no advisory age, binds after the ceiling, and never binds when an
// unusable ceiling already blocks everything.
func TestApplyMaturityLimitsAdvisoryAge(t *testing.T) {
	apply := func(limits access.MaturityLimits) ([]string, []any, int) {
		var conditions []string
		var args []any
		argIdx := 3
		ApplyMaturityLimits("ece", AccessFilter{MaturityLimits: limits}, &conditions, &args, &argIdx)
		return conditions, args, argIdx
	}

	conditions, args, next := apply(access.MaturityLimits{MaxAdvisoryAge: 10})
	if !slices.Equal(conditions, []string{"(ece.advisory_age IS NULL OR ece.advisory_age <= $3)"}) || !slices.Equal(args, []any{10}) || next != 4 {
		t.Fatalf("advisory only: %v %v %d", conditions, args, next)
	}

	conditions, args, next = apply(access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 10})
	want := []string{
		"(ece.content_rating_age IS NOT NULL AND ece.content_rating_age <= $3)",
		"(ece.advisory_age IS NULL OR ece.advisory_age <= $4)",
	}
	if !slices.Equal(conditions, want) || !slices.Equal(args, []any{8, 10}) || next != 5 {
		t.Fatalf("ceiling and advisory: %v %v %d", conditions, args, next)
	}

	// access.unrated_content governs the ceiling only; a title with no
	// advisory age always passes the advisory limit.
	conditions, _, _ = apply(access.MaturityLimits{MaxAdvisoryAge: 10, AllowUnratedContent: true})
	if !slices.Equal(conditions, []string{"(ece.advisory_age IS NULL OR ece.advisory_age <= $3)"}) {
		t.Fatalf("unrated setting changed the advisory predicate: %v", conditions)
	}

	conditions, args, next = apply(access.MaturityLimits{MaxContentRating: "NR", MaxAdvisoryAge: 10})
	if !slices.Equal(conditions, []string{"1 = 0"}) || len(args) != 0 || next != 3 {
		t.Fatalf("blocked ceiling: %v %v %d", conditions, args, next)
	}
}

// TestAdvisoryAgeLimitDB runs a profile's advisory-age limit through the
// catalog reads that apply it, against a real schema. Each read aliases the
// gating media_items row differently ("mi", "hydrated_mi", "s", or the
// episode_catalog_entries read model "ece"), and a relation missing
// advisory_age only fails once a limit is set, so the reads run here with one.
func TestAdvisoryAgeLimitDB(t *testing.T) {
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

	prefix := fmt.Sprintf("advisory-limit-%d", time.Now().UnixNano())
	var movies, shows int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix+"-movies").Scan(&movies); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, prefix+"-shows").Scan(&shows); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, shows})
	})

	// Titles share the search word "Kestrel" so one search reaches every row.
	type title struct {
		id, kind, rating string
		ratingAge        int
		advisory         *int
	}
	titles := []title{
		{prefix + "-none", "movie", "PG", 8, nil},
		{prefix + "-ten", "movie", "PG", 8, ageOf(10)},
		{prefix + "-thirteen", "movie", "PG", 8, ageOf(13)},
		{prefix + "-sixteen-r", "movie", "R", 17, ageOf(16)},
		{prefix + "-show-sixteen", "series", "TV-PG", 8, ageOf(16)},
		{prefix + "-show-none", "series", "TV-PG", 8, nil},
	}
	for _, it := range titles {
		source := any(nil)
		if it.advisory != nil {
			source = "commonsense"
		}
		folder := movies
		if it.kind == "series" {
			folder = shows
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items(content_id,type,title,status,genres,content_rating,content_rating_age,advisory_age,advisory_source)
			VALUES($1,$2,$3,'matched',ARRAY['Drama'],$4,$5,$6,$7)`,
			it.id, it.kind, "Kestrel "+it.id, it.rating, it.ratingAge, it.advisory, source); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, it.id, folder); err != nil {
			t.Fatal(err)
		}
	}
	episodes := map[string]string{
		prefix + "-show-sixteen-e1": prefix + "-show-sixteen",
		prefix + "-show-sixteen-e2": prefix + "-show-sixteen",
		prefix + "-show-none-e1":    prefix + "-show-none",
	}
	number := 0
	for episodeID, seriesID := range episodes {
		number++
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes(content_id,series_id,season_number,episode_number,title)
			VALUES($1,$2,1,$3,$4)`, episodeID, seriesID, number, "Kestrel episode "+episodeID); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, episodeID, shows); err != nil {
			t.Fatal(err)
		}
	}
	allIDs := make([]string, 0, len(titles)+len(episodes))
	for _, it := range titles {
		allIDs = append(allIDs, it.id)
	}
	for episodeID := range episodes {
		allIDs = append(allIDs, episodeID)
	}

	limitTwelve := access.MaturityLimits{MaxAdvisoryAge: 12}
	visibleUnderTwelve := []string{prefix + "-none", prefix + "-ten", prefix + "-show-none", prefix + "-show-none-e1"}

	t.Run("FilterAccessibleContentIDs", func(t *testing.T) {
		got, err := NewLibraryItemRepository(pool).FilterAccessibleContentIDs(ctx, allIDs, nil, nil, limitTwelve)
		if err != nil {
			t.Fatal(err)
		}
		assertVisible(t, got, visibleUnderTwelve, allIDs)
	})

	items := NewItemRepository(pool)
	t.Run("EnsureAccessibleIDs", func(t *testing.T) {
		// Item IDs only: episodes are not media_items rows and are reached
		// through their series.
		itemIDs := make([]string, 0, len(titles))
		for _, it := range titles {
			itemIDs = append(itemIDs, it.id)
		}
		got, err := items.EnsureAccessibleIDs(ctx, itemIDs, AccessFilter{MaturityLimits: limitTwelve})
		if err != nil {
			t.Fatal(err)
		}
		assertVisible(t, got, []string{prefix + "-none", prefix + "-ten", prefix + "-show-none"}, itemIDs)
	})
	t.Run("EnsureAccessible", func(t *testing.T) {
		if err := items.EnsureAccessible(ctx, prefix+"-thirteen", AccessFilter{MaturityLimits: limitTwelve}); !errors.Is(err, ErrItemNotFound) {
			t.Fatalf("advisory 13 under limit 12: err = %v, want ErrItemNotFound", err)
		}
		if err := items.EnsureAccessible(ctx, prefix+"-none", AccessFilter{MaturityLimits: limitTwelve}); err != nil {
			t.Fatalf("no advisory under limit 12: %v", err)
		}
	})

	t.Run("Browse and genres", func(t *testing.T) {
		browse := NewBrowseRepository(pool)
		filters := BrowseFilters{Type: "movie,series", LibraryIDs: []int{movies, shows}, SearchTerm: prefix, Limit: 50, MaturityLimits: limitTwelve}
		result, err := browse.BrowsePage(ctx, filters, true)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range result.Items {
			got = append(got, item.ContentID)
		}
		slices.Sort(got)
		want := []string{prefix + "-none", prefix + "-show-none", prefix + "-ten"}
		if !slices.Equal(got, want) {
			t.Fatalf("browse = %v, want %v", got, want)
		}
		if _, err := browse.ListGenres(ctx, filters); err != nil {
			t.Fatalf("ListGenres with a limit: %v", err)
		}
	})

	t.Run("BrowseEpisodes", func(t *testing.T) {
		episodesRepo := NewEpisodeRepository(pool)
		hidden, _, err := episodesRepo.BrowseEpisodes(ctx, prefix+"-show-sixteen", "", nil, "", BrowseFilters{Limit: 10}, AccessFilter{MaturityLimits: limitTwelve}, true)
		if err != nil {
			t.Fatal(err)
		}
		shown, _, err := episodesRepo.BrowseEpisodes(ctx, prefix+"-show-none", "", nil, "", BrowseFilters{Limit: 10}, AccessFilter{MaturityLimits: limitTwelve}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(hidden) != 0 || len(shown) != 1 {
			t.Fatalf("episodes of advisory-16 series = %d (want 0), of unadvised series = %d (want 1)", len(hidden), len(shown))
		}
	})

	t.Run("mixed search", func(t *testing.T) {
		found, _, _, _, err := items.SearchPage(ctx, "Kestrel", nil, 50, 0, AccessFilter{MaturityLimits: limitTwelve}, true)
		if err != nil {
			t.Fatal(err)
		}
		var ours []string
		for _, item := range found {
			if !strings.HasPrefix(item.ContentID, prefix) {
				continue
			}
			ours = append(ours, item.ContentID)
			if !slices.Contains(visibleUnderTwelve, item.ContentID) {
				t.Errorf("search returned %s above the advisory limit", item.ContentID)
			}
		}
		// Guard against a vacuous pass: the unadvised movie and episode match.
		for _, id := range []string{prefix + "-none", prefix + "-show-none-e1"} {
			if !slices.Contains(ours, id) {
				t.Errorf("search did not return %s; got %v", id, ours)
			}
		}
	})

	t.Run("episode catalog read model", func(t *testing.T) {
		visibleEpisodes := func() []string {
			t.Helper()
			conditions := []string{"ece.episode_id LIKE $1"}
			args := []any{prefix + "%"}
			argIdx := 2
			ApplySectionAccessFilter("ece", AccessFilter{MaturityLimits: limitTwelve}, &conditions, &args, &argIdx)
			rows, err := pool.Query(ctx, "SELECT ece.episode_id FROM episode_catalog_entries ece WHERE "+strings.Join(conditions, " AND ")+" ORDER BY 1", args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return ids
		}
		if got := visibleEpisodes(); !slices.Equal(got, []string{prefix + "-show-none-e1"}) {
			t.Fatalf("ece under limit 12 = %v", got)
		}
		// The series trigger carries a later advisory change to its episodes.
		if _, err := pool.Exec(ctx, `UPDATE media_items SET advisory_age = 9 WHERE content_id = $1`, prefix+"-show-sixteen"); err != nil {
			t.Fatal(err)
		}
		if got := visibleEpisodes(); len(got) != 3 {
			t.Fatalf("after lowering the series advisory to 9, ece under limit 12 = %v, want all three episodes", got)
		}
		if _, err := pool.Exec(ctx, `UPDATE media_items SET advisory_age = 16 WHERE content_id = $1`, prefix+"-show-sixteen"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ANDs with the content-rating ceiling", func(t *testing.T) {
		repo := NewLibraryItemRepository(pool)
		movieIDs := []string{prefix + "-none", prefix + "-ten", prefix + "-thirteen", prefix + "-sixteen-r"}
		// PG ceiling and a loose advisory limit: the R title falls to the
		// ceiling even though its advisory would pass.
		got, err := repo.FilterAccessibleContentIDs(ctx, movieIDs, nil, nil, access.MaturityLimits{MaxContentRating: "PG", MaxAdvisoryAge: 16})
		if err != nil {
			t.Fatal(err)
		}
		assertVisible(t, got, []string{prefix + "-none", prefix + "-ten", prefix + "-thirteen"}, movieIDs)
		// R ceiling and a tight advisory limit: the PG title with advisory 13
		// falls to the advisory even though its rating would pass.
		got, err = repo.FilterAccessibleContentIDs(ctx, movieIDs, nil, nil, access.MaturityLimits{MaxContentRating: "R", MaxAdvisoryAge: 12})
		if err != nil {
			t.Fatal(err)
		}
		assertVisible(t, got, []string{prefix + "-none", prefix + "-ten"}, movieIDs)
	})
}

func assertVisible(t *testing.T, got map[string]bool, want, all []string) {
	t.Helper()
	for _, id := range all {
		if got[id] != slices.Contains(want, id) {
			t.Errorf("%s visible = %v, want %v", id, got[id], slices.Contains(want, id))
		}
	}
}
