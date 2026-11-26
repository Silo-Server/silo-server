package auth

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// profileSetFixture is an account with profiles "main" and "p" and an
// identity, and a pending remote-playback device sign-in waiting to be
// approved for "p".
type profileSetFixture struct {
	pool       *pgxpool.Pool
	userID     int
	identityID int64
	userCode   string
}

func newProfileSetFixture(t *testing.T) *profileSetFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	f := &profileSetFixture{pool: poolWith(t, dsn, nil)}
	name := "profile-set-" + uuid.NewString()
	var installationID int64
	t.Cleanup(func() {
		ctx := context.WithoutCancel(ctx)
		_, _ = f.pool.Exec(ctx, `DELETE FROM device_login_requests WHERE device_name = $1`, name)
		_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.userID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, installationID)
	})
	if err := f.pool.QueryRow(ctx, `INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'x', 'user', true) RETURNING id`, name, name+"@example.test").Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"main", "p"} {
		if err := f.store(t).CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO plugin_installations (plugin_id, version, install_path)
		VALUES ($1, '1.0.0', $1) RETURNING id`, name).Scan(&installationID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO plugin_auth_identities (plugin_installation_id, external_subject, user_id)
		VALUES ($1, $2, $3) RETURNING id`, installationID, name, f.userID).Scan(&f.identityID); err != nil {
		t.Fatal(err)
	}
	start, err := f.devices().Start(ctx, DeviceLoginStartInput{DeviceName: name, ClientPurpose: DeviceLoginPurposeRemote, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	f.userCode = start.UserCode
	return f
}

func (f *profileSetFixture) store(t *testing.T) userstore.UserStore {
	t.Helper()
	store, err := pgstore.NewPostgresProvider(f.pool).ForUser(t.Context(), f.userID)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (f *profileSetFixture) devices() *DeviceLoginService {
	return NewDeviceLoginService(f.pool, NewUserRepository(f.pool), NewJWTService("profile-set-test-secret", time.Hour, 24*time.Hour),
		NewSessionRepository(f.pool), pgstore.NewPostgresProvider(f.pool), nil)
}

// hold runs query in a transaction of its own and returns it with its pid.
func (f *profileSetFixture) hold(ctx context.Context, t *testing.T, query string, args ...any) (pgx.Tx, int) {
	t.Helper()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// A profile set locks the account row, then every profile row. Approving a
// device sign-in for a profile locks the account row and then, through the
// request's foreign key, the profile row; a profile save holds the
// account's preference-settings lock, as the set does, before its profile
// row and the account row. With the set taking the profile rows first, an
// approval holding the account row and a set holding the profile rows waited
// for each other, and Postgres aborted the approval (a 500). Each pair runs
// with either one first; the later one is seen waiting for the earlier, and
// both finish.
func TestProfileSetLockOrderDB(t *testing.T) {
	set := func(ctx context.Context, t *testing.T, f *profileSetFixture) error {
		return f.store(t).(userstore.ProfileSetStore).WithProfileSetTransaction(ctx, nil,
			func(userstore.ProfileSetWriter) error { return nil })
	}
	approve := func(ctx context.Context, t *testing.T, f *profileSetFixture) error {
		return f.devices().ApproveForProfile(ctx, DeviceLoginLookupInput{UserCode: f.userCode}, f.userID, "p")
	}
	save := func(ctx context.Context, t *testing.T, f *profileSetFixture) error {
		// An access field: the save also advances the account's policy
		// revision, so it locks the profile row and then the account row.
		return f.store(t).UpdateProfile(ctx, "p", userstore.UpdateProfileInput{IsChild: new(true)})
	}
	type writer = func(context.Context, *testing.T, *profileSetFixture) error
	cases := []struct {
		name          string
		first, second writer
		// hold is the lock that stops the first after it took the locks
		// the pair turns on; the second then waits for the first.
		hold func(f *profileSetFixture) (string, []any)
	}{
		{"approval first", approve, set, func(f *profileSetFixture) (string, []any) {
			// After the approval locked the account row, at the identities.
			return `SELECT 1 FROM plugin_auth_identities WHERE id = $1 FOR UPDATE`, []any{f.identityID}
		}},
		{"set first", set, approve, func(f *profileSetFixture) (string, []any) {
			return `SELECT 1 FROM users WHERE id = $1 FOR SHARE`, []any{f.userID}
		}},
		{"save first", save, set, func(f *profileSetFixture) (string, []any) {
			return `SELECT pg_advisory_xact_lock($1, $2::integer)`, []any{int32(0x53494c4f), f.userID}
		}},
		{"set before a save", set, save, func(f *profileSetFixture) (string, []any) {
			return `SELECT pg_advisory_xact_lock($1, $2::integer)`, []any{int32(0x53494c4f), f.userID}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newProfileSetFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			query, args := c.hold(f)
			holder, holderPID := f.hold(ctx, t, query, args...)
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- c.first(ctx, t, f) }()
			firstPID, _ := lockWaiter(ctx, t, f.pool, []int{holderPID})
			go func() { secondDone <- c.second(ctx, t, f) }()
			_, secondWaitsOn := lockWaiter(ctx, t, f.pool, []int{holderPID, firstPID}, firstPID)
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			firstErr := awaitErr(ctx, t, "first", firstDone)
			secondErr := awaitErr(ctx, t, "second", secondDone)
			for _, aborted := range []string{deadlocked("the first", firstErr), deadlocked("the second", secondErr)} {
				if aborted != "" {
					t.Errorf("deadlock: %s", aborted)
				}
			}
			if firstErr != nil || secondErr != nil {
				t.Fatalf("first = %v, second = %v", firstErr, secondErr)
			}
			// The second waits for the first, or for the lock that holds
			// the first too; never for a row the first holds after it.
			if !slices.Contains(secondWaitsOn, firstPID) && !slices.Contains(secondWaitsOn, holderPID) {
				t.Errorf("the second waited on %v", secondWaitsOn)
			}
			var approved int
			if err := f.pool.QueryRow(context.WithoutCancel(ctx), `SELECT count(*) FROM device_login_requests
				WHERE approved_by_user_id = $1 AND approved_profile_id = 'p' AND status = 'approved'`, f.userID).Scan(&approved); err != nil {
				t.Fatal(err)
			}
			if wantApproved := c.name == "approval first" || c.name == "set first"; wantApproved != (approved == 1) {
				t.Fatalf("approved requests for p = %d", approved)
			}
		})
	}
}
