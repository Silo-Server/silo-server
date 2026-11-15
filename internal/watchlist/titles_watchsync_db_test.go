package watchlist

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// TestWatchSyncFindsThePromotedItemByKeptIDsDB pins the IDs watch sync keeps
// for a title and how it finds the library item once promotion has deleted
// the title. TMDB repointed the movie after the provider listed it, and the
// library item holds only the new TMDB ID; a movie's TMDB ID decides its
// match, so the listed TMDB ID and the IMDb ID alone no longer find it.
func TestWatchSyncFindsThePromotedItemByKeptIDsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	base := 720000000 + int(time.Now().UnixNano()%1000000)
	listedTMDB, newTMDB := base, base+1
	imdbID := "tt" + strconv.Itoa(base)

	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "wlws-"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user) })
	stores := pgstore.NewPostgresProvider(pool)
	store, err := stores.ForUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	profile := "p-" + sfx
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}

	titles := NewTitles(pool, catalog.NewItemRepository(pool), stores, nil, nil)
	sync := titles.WatchSync(nil)
	listed := watchsync.WatchlistTitle{Kind: mediaTypeMovie, TMDBID: listedTMDB, IMDbID: imdbID, Title: "Movie"}
	if _, err := sync.AddWatchlistTitle(ctx, user, profile, listed, time.Now().UTC(), false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles t USING watchlist_title_aliases a
			WHERE a.title_id = t.id AND a.media_type = 'movie' AND a.provider = 'tmdb' AND a.provider_id = $1`, strconv.Itoa(listedTMDB))
	})
	stored, err := titles.Find(ctx, mediaTypeMovie, listedTMDB)
	if err != nil || stored == nil {
		t.Fatalf("stored title = %v, %v", stored, err)
	}
	if _, err := titles.repo.repoint(ctx, stored.ID, listedTMDB, newTMDB, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// What sync keeps on the title's row while the entry exists.
	held, err := sync.WatchlistTitleIDs(ctx, user, profile, []watchsync.WatchlistTitle{listed})
	if err != nil {
		t.Fatal(err)
	}
	kept := slices.Sorted(slices.Values(held[listed]))
	want := []string{"imdb:" + imdbID, "tmdb:" + strconv.Itoa(listedTMDB), "tmdb:" + strconv.Itoa(newTMDB)}
	slices.Sort(want)
	if !slices.Equal(kept, want) {
		t.Fatalf("title ids = %v, want %v", kept, want)
	}

	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movie', $1, true) RETURNING id`, "wlws-"+sfx).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	movie := "wlws-movie-" + sfx
	exec(`INSERT INTO media_items (content_id, type, title, tmdb_id, imdb_id) VALUES ($1, 'movie', 'Movie', $2, $3)`,
		movie, strconv.Itoa(newTMDB), imdbID)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, movie, folder)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, movie)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder)
	})
	viewer := Viewer{UserID: user, ProfileID: profile, Access: catalog.AccessFilter{UserID: user, ProfileID: profile, AllowedLibraryIDs: []int{folder}}}
	if _, err := titles.PromoteProfile(ctx, viewer); err != nil {
		t.Fatal(err)
	}
	if gone, err := titles.Find(ctx, mediaTypeMovie, listedTMDB); err != nil || gone != nil {
		t.Fatalf("title after promotion = %v, %v; want it deleted with its aliases", gone, err)
	}

	promoted, err := sync.PromotedWatchlistItems(ctx, user, profile, listed, kept)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(promoted, []string{movie}) {
		t.Fatalf("promoted items = %v, want [%s]", promoted, movie)
	}
}
