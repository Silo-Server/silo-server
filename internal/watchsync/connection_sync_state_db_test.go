package watchsync

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestConnectionSyncStateUpdatesDB(t *testing.T) {
	ctx := t.Context()
	repo, saved, now := newSyncStateTestConnection(t)
	got := checkSyncStateKeepsRefreshedTokens(t, repo, saved, now)
	checkSyncStateDeferral(t, repo, saved.ID, got, now)

	// A disconnect during a sync stays a disconnect.
	if err := repo.DeleteConnection(ctx, saved.Provider, saved.UserID, saved.ProfileID); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateConnectionSyncState(ctx, saved.ID, connectionError("too late")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateConnectionSyncState(ctx, "", connectionError("no connection")); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := repo.GetConnection(ctx, saved.Provider, saved.UserID, saved.ProfileID); err != nil || exists {
		t.Fatalf("disconnected connection restored: exists=%v err=%v", exists, err)
	}
}

// newSyncStateTestConnection saves a connection for a new user and profile,
// removed when the test ends.
func newSyncStateTestConnection(t *testing.T) (*PostgresRepository, Connection, time.Time) {
	t.Helper()
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
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "sync-state-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID) })
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'Sync state')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("sync-state-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	now := time.Now().UTC().Truncate(time.Second)
	saved, err := repo.UpsertConnection(ctx, Connection{
		Provider: "trakt", UserID: userID, ProfileID: profileID, ProviderAccountID: "account",
		AccessToken: testOldAccessToken, RefreshToken: testOldRefreshToken, TokenExpiresAt: new(now.Add(time.Hour)),
		ScrobbleEnabled: true, LastError: "old error", LastOutboundSyncAt: new(now.Add(-time.Hour)),
		SyncCursors: map[string]string{"trakt.watched": "cursor-1", "trakt.ratings": "ratings-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(context.Background(), saved.Provider, userID, profileID) })
	return repo, saved, now
}

// checkSyncStateKeepsRefreshedTokens saves a flow's bookkeeping over a token
// refresh and returns the stored connection.
func checkSyncStateKeepsRefreshedTokens(t *testing.T, repo *PostgresRepository, saved Connection, now time.Time) Connection {
	t.Helper()
	ctx := t.Context()
	// A refresh lands while a sync holds the saved copy.
	refreshed, err := repo.UpdateConnectionTokens(ctx, saved, connectionWithTokens(saved, TokenSet{
		AccessToken: "new-access", RefreshToken: "new-refresh", TokenExpiresAt: new(now.Add(2 * time.Hour)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	update := flowSucceeded(now)
	update.LastInboundSyncAt = &now
	update.SyncCursors = map[string]string{"trakt.watched": "cursor-2", "blank": " "}
	if err := repo.UpdateConnectionSyncState(ctx, saved.ID, update); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.GetConnectionByID(ctx, saved.ID)
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	if got.AccessToken != "new-access" || got.RefreshToken != "new-refresh" || !got.TokenExpiresAt.Equal(*refreshed.TokenExpiresAt) {
		t.Fatalf("refreshed tokens overwritten: access=%q refresh=%q", got.AccessToken, got.RefreshToken)
	}
	if got.LastInboundSyncAt == nil || !got.LastInboundSyncAt.Equal(now) || got.LastError != "" ||
		got.LastOutboundSyncAt == nil || !got.LastOutboundSyncAt.Equal(now.Add(-time.Hour)) || !got.ScrobbleEnabled {
		t.Fatalf("bookkeeping = %+v", got)
	}
	if len(got.SyncCursors) != 2 || got.SyncCursors["trakt.watched"] != "cursor-2" || got.SyncCursors["trakt.ratings"] != "ratings-1" {
		t.Fatalf("cursors = %+v", got.SyncCursors)
	}
	if !got.UpdatedAt.After(refreshed.UpdatedAt) {
		t.Fatalf("updated_at did not advance: %v then %v", refreshed.UpdatedAt, got.UpdatedAt)
	}
	// An empty update keeps every stored value.
	if err := repo.UpdateConnectionSyncState(ctx, saved.ID, ConnectionSyncUpdate{}); err != nil {
		t.Fatal(err)
	}
	unchanged, _, err := repo.GetConnectionByID(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !unchanged.LastInboundSyncAt.Equal(*got.LastInboundSyncAt) || unchanged.LastError != got.LastError ||
		unchanged.RateLimitedUntil != nil || len(unchanged.SyncCursors) != 2 {
		t.Fatalf("empty update changed the connection: %+v", unchanged)
	}
	return unchanged
}

// checkSyncStateDeferral checks that a deferral and its error last until it
// ends, across a plugin credential rotation.
func checkSyncStateDeferral(t *testing.T, repo *PostgresRepository, id string, unchanged Connection, now time.Time) {
	t.Helper()
	ctx := t.Context()

	// A deferral replaces the stored one; a flow that finishes before it ends
	// keeps it, and one that finishes after it clears it.
	deferral := connectionError("rate limited")
	deferral.RateLimitedUntil = new(now.Add(time.Minute))
	if err := repo.UpdateConnectionSyncState(ctx, id, deferral); err != nil {
		t.Fatal(err)
	}
	// Credentials a plugin rotates with a copy loaded before the deferral
	// keep the deferral's error.
	if _, err := repo.UpdateConnectionTokens(ctx, unchanged, connectionWithTokens(unchanged, TokenSet{
		AccessToken: "rotated-access", RefreshToken: "rotated-refresh", TokenExpiresAt: new(now.Add(3 * time.Hour)),
	})); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		at        time.Time
		want      *time.Time
		wantError string
	}{
		{now, new(now.Add(time.Minute)), "rate limited"},
		{now.Add(time.Minute), nil, ""},
	} {
		if err := repo.UpdateConnectionSyncState(ctx, id, flowSucceeded(step.at)); err != nil {
			t.Fatal(err)
		}
		got, _, err := repo.GetConnectionByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (got.RateLimitedUntil == nil) != (step.want == nil) || step.want != nil && !got.RateLimitedUntil.Equal(*step.want) ||
			got.LastError != step.wantError || got.AccessToken != "rotated-access" {
			t.Fatalf("after a flow at %v: until=%v error=%q access=%q, want %v %q rotated-access",
				step.at, got.RateLimitedUntil, got.LastError, got.AccessToken, step.want, step.wantError)
		}
	}

}
