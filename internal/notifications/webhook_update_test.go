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

func TestWebhookGuardedUpdatePG(t *testing.T) {
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
	schema := fmt.Sprintf("webhook_update_%d", time.Now().UnixNano())
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
	data, err := os.ReadFile("../../migrations/sql/20260611120000_notification_webhooks.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, ddl, ok := strings.Cut(string(data), "CREATE TABLE public.notification_webhooks (")
	if !ok {
		t.Fatal("table missing")
	}
	ddl, _, ok = strings.Cut(ddl, "\n);")
	if !ok {
		t.Fatal("terminator missing")
	}
	if _, err = scoped.Exec(ctx, "CREATE TABLE notification_webhooks ("+ddl+"\n); ALTER TABLE notification_webhooks ADD COLUMN notify_requests boolean NOT NULL DEFAULT false"); err != nil {
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
	if _, err = scoped.Exec(ctx, `INSERT INTO notification_webhooks(id,user_id,profile_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,consecutive_failures) VALUES ('row',1,'owner','original','generic','url-cipher','example.test','original-secret',false,5)`); err != nil {
		t.Fatal(err)
	}
	repo := NewWebhookRepository(scoped)
	original, err := repo.GetByID(ctx, "owner", "row")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := scoped.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE notification_webhooks SET name='concurrent',signing_secret_ciphertext='new-secret',consecutive_failures=9 WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	stale := errors.New("stale original validator")
	result := make(chan error, 1)
	go func() {
		_, e := repo.UpdateGuarded(ctx, "owner", "row", func(row *Webhook) error {
			if row.Revision != original.Revision {
				return stale
			}
			row.Name = "stale-editor"
			return nil
		})
		result <- e
	}()
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
		t.Fatalf("stale update: %v", err)
	}
	current, err := repo.GetByID(ctx, "owner", "row")
	if err != nil {
		t.Fatal(err)
	}
	if current.Name != "concurrent" || *current.SigningSecretCiphertext != "new-secret" || current.ConsecutiveFailures != 9 {
		t.Fatal("stale update overwrote concurrent state")
	}
	svc := &WebhookService{repo: repo}
	updated, err := svc.UpdateGuarded(ctx, "owner", "row", WebhookInput{Name: new("renamed")}, func(rev int64) error {
		if rev != current.Revision {
			return stale
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision <= current.Revision || updated.Name != "renamed" || *updated.SigningSecretCiphertext != "new-secret" || updated.Enabled || updated.ConsecutiveFailures != 9 {
		t.Fatal("configuration update lost state or returned stale revision")
	}
	_, err = svc.UpdateGuarded(ctx, "owner", "row", WebhookInput{Name: new("again")}, func(rev int64) error {
		if rev != current.Revision {
			return stale
		}
		return nil
	})
	if !errors.Is(err, stale) {
		t.Fatalf("replay: %v", err)
	}
	enabled, err := svc.UpdateGuarded(ctx, "owner", "row", WebhookInput{Enabled: new(true)}, func(int64) error { return nil })
	if err != nil || !enabled.Enabled || enabled.ConsecutiveFailures != 0 || enabled.DisabledReason != nil {
		t.Fatalf("reenable: %v", err)
	}
	for _, scope := range []struct{ profile, id string }{{"foreign", "row"}, {"owner", "missing"}} {
		_, err = repo.UpdateGuarded(ctx, scope.profile, scope.id, func(*Webhook) error { t.Error("hidden precondition invoked"); return nil })
		if !errors.Is(err, ErrWebhookNotFound) {
			t.Fatalf("hidden: %v", err)
		}
	}
	_, err = svc.UpdateGuarded(ctx, "owner", "row", WebhookInput{Name: new(" ")}, func(int64) error { return nil })
	if !errors.Is(err, ErrWebhookInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	after, err := repo.GetByID(ctx, "owner", "row")
	if err != nil || after.Revision != enabled.Revision {
		t.Fatal("invalid update was stored")
	}
}
