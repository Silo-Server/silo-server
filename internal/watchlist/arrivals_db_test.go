package watchlist

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// memoryWatchlist is a library watchlist outside PostgreSQL, as the per-user
// SQLite store keeps it.
type memoryWatchlist map[string]time.Time

func (m memoryWatchlist) AddToWatchlistAt(_ context.Context, _, mediaItemID string, addedAt time.Time) (bool, error) {
	if _, ok := m[mediaItemID]; ok {
		return false, nil
	}
	m[mediaItemID] = addedAt
	return true, nil
}

func (m memoryWatchlist) GetWatchlistEntry(_ context.Context, profileID, mediaItemID string) (*userstore.WatchlistEntry, error) {
	at, ok := m[mediaItemID]
	if !ok {
		return nil, nil
	}
	return &userstore.WatchlistEntry{ProfileID: profileID, MediaItemID: mediaItemID, AddedAt: at.UTC().Format(time.RFC3339Nano)}, nil
}

func (m memoryWatchlist) ListWatchlistByMediaItems(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		_, out[id] = m[id]
	}
	return out, nil
}

// TestArrivalFindsAnEntryPromotedOutsidePostgresDB pins the arrival read for a
// profile whose watchlist read promoted its entry before the notification
// fanout ran, on a user store outside PostgreSQL. Promotion deleted the title
// entry and wrote the library entry where the fanout cannot query it; the
// promotion record still names the profile. Retention past the horizon drops
// the record.
func TestArrivalFindsAnEntryPromotedOutsidePostgresDB(t *testing.T) {
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
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	sfx := hex.EncodeToString(b)
	tmdbID := 730000000 + int(time.Now().UnixNano()%1000000)

	var user int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, permissions) VALUES ($1::text, $1::text || '@example.test', 'h', 'user', '{}') RETURNING id`, "wlar-"+sfx).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user) })
	profile := "p-" + sfx

	library := memoryWatchlist{}
	titles := NewTitles(pool, catalog.NewItemRepository(pool), nil, nil, nil)
	titles.storeFor = func(context.Context, int) (promotionStore, error) { return library, nil }
	addedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	listed := watchsync.WatchlistTitle{Kind: mediaTypeMovie, TMDBID: tmdbID, Title: "Movie"}
	if _, err := titles.WatchSync(nil).AddWatchlistTitle(ctx, user, profile, listed, addedAt, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM watchlist_titles t USING watchlist_title_aliases a
			WHERE a.title_id = t.id AND a.media_type = 'movie' AND a.provider = 'tmdb' AND a.provider_id = $1`, strconv.Itoa(tmdbID))
	})

	folder, movie := seedLibraryMovie(ctx, t, pool, sfx, tmdbID, "")
	arrivals := &Arrivals{}
	arrivedSince := time.Now().Add(-72 * time.Hour)
	waiting := waitersFor(t, arrivals, pool, movie, arrivedSince)
	want := Waiter{UserID: user, ProfileID: profile, MediaType: mediaTypeMovie, TMDBIDs: []int{tmdbID}}
	if got := waiting; len(got) != 1 || got[0].UserID != want.UserID || got[0].ProfileID != want.ProfileID || len(got[0].TMDBIDs) != 1 || got[0].TMDBIDs[0] != tmdbID {
		t.Fatalf("waiters before promotion = %+v, want [%+v]", got, want)
	}

	// The title once had another TMDB ID, which the library item does not
	// carry. Promotion keeps it for the request check.
	formerTMDB := tmdbID + 1
	if _, err := pool.Exec(ctx, `
		INSERT INTO watchlist_title_aliases (title_id, media_type, provider, provider_id)
		SELECT a.title_id, a.media_type, 'tmdb', $2 FROM watchlist_title_aliases a
		WHERE a.media_type = 'movie' AND a.provider = 'tmdb' AND a.provider_id = $1`,
		strconv.Itoa(tmdbID), strconv.Itoa(formerTMDB)); err != nil {
		t.Fatal(err)
	}

	viewer := Viewer{UserID: user, ProfileID: profile, Access: catalog.AccessFilter{UserID: user, ProfileID: profile, AllowedLibraryIDs: []int{folder}}}
	promoted, err := titles.PromoteProfile(ctx, viewer)
	if err != nil {
		t.Fatal(err)
	}
	if len(promoted) != 1 || promoted[0] != movie {
		t.Fatalf("promoted = %v, want [%s]", promoted, movie)
	}
	if _, ok := library[movie]; !ok {
		t.Fatal("promotion did not write the library entry to the profile's store")
	}

	waiting = waitersFor(t, arrivals, pool, movie, arrivedSince)
	if got := waiting; len(got) != 1 || got[0].UserID != user || got[0].ProfileID != profile ||
		len(got[0].TMDBIDs) != 2 || got[0].TMDBIDs[0] != tmdbID || got[0].TMDBIDs[1] != formerTMDB {
		t.Fatalf("waiters after promotion = %+v, want the profile with TMDB IDs [%d %d]", got, tmdbID, formerTMDB)
	}

	// An item older than the horizon has not arrived.
	waiting = waitersFor(t, arrivals, pool, movie, time.Now().Add(time.Minute))
	if len(waiting) != 0 {
		t.Fatalf("waiters for an item older than the horizon = %+v, want none", waiting)
	}

	// Taking the item off the library watchlist forgets that profile's
	// promotion and leaves another profile's.
	if _, err := pool.Exec(ctx, `
		INSERT INTO watchlist_title_promotions (user_id, profile_id, media_item_id, title_added_at)
		VALUES ($1, 'other', $2, now() - interval '30 days')`, user, movie); err != nil {
		t.Fatal(err)
	}
	if err := ForgetPromotion(ctx, pool, user, profile, movie); err != nil {
		t.Fatal(err)
	}
	waiting = waitersFor(t, arrivals, pool, movie, arrivedSince)
	if got := waiting; len(got) != 1 || got[0].ProfileID != "other" {
		t.Fatalf("waiters after the profile removed the item = %+v, want only the other profile", got)
	}

	if _, err := arrivals.PrunePromotions(ctx, pool, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM watchlist_title_promotions WHERE user_id = $1`, user).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("promotion records after pruning = %d, want 0", left)
	}
}

// waitersFor returns the profiles waiting for the item, as the notification
// fanout reads them.
func waitersFor(t *testing.T, arrivals *Arrivals, q Querier, contentID string, arrivedSince time.Time) []Waiter {
	t.Helper()
	waiting, err := arrivals.WaitingFor(t.Context(), q, []string{contentID}, arrivedSince)
	if err != nil {
		t.Fatal(err)
	}
	return waiting[contentID]
}
