package userdb

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An existing v13 store must gain the profile_onboarding table and land on
// the current schema version.
func TestMigrateToV14AddsProfileOnboarding(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Simulate an existing v13 database that predates the table.
	if _, err := db.Exec(`DROP TABLE IF EXISTS profile_onboarding`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 13"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	version, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	store := NewSQLiteUserStore(db)
	ctx := context.Background()

	// Missing state reads as nil, not an error.
	state, err := store.GetOnboardingState(ctx, "p1", "core-2026-07")
	if err != nil {
		t.Fatalf("GetOnboardingState: %v", err)
	}
	if state != nil {
		t.Fatalf("expected nil state for untouched profile, got %+v", state)
	}

	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{
		ProfileID: "p1", TourID: "core-2026-07", LastStep: "subtitles",
	}); err != nil {
		t.Fatalf("UpsertOnboardingState: %v", err)
	}
	state, err = store.GetOnboardingState(ctx, "p1", "core-2026-07")
	if err != nil || state == nil {
		t.Fatalf("read back: state=%v err=%v", state, err)
	}
	if state.LastStep != "subtitles" || state.CompletedAt != "" {
		t.Fatalf("state = %+v", state)
	}
}

// Completed/skipped are monotonic: a later progress write must not clear an
// earlier completion (finishing on one device, then opening on another).
func TestOnboardingCompletionIsMonotonic(t *testing.T) {
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

	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{
		ProfileID: "p1", TourID: "t", LastStep: "end", CompletedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{
		ProfileID: "p1", TourID: "t", LastStep: "welcome",
	}); err != nil {
		t.Fatalf("later progress write: %v", err)
	}

	state, err := store.GetOnboardingState(ctx, "p1", "t")
	if err != nil || state == nil {
		t.Fatalf("read back: state=%v err=%v", state, err)
	}
	if state.CompletedAt == "" {
		t.Error("completion cleared by a later progress write")
	}
	if state.LastStep != "welcome" {
		t.Errorf("last_step = %q, want welcome", state.LastStep)
	}
}
