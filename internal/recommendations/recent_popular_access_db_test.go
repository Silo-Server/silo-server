package recommendations

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TestRecentlyAddedAndPopularApplyAccessFilter: the filter applies inside the
// query, so titles a profile may not see neither appear nor take the places
// of titles it may see.
func TestRecentlyAddedAndPopularApplyAccessFilter(t *testing.T) {
	pool := newEngineTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var allowed, other int
	for _, id := range []*int{&allowed, &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`,
			fmt.Sprintf("recs-access-%d-%p", suffix, id)).Scan(id); err != nil {
			t.Fatalf("seed folder: %v", err)
		}
	}
	kids := fmt.Sprintf("movie-recs-kids-%d", suffix)
	adult := fmt.Sprintf("movie-recs-adult-%d", suffix)
	elsewhere := fmt.Sprintf("movie-recs-elsewhere-%d", suffix)
	all := []string{kids, adult, elsewhere}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE media_item_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_item_libraries WHERE content_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{allowed, other})
	})
	// content_rating_age is the age the rating maps to; the scanner fills it.
	for _, item := range []struct {
		id, rating  string
		age, folder int
	}{{kids, "G", 0, allowed}, {adult, "R", 17, allowed}, {elsewhere, "G", 0, other}} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, content_rating, content_rating_age) VALUES ($1, 'movie', $1, 'matched', $2, $3)`, item.id, item.rating, item.age); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, item.id, item.folder); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	var userID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("no user to record watch progress for: %v", err)
	}
	for _, id := range all {
		if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
			VALUES ($1, '', $2, 100, 100, TRUE, NOW())`, userID, id); err != nil {
			t.Fatalf("seed progress: %v", err)
		}
	}

	repo := NewRepo(pool)
	kidsFilter := catalog.AccessFilter{AllowedLibraryIDs: []int{allowed}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	ids := func(items []ScoredItem, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		var out []string
		for _, item := range items {
			if slices.Contains(all, item.MediaItemID) {
				out = append(out, item.MediaItemID)
			}
		}
		slices.Sort(out)
		return out
	}
	want := []string{kids}
	if got := ids(repo.GetRecentlyAddedItems(ctx, 1, 1000, kidsFilter)); !slices.Equal(got, want) {
		t.Errorf("recently added for the kids filter = %v, want %v", got, want)
	}
	if got := ids(repo.GetPopularItems(ctx, 1, 1000, kidsFilter)); !slices.Equal(got, want) {
		t.Errorf("popular for the kids filter = %v, want %v", got, want)
	}
	everything := slices.Sorted(slices.Values(all))
	if got := ids(repo.GetRecentlyAddedItems(ctx, 1, 1000, catalog.AccessFilter{})); !slices.Equal(got, everything) {
		t.Errorf("recently added with no filter = %v, want %v", got, everything)
	}
	if got := ids(repo.GetPopularItems(ctx, 1, 1000, catalog.AccessFilter{})); !slices.Equal(got, everything) {
		t.Errorf("popular with no filter = %v, want %v", got, everything)
	}
	if got := ids(repo.GetPopularItems(ctx, 1, 1000, catalog.AccessFilter{AllowedLibraryIDs: []int{}})); len(got) != 0 {
		t.Errorf("popular with no allowed library = %v, want none", got)
	}
}
