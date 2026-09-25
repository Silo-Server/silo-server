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

// The advisory-age limit is an access policy field: changing it must bump the
// account's access_policy_revision like the content-rating ceiling does, so
// profile tokens and cached scopes minted under the old limit stop being
// honored. The column also refuses a limit outside 1..21.
func TestUpdateProfileAdvisoryAgeBumpsAccessPolicyRevision(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("advisory-limit-%d", time.Now().UnixNano())).Scan(&id); err != nil {
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
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid"}); err != nil {
		t.Fatal(err)
	}
	before := revision()
	age := 10
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{MaxAdvisoryAge: &age}); err != nil {
		t.Fatal(err)
	}
	if after := revision(); after != before+1 {
		t.Fatalf("access_policy_revision = %d after setting the limit, want %d", after, before+1)
	}

	var stored *int
	if err := pool.QueryRow(ctx, `SELECT max_advisory_age FROM user_profiles WHERE user_id=$1 AND id='kid'`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != 10 {
		t.Fatalf("stored max_advisory_age = %v, want 10", stored)
	}
	clear := 0
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{MaxAdvisoryAge: &clear}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT max_advisory_age FROM user_profiles WHERE user_id=$1 AND id='kid'`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != nil {
		t.Fatalf("cleared max_advisory_age stored as %d, want NULL", *stored)
	}

	tooOld := 22
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{MaxAdvisoryAge: &tooOld}); err == nil {
		t.Fatal("stored an advisory-age limit outside 1..21")
	}
}
