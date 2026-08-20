// Package dbtest provisions throwaway databases for tests that cannot share
// one.
//
// Most DB-backed tests in this repo run against the single database named by
// SILO_TEST_DATABASE_URL and keep out of each other's way by suffixing the rows
// they create. That works until a test's subject is global: a migration
// rollback rewrites the schema every other package is reading, and a whole-table
// pass like recommendations' EmbedAll cannot assert "nothing needed doing"
// while any other writer exists. Those tests get a database instead of a
// suffix.
package dbtest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate applies a schema to a freshly created database.
type Migrate func(ctx context.Context, pool *pgxpool.Pool) error

const (
	// setupTimeout bounds connecting to the server and CREATE DATABASE. Both
	// are fast whenever the server answers, so this only has to outlast a slow
	// connect — and an unbounded one hangs the test binary until
	// `go test -timeout` kills it, with nothing to point at.
	setupTimeout = 30 * time.Second

	// dropTimeout bounds the teardown, for the same reason.
	dropTimeout = 30 * time.Second
)

// Provision creates a uniquely-named database on the server that dsn points at,
// applies migrate to it, and returns a config connected to it together with a
// function that drops it.
//
// migrate runs on the caller's context, deliberately: how long a schema takes to
// apply is the caller's business, not this package's, and guessing a bound here
// would either fire on a slow machine or be too loose to mean anything. The
// steps this package does own — connect, CREATE DATABASE, DROP DATABASE — are
// bounded.
//
// The drop function returns its error rather than swallowing it: a drop that
// fails silently leaves a whole database behind, and the next run has no way to
// tell that from a clean one. When provisioning itself fails, a failed cleanup
// is joined onto the returned error rather than discarded, so a leaked database
// is never invisible.
//
// Callers get a *pgxpool.Config rather than a connection string on purpose:
// ConnConfig.ConnString reports the string it was parsed from and does not
// reflect a later change to ConnConfig.Database, so round-tripping through a
// string silently hands the caller the shared database back while the
// migrations run somewhere else.
func Provision(ctx context.Context, dsn, prefix string, migrate Migrate) (*pgxpool.Config, func() error, error) {
	setupCtx, cancelSetup := context.WithTimeout(ctx, setupTimeout)
	defer cancelSetup()

	admin, err := pgxpool.New(setupCtx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to provision %s database: %w", prefix, err)
	}
	// name is what the connection config needs; quoted is what SQL needs.
	// CREATE/DROP DATABASE take no bind parameters, so the identifier is
	// interpolated either way — keep both rather than trying to recover one
	// from the other, which Sanitize's quote-doubling makes lossy.
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	quoted := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(setupCtx, `CREATE DATABASE `+quoted)
	admin.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("create %s database: %w", prefix, err)
	}

	drop := func() error {
		// WithoutCancel so the drop still runs when the caller's context is
		// already done — cleanup usually happens after the work it belongs to.
		// WithTimeout because WithoutCancel drops the deadline along with the
		// cancellation.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dropTimeout)
		defer cancel()
		dropper, err := pgxpool.New(dropCtx, dsn)
		if err != nil {
			return fmt.Errorf("reconnect to drop %s: %w", name, err)
		}
		defer dropper.Close()
		// FORCE terminates connections the caller left behind; without it a
		// straggler keeps the database alive and it survives into the next run.
		if _, err := dropper.Exec(dropCtx, `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
		return nil
	}

	// abandon reports why provisioning failed, and — when the database it
	// already created could not be removed — that one was left behind and what
	// it is called.
	abandon := func(cause error) error {
		return errors.Join(cause, drop())
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, nil, abandon(fmt.Errorf("parse dsn: %w", err))
	}
	config.ConnConfig.Database = name

	// Copy: a *pgxpool.Config may not back more than one pool, and this one is
	// returned to the caller as a template.
	pool, err := pgxpool.NewWithConfig(setupCtx, config.Copy())
	if err != nil {
		return nil, nil, abandon(fmt.Errorf("connect to %s database: %w", prefix, err))
	}
	err = migrate(ctx, pool)
	pool.Close()
	if err != nil {
		return nil, nil, abandon(fmt.Errorf("migrate %s database: %w", prefix, err))
	}
	return config, drop, nil
}
