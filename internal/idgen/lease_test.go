package idgen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

// leaseTestPool connects to a private schema holding only the committed
// idgen_machine_leases migration, so claims never touch a shared table.
func leaseTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("idgen_lease_%d", time.Now().UnixNano())
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})

	raw, err := migrations.FS.ReadFile("sql/20260926062444_idgen_machine_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}

// restoreActive puts back the generator NextID used before the test.
func restoreActive(t *testing.T) {
	t.Helper()
	prev := active.Load()
	t.Cleanup(func() { active.Store(prev) })
}

func startLease(t *testing.T, pool *pgxpool.Pool, holder string) (*Lease, *generator) {
	t.Helper()
	lease, err := Start(t.Context(), pool, holder)
	if err != nil {
		t.Fatalf("Start(%s): %v", holder, err)
	}
	t.Cleanup(lease.Stop)
	return lease, active.Load()
}

func TestStartGivesEachProcessItsOwnMachineID(t *testing.T) {
	pool := leaseTestPool(t)
	restoreActive(t)

	seen := map[int]bool{}
	for i := range 20 {
		_, g := startLease(t, pool, fmt.Sprintf("replica-%d", i))
		if seen[g.machineID] {
			t.Fatalf("machine ID %d leased twice", g.machineID)
		}
		seen[g.machineID] = true
		if g.expired() {
			t.Fatalf("fresh lease on machine ID %d is already expired", g.machineID)
		}
	}
	if _, err := NextID(); err != nil {
		t.Fatalf("NextID after Start: %v", err)
	}
}

func TestStartReusesOnlyTheLongestExpiredMachineID(t *testing.T) {
	pool := leaseTestPool(t)
	restoreActive(t)
	ctx := t.Context()

	// Every machine ID has been used; 7 expired longest ago, 9 expired within
	// the reuse window, and the rest are still held.
	if _, err := pool.Exec(ctx, `
		INSERT INTO idgen_machine_leases (machine_id, token, holder, expires_at)
		SELECT n, gen_random_uuid(), 'old', CASE n
			WHEN 7 THEN now() - interval '3 hours'
			WHEN 8 THEN now() - interval '2 hours'
			WHEN 9 THEN now() - interval '5 minutes'
			ELSE now() + interval '1 hour' END
		FROM generate_series(0, 65535) AS n`); err != nil {
		t.Fatal(err)
	}
	_, g := startLease(t, pool, "new")
	if g.machineID != 7 {
		t.Fatalf("claimed machine ID %d, want the longest-expired 7", g.machineID)
	}

	if _, err := pool.Exec(ctx, `UPDATE idgen_machine_leases SET expires_at = now() + interval '1 hour' WHERE machine_id = 8`); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(ctx, pool, "late"); err == nil {
		t.Fatal("Start succeeded with only a recently expired machine ID free")
	}
}

func TestRenewExtendsTheLease(t *testing.T) {
	pool := leaseTestPool(t)
	restoreActive(t)
	lease, g := startLease(t, pool, "replica")

	g.validUntil.Store(&instant{}) // lapsed long ago
	if _, err := NextID(); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("NextID on a lapsed lease: err = %v, want ErrLeaseExpired", err)
	}
	if got := lease.renew(t.Context(), g); got != g {
		t.Fatalf("renew switched machine ID %d to %d", g.machineID, got.machineID)
	}
	if _, err := NextID(); err != nil {
		t.Fatalf("NextID after renewal: %v", err)
	}

	var remaining time.Duration
	if err := pool.QueryRow(t.Context(), `
		SELECT expires_at - now() FROM idgen_machine_leases WHERE machine_id = $1`,
		g.machineID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining < leaseTTL-time.Minute {
		t.Fatalf("lease expires in %s after renewal, want about %s", remaining, leaseTTL)
	}
}

func TestRenewMovesToANewMachineIDAfterATakeover(t *testing.T) {
	pool := leaseTestPool(t)
	restoreActive(t)
	lease, g := startLease(t, pool, "replica")

	if _, err := pool.Exec(t.Context(), `
		UPDATE idgen_machine_leases SET token = gen_random_uuid(), holder = 'other'
		WHERE machine_id = $1`, g.machineID); err != nil {
		t.Fatal(err)
	}
	next := lease.renew(t.Context(), g)
	if next.machineID == g.machineID {
		t.Fatalf("renew kept machine ID %d after another process took it", g.machineID)
	}
	if active.Load() != next {
		t.Fatal("NextID does not use the new machine ID")
	}
	if _, err := NextID(); err != nil {
		t.Fatalf("NextID after takeover: %v", err)
	}
}

func TestConcurrentClaimsGetDistinctMachineIDs(t *testing.T) {
	pool := leaseTestPool(t)
	const replicas = 16
	ids := make(chan int, replicas)
	errs := make(chan error, replicas)
	for i := range replicas {
		go func() {
			l := &Lease{pool: pool, token: uuid.New(), holder: fmt.Sprintf("replica-%d", i)}
			g, err := l.claim(t.Context())
			if err != nil {
				errs <- err
				return
			}
			ids <- g.machineID
		}()
	}
	seen := map[int]bool{}
	for range replicas {
		select {
		case err := <-errs:
			t.Fatalf("claim: %v", err)
		case id := <-ids:
			if seen[id] {
				t.Fatalf("machine ID %d claimed twice", id)
			}
			seen[id] = true
		}
	}
}

func TestClaimMeasuresTheLeaseFromAfterTheLockWait(t *testing.T) {
	pool := leaseTestPool(t)
	ctx := t.Context()

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, claimLockKey); err != nil {
		t.Fatal(err)
	}

	type result struct {
		g   *generator
		err error
	}
	done := make(chan result, 1)
	go func() {
		l := &Lease{pool: pool, token: uuid.New(), holder: "waiting"}
		g, err := l.claim(ctx)
		done <- result{g, err}
	}()

	// Release only once the claim is queued behind the lock.
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
					AND ((classid::bigint << 32) | objid::bigint) = $1
			)`, claimLockKey).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("claim finished while the lock was held: %+v", r)
		case <-time.After(10 * time.Millisecond):
		}
	}
	released := currentInstant()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, claimLockKey); err != nil {
		t.Fatal(err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("claim: %v", r.err)
	}
	if got, want := r.g.validUntil.Load(), validUntil(released); got.mono < want.mono {
		t.Fatalf("lease deadline counts from before the lock wait: %d < %d", got.mono, want.mono)
	}
}

func TestClaimDoesNotTakeOverALeaseBeingRenewed(t *testing.T) {
	pool := leaseTestPool(t)
	ctx := t.Context()

	// Every machine ID is used. 7 expired longest ago but its holder is
	// renewing it right now; 8 is the next reusable one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO idgen_machine_leases (machine_id, token, holder, expires_at)
		SELECT n, gen_random_uuid(), 'old', CASE n
			WHEN 7 THEN now() - interval '3 hours'
			WHEN 8 THEN now() - interval '2 hours'
			ELSE now() + interval '1 hour' END
		FROM generate_series(0, 65535) AS n`); err != nil {
		t.Fatal(err)
	}
	var renewingToken uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT token FROM idgen_machine_leases WHERE machine_id = 7`).Scan(&renewingToken); err != nil {
		t.Fatal(err)
	}
	renewal, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = renewal.Rollback(context.Background()) }()
	if _, err := renewal.Exec(ctx, `
		UPDATE idgen_machine_leases SET expires_at = now() + interval '2 minutes'
		WHERE machine_id = 7`); err != nil {
		t.Fatal(err)
	}

	type result struct {
		g   *generator
		err error
	}
	done := make(chan result, 1)
	go func() {
		l := &Lease{pool: pool, token: uuid.New(), holder: "new"}
		g, err := l.claim(ctx)
		done <- result{g, err}
	}()

	// A claim that queues behind the renewal would take the row over once the
	// renewal commits, so commit it as soon as the claim finishes or blocks.
	var r result
	for waiting := false; !waiting; {
		select {
		case r = <-done:
			waiting = true
			continue
		case <-time.After(10 * time.Millisecond):
		}
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'transactionid' AND NOT granted)`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
	}
	if err := renewal.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if r.g == nil && r.err == nil {
		r = <-done
	}
	if r.err != nil {
		t.Fatalf("claim: %v", r.err)
	}
	if r.g.machineID != 8 {
		t.Fatalf("claimed machine ID %d, want 8 while 7 is being renewed", r.g.machineID)
	}
	var token uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT token FROM idgen_machine_leases WHERE machine_id = 7`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != renewingToken {
		t.Fatal("claim took over the lease its holder was renewing")
	}
}
