package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// migrationWrapperLockID spells "SILOMIGR". It wraps the whole startup
// migration sequence: the legacy bootstrap, the pending check, the backup
// taken before migrations, and goose Up. Lock order is this wrapper, then
// the backup subsystem lock, then goose's own lock (schemaMigrationsLockID);
// nothing takes the wrapper while holding either of the others.
const migrationWrapperLockID int64 = 0x53494C4F4D494752

const (
	// migrationWaitEnv bounds how long a server waits for another server's
	// migration sequence (a Go duration such as "90m").
	migrationWaitEnv     = "SILO_MIGRATION_WAIT"
	defaultMigrationWait = 60 * time.Minute
	// wrapperCheckInterval is how often the wrapper's session is checked.
	wrapperCheckInterval = 10 * time.Second
)

// ErrMigrationLockLost reports that the session holding the migration
// wrapper or the lock the migrations ran under ended: another server may
// already be migrating, so this one stops.
var ErrMigrationLockLost = errors.New("the database migration lock's session ended")

// MigrationWait returns how long a server waits for another server's
// migration sequence: SILO_MIGRATION_WAIT, default 60 minutes. Zero or less
// waits indefinitely. Unparseable falls back to the default.
func MigrationWait() time.Duration {
	raw := strings.TrimSpace(os.Getenv(migrationWaitEnv))
	if raw == "" {
		return defaultMigrationWait
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("invalid migration wait; using default",
			"env", migrationWaitEnv, "value", raw,
			"default", defaultMigrationWait.String(), "error", err)
		return defaultMigrationWait
	}
	return d
}

// Guard is a lock the migrations run under, held from before them until
// goose Up returns: the backup subsystem lock. A lost guard cancels Up.
type Guard interface {
	Lost() <-chan struct{}
	Release(ctx context.Context)
}

// StartupOptions configure MigrateAtStartup.
type StartupOptions struct {
	// Wait bounds the wait for the wrapper while another server runs the
	// sequence. Zero or less waits indefinitely.
	Wait time.Duration
	// BeforeMigrate runs under the wrapper when migrations are pending on a
	// database that already has a schema: it takes the backup before
	// migrations. It returns the guard goose Up runs under, or nil. An error
	// stops startup before anything is migrated.
	BeforeMigrate func(ctx context.Context) (Guard, error)
	// CheckInterval overrides how often the wrapper's session is checked.
	CheckInterval time.Duration
	Logger        *slog.Logger
}

// MigrateAtStartup is the migration sequence every API server runs at boot
// and --migrate-only runs on its own:
//
//  1. take the wrapper lock on a connection outside the pool, waiting up to
//     opts.Wait while another server holds it;
//  2. run the legacy schema_versions bootstrap, so the pending check sees the
//     versions a legacy install already applied;
//  3. ask goose whether migrations are pending;
//  4. if so, on a database that has a schema, run opts.BeforeMigrate and then
//     goose Up with the same provider under the guard it returned;
//  5. release both, then ensure the audit-detail indexes.
//
// A lost wrapper or guard session cancels the backup or migration in flight
// and fails the sequence. Goose Up and the index step get the
// SILO_MIGRATE_TIMEOUT budget; the backup is bounded only by cancellation.
func MigrateAtStartup(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, opts StartupOptions) error {
	err := migrateAtStartup(ctx, pool, opts, func() (*migrationProvider, error) {
		return openMigrationProvider(pool, fsys, dir, goMigrations())
	})
	if err != nil {
		return err
	}
	idxCtx, cancel := MigrationContext(ctx)
	defer cancel()
	return ensureAuditIndexes(idxCtx, pool)
}

// migrationProvider is a goose provider with the bootstrap locker it runs
// under and the database handle both use. Closing the provider closes db.
type migrationProvider struct {
	*goose.Provider
	db     *sql.DB
	locker lock.SessionLocker
}

func openMigrationProvider(pool *pgxpool.Pool, fsys fs.FS, dir string, goMigs []*goose.Migration) (*migrationProvider, error) {
	migrationFS, err := migrationSubFS(fsys, dir)
	if err != nil {
		return nil, err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	inner, err := lock.NewPostgresSessionLocker(lock.WithLockID(schemaMigrationsLockID))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("creating goose migration lock: %w", err)
	}
	locker := &legacyBootstrapLocker{delegate: inner}
	provider, err := newGooseProvider(sqlDB, migrationFS, locker, goMigs)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &migrationProvider{Provider: provider, db: sqlDB, locker: locker}, nil
}

// bootstrap runs the legacy schema_versions bootstrap under goose's own
// lock, as every goose operation that takes the lock does. HasPending takes
// no lock, so without this a legacy install would look entirely pending.
func (p *migrationProvider) bootstrap(ctx context.Context) error {
	conn, err := p.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := p.locker.SessionLock(ctx, conn); err != nil {
		return err
	}
	return p.locker.SessionUnlock(context.WithoutCancel(ctx), conn)
}

func migrateAtStartup(ctx context.Context, pool *pgxpool.Pool, opts StartupOptions, open func() (*migrationProvider, error)) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	interval := opts.CheckInterval
	if interval <= 0 {
		interval = wrapperCheckInterval
	}
	wrapper, err := acquireMigrationWrapper(ctx, pool, opts.Wait, logger)
	if err != nil {
		return err
	}
	defer wrapper.release(context.WithoutCancel(ctx))
	go wrapper.watch(interval)
	ctx, cancel := cancelOnLost(ctx, wrapper.lost)
	defer cancel()

	provider, err := open()
	if err != nil {
		return err
	}
	defer func() { _ = provider.Close() }()

	if err := provider.bootstrap(ctx); err != nil {
		return lostOr(ctx, fmt.Errorf("bootstrapping goose migration versions: %w", err))
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		return lostOr(ctx, fmt.Errorf("checking for pending migrations: %w", err))
	}
	if !pending {
		logger.InfoContext(ctx, "database schema is up to date")
		return nil
	}
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return lostOr(ctx, fmt.Errorf("reading the database schema version: %w", err))
	}
	if current == 0 {
		logger.InfoContext(ctx, "new database; no backup before migrations")
	} else if opts.BeforeMigrate != nil {
		guard, err := opts.BeforeMigrate(ctx)
		if err != nil {
			return lostOr(ctx, err)
		}
		if guard != nil {
			defer guard.Release(context.WithoutCancel(ctx))
			var cancelGuard context.CancelFunc
			ctx, cancelGuard = cancelOnLost(ctx, guard.Lost())
			defer cancelGuard()
		}
	}
	if err := ctx.Err(); err != nil {
		return lostOr(ctx, err)
	}
	upCtx, cancelUp := MigrationContext(ctx)
	defer cancelUp()
	if err := applyMigrationsLogged(upCtx, provider, logger, migrationHeartbeatInterval); err != nil {
		return lostOr(ctx, err)
	}
	// Connections look up the vector types when they open. On a new database
	// the migrations create them, so reconnect for the binary codecs.
	pool.Reset()
	return nil
}

// cancelOnLost returns a context canceled with ErrMigrationLockLost once
// lost is closed.
func cancelOnLost(ctx context.Context, lost <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(ctx)
	go func() {
		select {
		case <-lost:
			cancel(ErrMigrationLockLost)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(context.Canceled) }
}

// lostOr names a lost lock as the reason when it is one.
func lostOr(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrMigrationLockLost) && !errors.Is(err, ErrMigrationLockLost) {
		return fmt.Errorf("%w: %w", ErrMigrationLockLost, err)
	}
	return err
}

// migrationWrapper is the wrapper lock on a connection of its own, outside
// the pool, so the work under it can use every pool connection.
type migrationWrapper struct {
	mu       sync.Mutex
	conn     *pgx.Conn
	lost     chan struct{}
	stop     chan struct{}
	lostOnce sync.Once
	stopOnce sync.Once
	released bool
}

func acquireMigrationWrapper(ctx context.Context, pool *pgxpool.Pool, wait time.Duration, logger *slog.Logger) (*migrationWrapper, error) {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("opening the migration lock session: %w", err)
	}
	fail := func(err error) (*migrationWrapper, error) {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
		return nil, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, migrationWrapperLockID).Scan(&acquired); err != nil {
		return fail(fmt.Errorf("taking the migration lock: %w", err))
	}
	if !acquired {
		if err := waitForMigrationWrapper(ctx, conn, wait, logger); err != nil {
			return fail(err)
		}
	}
	return &migrationWrapper{conn: conn, lost: make(chan struct{}), stop: make(chan struct{})}, nil
}

// waitForMigrationWrapper blocks on the wrapper lock for up to wait, logging
// the wait. After an error the lock may have been granted just before it, so
// the caller closes the session, which releases it.
func waitForMigrationWrapper(ctx context.Context, conn *pgx.Conn, wait time.Duration, logger *slog.Logger) error {
	waitFor := "indefinitely"
	if wait > 0 {
		waitFor = wait.String()
	}
	logger.InfoContext(ctx, "another server is migrating the database; waiting for it before starting (not ready)",
		"wait", waitFor, "env", migrationWaitEnv)
	started := time.Now()
	stop := startMigrationHeartbeat(migrationHeartbeatInterval, func(elapsed string) {
		logger.InfoContext(ctx, "still waiting for another server's database migrations (not ready)", "elapsed", elapsed)
	})
	defer stop()
	var acquired bool
	var err error
	if wait > 0 {
		acquired, err = pglock.LockWithin(ctx, conn, migrationWrapperLockID, wait)
	} else {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationWrapperLockID)
		acquired = err == nil
	}
	switch {
	case err != nil:
		return fmt.Errorf("waiting for the migration lock: %w", err)
	case !acquired:
		return fmt.Errorf("another server held the database migration lock for longer than %s; "+
			"raise %s if its backup and migrations need longer", wait, migrationWaitEnv)
	}
	logger.InfoContext(ctx, "the other server's migration sequence finished", "waited", roundMigrationDuration(time.Since(started)))
	return nil
}

// watch checks the session every interval and closes lost once it ended.
func (w *migrationWrapper) watch(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.check(); err != nil {
				slog.Warn("database migration lock session ended; stopping startup", "error", err)
				w.lostOnce.Do(func() { close(w.lost) })
				return
			}
		}
	}
}

func (w *migrationWrapper) check() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := w.conn.Exec(ctx, `SELECT 1`)
	return err
}

// release stops watch, unlocks, and closes the session; closing releases
// the lock even when the unlock fails. It is idempotent.
func (w *migrationWrapper) release(ctx context.Context) {
	w.stopOnce.Do(func() { close(w.stop) })
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.released {
		return
	}
	w.released = true
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = w.conn.Exec(closeCtx, `SELECT pg_advisory_unlock($1)`, migrationWrapperLockID)
	_ = w.conn.Close(closeCtx)
}
