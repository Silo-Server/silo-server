package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An update that holds the profile row when an audited delete starts must be
// reflected in the image handed to audit: the delete waits for the row lock
// and reads the committed name, not the one from before the update.
func TestPostgresDeleteProfileAuditedSeesConcurrentUpdate(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	store := newStore(pool, userID)
	ctx := t.Context()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid"}); err != nil {
		t.Fatal(err)
	}

	update, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer update.Rollback(context.Background()) //nolint:errcheck
	if _, err := update.Exec(ctx, `UPDATE user_profiles SET name = 'Kiddo' WHERE user_id = $1 AND id = 'kid'`, userID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		name string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var name string
		err := store.DeleteProfileAudited(ctx, "kid", func(_ pgx.Tx, deleted *userstore.Profile) error {
			name = deleted.Name
			return nil
		})
		done <- result{name, err}
	}()

	// Commit only once the delete is blocked on the update's row lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%user_profiles%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("delete finished before the update committed: %+v", r)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("delete never waited on the profile row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := update.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("DeleteProfileAudited: %v", r.err)
	}
	if r.name != "Kiddo" {
		t.Fatalf("audited profile name = %q, want the concurrently committed %q", r.name, "Kiddo")
	}
	if p, err := store.GetProfile(ctx, "kid"); err != nil || p != nil {
		t.Fatalf("profile after delete = %+v, %v", p, err)
	}
}
