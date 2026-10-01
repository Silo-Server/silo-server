package notifications

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWebhookSecretRotationPG(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
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
	if _, err = pool.Exec(ctx, "CREATE TEMP TABLE notification_webhooks ("+ddl+"\n); ALTER TABLE notification_webhooks ADD COLUMN notify_requests boolean NOT NULL DEFAULT false, ADD COLUMN revision bigint NOT NULL DEFAULT 1"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notification_webhooks(id,user_id,profile_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,consecutive_failures) VALUES ('row',1,'owner','original','generic','url-cipher','example.test','initial-cipher',false,5),('discord',1,'owner','discord','discord','url-cipher','example.test',NULL,true,0)`); err != nil {
		t.Fatal(err)
	}
	repo := NewWebhookRepository(pool)
	// Configuration/provider state can have changed since a rotation was requested.
	if _, err = pool.Exec(ctx, `UPDATE notification_webhooks SET name='newer-config',notify_requests=true,consecutive_failures=9 WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	if err = repo.ReplaceSigningSecret(ctx, "owner", "row", "replacement-cipher"); err != nil {
		t.Fatal(err)
	}
	row, err := repo.GetByID(ctx, "owner", "row")
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "newer-config" || row.Enabled || !row.NotifyRequests || row.ConsecutiveFailures != 9 || row.URLCiphertext != "url-cipher" || row.SigningSecretCiphertext == nil || *row.SigningSecretCiphertext != "replacement-cipher" {
		t.Fatalf("rotation changed unrelated state: %+v", row)
	}
	if err = repo.ReplaceSigningSecret(ctx, "foreign", "row", "foreign"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	if err = repo.ReplaceSigningSecret(ctx, "owner", "missing", "missing"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("missing: %v", err)
	}
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &WebhookService{repo: repo, cipher: cipher}
	value, err := svc.RotateSecretV2(ctx, "owner", "row")
	if err != nil || value == "" {
		t.Fatalf("rotate: %v", err)
	}
	row, err = repo.GetByID(ctx, "owner", "row")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := cipher.Decrypt(*row.SigningSecretCiphertext, webhookSecretAAD("row"))
	if err != nil || decoded != value {
		t.Fatal("stored secret did not match receipt")
	}
	if _, err = svc.RotateSecretV2(ctx, "owner", "discord"); !errors.Is(err, ErrWebhookInvalid) {
		t.Fatalf("discord: %v", err)
	}
	if _, err = svc.RotateSecretV2(ctx, "other", "row"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("other: %v", err)
	}
	if err = repo.Delete(ctx, "owner", "row"); err != nil {
		t.Fatal(err)
	}
	if err = repo.ReplaceSigningSecret(ctx, "owner", "row", "after-deletion"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("deleted row success: %v", err)
	}
}
