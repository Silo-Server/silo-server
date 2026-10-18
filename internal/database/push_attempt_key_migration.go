package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

// pushAttemptKeyScopeVersion first shipped as an SQL migration of the same
// version; a deployment that applied that file skips this one.
const pushAttemptKeyScopeVersion int64 = 20261012230946

const (
	// pushAttemptKey is the original at-most-once key over every attempt
	// that names a delivery.
	pushAttemptKey = "push_delivery_attempts_delivery_unique"
	// pushAttemptDeliveryKey is the same key over delivery attempts only.
	pushAttemptDeliveryKey = "push_delivery_attempts_delivery_trigger_unique"
	pushAttemptKeyColumns  = "push_delivery_attempts (push_device_id, notification_delivery_id, attempt_number)"
)

// pushAttemptKeyScopeMigration scopes push_delivery_attempts' at-most-once key
// to delivery attempts. An administrator test push carries the target
// profile's newest notification, so a test attempt names a real delivery and
// can share its (device, delivery, attempt number) with that delivery's own
// attempt and with earlier tests. The key only ever guarded fanout's enqueue.
//
// A Go migration for the reason invalidIndexRebuildMigration is: a retry must
// keep an index an earlier attempt built. goose records the version only after
// every statement has run, so an attempt that dropped the old key but was not
// recorded runs again, and dropping the replacement then would leave the table
// with neither key while it was rebuilt.
func pushAttemptKeyScopeMigration() *goose.Migration {
	m := goose.NewGoMigration(
		pushAttemptKeyScopeVersion,
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			return scopePushAttemptKey(ctx, db, "public")
		}},
		&goose.GoFunc{RunDB: func(ctx context.Context, db *sql.DB) error {
			return widenPushAttemptKey(ctx, db, "public")
		}},
	)
	m.Source = fmt.Sprintf("%d_scope_push_attempt_key_to_deliveries.go", pushAttemptKeyScopeVersion)
	return m
}

// scopePushAttemptKey replaces the old key with the delivery-only one.
func scopePushAttemptKey(ctx context.Context, db *sql.DB, schema string) error {
	return swapPushAttemptKey(ctx, db, schema, pushAttemptDeliveryKey, "trigger_type = 'delivery'", pushAttemptKey)
}

// widenPushAttemptKey restores the old key. It cannot hold test attempts
// beside a delivery's own, so they are detached from their deliveries first;
// their history rows remain.
func widenPushAttemptKey(ctx context.Context, db *sql.DB, schema string) error {
	if _, err := db.ExecContext(ctx, `UPDATE `+pgx.Identifier{schema}.Sanitize()+`.push_delivery_attempts
		SET notification_delivery_id = NULL
		WHERE trigger_type = 'test' AND notification_delivery_id IS NOT NULL`); err != nil {
		return fmt.Errorf("detaching test push attempts: %w", err)
	}
	return swapPushAttemptKey(ctx, db, schema, pushAttemptKey, "notification_delivery_id IS NOT NULL", pushAttemptDeliveryKey)
}

// swapPushAttemptKey builds the key named keep, over the rows predicate
// selects, before dropping the key named drop, so fanout is never without one.
func swapPushAttemptKey(ctx context.Context, db *sql.DB, schema, keep, predicate, drop string) error {
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if err := ensureIndexConcurrently(ctx, db, quotedSchema+"."+keep, fmt.Sprintf(
		"CREATE UNIQUE INDEX CONCURRENTLY %s ON %s.%s WHERE %s", keep, quotedSchema, pushAttemptKeyColumns, predicate)); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+quotedSchema+"."+drop); err != nil {
		return fmt.Errorf("dropping %s: %w", drop, err)
	}
	return nil
}
