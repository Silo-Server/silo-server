package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// TestABSTokenIssuanceWaitsForTheAccountDB: IssueTokens and a sign-out
// everywhere take turns on the account's users row. A sign-out holding it
// makes issuance wait and then refuse; issuance holding it makes the
// sign-out wait and then revoke what was issued.
func TestABSTokenIssuanceWaitsForTheAccountDB(t *testing.T) {
	pool, _, user := absTestAccount(t)
	ctx := t.Context()
	store := &ABSSessionStore{Pool: pool}
	// seed stores a live token for the account and returns its JTI.
	seed := func(t *testing.T, name string) string {
		t.Helper()
		jti := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
		if _, err := pool.Exec(ctx, `INSERT INTO abs_sessions (user_id, token_hash, token_type, device_id)
			VALUES ($1, $2, 'access', $3)`, user.ID, absTokenHash(jti), jti); err != nil {
			t.Fatal(err)
		}
		return jti
	}
	issue := func(from abs.TokenSource) <-chan error {
		jti := fmt.Sprintf("issued-%d", time.Now().UnixNano())
		done := make(chan error, 1)
		go func() {
			done <- store.IssueTokens(ctx, from, abs.ABSToken{ID: jti, UserID: strconv.Itoa(user.ID), Type: "access", JTI: jti,
				ExpiresAt: time.Now().Add(time.Hour)})
		}()
		return done
	}

	t.Run("sign-out holds the account", func(t *testing.T) {
		presented := seed(t, "presented")
		holder, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := holder.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, user.ID); err != nil {
			t.Fatal(err)
		}
		if err := auth.RevokeSignInsInTransaction(ctx, holder, user.ID); err != nil {
			t.Fatal(err)
		}
		issued := issue(abs.TokenSource{JTI: presented})
		if waiterOn(t, pool, backendPID(t, holder), issued) == 0 {
			t.Fatalf("issuance finished without waiting for the sign-out: %v", <-issued)
		}
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-issued; !errors.Is(err, abs.ErrNotFound) {
			t.Fatalf("issuance after the sign-out = %v, want ErrNotFound", err)
		}
		if live := liveABSTokens(t, pool, user.ID); live != 0 {
			t.Fatalf("%d tokens live after the sign-out", live)
		}
	})

	t.Run("issuance holds the account", func(t *testing.T) {
		presented := seed(t, "refresh")
		// Hold issuance inside its transaction, after it locked the account:
		// rotating the presented token waits for this row lock.
		blocker, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = blocker.Rollback(context.WithoutCancel(ctx)) }()
		if _, err := blocker.Exec(ctx, `SELECT 1 FROM abs_sessions WHERE token_hash = $1 FOR UPDATE`, absTokenHash(presented)); err != nil {
			t.Fatal(err)
		}
		issued := issue(abs.TokenSource{JTI: presented, Rotate: true})
		issuer := waiterOn(t, pool, backendPID(t, blocker), issued)
		if issuer == 0 {
			t.Fatalf("issuance finished without rotating: %v", <-issued)
		}
		signedOut := make(chan error, 1)
		go func() { signedOut <- signOutEverywhere(ctx, pool, user.ID) }()
		if waiterOn(t, pool, issuer, signedOut) == 0 {
			t.Fatalf("the sign-out finished without waiting for issuance: %v", <-signedOut)
		}
		if err := blocker.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-issued; err != nil {
			t.Fatalf("issuance before the sign-out: %v", err)
		}
		if err := <-signedOut; err != nil {
			t.Fatal(err)
		}
		if live := liveABSTokens(t, pool, user.ID); live != 0 {
			t.Fatalf("%d tokens issued before the sign-out outlived it", live)
		}
	})
}

func backendPID(t *testing.T, tx pgx.Tx) int {
	t.Helper()
	var pid int
	if err := tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return pid
}

// waiterOn waits until a backend of this database waits on a lock pid holds
// and returns its pid, or returns 0 once done has a result.
func waiterOn(t *testing.T, pool *pgxpool.Pool, pid int, done <-chan error) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if len(done) > 0 {
			return 0
		}
		var waiter int
		err := pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity
			WHERE datname = current_database() AND $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, pid).Scan(&waiter)
		if err == nil {
			return waiter
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("waiting for a backend to wait on %d: %v", pid, err)
		}
	}
}
