package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestStaleMediaIDRepository_ListActionable_MatchesPredicate checks that the
// SQL predicate ListActionable pages with agrees with
// IsActionableStaleProviderID row for row, and that the page is cut in the
// database.
func TestStaleMediaIDRepository_ListActionable_MatchesPredicate(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano() % 1_000_000_000
	id := func(n int) string { return fmt.Sprintf("%d%d", suffix, n) }
	type fixture struct {
		item  models.MediaItem
		stale models.StaleMediaID
	}
	fixtures := []fixture{
		// Matched item whose current tmdb_id failed: actionable.
		{item: models.MediaItem{ContentID: "movie-tmdb-" + id(1), Type: "movie", Title: "A", Status: "matched", TmdbID: id(1)}, stale: models.StaleMediaID{Provider: "tmdb", ProviderID: id(1)}},
		// Matched item with a foreign cross-reference: not actionable.
		{item: models.MediaItem{ContentID: "movie-tmdb-" + id(2), Type: "movie", Title: "B", Status: "matched", TmdbID: id(2)}, stale: models.StaleMediaID{Provider: "tmdb", ProviderID: id(9)}},
		// IMDb anchor compared case-insensitively: actionable.
		{item: models.MediaItem{ContentID: "movie-imdb-tt" + id(3), Type: "movie", Title: "C", Status: "matched"}, stale: models.StaleMediaID{Provider: "imdb", ProviderID: "TT" + id(3)}},
		// Unmatched local item with a bad path-provided ID: actionable.
		{item: models.MediaItem{ContentID: "local-" + id(4), Type: "movie", Title: "D", Status: "unmatched"}, stale: models.StaleMediaID{Provider: "tvdb", ProviderID: id(4)}},
		// Unknown provider: not actionable.
		{item: models.MediaItem{ContentID: "local-" + id(5), Type: "movie", Title: "E", Status: "unmatched"}, stale: models.StaleMediaID{Provider: "other", ProviderID: id(5)}},
		// Matched series with a tvdb anchor and empty tvdb_id column: actionable.
		{item: models.MediaItem{ContentID: "series-tvdb-" + id(6), Type: "series", Title: "F", Status: "matched"}, stale: models.StaleMediaID{Provider: "TVDB", ProviderID: id(6)}},
	}
	for _, f := range fixtures {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, year, status, tmdb_id, tvdb_id, imdb_id)
			VALUES ($1, $2, $3, 2000, $4, $5, '', '')
		`, f.item.ContentID, f.item.Type, f.item.Title, f.item.Status, f.item.TmdbID); err != nil {
			t.Fatalf("insert media item %s: %v", f.item.ContentID, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO stale_media_ids (content_id, provider, provider_id, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, NOW(), NOW())
		`, f.item.ContentID, f.stale.Provider, f.stale.ProviderID); err != nil {
			t.Fatalf("insert stale id for %s: %v", f.item.ContentID, err)
		}
	}
	t.Cleanup(func() {
		for _, f := range fixtures {
			_, _ = pool.Exec(ctx, `DELETE FROM stale_media_ids WHERE content_id = $1`, f.item.ContentID)
			_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, f.item.ContentID)
		}
	})

	mine := map[string]bool{}
	want := map[string]bool{}
	for _, f := range fixtures {
		mine[f.item.ContentID] = true
		stale := f.stale
		stale.ContentID = f.item.ContentID
		item := f.item
		if IsActionableStaleProviderID(&item, &stale) {
			want[f.item.ContentID] = true
		}
	}
	if len(want) != 4 {
		t.Fatalf("predicate fixture drifted: %v", want)
	}

	repo := NewStaleMediaIDRepository(pool)
	searched, err := repo.ListActionable(ctx, 1, 0, id(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(searched) != 1 || searched[0].ProviderID != id(4) {
		t.Fatalf("provider search before paging: %+v", searched)
	}
	searched, err = repo.ListActionable(ctx, 1, 0, "nonexistent-"+id(8))
	if err != nil || len(searched) != 0 {
		t.Fatalf("unmatched search: %+v %v", searched, err)
	}
	got := map[string]bool{}
	const page = 2
	for offset := 0; ; offset += page {
		rows, err := repo.ListActionable(ctx, page+1, offset, "")
		if err != nil {
			t.Fatalf("ListActionable(offset %d): %v", offset, err)
		}
		if len(rows) > page+1 {
			t.Fatalf("page of %d rows exceeds the limit+1 probe", len(rows))
		}
		for i, row := range rows {
			if i == page {
				break
			}
			if mine[row.ContentID] {
				got[row.ContentID] = true
			}
		}
		if len(rows) <= page {
			break
		}
	}
	for cid := range want {
		if !got[cid] {
			t.Errorf("%s: actionable in Go but not listed by SQL", cid)
		}
	}
	for cid := range got {
		if !want[cid] {
			t.Errorf("%s: listed by SQL but not actionable in Go", cid)
		}
	}
}
