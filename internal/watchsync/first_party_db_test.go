package watchsync

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// A connection a built-in provider stored has tokens only in the dedicated
// columns. Once a first-party plugin serves the same key, the row must read
// as before and gain the full credential bundle on its next token write.
func TestFormerBuiltInConnectionCarriesOverToItsPluginDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "first-party-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'First party')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("first-party-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	conn, err := repo.UpsertConnection(ctx, Connection{
		Provider: "trakt", UserID: userID, ProfileID: profileID, ProviderAccountID: "slug",
		AccessToken: testOldAccessToken, RefreshToken: testOldRefreshToken, TokenExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Strip the bundle so the row looks like one a built-in provider wrote.
	if _, err := pool.Exec(ctx, "UPDATE watch_provider_connections SET plugin_credentials='' WHERE id=$1::uuid", conn.ID); err != nil {
		t.Fatal(err)
	}
	legacy, found, err := repo.GetConnection(ctx, "trakt", userID, profileID)
	if err != nil || !found {
		t.Fatalf("GetConnection() found = %t, err = %v", found, err)
	}
	if legacy.AccessToken != testOldAccessToken || legacy.RefreshToken != testOldRefreshToken {
		t.Fatalf("legacy tokens = %q/%q", legacy.AccessToken, legacy.RefreshToken)
	}

	updated := legacy
	updated.AccessToken, updated.RefreshToken = "new-access", "new-refresh"
	updated.SecretAttributes = map[string]string{"vip": "true"}
	if _, err := repo.UpdateConnectionTokens(ctx, legacy, updated); err != nil {
		t.Fatal(err)
	}
	reread, _, err := repo.GetConnection(ctx, "trakt", userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.AccessToken != "new-access" || reread.RefreshToken != "new-refresh" || reread.SecretAttributes["vip"] != "true" {
		t.Fatalf("connection after plugin token write = %#v", reread)
	}

	connected, err := repo.HasConnections(ctx, "trakt")
	if err != nil || !connected {
		t.Fatalf("HasConnections(trakt) = %t, %v", connected, err)
	}
}

func TestFirstPartyMigrationMarkerDB(t *testing.T) {
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
	cipher, err := secret.New([]byte("first-party-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	key := firstPartyMigrationSettingKey("simkl")
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM server_settings WHERE key=$1", key) }()

	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "first-party-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	profileID := uuid.NewString()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,$2,'First party')", userID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertConnection(ctx, Connection{Provider: "simkl", UserID: userID, ProfileID: profileID, AccessToken: testOldAccessToken}); err != nil {
		t.Fatal(err)
	}

	toInstall, err := FirstPartyPluginsToInstall(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(toInstall, "silo.watchprovider.simkl") {
		t.Fatalf("plugins to install = %v, want Simkl while it has connections", toInstall)
	}
	if err := MarkFirstPartyMigrated(ctx, repo, "simkl"); err != nil {
		t.Fatal(err)
	}
	// Marking twice is harmless: every API node records it on reload.
	if err := MarkFirstPartyMigrated(ctx, repo, "simkl"); err != nil {
		t.Fatal(err)
	}
	toInstall, err = FirstPartyPluginsToInstall(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(toInstall, "silo.watchprovider.simkl") {
		t.Fatalf("plugins to install = %v, want Simkl dropped once migrated", toInstall)
	}
}
