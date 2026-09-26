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

func TestServerChannelConfigurationPG(t *testing.T) {
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
	schema := fmt.Sprintf("channel_update_%d", time.Now().UnixNano())
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
	if _, err = scoped.Exec(ctx, "CREATE TABLE notification_server_channels ("+ddl+"\n); ALTER TABLE notification_server_channels ADD COLUMN notify_new_audiobooks boolean NOT NULL DEFAULT false, ADD COLUMN notify_new_ebooks boolean NOT NULL DEFAULT false"); err != nil {
		t.Fatal(err)
	}
	if _, err = scoped.Exec(ctx, `INSERT INTO notification_server_channels(id,created_by_user_id,name,type,url_ciphertext,url_host,signing_secret_ciphertext,enabled,consecutive_failures,watermark_created_at,watermark_id) VALUES ('row',1,'original','generic','url-cipher','example.test','initial-cipher',false,5,'2020-01-01','original-watermark')`); err != nil {
		t.Fatal(err)
	}
	repo := NewServerChannelRepository(scoped)
	svc := &ServerChannelService{repo: repo}
	tx, err := scoped.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE notification_server_channels SET enabled=true,signing_secret_ciphertext='new-secret',watermark_id='concurrent-watermark',consecutive_failures=9 WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, e := svc.UpdateV2(ctx, "row", ServerChannelInput{Name: new("renamed"), Enabled: new(true)})
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
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	row, err := repo.GetByID(ctx, "row")
	if err != nil {
		t.Fatal(err)
	}
	if row.Name != "renamed" || *row.SigningSecretCiphertext != "new-secret" || row.WatermarkID != "concurrent-watermark" || row.ConsecutiveFailures != 9 {
		t.Fatal("update used stale state or lost concurrent writer")
	}

	// A reset waiting for a row lock must use the post-wait clock, not transaction start.
	held, err := scoped.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Rollback(ctx) }()
	if _, err = held.Exec(ctx, `UPDATE notification_server_channels SET enabled=false WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	resetResult := make(chan *ServerChannel, 1)
	go func() {
		v, e := svc.UpdateV2(ctx, "row", ServerChannelInput{Enabled: new(true)})
		resetResult <- v
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
	var afterWait time.Time
	if err = pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&afterWait); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	resetRow := <-resetResult
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if resetRow.WatermarkCreatedAt.Before(afterWait) {
		t.Fatal("reset used pre-lock transaction timestamp")
	}
	disabled, err := svc.UpdateV2(ctx, "row", ServerChannelInput{Enabled: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Enabled || disabled.WatermarkID != "" {
		t.Fatal("disable reset delivery state")
	}
	enabled, err := svc.UpdateV2(ctx, "row", ServerChannelInput{Enabled: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.WatermarkID != "" || enabled.ConsecutiveFailures != 0 || enabled.DisabledReason != nil || !enabled.WatermarkCreatedAt.After(disabled.WatermarkCreatedAt) || *enabled.SigningSecretCiphertext != "new-secret" {
		t.Fatal("reenable failed atomic reset or changed secret")
	}
	again, err := svc.UpdateV2(ctx, "row", ServerChannelInput{Enabled: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	if !again.WatermarkCreatedAt.Equal(enabled.WatermarkCreatedAt) {
		t.Fatal("already delivering reset")
	}
	if _, err = scoped.Exec(ctx, `UPDATE notification_server_channels SET disabled_reason='failures',consecutive_failures=12,last_attempt_at=now(),watermark_id='auto-disabled' WHERE id='row'`); err != nil {
		t.Fatal(err)
	}
	auto, err := svc.UpdateV2(ctx, "row", ServerChannelInput{Name: new("recover")})
	if err != nil {
		t.Fatal(err)
	}
	if auto.DisabledReason != nil || auto.ConsecutiveFailures != 0 || auto.LastAttemptAt != nil || auto.WatermarkID != "" {
		t.Fatal("auto-disabled reset differs from bridge")
	}
	_, err = svc.UpdateV2(ctx, "row", ServerChannelInput{Name: new(" ")})
	if !errors.Is(err, ErrServerChannelInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	after, err := repo.GetByID(ctx, "row")
	if err != nil || after.Name != "recover" || !after.WatermarkCreatedAt.Equal(auto.WatermarkCreatedAt) {
		t.Fatal("invalid write changed row")
	}
	_, err = svc.UpdateV2(ctx, "missing", ServerChannelInput{})
	if !errors.Is(err, ErrServerChannelNotFound) {
		t.Fatalf("missing: %v", err)
	}
}
