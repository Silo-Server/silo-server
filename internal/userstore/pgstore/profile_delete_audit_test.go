package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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
	defer update.Rollback(context.Background()) //nolint:errcheck // rollback after commit is a no-op
	if _, err := update.Exec(ctx, `UPDATE user_profiles SET name = 'Kiddo' WHERE user_id = $1 AND id = 'kid'`, userID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		name string
		err  error
	}
	done := make(chan result, 1)
	deletePool, deleteApp := newDeleteSessionPool(t)
	go func() {
		var name string
		err := newStore(deletePool, userID).DeleteProfileAudited(ctx, "kid", func(_ pgx.Tx, deleted *userstore.Profile) error {
			name = deleted.Name
			return nil
		})
		done <- result{name, err}
	}()

	// Commit only once the delete is blocked on the update's row lock.
	waitForBlockedDelete(t, pool, deleteApp, func() {
		select {
		case r := <-done:
			t.Fatalf("delete finished before the update committed: %+v", r)
		default:
		}
	})
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

// A setting clear deletes the profile's user_setting_values row and then
// writes the legacy column on user_profiles. An audited delete that starts in
// between must queue behind the clear rather than lock the profile and then
// wait on the settings row, which PostgreSQL would abort as a deadlock.
func TestPostgresDeleteProfileAuditedWaitsForSettingClear(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	store := newStore(pool, userID)
	ctx := t.Context()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "kid", Name: "Kid"}); err != nil {
		t.Fatal(err)
	}
	identity := userstore.SettingIdentity{
		Key: settingskeys.PlaybackAutoSkipIntro, Scope: settingscontract.ScopeProfile, ProfileID: "kid",
	}
	if _, err := store.UpsertSettingValue(ctx, identity, json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}

	deletePool, deleteApp := newDeleteSessionPool(t)
	deleteDone := make(chan error, 1)
	clearErr := store.WithSettingMutationTransaction(ctx, "", func(writer userstore.SettingMutationWriter) error {
		if removed, err := writer.DeleteSettingValue(ctx, identity); err != nil || !removed {
			t.Fatalf("DeleteSettingValue = %v, %v", removed, err)
		}
		go func() {
			deleteDone <- newStore(deletePool, userID).DeleteProfileAudited(ctx, "kid", func(pgx.Tx, *userstore.Profile) error { return nil })
		}()
		waitForBlockedDelete(t, pool, deleteApp, func() {
			select {
			case err := <-deleteDone:
				t.Fatalf("delete finished while the clear was open: %v", err)
			default:
			}
		})
		enabled := false
		return writer.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{AutoSkipIntro: &enabled})
	})
	if clearErr != nil {
		t.Fatalf("setting clear: %v", clearErr)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("DeleteProfileAudited: %v", err)
	}
	if p, err := store.GetProfile(ctx, "kid"); err != nil || p != nil {
		t.Fatalf("profile after delete = %+v, %v", p, err)
	}
}

// newDeleteSessionPool opens a pool whose sessions carry a unique
// application_name, so waitForBlockedDelete can tell the delete's session
// apart from other tests sharing the database.
func newDeleteSessionPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("profile-delete-audit-%d", time.Now().UnixNano())
	cfg.ConnConfig.RuntimeParams["application_name"] = name
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, name
}

// waitForBlockedDelete returns once a session named app waits on a lock,
// calling failIfDone on each poll so the caller can fail early.
func waitForBlockedDelete(t *testing.T, pool *pgxpool.Pool, app string, failIfDone func()) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE application_name = $1 AND wait_event_type = 'Lock')`, app).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		failIfDone()
		if time.Now().After(deadline) {
			t.Fatal("delete never waited on a lock")
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_stat_activity, the observable lock state
	}
}
