package handlers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// WatchlistTitleOff counts a title as off the watchlist only when neither form
// holds it: an entry outside the library, or the library copy on the library
// watchlist. A title the library received and promotion moved is still on,
// so the add and delete race handling never withdraws its request.
func TestWatchlistTitleOffDB(t *testing.T) {
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
	stamp := time.Now().UnixNano()
	prefix := fmt.Sprintf("wlt-off-%d", stamp)
	tmdbID := 1_500_000_000 + int(stamp/1000%100_000)
	var userID, folder int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM watchlist_titles WHERE tmdb_id = $1`, tmdbID)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = $1`, prefix)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	items := catalog.NewItemRepository(pool)
	h := NewPersonalDataHandler(provider, items)
	h.SetWatchlistTitles(watchlist.NewTitles(pool, items, provider, nil, nil))
	viewer := PersonalListViewer{UserID: userID, ProfileID: "p1"}
	snap := watchlist.Snapshot{MediaType: "movie", TMDBID: tmdbID, Title: "Arriving"}

	check := func(want bool, when string) {
		t.Helper()
		off, err := h.WatchlistTitleOff(ctx, viewer, snap)
		if err != nil || off != want {
			t.Fatalf("%s: off = %v %v, want %v", when, off, err, want)
		}
	}
	check(true, "never added")
	if _, _, err := h.watchlistTitles.AddSnapshot(ctx, viewer.watchlistViewer(), snap, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check(false, "entry outside the library")

	// The library receives the title and a read promotes the entry.
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id) VALUES ($1, 'movie', $1, '{}'::text[], $2)`,
		prefix, strconv.Itoa(tmdbID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, prefix, folder); err != nil {
		t.Fatal(err)
	}
	if promoted, err := h.watchlistTitles.PromoteProfile(ctx, viewer.watchlistViewer()); err != nil || len(promoted) != 1 {
		t.Fatalf("PromoteProfile = %v %v", promoted, err)
	}
	check(false, "promoted onto the library watchlist")

	if err := store.RemoveFromWatchlist(ctx, "p1", prefix); err != nil {
		t.Fatal(err)
	}
	check(true, "library watchlist entry removed")
}
