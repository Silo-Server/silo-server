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
)

// TestLibraryMatchesUsesTheTitlesRetainedIDsDB pins that the add and remove
// paths' library match checks a stored title's retained IDs, as promotion
// does. After TVDB remaps a series, the title keeps its former TVDB ID as an
// alias; promotion moves it onto a library show holding that ID, so removing
// the title must find the same show or its library watchlist entry stays.
func TestLibraryMatchesUsesTheTitlesRetainedIDsDB(t *testing.T) {
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
	base := 710000000 + int(time.Now().UnixNano()%1000000)
	tmdbID, oldTVDB, newTVDB, otherTMDB := base, base+1, base+2, base+3
	imdbID := "tt" + strconv.Itoa(base)

	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "wllm-"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user) })
	profile := "p-" + sfx
	exec(`INSERT INTO user_profiles (id, user_id, name, is_primary) VALUES ($1, $2, $1, true)`, profile, user)

	// The library show carries the title's former TVDB ID and IMDb ID, and a
	// TMDB ID of its own.
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, "wllm-"+sfx).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	show := "wllm-show-" + sfx
	exec(`INSERT INTO media_items (content_id, type, title, tmdb_id, tvdb_id, imdb_id) VALUES ($1, 'series', 'Show', $2, $3, $4)`,
		show, strconv.Itoa(otherTMDB), strconv.Itoa(oldTVDB), imdbID)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, show, folder)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, show)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folder)
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE media_type = 'series' AND tmdb_id = $1`, tmdbID)
	})

	titles := NewTitles(pool, catalog.NewItemRepository(pool), nil, nil, nil)
	entry, _, err := titles.AddSnapshot(ctx, Viewer{UserID: user, ProfileID: profile},
		Snapshot{MediaType: mediaTypeSeries, TMDBID: tmdbID, IMDbID: imdbID, TVDBID: oldTVDB, Title: "Show"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	// TVDB remapped the series: the title now carries the new TVDB ID and
	// keeps the old one as an alias.
	exec(`UPDATE watchlist_titles SET tvdb_id = $2 WHERE id = $1`, entry.Title.ID, newTVDB)
	exec(`INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id) VALUES ($1, 'series', 'tvdb', $2)`,
		entry.Title.ID, strconv.Itoa(newTVDB))

	// What the remove path passes for the stored title.
	got, err := titles.LibraryMatches(ctx, mediaTypeSeries, []int{tmdbID}, imdbID, newTVDB)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{show}) {
		t.Fatalf("LibraryMatches = %v, want [%s], the show promotion moves the title onto", got, show)
	}
}
