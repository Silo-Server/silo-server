package pgstore

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

// TestPostgresProfilePINRevision runs the profile PIN revision conformance test
// against the Postgres backend; the SQLite backend runs the same suite in
// internal/userdb. Skips unless SILO_TEST_DATABASE_URL is set.
func TestPostgresProfilePINRevision(t *testing.T) {
	storetest.RunProfilePINRevision(t, func(t *testing.T) userstore.UserStore {
		pool, userID := newConstraintTestUser(t)
		return newStore(pool, userID)
	})
}

// The account-wide access_policy_revision keeps its existing bumps: scope
// caches, events-socket access_changed and policy evaluation still read it.
// Only the profile token stopped depending on it.
func TestPostgresProfileEditsStillBumpAccountRevision(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	store := newStore(pool, userID)
	ctx := t.Context()
	for _, p := range []userstore.Profile{{ID: "parent", Name: "Parent"}, {ID: "kid", Name: "Kid"}} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatalf("CreateProfile(%s): %v", p.ID, err)
		}
	}
	accountRevision := func() int64 {
		t.Helper()
		var rev int64
		if err := pool.QueryRow(ctx, `SELECT access_policy_revision FROM users WHERE id = $1`, userID).Scan(&rev); err != nil {
			t.Fatalf("read account revision: %v", err)
		}
		return rev
	}
	str := func(s string) *string { return &s }

	before := accountRevision()
	if err := store.UpdateProfile(ctx, "kid", userstore.UpdateProfileInput{MaxContentRating: str("PG")}); err != nil {
		t.Fatal(err)
	}
	if got := accountRevision(); got != before+1 {
		t.Fatalf("kid rating edit: account revision = %d, want %d", got, before+1)
	}
	if err := store.UpdateProfile(ctx, "parent", userstore.UpdateProfileInput{PIN: str("1234")}); err != nil {
		t.Fatal(err)
	}
	if got := accountRevision(); got != before+2 {
		t.Fatalf("parent PIN set: account revision = %d, want %d", got, before+2)
	}
	if err := store.UpdateProfile(ctx, "parent", userstore.UpdateProfileInput{Name: str("Mum")}); err != nil {
		t.Fatal(err)
	}
	if got := accountRevision(); got != before+2 {
		t.Fatalf("parent rename: account revision = %d, want %d", got, before+2)
	}
}

// A writer that does not know pin_revision, such as a node from an older
// release during a rolling deploy or manual SQL, still advances it when it
// writes pin_hash, so tokens minted for the old PIN stop being accepted. Raw
// writes to other columns leave it alone.
func TestPostgresRawPINWriteAdvancesPINRevision(t *testing.T) {
	pool, userID := newConstraintTestUser(t)
	store := newStore(pool, userID)
	ctx := t.Context()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "parent", Name: "Parent"}); err != nil {
		t.Fatal(err)
	}
	pin := "1234"
	if err := store.UpdateProfile(ctx, "parent", userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatal(err)
	}
	load := func() *userstore.Profile {
		t.Helper()
		p, err := store.GetProfile(ctx, "parent")
		if err != nil || p == nil {
			t.Fatalf("GetProfile: profile=%v err=%v", p, err)
		}
		return p
	}
	profile := load()
	if profile.PINRevision != 1 {
		t.Fatalf("PINRevision after one PIN set = %d, want 1 (the trigger and the store must not both bump)", profile.PINRevision)
	}
	tokens := access.NewProfileTokenService("pg-pin-revision-test-secret-0123456789", 0)
	token, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: userID, SessionID: "s1", ProfileID: "parent", PINRevision: profile.PINRevision})
	if err != nil {
		t.Fatal(err)
	}
	if err := access.CheckProfileToken(tokens, token, userID, "s1", profile); err != nil {
		t.Fatalf("fresh token refused: %v", err)
	}

	// Raw writes that leave pin_hash alone, including one that names it with
	// its current value.
	for _, stmt := range []string{
		`UPDATE user_profiles SET name = 'Mum', max_content_rating = 'R' WHERE user_id = $1 AND id = 'parent'`,
		`UPDATE user_profiles SET pin_hash = pin_hash, is_child = false WHERE user_id = $1 AND id = 'parent'`,
	} {
		if _, err := pool.Exec(ctx, stmt, userID); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
		if got := load(); got.PINRevision != 1 {
			t.Fatalf("%s: PINRevision = %d, want 1", stmt, got.PINRevision)
		}
	}
	if err := access.CheckProfileToken(tokens, token, userID, "s1", load()); err != nil {
		t.Fatalf("token refused after non-PIN writes: %v", err)
	}

	// An older node's PIN change: it writes pin_hash and never names
	// pin_revision.
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET pin_hash = 'old-node-hash', updated_at = now() WHERE user_id = $1 AND id = 'parent'`, userID); err != nil {
		t.Fatal(err)
	}
	profile = load()
	if profile.PINRevision != 2 {
		t.Fatalf("PINRevision after raw PIN write = %d, want 2", profile.PINRevision)
	}
	if err := access.CheckProfileToken(tokens, token, userID, "s1", profile); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("old-PIN token after raw PIN write: err = %v, want ErrProfileUnverified", err)
	}
	// Clearing it is a change too.
	if _, err := pool.Exec(ctx, `UPDATE user_profiles SET pin_hash = '' WHERE user_id = $1 AND id = 'parent'`, userID); err != nil {
		t.Fatal(err)
	}
	if got := load(); got.PINRevision != 3 {
		t.Fatalf("PINRevision after raw PIN clear = %d, want 3", got.PINRevision)
	}
}
