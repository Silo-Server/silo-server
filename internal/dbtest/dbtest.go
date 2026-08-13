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
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate applies a schema to a freshly created database.
type Migrate func(ctx context.Context, pool *pgxpool.Pool) error

// Provision creates a uniquely-named database on the server that dsn points at,
// applies migrate to it, and returns a config connected to it together with a
// function that drops it. The returned drop function is safe to call once.
//
// Callers get a *pgxpool.Config rather than a connection string on purpose:
// ConnConfig.ConnString reports the string it was parsed from and does not
// reflect a later change to ConnConfig.Database, so round-tripping through a
// string silently hands the caller the shared database back while the
// migrations run somewhere else.
func Provision(ctx context.Context, dsn, prefix string, migrate Migrate) (*pgxpool.Config, func(), error) {
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to provision %s database: %w", prefix, err)
	}
	// The name is generated, but CREATE/DROP DATABASE take no bind parameters,
	// so the identifier is interpolated either way — quote it.
	quoted := pgx.Identifier{fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())}.Sanitize()
	_, err = admin.Exec(ctx, `CREATE DATABASE `+quoted)
	admin.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("create %s database: %w", prefix, err)
	}

	drop := func() {
		dropper, err := pgxpool.New(context.WithoutCancel(ctx), dsn)
		if err != nil {
			return
		}
		defer dropper.Close()
		// FORCE terminates connections the caller left behind; without it a
		// straggler keeps the database alive and it survives into the next run.
		_, _ = dropper.Exec(context.WithoutCancel(ctx), `DROP DATABASE IF EXISTS `+quoted+` WITH (FORCE)`)
	}

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		drop()
		return nil, nil, fmt.Errorf("parse dsn: %w", err)
	}
	config.ConnConfig.Database = strings.Trim(quoted, `"`)

	// Copy: a *pgxpool.Config may not back more than one pool, and this one is
	// returned to the caller as a template.
	pool, err := pgxpool.NewWithConfig(ctx, config.Copy())
	if err != nil {
		drop()
		return nil, nil, fmt.Errorf("connect to %s database: %w", prefix, err)
	}
	err = migrate(ctx, pool)
	pool.Close()
	if err != nil {
		drop()
		return nil, nil, fmt.Errorf("migrate %s database: %w", prefix, err)
	}
	return config, drop, nil
}
