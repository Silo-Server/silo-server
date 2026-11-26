package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// rename renames a profile through the shared seam, acting as the primary
// profile.
func (f *profileLimitDBFixture) rename(profileID, name string) error {
	ctx := apimw.SetProfileID(apimw.SetClaims(context.Background(), &auth.Claims{
		UserID: f.userID, Role: "user", TokenType: auth.TokenTypeAccess,
	}), f.primary)
	_, err := f.handler.UpdateProfile(ctx, ProfileUpdateCommand{
		UserID: f.userID, ProfileID: profileID, ActiveProfileID: f.primary,
		Request: ProfileUpdateRequest{Name: &name}, VerifyProfile: noProfileVerification,
	})
	return err
}

// profileIDNamed is the ID of the account's profile with this exact name.
func (f *profileLimitDBFixture) profileIDNamed(t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(t.Context(), `SELECT id FROM user_profiles WHERE user_id = $1 AND name = $2`, f.userID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestProfileNameRaceDB: two overlapping writes of one name on one account
// (two creates, a rename against a create, two renames) leave one profile
// with that name, and the other write answers name_conflict. Both requests
// pass the early check, then wait on the account's preference-settings lock,
// which the test holds until both are queued on it; each must read the names
// again once it holds the lock. It catches the check-then-write race the
// early check alone leaves, which put two profiles of one name on an account,
// a state a profile set must then refuse.
func TestProfileNameRaceDB(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup creates the profiles the writes rename.
		setup  []string
		writes func(f *profileLimitDBFixture, t *testing.T) [2]func() error
	}{
		{"two creates", nil, func(f *profileLimitDBFixture, _ *testing.T) [2]func() error {
			return [2]func() error{func() error { return f.create("Sam") }, func() error { return f.create("sam") }}
		}},
		{"a rename against a create", []string{"Kid"}, func(f *profileLimitDBFixture, t *testing.T) [2]func() error {
			kid := f.profileIDNamed(t, "Kid")
			return [2]func() error{func() error { return f.rename(kid, "Sam") }, func() error { return f.create(" SAM ") }}
		}},
		{"two renames", []string{"Kid", "Guest"}, func(f *profileLimitDBFixture, t *testing.T) [2]func() error {
			kid, guest := f.profileIDNamed(t, "Kid"), f.profileIDNamed(t, "Guest")
			return [2]func() error{func() error { return f.rename(kid, "Sam") }, func() error { return f.rename(guest, "sam") }}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProfileLimitDBFixture(t, 10)
			for _, name := range tc.setup {
				if err := f.create(name); err != nil {
					t.Fatal(err)
				}
			}
			writes := tc.writes(f, t)
			ctx := t.Context()
			holder, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Rollback(context.Background()) }()
			if err := pgstore.LockAccountForImport(ctx, holder, f.userID); err != nil {
				t.Fatal(err)
			}
			var holderPID int
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 2)
			for _, write := range writes {
				go func() { done <- write() }()
			}
			waitForLockWaiters(t, f, holderPID, 2, done)
			if err := holder.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			created := 0
			for range writes {
				err := <-done
				if err == nil {
					created++
					continue
				}
				if apiErr := requireAPIStatus(t, err, http.StatusConflict); apiErr.Code != "name_conflict" {
					t.Fatalf("error = %+v, want name_conflict", apiErr)
				}
			}
			if created != 1 {
				t.Fatalf("%d of 2 overlapping writes of one name succeeded, want exactly 1", created)
			}
			profiles, err := f.store.ListProfiles(ctx)
			if err != nil {
				t.Fatal(err)
			}
			named := 0
			for _, p := range profiles {
				if strings.EqualFold(strings.TrimSpace(p.Name), "sam") {
					named++
				}
			}
			if named != 1 {
				t.Fatalf("the account has %d profiles named Sam, want 1: %+v", named, profiles)
			}
		})
	}
}

// waitForLockWaiters waits until n sessions are blocked behind holderPID,
// failing if a write finishes first.
func waitForLockWaiters(t *testing.T, f *profileLimitDBFixture, holderPID, n int, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("a write finished (%v) while the account lock was held", err)
		case <-deadline:
			t.Fatalf("fewer than %d writes waited for the account lock", n)
		case <-ticker.C:
			var waiting int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid))`, holderPID).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting >= n {
				return
			}
		}
	}
}
