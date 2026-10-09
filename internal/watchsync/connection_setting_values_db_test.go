package watchsync

import (
	"maps"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestConnectionSettingValuesDB(t *testing.T) {
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
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "watch-settings-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", userID) }()
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'settings-p','Settings')", userID); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("watch-settings-test-key-with-enough-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewPostgresRepository(pool, cipher)
	conn := Connection{Provider: "settings", UserID: userID, ProfileID: "settings-p", AccessToken: "token"}
	created, err := repo.UpsertConnection(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ConnectionSettings) != 0 {
		t.Fatalf("new connection settings = %v, want none", created.ConnectionSettings)
	}

	// Each update merges its keys into the stored settings and bumps the
	// connection version.
	first, err := repo.UpdateConnectionSettings(ctx, "settings", userID, "settings-p", nil, ConnectionUpdate{ConnectionSettings: map[string]any{"track_rewatches": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.UpdatedAt.After(created.UpdatedAt) {
		t.Fatal("a settings change must bump the connection version")
	}
	second, err := repo.UpdateConnectionSettings(ctx, "settings", userID, "settings-p", nil, ConnectionUpdate{ConnectionSettings: map[string]any{"other": false}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"track_rewatches": "true", "other": "false"}; !maps.Equal(second.ConnectionSettings, want) {
		t.Fatalf("merged settings = %v, want %v", second.ConnectionSettings, want)
	}
	// An update without settings leaves them alone.
	scrobbleOff := false
	third, err := repo.UpdateConnectionSettings(ctx, "settings", userID, "settings-p", nil, ConnectionUpdate{ScrobbleEnabled: &scrobbleOff}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(third.ConnectionSettings, second.ConnectionSettings) {
		t.Fatalf("settings after an unrelated update = %v", third.ConnectionSettings)
	}

	// Reconnecting or refreshing tokens upserts the row; the profile's
	// settings stay.
	conn.AccessToken = "new-token"
	reconnected, err := repo.UpsertConnection(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(reconnected.ConnectionSettings, second.ConnectionSettings) {
		t.Fatalf("settings after reconnect = %v, want %v", reconnected.ConnectionSettings, second.ConnectionSettings)
	}
	stored, ok, err := repo.GetConnection(ctx, "settings", userID, "settings-p")
	if err != nil || !ok || !maps.Equal(stored.ConnectionSettings, second.ConnectionSettings) {
		t.Fatalf("stored = %v, %v, %v", stored.ConnectionSettings, ok, err)
	}
}
