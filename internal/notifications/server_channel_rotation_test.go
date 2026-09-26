package notifications

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServerChannelSecretRotationPG(t *testing.T) {
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
	data, err := os.ReadFile("../../migrations/sql/20260612020209_server_notification_channels.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, ddl, ok := strings.Cut(string(data), "CREATE TABLE public.notification_server_channels (")
	if !ok {
		t.Fatal("table missing")
	}
	ddl, _, ok = strings.Cut(ddl, "\n);")
	if !ok {
		t.Fatal("terminator missing")
	}
	if _, err = pool.Exec(ctx, "CREATE TEMP TABLE notification_server_channels ("+ddl+"\n); ALTER TABLE notification_server_channels ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT false, ADD COLUMN notify_new_ebooks boolean NOT NULL DEFAULT false"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO notification_server_channels(id,created_by_user_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,consecutive_failures) VALUES ('row',1,'original','generic','url-cipher','example.test','initial-cipher',false,5),('discord',1,'discord','discord','url-cipher','example.test',NULL,true,0)`); err != nil {
		t.Fatal(err)
	}
	repo := NewServerChannelRepository(pool)
	// Configuration/provider state can have changed since a rotation was requested.
	if _, err = pool.Exec(ctx, `UPDATE notification_server_channels SET name='newer-config',notify_request_submitted=true,consecutive_failures=9,watermark_id='new-watermark',last_attempt_at='2026-01-01T00:00:00Z' WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	if err = repo.ReplaceSigningSecret(ctx, "row", "replacement-cipher"); err != nil {
		t.Fatal(err)
	}
	row, err := repo.GetByID(ctx, "row")
	if err != nil {
		t.Fatal(err)
	}
	if row.WatermarkID != "new-watermark" || row.LastAttemptAt == nil || row.Name != "newer-config" || row.Enabled || !row.NotifyRequestSubmitted || row.ConsecutiveFailures != 9 || row.URLCiphertext != "url-cipher" || row.SigningSecretCiphertext == nil || *row.SigningSecretCiphertext != "replacement-cipher" {
		t.Fatalf("rotation changed unrelated state: %+v", row)
	}
	if err = repo.ReplaceSigningSecret(ctx, "missing", "missing"); !errors.Is(err, ErrServerChannelNotFound) {
		t.Fatalf("missing: %v", err)
	}
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &ServerChannelService{repo: repo, cipher: cipher}
	value, err := svc.RotateSecretV2(ctx, "row")
	if err != nil || value == "" {
		t.Fatalf("rotate: %v", err)
	}
	row, err = repo.GetByID(ctx, "row")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := cipher.Decrypt(*row.SigningSecretCiphertext, serverChannelSecretAAD("row"))
	if err != nil || decoded != value {
		t.Fatal("stored secret did not match receipt")
	}
	if _, err = svc.RotateSecretV2(ctx, "discord"); !errors.Is(err, ErrServerChannelInvalid) {
		t.Fatalf("discord: %v", err)
	}
	if err = repo.Delete(ctx, "row"); err != nil {
		t.Fatal(err)
	}
	if err = repo.ReplaceSigningSecret(ctx, "row", "after-deletion"); !errors.Is(err, ErrServerChannelNotFound) {
		t.Fatalf("deleted row success: %v", err)
	}
}
