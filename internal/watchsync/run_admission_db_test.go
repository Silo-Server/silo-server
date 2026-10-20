package watchsync

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/secret"
)

// admissionLockTrace coordinates two repository calls at the row lock. These
// fixtures never execute a service or provider, and use no timing-based waits.
type admissionLockTrace struct {
	started chan uint32
	locked  chan struct{}
	release chan struct{}
}

func (tr *admissionLockTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FOR NO KEY UPDATE") {
		if tr.started != nil {
			tr.started <- conn.PgConn().PID()
		}
		return context.WithValue(ctx, admissionTraceKey{}, true)
	}
	return ctx
}

func (tr *admissionLockTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(admissionTraceKey{}) == true && data.Err == nil && tr.locked != nil {
		tr.locked <- struct{}{}
		select {
		case <-tr.release:
		case <-ctx.Done():
		}
	}
}

type admissionTraceKey struct{}

type admissionReply struct {
	admitted syncRunAdmission
	err      error
}

// awaitAdmissionLock observes a real PostgreSQL wait, rather than a client
// query-start callback. Its read-only snapshots are bounded; only the two fixed
// admission operations above contend, and no provider work runs.
func awaitAdmissionLock(t *testing.T, ctx context.Context, observer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, pid uint32) {
	t.Helper()
	for range 128 {
		var blocked bool
		if err := observer.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1::integer)) > 0", pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("repository operation did not enter the expected PostgreSQL lock wait")
}

//nolint:funlen,gocyclo // Transaction scenarios share one bounded synthetic account fixture.
func TestSyncRunAdmissionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cipher, err := secret.New([]byte("synthetic-admission-fixture-key-with-entropy"))
	if err != nil {
		t.Fatal(err)
	}
	newPool := func(t *testing.T, tracer pgx.QueryTracer) *pgxpool.Pool {
		t.Helper()
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = 1
		cfg.ConnConfig.Tracer = tracer
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	pool := newPool(t, nil)
	repo := NewPostgresRepository(pool, cipher)
	var userID int
	if err := pool.QueryRow(ctx, "INSERT INTO users(username,role) VALUES($1,'user') RETURNING id", "admission-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID) })
	if _, err := pool.Exec(ctx, "INSERT INTO user_profiles(user_id,id,name) VALUES($1,'admission-p','Synthetic')", userID); err != nil {
		t.Fatal(err)
	}
	connection := func(t *testing.T) Connection {
		t.Helper()
		conn, err := repo.UpsertConnection(ctx, Connection{Provider: uuid.NewString(), UserID: userID, ProfileID: "admission-p"})
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	admit := func(t *testing.T, conn Connection, trigger string, at time.Time) syncRunAdmission {
		t.Helper()
		repo.now = func() time.Time { return at }
		got, err := repo.AdmitSyncRun(ctx, conn, SyncRun{Trigger: trigger})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	fixtureRun := func(t *testing.T, conn Connection, run SyncRun) SyncRun {
		t.Helper()
		run.ConnectionID, run.Provider = conn.ID, conn.Provider
		var saved SyncRun
		if err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			var err error
			saved, err = createSyncRun(ctx, tx, run)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return saved
	}

	t.Run("two independent repositories commit one manual reservation", func(t *testing.T) {
		conn := connection(t)
		firstTrace := &admissionLockTrace{locked: make(chan struct{}, 1), release: make(chan struct{})}
		release := sync.OnceFunc(func() { close(firstTrace.release) })
		defer release()
		secondTrace := &admissionLockTrace{started: make(chan uint32, 1)}
		first := NewPostgresRepository(newPool(t, firstTrace), cipher)
		second := NewPostgresRepository(newPool(t, secondTrace), cipher)
		first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
		// Buffered results and a bounded context also release the fixtures on failure.
		results := make(chan admissionReply, 2)
		go func() {
			got, err := first.AdmitSyncRun(ctx, conn, SyncRun{Trigger: "manual", StartedAt: now})
			results <- admissionReply{got, err}
		}()
		select {
		case <-firstTrace.locked:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		go func() {
			got, err := second.AdmitSyncRun(ctx, conn, SyncRun{Trigger: "manual", StartedAt: now})
			results <- admissionReply{got, err}
		}()
		select {
		case pid := <-secondTrace.started:
			awaitAdmissionLock(t, ctx, pool, pid)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		release()
		a, b := <-results, <-results
		if a.err != nil || b.err != nil || a.admitted.Created == b.admitted.Created || a.admitted.Run.ID != b.admitted.Run.ID || a.admitted.Run.ID == "" {
			t.Fatalf("reservation outcomes = %+v / %+v", a, b)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM watch_provider_sync_runs WHERE connection_id=$1::uuid", conn.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("durable run count = %d, err %v", count, err)
		}
	})

	t.Run("queued and running rows coalesce through timeout boundary", func(t *testing.T) {
		for _, status := range []SyncRunStatus{SyncRunStatusQueued, SyncRunStatusRunning} {
			conn := connection(t)
			run := fixtureRun(t, conn, SyncRun{Trigger: "scheduled", Status: string(status), StartedAt: now})
			got := admit(t, conn, "manual", now.Add(manualSyncTimeout))
			if got.Created || got.Run.ID != run.ID || got.RetryAfterSeconds != 0 {
				t.Fatalf("coalesced = %+v", got)
			}
		}
	})

	t.Run("stale retirement commits with cooldown and retains late results", func(t *testing.T) {
		conn := connection(t)
		run := fixtureRun(t, conn, SyncRun{Trigger: "scheduled", StartedAt: now.Add(-manualSyncTimeout - time.Microsecond)})
		got := admit(t, conn, "manual", now)
		if got.Created || got.RetryAfterSeconds != 3600 {
			t.Fatalf("retired outcome = %+v", got)
		}
		retired, found, err := repo.GetLatestSyncRun(ctx, conn.ID)
		if err != nil || !found || retired.Status != string(SyncRunStatusFailed) || retired.CompletedAt == nil || !retired.CompletedAt.Equal(now) {
			t.Fatalf("retirement = %+v, %v", retired, err)
		}
		got = admit(t, conn, "manual", now.Add(time.Minute))
		if got.RetryAfterSeconds != 3540 {
			t.Fatalf("retirement reset cooldown: %+v", got)
		}
		// A real scheduled result can arrive after manual timeout retirement.
		run.Status, run.OutboundSent, run.CompletedAt = string(SyncRunStatusSuccess), 2, new(now.Add(2*time.Minute))
		completed, err := repo.CompleteSyncRun(ctx, run)
		if err != nil || completed.OutboundSent != 2 || completed.Status != string(SyncRunStatusSuccess) {
			t.Fatalf("late completion = %+v, %v", completed, err)
		}
	})

	t.Run("completion reference and exact cooldown boundary", func(t *testing.T) {
		conn := connection(t)
		fixtureRun(t, conn, SyncRun{Trigger: "manual", Status: string(SyncRunStatusSuccess), StartedAt: now.Add(-2 * time.Hour), CompletedAt: new(now.Add(-time.Hour + time.Microsecond))})
		if got := admit(t, conn, "manual", now); got.Created || got.RetryAfterSeconds != 1 {
			t.Fatalf("before boundary = %+v", got)
		}
		if got := admit(t, conn, "manual", now.Add(time.Microsecond)); !got.Created {
			t.Fatalf("at boundary = %+v", got)
		}
	})

	t.Run("current provider deferral precedes coalescing and expires", func(t *testing.T) {
		conn := connection(t)
		until := now.Add(time.Second + time.Microsecond)
		if _, err := pool.Exec(ctx, "UPDATE watch_provider_connections SET rate_limited_until=$2 WHERE id=$1::uuid", conn.ID, until); err != nil {
			t.Fatal(err)
		}
		if got := admit(t, conn, "manual", now); got.Created || got.RetryAfterSeconds != 2 {
			t.Fatalf("deferral = %+v", got)
		}
		if got := admit(t, conn, "manual", until); !got.Created {
			t.Fatalf("expired deferral = %+v", got)
		}
	})

	t.Run("ownership and stale connection identity", func(t *testing.T) {
		conn := connection(t)
		for _, wrong := range []Connection{
			{Provider: conn.Provider, UserID: userID + 1, ProfileID: conn.ProfileID},
			{Provider: conn.Provider, UserID: userID, ProfileID: "other-profile"},
			{ID: uuid.NewString(), Provider: conn.Provider, UserID: userID, ProfileID: conn.ProfileID},
		} {
			if _, err := repo.AdmitSyncRun(ctx, wrong, SyncRun{Trigger: "manual", StartedAt: now}); !errors.Is(err, ErrConnectionNotFound) {
				t.Fatalf("scope error = %v", err)
			}
		}
		if got := admit(t, conn, "manual", now); !got.Created {
			t.Fatalf("valid owner = %+v", got)
		}
	})

	t.Run("both producers respect manual reservations without changing scheduled policy", func(t *testing.T) {
		conn := connection(t)
		manual := admit(t, conn, "manual", now)
		if got := admit(t, conn, "scheduled", now); got.Created || got.Run.ID != manual.Run.ID {
			t.Fatalf("scheduled after manual = %+v", got)
		}
		if got := admit(t, conn, "scheduled", now.Add(manualSyncTimeout+time.Microsecond)); !got.Created {
			t.Fatalf("scheduled after manual deadline = %+v", got)
		}
		conn = connection(t)
		scheduled := admit(t, conn, "scheduled", now)
		if got := admit(t, conn, "manual", now); got.Created || got.Run.ID != scheduled.Run.ID {
			t.Fatalf("manual after scheduled = %+v", got)
		}
		if got := admit(t, conn, "scheduled", now); !got.Created {
			t.Fatalf("existing scheduled overlap policy = %+v", got)
		}
	})

	t.Run("canceled blocked admission rolls back and releases its only pool connection", func(t *testing.T) {
		conn := connection(t)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, "SELECT id FROM watch_provider_connections WHERE id=$1::uuid FOR NO KEY UPDATE", conn.ID); err != nil {
			t.Fatal(err)
		}
		trace := &admissionLockTrace{started: make(chan uint32, 1)}
		otherPool := newPool(t, trace)
		other := NewPostgresRepository(otherPool, cipher)
		other.now = func() time.Time { return now }
		blockedCtx, stop := context.WithCancel(ctx)
		defer stop()
		result := make(chan error, 1)
		go func() {
			_, err := other.AdmitSyncRun(blockedCtx, conn, SyncRun{Trigger: "manual", StartedAt: now})
			result <- err
		}()
		select {
		case pid := <-trace.started:
			awaitAdmissionLock(t, ctx, tx, pid)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		stop()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error = %v", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		trace.started = nil // the preceding call is terminal; subsequent calls need no coordination
		got, err := other.AdmitSyncRun(ctx, conn, SyncRun{Trigger: "manual", StartedAt: now})
		if err != nil || !got.Created {
			t.Fatalf("admission after rollback = %+v, %v", got, err)
		}
	})

	t.Run("admission samples time after waiting for the connection lock", func(t *testing.T) {
		conn := connection(t)
		fixtureRun(t, conn, SyncRun{Trigger: "manual", StartedAt: now})
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, "SELECT id FROM watch_provider_connections WHERE id=$1::uuid FOR NO KEY UPDATE", conn.ID); err != nil {
			t.Fatal(err)
		}
		trace := &admissionLockTrace{started: make(chan uint32, 1)}
		other := NewPostgresRepository(newPool(t, trace), cipher)
		var clock atomic.Pointer[time.Time]
		clock.Store(new(now.Add(manualSyncTimeout)))
		other.now = func() time.Time { return *clock.Load() }
		result := make(chan admissionReply, 1)
		go func() {
			got, err := other.AdmitSyncRun(ctx, conn, SyncRun{Trigger: "scheduled", StartedAt: now.Add(manualSyncTimeout)})
			result <- admissionReply{got, err}
		}()
		select {
		case pid := <-trace.started:
			awaitAdmissionLock(t, ctx, tx, pid)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		after := now.Add(manualSyncTimeout + time.Microsecond)
		clock.Store(new(after))
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		got := <-result
		if got.err != nil || !got.admitted.Created || !got.admitted.Run.StartedAt.Equal(after) {
			t.Fatalf("admission after boundary = %+v, %v", got.admitted, got.err)
		}
	})

	t.Run("failed completion and deleted parent preserve terminal semantics", func(t *testing.T) {
		conn := connection(t)
		run := admit(t, conn, "manual", now).Run
		run.Status, run.Error, run.CompletedAt = string(SyncRunStatusFailed), "synthetic cancellation", new(now)
		if got, err := repo.CompleteSyncRun(ctx, run); err != nil || got.Status != string(SyncRunStatusFailed) {
			t.Fatalf("failure completion = %+v, %v", got, err)
		}
		if err := repo.DeleteConnection(ctx, conn.Provider, conn.UserID, conn.ProfileID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.CompleteSyncRun(ctx, run); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("completion after disconnect = %v", err)
		}
	})
}
