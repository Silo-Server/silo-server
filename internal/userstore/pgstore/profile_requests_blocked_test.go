package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Blocking a profile from requests is an access policy field: changing it
// bumps the account's access_policy_revision, and the column round-trips
// through create, get, list and update.
func TestUpdateProfileRequestsBlockedBumpsAccessPolicyRevision(t *testing.T) {
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
	var id int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("requests-blocked-%d", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) }()

	revision := func() int64 {
		t.Helper()
		var rev int64
		if err := pool.QueryRow(ctx, `SELECT access_policy_revision FROM users WHERE id=$1`, id).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}

	store := newStore(pool, id)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "parent", Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid", RequestsBlocked: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetProfile(ctx, "parent"); err != nil || got == nil || got.RequestsBlocked {
		t.Fatalf("a profile created without the block = %+v, %v; want allowed", got, err)
	}
	listed, err := store.ListProfiles(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListProfiles = %+v, %v; want two profiles", listed, err)
	}
	// Both rows can share a created_at, so match them by ID, not position.
	for _, p := range listed {
		if p.RequestsBlocked != (p.ID == "kid") {
			t.Fatalf("ListProfiles = %+v; want only the kid blocked", listed)
		}
	}

	for _, want := range []bool{false, true} {
		before := revision()
		value := want
		if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{RequestsBlocked: &value}); err != nil {
			t.Fatal(err)
		}
		if after := revision(); after != before+1 {
			t.Fatalf("access_policy_revision = %d after setting blocked=%v, want %d", after, want, before+1)
		}
		if got, err := store.GetProfile(ctx, "kid"); err != nil || got == nil || got.RequestsBlocked != want {
			t.Fatalf("RequestsBlocked after update to %v = %+v, %v", want, got, err)
		}
	}
}
