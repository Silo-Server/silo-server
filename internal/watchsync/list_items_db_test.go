package watchsync

import (
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// Two library items can carry the same provider key, such as two items for one
// title with the same IMDb id. Each keeps its own shadow row.
func TestListItemsSharingProviderKeyDB(t *testing.T) {
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
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-lists-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'lists-p','Lists')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-lists-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "lists", UserID: userID, ProfileID: "lists-p", AccessToken: "token",
		ExportFavoritesEnabled: true, SyncFavoriteRemovalsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Copies of one title (a, b), a series with the same key (series), and a
	// key shared by a live item (d) and one deleted from the catalog (gone),
	// which therefore has no media_items row.
	suffix := uuid.NewString()
	a, b, series, gone, d := "movie:a-"+suffix, "movie:b-"+suffix, "series:s-"+suffix, "movie:gone-"+suffix, "movie:d-"+suffix
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id,type,title,genres) VALUES ($1,'movie','A','{}'),($2,'movie','B','{}'),($3,'series','S','{}'),($4,'movie','D','{}')`, a, b, series, d); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id = ANY($1)", []string{a, b, series, d})
	}()
	now := time.Now().UTC()
	state := func(mediaItemID, kind, key string) ListItemState {
		return ListItemState{
			ConnectionID: conn.ID, ListKind: ListKindFavorites, MediaItemID: mediaItemID,
			ProviderItemKey: key, Kind: kind, LocalPresent: true, LastSeenLocalAt: &now,
		}
	}
	states := []ListItemState{
		state(a, "movie", "tmdb:550"), state(b, "movie", "tmdb:550"), state(series, "series", "tmdb:550"),
		state(gone, "movie", "imdb:tt0000001"), state(d, "movie", "imdb:tt0000001"),
	}
	if err := repo.UpsertListItemStates(ctx, states); err != nil {
		t.Fatalf("upsert items sharing a key: %v", err)
	}
	pending, err := repo.ListPendingListItemExports(ctx, conn.ID, ListKindFavorites, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(states) {
		t.Fatalf("pending exports = %d, want %d", len(pending), len(states))
	}
	for _, state := range states {
		if err := repo.MarkListItemExported(ctx, conn.ID, ListKindFavorites, state.MediaItemID, now); err != nil {
			t.Fatal(err)
		}
	}
	pendingRemovals := func() []string {
		t.Helper()
		removals, err := repo.ListPendingListItemRemovals(ctx, conn.ID, ListKindFavorites, 10)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(removals))
		for _, removal := range removals {
			ids = append(ids, removal.MediaItemID)
		}
		sort.Strings(ids)
		return ids
	}
	localRemove := func(ids ...string) {
		t.Helper()
		for _, id := range ids {
			if err := repo.MarkListItemLocalRemoved(ctx, conn.ID, ListKindFavorites, id, now); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Copy a leaves the local list while copy b still holds the key.
	localRemove(a)
	if got := pendingRemovals(); len(got) != 0 {
		t.Fatalf("pending removals = %v, want none while b holds the key", got)
	}
	held, err := repo.ListListItemsWithSharedKeyHeld(ctx, conn.ID, ListKindFavorites, []string{a, b, series})
	if err != nil {
		t.Fatal(err)
	}
	if !held[a] || held[b] || held[series] {
		t.Fatalf("held = %v, want only a", held)
	}

	// Once b leaves too, both copies go; the series is a different title and
	// does not hold the movie's key.
	localRemove(b)
	if got, want := pendingRemovals(), []string{a, b}; !slices.Equal(got, want) {
		t.Fatalf("pending removals = %v, want %v", got, want)
	}

	// An item deleted from the catalog no longer holds its key.
	if err := repo.MarkListItemRemoteRemoved(ctx, conn.ID, ListKindFavorites, a, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkListItemRemoteRemoved(ctx, conn.ID, ListKindFavorites, b, now); err != nil {
		t.Fatal(err)
	}
	localRemove(d)
	if got, want := pendingRemovals(), []string{d}; !slices.Equal(got, want) {
		t.Fatalf("pending removals = %v, want %v", got, want)
	}
}
