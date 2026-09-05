package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/google/uuid"
)

// TestJellycompatPredicatesPostgres runs the actual count/page queries against
// synthetic catalog and profile state. Set SILO_TEST_DATABASE_URL to a migrated
// disposable database; every inserted row is removed on completion.
func TestJellycompatPredicatesPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "jf-pred-" + uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	var userID, libraryID, disabledID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id=ANY($1)`, []int{libraryID, disabledID})
	})
	profiles := []string{uuid.NewString(), uuid.NewString()}
	for _, profile := range profiles {
		exec(`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'Synthetic profile')`, profile, userID)
	}
	for i := range 2 {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, fmt.Sprintf("%s library%d", prefix, i)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			libraryID = id
		} else {
			disabledID = id
		}
	}
	favorite := func(id, profile string) {
		exec(`INSERT INTO user_favorites(user_id,profile_id,media_item_id,added_at) VALUES($1,$2,$3,NOW())`, userID, profile, id)
	}
	played := func(id, profile string) {
		exec(`INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds,duration_seconds,completed,updated_at) VALUES($1,$2,$3,100,100,true,NOW())`, userID, profile, id)
	}
	link := func(id string, library int) {
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, id, library)
	}
	var movieIDs []string
	for i := range 8 {
		id := fmt.Sprintf("%s-m%d", prefix, i)
		movieIDs = append(movieIDs, id)
		seedSortableItem(t, pool, id, fmt.Sprintf("Synthetic Movie %d", i), 2024)
		genre := "Drama"
		year := 2024
		if i == 3 {
			genre = "Comedy"
		}
		if i == 4 {
			year = 2023
		}
		exec(`UPDATE media_items SET genres=ARRAY[$2]::text[],year=$3,content_rating='PG',status='matched' WHERE content_id=$1`, id, genre, year)
		link(id, libraryID)
		if i != 5 && i != 6 {
			favorite(id, profiles[0])
		}
		if i == 6 {
			favorite(id, profiles[1])
		}
		if i == 2 {
			played(id, profiles[0])
		}
		if i == 7 {
			link(id, disabledID)
		}
	}
	base := BrowseFilters{Type: "movie", ContentIDs: movieIDs, Genres: []string{"Drama"}, Years: []int{2024}, IsFavorite: true, IsPlayed: new(false), UserID: userID, ProfileID: profiles[0], LibraryIDs: []int{libraryID}, DisabledLibraryIDs: []int{disabledID}, MaxContentRating: "PG", Sort: "sort_title", Order: "asc", Limit: 1, Offset: 1}
	browse := NewBrowseRepository(pool)
	t.Run("played only binds every parameter", func(t *testing.T) {
		for _, completed := range []bool{true, false} {
			query := base
			query.IsFavorite = false
			query.IsPlayed = new(completed)
			query.Offset = 0
			result, err := browse.BrowsePage(ctx, query, true)
			if err != nil {
				t.Fatal(err)
			}
			want := 4
			if completed {
				want = 1
			}
			if result.Total != want || len(result.Items) != 1 {
				t.Fatalf("played=%v total=%d items=%v", completed, result.Total, orderedIDs(result.Items))
			}
		}
	})
	t.Run("movie intersections total and offset", func(t *testing.T) {
		result, err := browse.BrowsePage(ctx, base, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 2 || len(result.Items) != 1 || result.Items[0].ContentID != movieIDs[1] {
			t.Fatalf("page total=%d ids=%v", result.Total, orderedIDs(result.Items))
		}
	})
	t.Run("movie played inversion", func(t *testing.T) {
		q := base
		q.IsPlayed = new(true)
		q.Offset = 0
		result, err := browse.BrowsePage(ctx, q, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 1 || len(result.Items) != 1 || result.Items[0].ContentID != movieIDs[2] {
			t.Fatalf("played total=%d ids=%v", result.Total, orderedIDs(result.Items))
		}
	})
	t.Run("movie profile isolation", func(t *testing.T) {
		q := base
		q.ProfileID = profiles[1]
		q.Offset = 0
		result, err := browse.BrowsePage(ctx, q, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 1 || len(result.Items) != 1 || result.Items[0].ContentID != movieIDs[6] {
			t.Fatalf("profile total=%d ids=%v", result.Total, orderedIDs(result.Items))
		}
	})
	t.Run("movie denied library", func(t *testing.T) {
		q := base
		q.DisabledLibraryIDs = []int{libraryID}
		q.Offset = 0
		result, err := browse.BrowsePage(ctx, q, true)
		if err != nil {
			t.Fatal(err)
		}
		if result.Total != 0 || len(result.Items) != 0 {
			t.Fatalf("denied result: %+v", result)
		}
	})
	seriesID, otherSeries := prefix+"-series", prefix+"-other-series"
	for _, id := range []string{seriesID, otherSeries} {
		exec(`INSERT INTO media_items(content_id,type,title,genres,content_rating) VALUES($1,'series','Synthetic Series',ARRAY['Drama'],'PG')`, id)
		link(id, libraryID)
	}
	seasonIDs := []string{prefix + "-season1", prefix + "-season2"}
	for i, id := range seasonIDs {
		if err := NewSeasonRepository(pool).Upsert(ctx, &models.Season{ContentID: id, SeriesID: seriesID, SeasonNumber: i + 1, Title: "Synthetic Season", MetadataSource: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	var episodeIDs []string
	for i := range 6 {
		id := fmt.Sprintf("%s-e%d", prefix, i)
		episodeIDs = append(episodeIDs, id)
		series, season, number, year := seriesID, seasonIDs[0], 1, 2024
		if i >= 3 {
			season = seasonIDs[1]
			number = 2
		}
		if i == 3 {
			year = 2023
		}
		if i == 5 {
			series = otherSeries
			season = ""
			number = 1
		}
		exec(`INSERT INTO episodes(content_id,series_id,season_id,season_number,episode_number,title,air_date) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7)`, id, series, season, number, i+1, fmt.Sprintf("Synthetic Episode %d", i), time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC))
		exec(`INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, id, libraryID)
		profile := profiles[0]
		if i == 4 {
			profile = profiles[1]
		}
		favorite(id, profile)
		if i == 2 {
			played(id, profiles[0])
		}
	}
	episodes := NewEpisodeRepository(pool)
	t.Run("available episode IDs are scoped and paged", func(t *testing.T) {
		metadataID := prefix + "-metadata-only"
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,9,99,'Metadata only')`, metadataID, seriesID)
		ids, err := episodes.ListAvailableIDsBySeriesPage(ctx, seriesID, 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 || ids[0] != episodeIDs[2] || ids[1] != episodeIDs[3] {
			t.Fatalf("scoped ID page: %v", ids)
		}
		ids, err = episodes.ListAvailableIDsBySeriesPage(ctx, seriesID, 100, 4)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != episodeIDs[4] {
			t.Fatalf("unavailable or foreign episode included: %v", ids)
		}
	})
	access := AccessFilter{AllowedLibraryIDs: []int{libraryID}, DisabledLibraryIDs: []int{disabledID}, MaxContentRating: "PG"}
	q := base
	q.ContentIDs = nil
	q.Sort = ""
	q.Order = ""
	q.Offset = 1
	t.Run("episode played only binds every parameter", func(t *testing.T) {
		for _, completed := range []bool{true, false} {
			query := q
			query.IsFavorite = false
			query.IsPlayed = new(completed)
			query.Offset = 0
			items, total, err := episodes.BrowseEpisodes(ctx, seriesID, seasonIDs[0], nil, "", query, access)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if completed {
				want = 1
			}
			if total != want || len(items) != 1 {
				t.Fatalf("played=%v total=%d items=%v", completed, total, items)
			}
		}
	})
	t.Run("episode parent numeric season count offset", func(t *testing.T) {
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", new(1), "", q, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(items) != 1 || items[0].ContentID != episodeIDs[1] {
			t.Fatalf("episode page total=%d items=%v", total, items)
		}
	})
	t.Run("episode played inversion", func(t *testing.T) {
		query := q
		query.IsPlayed = new(true)
		query.Offset = 0
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, seasonIDs[0], nil, "", query, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(items) != 1 || items[0].ContentID != episodeIDs[2] {
			t.Fatalf("played episode total=%d items=%v", total, items)
		}
	})
	t.Run("episode profile isolation", func(t *testing.T) {
		query := q
		query.ProfileID = profiles[1]
		query.Offset = 0
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", nil, "", query, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(items) != 1 || items[0].ContentID != episodeIDs[4] {
			t.Fatalf("profile episode total=%d items=%v", total, items)
		}
	})
	t.Run("episode negative genre", func(t *testing.T) {
		query := q
		query.Genres = []string{"Comedy"}
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", nil, "", query, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(items) != 0 {
			t.Fatalf("genre exclusion total=%d items=%v", total, items)
		}
	})
	t.Run("episode exact year", func(t *testing.T) {
		query := q
		query.Years = []int{2023}
		query.Offset = 0
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", nil, "", query, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(items) != 1 || items[0].ContentID != episodeIDs[3] {
			t.Fatalf("year page total=%d items=%v", total, items)
		}
	})
	t.Run("episode absent year", func(t *testing.T) {
		query := q
		query.Years = []int{2022}
		query.Offset = 0
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", nil, "", query, access)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(items) != 0 {
			t.Fatalf("year exclusion total=%d items=%v", total, items)
		}
	})
	t.Run("episode disabled parent library", func(t *testing.T) {
		scope := access
		scope.DisabledLibraryIDs = []int{libraryID}
		items, total, err := episodes.BrowseEpisodes(ctx, seriesID, "", nil, "", q, scope)
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(items) != 0 {
			t.Fatalf("denied episodes total=%d items=%v", total, items)
		}
	})
}
