package notifications

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWebhookGuardedDeletePG(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	schema := fmt.Sprintf("webhook_guard_%d", time.Now().UnixNano())
	if _, err = pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.WithoutCancel(ctx), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	scoped, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	if _, err = scoped.Exec(ctx, `CREATE TABLE notification_webhooks (id text PRIMARY KEY, profile_id text NOT NULL, name text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()); CREATE TABLE attempts(id text PRIMARY KEY, webhook_id text REFERENCES notification_webhooks(id) ON DELETE CASCADE)`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906145619_webhook_conditional_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = scoped.Exec(ctx, strings.ReplaceAll(up, "public.", "")); err != nil {
		t.Fatal(err)
	}
	if _, err = scoped.Exec(ctx, `INSERT INTO notification_webhooks(id,profile_id,name) VALUES ('row','owner','original'); INSERT INTO attempts VALUES ('attempt','row')`); err != nil {
		t.Fatal(err)
	}
	var original int64
	if err = scoped.QueryRow(ctx, `SELECT revision FROM notification_webhooks WHERE id='row'`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	tx, err := scoped.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revised int64
	if err = tx.QueryRow(ctx, `UPDATE notification_webhooks SET name='changed' WHERE id='row' RETURNING revision`).Scan(&revised); err != nil {
		t.Fatal(err)
	}
	if revised <= original {
		t.Fatal("revision did not advance")
	}
	var second int64
	if err = tx.QueryRow(ctx, `UPDATE notification_webhooks SET name='changed-again' WHERE id='row' RETURNING revision`).Scan(&second); err != nil || second <= revised {
		t.Fatalf("same transaction revision: %d %v", second, err)
	}
	stale := errors.New("stale original validator")
	repo := NewWebhookRepository(scoped)
	result := make(chan error, 1)
	go func() {
		result <- repo.DeleteGuarded(ctx, "owner", "row", func(current int64) error {
			if current != original {
				return stale
			}
			return nil
		})
	}()
	// Observe the DELETE's SELECT FOR UPDATE blocked behind the editing transaction.
	for {
		var blocked bool
		err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND cardinality(pg_blocking_pids(pid))>0)`, schema).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, stale) {
		t.Fatalf("stale delete: %v", err)
	}
	var name string
	var attempts int
	if err = scoped.QueryRow(ctx, `SELECT name FROM notification_webhooks WHERE id='row'`).Scan(&name); err != nil || name != "changed-again" {
		t.Fatalf("lost update %s %v", name, err)
	}
	if err = scoped.QueryRow(ctx, `SELECT count(*) FROM attempts`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("stale delete changed attempts")
	}
	if err = repo.DeleteGuarded(ctx, "other", "row", func(int64) error { t.Error("foreign precondition evaluated"); return nil }); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	if err = repo.DeleteGuarded(ctx, "owner", "row", func(current int64) error {
		if current != second {
			return stale
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = scoped.QueryRow(ctx, `SELECT count(*) FROM attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("attempts did not cascade")
	}
	if err = repo.DeleteGuarded(ctx, "owner", "row", func(int64) error { return nil }); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err = scoped.Exec(ctx, `INSERT INTO notification_webhooks(id,profile_id,name,revision) VALUES ('row','owner','recreated',1)`); err != nil {
		t.Fatal(err)
	}
	var recreated int64
	if err = scoped.QueryRow(ctx, `SELECT revision FROM notification_webhooks WHERE id='row'`).Scan(&recreated); err != nil || recreated <= second {
		t.Fatal("recreated validator reused")
	}
}
