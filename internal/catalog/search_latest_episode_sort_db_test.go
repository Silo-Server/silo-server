package catalog

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Issue #2061: sorting by latest_episode_added must work whenever episodes can
// appear (a mixed text search, or the episode scope), not only for series.
// Episodes and movies have no value and sort last.
func TestSortLatestEpisodeAddedWithEpisodesDB(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("latest-episode-sort-%d", time.Now().UnixNano())
	token := fmt.Sprintf("zq%dtoken", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
	}()
	movie, older, newer, episode := prefix+"-movie", prefix+"-series-older", prefix+"-series-newer", prefix+"-episode"
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,year) VALUES($1,'movie',$2,'matched','{}',2001)`, movie, token+" Arrival")
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,year,latest_episode_added_at) VALUES($1,'series',$2,'matched','{}',2001,NOW()-INTERVAL '2 days')`, older, token+" Chronicles")
	exec(`INSERT INTO media_items(content_id,type,title,status,genres,year,latest_episode_added_at) VALUES($1,'series',$2,'matched','{}',2001,NOW()-INTERVAL '1 day')`, newer, token+" Dispatches")
	for _, id := range []string{movie, older, newer} {
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
	}
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,$3)`, episode, older, token+" Begins")
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id,first_seen_at) VALUES($1,$2,NOW())`, episode, library)

	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool))
	resolve := func(t *testing.T, values url.Values) []string {
		t.Helper()
		req, err := ParseCatalogRequest(values)
		if err != nil {
			t.Fatal(err)
		}
		req.CursorPaging = true
		req.Limit = 50
		result, err := resolver.Resolve(ctx, req, AccessFilter{})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ContentID)
		}
		return ids
	}

	for _, order := range []string{"desc", "asc"} {
		t.Run("mixed "+order, func(t *testing.T) {
			ids := resolve(t, url.Values{"q": {token}, "sort": {"latest_episode_added"}, "order": {order}})
			if len(ids) != 4 {
				t.Fatalf("expected movie, two series and episode, got %v", ids)
			}
			series := []string{newer, older}
			if order == "asc" {
				series = []string{older, newer}
			}
			if !slices.Equal(ids[:2], series) {
				t.Fatalf("series should lead in %s order of their newest episode, got %v", order, ids)
			}
			if rest := []string{ids[2], ids[3]}; !slices.Contains(rest, movie) || !slices.Contains(rest, episode) {
				t.Fatalf("movie and episode have no value and should sort last, got %v", ids)
			}
		})
	}
	t.Run("series scope", func(t *testing.T) {
		ids := resolve(t, url.Values{"q": {token}, "type": {"series"}, "sort": {"latest_episode_added"}})
		if !slices.Equal(ids, []string{newer, older}) {
			t.Fatalf("got %v", ids)
		}
	})
	t.Run("episode scope", func(t *testing.T) {
		ids := resolve(t, url.Values{"q": {token}, "type": {"episode"}, "sort": {"latest_episode_added"}})
		if !slices.Equal(ids, []string{episode}) {
			t.Fatalf("got %v", ids)
		}
	})
	t.Run("browse episode scope", func(t *testing.T) {
		ids := resolve(t, url.Values{"type": {"episode"}, "library_id": {fmt.Sprint(library)}, "sort": {"latest_episode_added"}})
		if !slices.Equal(ids, []string{episode}) {
			t.Fatalf("got %v", ids)
		}
	})
}
