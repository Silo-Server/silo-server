package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// CreateProfile is also used by account provisioning without the native
// preference writer. A failure writing its library policy must not leave a
// profile that consumes quota or becomes the household's primary.
func TestCreateProfileLibraryFailureRollsBack(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set; requires a migrated private PostgreSQL fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role, max_profiles)
		VALUES($1,'user',1) RETURNING id`, "atomic-profile-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID); err != nil {
			t.Errorf("remove atomic profile fixture: %v", err)
		}
		for _, table := range []string{"user_profiles", "user_profile_allowed_libraries"} {
			var leaked int
			if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE user_id=$1", table), userID).Scan(&leaked); err != nil {
				t.Errorf("check %s cleanup: %v", table, err)
			} else if leaked != 0 {
				t.Errorf("cleanup leaked %d %s rows", leaked, table)
			}
		}
	})
	store := newStore(pool, userID)
	if err := store.CreateProfile(t.Context(), userstore.Profile{
		ID: "rejected", Name: "Rejected", AllowedLibraryIDs: []int{-1},
	}); err == nil {
		t.Fatal("profile with an invalid library unexpectedly committed")
	}
	var profiles int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM user_profiles WHERE user_id=$1`, userID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 {
		t.Fatalf("failed profile create left %d profiles; want exact rollback", profiles)
	}
	if err := store.CreateProfile(t.Context(), userstore.Profile{ID: "primary", Name: "Parent"}); err != nil {
		t.Fatalf("create after rollback: %v", err)
	}
	profile, err := store.GetProfile(t.Context(), "primary")
	if err != nil || profile == nil || !profile.IsPrimary {
		t.Fatalf("profile after rollback = %+v, %v; want a primary profile", profile, err)
	}
}
