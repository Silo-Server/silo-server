package watchsync

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

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

	t.Run("rating sync lock serializes across nodes", func(t *testing.T) {
		// Each repository stands in for one node: its own process-local slots,
		// one shared database.
		other := NewPostgresRepository(pool, cipher)
		held := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := repo.WithRatingSyncLock(ctx, conn.ID, false, func(context.Context) error {
				close(held)
				<-release
				return nil
			})
			done <- err
		}()
		<-held
		// Another node cannot take the lock while the first holds it.
		locked, err := other.WithRatingSyncLock(ctx, conn.ID, false, func(context.Context) error {
			t.Error("ran while another node held the lock")
			return nil
		})
		if err != nil || locked {
			t.Fatalf("try while held = %v, %v; want not locked", locked, err)
		}
		short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		if _, err := other.WithRatingSyncLock(short, conn.ID, true, func(context.Context) error { return nil }); err == nil {
			t.Fatal("a wait while held returned before its deadline")
		}
		// Another connection's lock is independent.
		if locked, err := other.WithRatingSyncLock(ctx, "00000000-0000-0000-0000-000000000000", false, func(context.Context) error { return nil }); err != nil || !locked {
			t.Fatalf("other connection lock = %v, %v", locked, err)
		}
		// A waiter on another node proceeds once the holder releases.
		waited := make(chan bool, 1)
		go func() {
			locked, _ := other.WithRatingSyncLock(ctx, conn.ID, true, func(context.Context) error { return nil })
			waited <- locked
		}()
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !<-waited {
			t.Fatal("waiter did not get the lock after release")
		}
		// An error from fn still releases the lock.
		boom := errors.New("boom")
		if _, err := repo.WithRatingSyncLock(ctx, conn.ID, false, func(context.Context) error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("fn error = %v", err)
		}
		if locked, err := other.WithRatingSyncLock(ctx, conn.ID, false, func(context.Context) error { return nil }); err != nil || !locked {
			t.Fatalf("lock after an fn error = %v, %v; want it released", locked, err)
		}
	})

	t.Run("rating sync lock leaves a one-connection pool usable", func(t *testing.T) {
		config, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.MaxConns = 1
		single, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer single.Close()
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		locked, err := NewPostgresRepository(single, cipher).WithRatingSyncLock(bounded, conn.ID, true, func(ctx context.Context) error {
			var one int
			return single.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
		if err != nil || !locked {
			t.Fatalf("lock with a one-connection pool = %v, %v; want fn to reach the pool", locked, err)
		}
	})

	t.Run("rating sync lock sessions are capped per node", func(t *testing.T) {
		config, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.MaxConns = 1
		single, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		defer single.Close()
		node := NewPostgresRepository(single, cipher)
		held := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := node.WithRatingSyncLock(ctx, conn.ID, false, func(context.Context) error {
				close(held)
				<-release
				return nil
			})
			done <- err
		}()
		<-held
		// A one-connection pool allows one lock session, so a lock for a
		// different connection waits for it.
		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		if _, err := node.WithRatingSyncLock(short, "00000000-0000-0000-0000-000000000001", true, func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second session = %v, want it to wait for the cap", err)
		}
		// A try does not wait for the cap: it reports the lock busy at once.
		started := time.Now()
		if locked, err := node.WithRatingSyncLock(ctx, "00000000-0000-0000-0000-000000000001", false, func(context.Context) error { return nil }); err != nil || locked {
			t.Fatalf("try at the cap = %v, %v; want not locked", locked, err)
		}
		if waited := time.Since(started); waited > time.Second {
			t.Fatalf("try at the cap took %v, want it immediate", waited)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if locked, err := node.WithRatingSyncLock(ctx, "00000000-0000-0000-0000-000000000001", false, func(context.Context) error { return nil }); err != nil || !locked {
			t.Fatalf("lock after the first session closed = %v, %v", locked, err)
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
