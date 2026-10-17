package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The SQLite backend stores the requests block the way Postgres does: set on
// create, changed by an update that names it, left alone by one that does not.
func TestSQLiteProfileRequestsBlockedRoundTrips(t *testing.T) {
	ctx := t.Context()
	store := newConformanceStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid", RequestsBlocked: true}); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListProfiles(ctx)
	if err != nil || len(listed) != 1 || !listed[0].RequestsBlocked {
		t.Fatalf("ListProfiles = %+v, %v; want one blocked profile", listed, err)
	}

	off := false
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{RequestsBlocked: &off}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetProfile(ctx, "kid"); err != nil || got == nil || got.RequestsBlocked {
		t.Fatalf("after unblocking = %+v, %v; want false", got, err)
	}

	on := true
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{RequestsBlocked: &on}); err != nil {
		t.Fatal(err)
	}
	name := "Kiddo"
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetProfile(ctx, "kid"); err != nil || got == nil || !got.RequestsBlocked {
		t.Fatalf("after block and an unrelated update = %+v, %v; want true", got, err)
	}
}

// An existing v33 store gains profiles.requests_blocked, off for every
// profile it already holds.
func TestMigrateToV34AddsProfileRequestsBlocked(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	store := NewSQLiteUserStore(db)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "old", Name: "Old"}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE profiles DROP COLUMN requests_blocked`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 33"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	if version, err := userVersion(db); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d, %v; want %d", version, err, schemaVersion)
	}
	old, err := store.GetProfile(ctx, "old")
	if err != nil || old == nil || old.RequestsBlocked {
		t.Fatalf("migrated profile = %+v, %v; want not blocked", old, err)
	}
}
