package watchsync

import (
	"os"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestRatingSyncRepositoryDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-ratings-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'ratings-p','Ratings')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-ratings-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)

	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "ratings", UserID: userID, ProfileID: "ratings-p", AccessToken: "token",
		ImportRatingsEnabled: true, ExportRatingsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.ImportRatingsEnabled || !conn.ExportRatingsEnabled {
		t.Fatalf("inserted rating toggles = %v/%v, want on", conn.ImportRatingsEnabled, conn.ExportRatingsEnabled)
	}

	t.Run("toggles are insert-only on upsert", func(t *testing.T) {
		again := conn
		again.ImportRatingsEnabled, again.ExportRatingsEnabled = false, false
		saved, err := repo.UpsertConnection(ctx, again)
		if err != nil {
			t.Fatal(err)
		}
		if !saved.ImportRatingsEnabled || !saved.ExportRatingsEnabled {
			t.Fatal("a token upsert must not change rating toggles")
		}
	})

	t.Run("settings update and event connections", func(t *testing.T) {
		updated, err := repo.UpdateConnectionSettings(ctx, "ratings", userID, "ratings-p", nil, ConnectionUpdate{ExportRatingsEnabled: new(false)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !updated.ImportRatingsEnabled || updated.ExportRatingsEnabled || !updated.UpdatedAt.After(conn.UpdatedAt) {
			t.Fatalf("updated = import %v export %v", updated.ImportRatingsEnabled, updated.ExportRatingsEnabled)
		}
		conns, err := repo.ListRatingEventConnections(ctx, userID, "ratings-p")
		if err != nil {
			t.Fatal(err)
		}
		if len(conns) != 0 {
			t.Fatalf("event connections with export off = %d, want 0", len(conns))
		}
		if _, err := repo.UpdateConnectionSettings(ctx, "ratings", userID, "ratings-p", nil, ConnectionUpdate{ExportRatingsEnabled: new(true)}, nil); err != nil {
			t.Fatal(err)
		}
		conns, err = repo.ListRatingEventConnections(ctx, userID, "ratings-p")
		if err != nil {
			t.Fatal(err)
		}
		if len(conns) != 1 || conns[0].ID != conn.ID {
			t.Fatalf("event connections = %#v, want the rating connection", conns)
		}
		due, err := repo.ListConnectionsDueForSync(ctx, conn.UpdatedAt)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range due {
			found = found || candidate.ID == conn.ID
		}
		if !found {
			t.Fatal("a connection with only rating sync on must be due for sync")
		}
	})

	t.Run("sync run counters round-trip", func(t *testing.T) {
		run, err := repo.CreateSyncRun(ctx, SyncRun{ConnectionID: conn.ID, Trigger: "test", Provider: "ratings", InboundRatingsFound: 1})
		if err != nil {
			t.Fatal(err)
		}
		run.InboundRatingsFound, run.InboundRatingsImported, run.OutboundRatingsFound, run.OutboundRatingsSent = 4, 3, 2, 1
		run.Status = string(SyncRunStatusSuccess)
		completed, err := repo.CompleteSyncRun(ctx, run)
		if err != nil {
			t.Fatal(err)
		}
		if completed.InboundRatingsFound != 4 || completed.InboundRatingsImported != 3 || completed.OutboundRatingsFound != 2 || completed.OutboundRatingsSent != 1 {
			t.Fatalf("completed run counters = %#v", completed)
		}
	})

	t.Run("agreed ratings", func(t *testing.T) {
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{
			{ConnectionID: conn.ID, MediaItemID: "m-1", Kind: "movie", ProviderItemKey: "imdb:tt1", SyncedRating: 4},
			{ConnectionID: conn.ID, MediaItemID: "s-1", Kind: "series", ProviderItemKey: "tvdb:1", SyncedRating: 2, RemoteSeen: true},
		}); err != nil {
			t.Fatal(err)
		}
		// Updating keeps the recorded identity when the update carries none.
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{{ConnectionID: conn.ID, MediaItemID: "m-1", SyncedRating: 5, RemoteSeen: true}}); err != nil {
			t.Fatal(err)
		}
		all, err := repo.ListRatingSyncStates(ctx, conn.ID, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].MediaItemID < all[j].MediaItemID })
		want := []RatingSyncState{
			{ConnectionID: conn.ID, MediaItemID: "m-1", Kind: "movie", ProviderItemKey: "imdb:tt1", SyncedRating: 5, RemoteSeen: true},
			{ConnectionID: conn.ID, MediaItemID: "s-1", Kind: "series", ProviderItemKey: "tvdb:1", SyncedRating: 2, RemoteSeen: true},
		}
		if len(all) != 2 || all[0] != want[0] || all[1] != want[1] {
			t.Fatalf("states = %#v, want %#v", all, want)
		}
		only, err := repo.ListRatingSyncStates(ctx, conn.ID, "", []string{"s-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(only) != 1 || only[0].MediaItemID != "s-1" {
			t.Fatalf("filtered states = %#v", only)
		}
		if err := repo.DeleteRatingSyncStates(ctx, conn.ID, "other-account", []string{"m-1"}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "", nil); len(states) != 2 {
			t.Fatalf("another account's delete removed rows: %#v", states)
		}
		if err := repo.DeleteRatingSyncStates(ctx, conn.ID, "", []string{"m-1"}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "", nil); len(states) != 1 {
			t.Fatalf("states after delete = %#v", states)
		}
		if err := repo.ClearRatingSyncStates(ctx, conn.ID, "new-account"); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "", nil); len(states) != 0 {
			t.Fatalf("states after clear = %#v", states)
		}
	})

	bind := func(t *testing.T, account string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE watch_provider_connections SET provider_account_id=$2 WHERE id=$1::uuid`, conn.ID, account); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("agreed ratings are scoped to the provider account", func(t *testing.T) {
		bind(t, "account-a")
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{
			{ConnectionID: conn.ID, ProviderAccountID: "account-a", MediaItemID: "m-3", Kind: "movie", SyncedRating: 3},
		}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-b", nil); len(states) != 0 {
			t.Fatalf("another account's rows = %#v, want none", states)
		}
		states, err := repo.ListRatingSyncStates(ctx, conn.ID, "account-a", nil)
		if err != nil || len(states) != 1 || states[0].ProviderAccountID != "account-a" {
			t.Fatalf("account rows = %#v (%v)", states, err)
		}
		// Re-agreeing under the new account takes the row over.
		bind(t, "account-b")
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{
			{ConnectionID: conn.ID, ProviderAccountID: "account-b", MediaItemID: "m-3", Kind: "movie", SyncedRating: 5},
		}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-a", nil); len(states) != 0 {
			t.Fatalf("old account still sees %#v", states)
		}
		// A run still writing for the previous account changes nothing.
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{
			{ConnectionID: conn.ID, ProviderAccountID: "account-a", MediaItemID: "m-3", Kind: "movie", SyncedRating: 1},
			{ConnectionID: conn.ID, ProviderAccountID: "account-a", MediaItemID: "m-5", Kind: "movie", SyncedRating: 1},
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.DeleteRatingSyncStates(ctx, conn.ID, "account-a", []string{"m-3"}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-b", nil); len(states) != 1 || states[0].SyncedRating != 5 {
			t.Fatalf("bound account rows after a stale write = %#v, want m-3 at 5", states)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-a", nil); len(states) != 0 {
			t.Fatalf("stale write recorded %#v", states)
		}
		// Clearing keeps the bound account's rows and drops the others.
		if _, err := pool.Exec(ctx, `INSERT INTO watch_provider_rating_items (connection_id, provider_account_id, media_item_id, kind, synced_rating) VALUES ($1::uuid, 'account-a', 'm-4', 'movie', 2)`, conn.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.ClearRatingSyncStates(ctx, conn.ID, "account-b"); err != nil {
			t.Fatal(err)
		}
		if kept, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-b", nil); len(kept) != 1 {
			t.Fatalf("bound account rows after clear = %#v, want m-3 kept", kept)
		}
		if old, _ := repo.ListRatingSyncStates(ctx, conn.ID, "account-a", nil); len(old) != 0 {
			t.Fatalf("previous account rows after clear = %#v", old)
		}
		if err := repo.ClearRatingSyncStates(ctx, conn.ID, "none"); err != nil {
			t.Fatal(err)
		}
		bind(t, "")
	})

	t.Run("rating cursors update in place for the bound account only", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE watch_provider_connections SET provider_account_id='acct', sync_cursors='{"trakt.watched":"w","test.ratings.movies":"old"}' WHERE id=$1::uuid`, conn.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateRatingCursors(ctx, conn.ID, "acct", []string{"test.ratings.movies"}, map[string]string{"test.ratings.shows": "s1"}); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpdateRatingCursors(ctx, conn.ID, "other-acct", nil, map[string]string{"stale": "x"}); err != nil {
			t.Fatal(err)
		}
		fresh, ok, err := repo.GetConnectionByID(ctx, conn.ID)
		if err != nil || !ok {
			t.Fatalf("reload: %v", err)
		}
		want := map[string]string{"trakt.watched": "w", "test.ratings.shows": "s1"}
		if len(fresh.SyncCursors) != len(want) || fresh.SyncCursors["trakt.watched"] != "w" || fresh.SyncCursors["test.ratings.shows"] != "s1" {
			t.Fatalf("cursors = %#v, want %#v", fresh.SyncCursors, want)
		}
	})

	t.Run("connection delete cascades", func(t *testing.T) {
		bind(t, "")
		if err := repo.UpsertRatingSyncStates(ctx, []RatingSyncState{{ConnectionID: conn.ID, MediaItemID: "m-2", Kind: "movie", SyncedRating: 3}}); err != nil {
			t.Fatal(err)
		}
		if states, _ := repo.ListRatingSyncStates(ctx, conn.ID, "", nil); len(states) != 1 {
			t.Fatalf("states before delete = %#v, want one", states)
		}
		if err := repo.DeleteConnection(ctx, "ratings", userID, "ratings-p"); err != nil {
			t.Fatal(err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM watch_provider_rating_items WHERE connection_id=$1::uuid", conn.ID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("agreed ratings left after connection delete: %d", remaining)
		}
	})
}
