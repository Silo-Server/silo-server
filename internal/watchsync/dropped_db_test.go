package watchsync

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestDroppedSyncRepositoryDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-dropped-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'dropped-p','Dropped')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-dropped-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)

	// Only dropped-show sync is on, so the connection is due for that alone.
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "dropped", UserID: userID, ProfileID: "dropped-p", AccessToken: "token",
		ProviderAccountID: "acct-1", SyncDroppedEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !conn.SyncDroppedEnabled {
		t.Fatal("inserted dropped toggle is off")
	}
	due, err := repo.ListConnectionsDueForSync(ctx, conn.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range due {
		found = found || c.ID == conn.ID
	}
	if !found {
		t.Fatal("a connection syncing only dropped shows must be due")
	}

	again := conn
	again.SyncDroppedEnabled = false
	if saved, err := repo.UpsertConnection(ctx, again); err != nil || !saved.SyncDroppedEnabled {
		t.Fatalf("a token upsert must not change the dropped toggle: %v %v", saved.SyncDroppedEnabled, err)
	}
	events, err := repo.ListDroppedEventConnections(ctx, userID, "dropped-p")
	if err != nil || len(events) != 1 {
		t.Fatalf("event connections = %d, %v; want 1", len(events), err)
	}
	if _, err := repo.UpdateConnectionSettings(ctx, "dropped", userID, "dropped-p", nil, ConnectionUpdate{SyncDroppedEnabled: new(false)}, nil); err != nil {
		t.Fatal(err)
	}
	if events, err := repo.ListDroppedEventConnections(ctx, userID, "dropped-p"); err != nil || len(events) != 0 {
		t.Fatalf("event connections with sync off = %d, %v; want 0", len(events), err)
	}

	// States are fenced to the bound account and keep a known provider key.
	if err := repo.UpsertDroppedSyncStates(ctx, []DroppedSyncState{
		{ConnectionID: conn.ID, ProviderAccountID: "acct-1", SeriesID: "series-1", ProviderItemKey: "tvdb:1"},
		{ConnectionID: conn.ID, ProviderAccountID: "acct-old", SeriesID: "series-2", ProviderItemKey: "tvdb:2"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDroppedSyncStates(ctx, []DroppedSyncState{
		{ConnectionID: conn.ID, ProviderAccountID: "acct-1", SeriesID: "series-1", RemoteSeen: true},
	}); err != nil {
		t.Fatal(err)
	}
	states, err := repo.ListDroppedSyncStates(ctx, conn.ID, "acct-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].SeriesID != "series-1" || !states[0].RemoteSeen || states[0].ProviderItemKey != "tvdb:1" {
		t.Fatalf("states = %#v, want series-1 seen with its key", states)
	}
	if err := repo.DeleteDroppedSyncStates(ctx, conn.ID, "acct-1", []string{"series-1"}); err != nil {
		t.Fatal(err)
	}
	if states, _ := repo.ListDroppedSyncStates(ctx, conn.ID, "acct-1", nil); len(states) != 0 {
		t.Fatalf("states after delete = %#v", states)
	}
}
