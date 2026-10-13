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

// failingRemoveProvider hands out stores whose library watchlist remove fails
// while fail is set.
type failingRemoveProvider struct {
	userstore.UserStoreProvider
	fail *bool
}

func (p failingRemoveProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	store, err := p.UserStoreProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return failingRemoveStore{UserStore: store, fail: p.fail}, nil
}

type failingRemoveStore struct {
	userstore.UserStore
	fail *bool
}

func (s failingRemoveStore) RemoveFromWatchlist(ctx context.Context, profileID, mediaItemID string) error {
	if *s.fail {
		return fmt.Errorf("user store unavailable")
	}
	return s.UserStore.RemoveFromWatchlist(ctx, profileID, mediaItemID)
}

// A delete whose library step fails leaves the title's entry, and with it
// every TMDB ID the title has had, so a retry still finds a library item
// known only by a former ID and takes both off.
func TestRemoveWatchlistTitleRetryKeepsFormerIDsDB(t *testing.T) {
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
	prefix := fmt.Sprintf("wlt-retry-%d", stamp)
	former := 1_600_000_000 + int(stamp/1000%100_000)
	current := former + 1
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
		_, _ = pool.Exec(bg, `DELETE FROM watchlist_titles WHERE tmdb_id IN ($1, $2)`, former, current)
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = $1`, prefix)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	fail := true
	provider := failingRemoveProvider{UserStoreProvider: pgstore.NewPostgresProvider(pool), fail: &fail}
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "p1"}); err != nil {
		t.Fatal(err)
	}
	items := catalog.NewItemRepository(pool)
	titles := watchlist.NewTitles(pool, items, provider, nil, nil)
	h := NewPersonalDataHandler(provider, items)
	h.SetWatchlistTitles(titles)
	viewer := PersonalListViewer{UserID: userID, ProfileID: "p1"}

	// The entry was added under the former ID and TMDB repointed it since.
	entry, _, err := titles.AddSnapshot(ctx, viewer.watchlistViewer(), watchlist.Snapshot{MediaType: "movie", TMDBID: former, Title: "Repointed"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id) VALUES ($1, 'movie', 'tmdb', $2)`,
		entry.Title.ID, strconv.Itoa(current)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE watchlist_titles SET tmdb_id = $2 WHERE id = $1`, entry.Title.ID, current); err != nil {
		t.Fatal(err)
	}
	// The library copy carries only the former ID and is on the library watchlist.
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id) VALUES ($1, 'movie', $1, '{}'::text[], $2)`,
		prefix, strconv.Itoa(former)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, prefix, folder); err != nil {
		t.Fatal(err)
	}
	if err := store.AddToWatchlist(ctx, "p1", prefix); err != nil {
		t.Fatal(err)
	}

	if _, err := h.RemoveWatchlistTitle(ctx, viewer, "movie", current); err == nil {
		t.Fatal("a failed library removal answered success")
	}
	if found, err := titles.Find(ctx, "movie", current); err != nil || found == nil {
		t.Fatalf("after the failure the title = %v %v, want it kept for the retry", found, err)
	}

	fail = false
	if _, err := h.RemoveWatchlistTitle(ctx, viewer, "movie", current); err != nil {
		t.Fatal(err)
	}
	if onList, err := store.GetWatchlistEntry(ctx, "p1", prefix); err != nil || onList != nil {
		t.Fatalf("library watchlist entry = %v %v, want removed through the former ID", onList, err)
	}
	if found, err := titles.Find(ctx, "movie", current); err != nil || found != nil {
		t.Fatalf("title = %v %v, want gone after the retry", found, err)
	}
}

// Promotion scans the whole profile, so a paged titles read runs it once, on
// the first page: a later page leaves an arrived title where it is, and the
// next first page moves it.
func TestListWatchlistTitlesPagePromotesOnFirstPageOnlyDB(t *testing.T) {
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
	prefix := fmt.Sprintf("wlt-page-%d", stamp)
	tmdbID := 1_700_000_000 + int(stamp/1000%100_000)
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
	titles := watchlist.NewTitles(pool, items, provider, nil, nil)
	h := NewPersonalDataHandler(provider, items)
	h.SetWatchlistTitles(titles)
	viewer := PersonalListViewer{UserID: userID, ProfileID: "p1"}
	if _, _, err := titles.AddSnapshot(ctx, viewer.watchlistViewer(), watchlist.Snapshot{MediaType: "movie", TMDBID: tmdbID, Title: "Arriving"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, genres, tmdb_id) VALUES ($1, 'movie', $1, '{}'::text[], $2)`,
		prefix, strconv.Itoa(tmdbID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, prefix, folder); err != nil {
		t.Fatal(err)
	}

	later := &watchlist.PageKey{AddedAt: time.Now().Add(time.Hour), TitleID: 1 << 62}
	if _, err := h.ListWatchlistTitlesPage(ctx, viewer, later, 10); err != nil {
		t.Fatal(err)
	}
	if onList, err := store.GetWatchlistEntry(ctx, "p1", prefix); err != nil || onList != nil {
		t.Fatalf("a later page promoted the title: %v %v", onList, err)
	}
	if _, err := h.ListWatchlistTitlesPage(ctx, viewer, nil, 10); err != nil {
		t.Fatal(err)
	}
	if onList, err := store.GetWatchlistEntry(ctx, "p1", prefix); err != nil || onList == nil {
		t.Fatalf("the first page did not promote the title: %v %v", onList, err)
	}
}
