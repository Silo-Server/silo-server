package idgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// leaseTTL is how long a claim or renewal holds a machine ID.
	leaseTTL = 2 * time.Minute
	// renewInterval leaves several renewal attempts inside one TTL, so a
	// short database outage does not stop ID generation.
	renewInterval = 20 * time.Second
	// expiryMargin ends this process's use of a machine ID before the
	// database expiry, covering sequence waits inside NextID.
	expiryMargin = 10 * time.Second
	// reuseAfter is how long an expired lease rests before another process may
	// claim its machine ID, so clock skew between two holders cannot put their
	// IDs in the same time slot. Claims prefer never-used machine IDs, so
	// reuse starts only once all 65,536 have been handed out.
	reuseAfter = time.Hour
	// claimLockKey serializes claims across processes ("SILOIDM").
	claimLockKey int64 = 0x53494c4f49444d
)

// Lease is this process's hold on a Sonyflake machine ID. It renews in the
// background until [Lease.Stop].
type Lease struct {
	pool   *pgxpool.Pool
	token  uuid.UUID
	holder string
	cancel context.CancelFunc
	done   chan struct{}
}

// Start leases a machine ID from pool and makes NextID use it. holder names
// this process in idgen_machine_leases for operators; uniqueness comes from
// the lease itself. Call it once at startup, after migrations and before
// anything generates IDs. Callers own the returned Lease and should Stop it
// before closing pool.
func Start(ctx context.Context, pool *pgxpool.Pool, holder string) (*Lease, error) {
	l := &Lease{pool: pool, token: uuid.New(), holder: holder, done: make(chan struct{})}
	g, err := l.claim(ctx)
	if err != nil {
		return nil, err
	}
	active.Store(g)
	slog.InfoContext(ctx, "idgen: leased Sonyflake machine ID", "machine_id", g.machineID, "holder", holder)

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	l.cancel = cancel
	go l.run(runCtx, g)
	return l, nil
}

// Stop ends renewal. NextID keeps working until the current lease runs out,
// which leaves shutdown time to drain in-flight writers.
func (l *Lease) Stop() {
	if l == nil {
		return
	}
	l.cancel()
	<-l.done
}

func (l *Lease) run(ctx context.Context, g *generator) {
	defer close(l.done)
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g = l.renew(ctx, g)
		}
	}
}

// renew extends g's lease and returns the generator NextID should use next.
// A failed renewal leaves g in place; NextID refuses it once its deadline
// passes and resumes when a later renewal succeeds.
func (l *Lease) renew(ctx context.Context, g *generator) *generator {
	started := currentInstant()
	queryCtx, cancel := context.WithTimeout(ctx, renewInterval)
	defer cancel()
	tag, err := l.pool.Exec(queryCtx, `
		UPDATE idgen_machine_leases
		SET expires_at = statement_timestamp() + make_interval(secs => $3)
		WHERE machine_id = $1 AND token = $2`,
		g.machineID, l.token, leaseTTL.Seconds())
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "idgen: renewing machine ID lease failed", "machine_id", g.machineID, "error", err)
		}
		return g
	}
	if tag.RowsAffected() == 1 {
		g.validUntil.Store(validUntil(started))
		return g
	}

	// Another process claimed the machine ID after this lease lapsed. g has
	// been refused since its deadline, so switch to a fresh machine ID.
	slog.ErrorContext(ctx, "idgen: machine ID lease was taken over; claiming a new one", "machine_id", g.machineID)
	next, err := l.claim(queryCtx)
	if err != nil {
		slog.ErrorContext(ctx, "idgen: claiming a new machine ID failed", "error", err)
		return g
	}
	active.Store(next)
	slog.InfoContext(ctx, "idgen: leased Sonyflake machine ID", "machine_id", next.machineID, "holder", l.holder)
	return next
}

// validUntil is the local deadline for a lease the database extended to
// statement_timestamp() + leaseTTL by a statement sent at started. The
// statement cannot start before it was sent, so the local deadline always
// comes first. Expiries use statement_timestamp() rather than now(), which is
// the transaction start and would predate a claim's wait for claimLockKey.
func validUntil(started instant) *instant {
	valid := int64(leaseTTL - expiryMargin)
	return &instant{mono: started.mono + valid, wall: started.wall + valid}
}

// claim leases a machine ID, preferring one no process has ever used, and
// otherwise the one whose lease expired longest ago, at least reuseAfter ago.
func (l *Lease) claim(ctx context.Context) (*generator, error) {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("idgen: claiming machine ID: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, claimLockKey); err != nil {
		return nil, fmt.Errorf("idgen: claiming machine ID: %w", err)
	}
	// Measure the lease from after the lock wait, which can be long when
	// several processes start at once.
	started := currentInstant()
	var machineID int
	err = tx.QueryRow(ctx, `
		INSERT INTO idgen_machine_leases (machine_id, token, holder, expires_at)
		SELECT n, $1, $2, statement_timestamp() + make_interval(secs => $3)
		FROM generate_series(0, 65535) AS n
		WHERE NOT EXISTS (SELECT 1 FROM idgen_machine_leases l WHERE l.machine_id = n)
		ORDER BY random()
		LIMIT 1
		RETURNING machine_id`,
		l.token, l.holder, leaseTTL.Seconds()).Scan(&machineID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			UPDATE idgen_machine_leases
			SET token = $1, holder = $2, acquired_at = statement_timestamp(),
			    expires_at = statement_timestamp() + make_interval(secs => $3)
			WHERE machine_id = (
				SELECT machine_id FROM idgen_machine_leases
				WHERE expires_at < statement_timestamp() - make_interval(secs => $4)
				ORDER BY expires_at
				LIMIT 1
				-- Lock the candidate so its expiry is rechecked against the
				-- latest row: a lease renewed after this statement started is
				-- skipped instead of taken over while its holder still mints.
				FOR UPDATE SKIP LOCKED
			)
			RETURNING machine_id`,
			l.token, l.holder, leaseTTL.Seconds(), reuseAfter.Seconds()).Scan(&machineID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("idgen: every Sonyflake machine ID is leased or expired within the last hour")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("idgen: claiming machine ID: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("idgen: claiming machine ID: %w", err)
	}

	g, err := newGenerator(machineID)
	if err != nil {
		return nil, fmt.Errorf("idgen: %w", err)
	}
	g.validUntil.Store(validUntil(started))
	return g, nil
}
