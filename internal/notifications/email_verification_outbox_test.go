package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func emailOutboxFixture(t *testing.T) (*EmailPrefsRepository, *pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL for isolated email verification tests")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "email_outbox_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	cfg := admin.Config()
	cfg.MaxConns = 6
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `CREATE TABLE users(id integer PRIMARY KEY,username text,email text);INSERT INTO users VALUES(7,'owner','owner@example.test'),(8,'other','other@example.test')`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../migrations/sql/20260612001031_profile_email_notifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(b)
	start := strings.Index(ddl, "CREATE TABLE public.notification_email_prefs (")
	end := strings.Index(ddl[start:], ");") + start + 2
	if _, err = pool.Exec(ctx, strings.ReplaceAll(ddl[start:end], "public.", "")); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE UNIQUE INDEX email_fixture_verified_unique ON notification_email_prefs(lower(custom_email)) WHERE custom_email<>''`); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("../../migrations/sql/20260906191317_notification_email_verification_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl = strings.Split(strings.Split(string(b), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0]
	if _, err = pool.Exec(ctx, strings.ReplaceAll(ddl, "public.", "")); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile("../../migrations/sql/20260907045012_notification_email_verification_dispatch.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl = strings.Split(strings.Split(string(b), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0]
	if _, err = pool.Exec(ctx, strings.ReplaceAll(ddl, "public.", "")); err != nil {
		t.Fatal(err)
	}
	return NewEmailPrefsRepository(pool), pool, schema
}
func tokenHashFromMessage(t *testing.T, message mail.Message) string {
	t.Helper()
	match := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(message.TextBody)
	if len(match) != 2 {
		t.Fatal("message carries no link")
	}
	return hashEmailToken(match[1])
}
func emailOutboxIntent() EmailVerificationIntent {
	return EmailVerificationIntent{ID: uuid.NewString(), UserID: 7, ProfileID: "profile", Address: "destination@example.test", ProfileName: "Synthetic profile", LinkBase: "https://example.test"}
}
func TestEmailOutboxRetainsMessageAndReplay(t *testing.T) {
	repo, pool, _ := emailOutboxFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	cipher := testPushCipher(t)
	first, err := repo.QueueVerification(ctx, in, cipher)
	if err != nil || !first.Current {
		t.Fatal(first, err)
	}
	var encrypted, hash string
	var count int
	if err = pool.QueryRow(ctx, `SELECT payload_ciphertext,pending_token_hash FROM notification_email_verifications WHERE id=$1`, in.ID).Scan(&encrypted, &hash); err != nil {
		t.Fatal(err)
	}
	plaintext, err := cipher.Decrypt(encrypted, emailVerificationAAD(in.ID))
	if err != nil {
		t.Fatal(err)
	}
	var message mail.Message
	if err = json.Unmarshal([]byte(plaintext), &message); err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`token=([A-Za-z0-9_-]+)`).FindStringSubmatch(message.TextBody)
	if len(message.To) != 1 || message.To[0] != in.Address || len(match) != 2 || hashEmailToken(match[1]) != hash || !strings.Contains(message.HTMLBody, match[1]) {
		t.Fatal("stored message does not match pending link")
	}
	if strings.Contains(encrypted, in.Address) || strings.Contains(encrypted, match[1]) {
		t.Fatal("plaintext persisted")
	}
	if _, err = cipher.Decrypt(encrypted, emailVerificationAAD(uuid.NewString())); err == nil {
		t.Fatal("AAD identity not bound")
	}
	in.ProfileName = "changed server observation"
	in.LinkBase = "" // Removing server configuration must not invalidate an accepted receipt.
	replay, err := repo.QueueVerification(ctx, in, cipher)
	if err != nil || replay.ID != first.ID || replay.Current != first.Current || !replay.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatal("replay changed receipt", err)
	}
	var again string
	if err = pool.QueryRow(ctx, `SELECT payload_ciphertext FROM notification_email_verifications WHERE id=$1`, in.ID).Scan(&again); err != nil || again != encrypted {
		t.Fatal("replay changed message", err)
	}
	if err = pool.QueryRow(ctx, `SELECT verify_sends_today FROM notification_email_prefs WHERE profile_id=$1`, in.ProfileID).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay consumed rate", err)
	}
	changed := in
	changed.Address = "different@example.test"
	if _, err = repo.QueueVerification(ctx, changed, cipher); !errors.Is(err, ErrEmailVerificationConflict) {
		t.Fatal("changed replay", err)
	}
	changed = in
	changed.UserID = 8
	if _, err = repo.QueueVerification(ctx, changed, cipher); !errors.Is(err, ErrEmailVerificationConflict) {
		t.Fatal("foreign owner", err)
	}
	changed.ProfileID = "other-profile"
	if _, err = repo.QueueVerification(ctx, changed, cipher); !errors.Is(err, ErrEmailVerificationConflict) {
		t.Fatal("foreign intent", err)
	}
	outcome, err := repo.ConsumeVerifyToken(ctx, hash)
	if err != nil || outcome != EmailVerifyOK {
		t.Fatal("verify", outcome, err)
	}
	replay, err = repo.QueueVerification(ctx, in, cipher)
	if err != nil || replay.Current {
		t.Fatal("verified replay recreated pending", err)
	}
	outcome, err = repo.ConsumeVerifyToken(ctx, hash)
	if err != nil || outcome != EmailVerifyInvalid {
		t.Fatal("link reused", err)
	}
}
func TestEmailOutboxClearReplacementRateAndRollback(t *testing.T) {
	repo, pool, _ := emailOutboxFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	cipher := testPushCipher(t)
	if _, err := repo.QueueVerification(ctx, in, cipher); err != nil {
		t.Fatal(err)
	}
	next := in
	next.ID = uuid.NewString()
	next.Address = "next@example.test"
	if _, err := repo.QueueVerification(ctx, next, cipher); !errors.Is(err, ErrEmailVerifyRateLimited) {
		t.Fatal("minimum gap", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notification_email_prefs SET pending_last_sent_at=now()-interval '2 minutes',verify_sends_today=10`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueVerification(ctx, next, cipher); !errors.Is(err, ErrEmailVerifyRateLimited) {
		t.Fatal("daily cap", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE notification_email_prefs SET pending_last_sent_at=now()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueVerification(ctx, next, cipher); err != nil {
		t.Fatal("new UTC day", err)
	}
	if old, err := repo.QueueVerification(ctx, in, cipher); err != nil || old.Current {
		t.Fatal("superseded replay", err)
	}
	if err := repo.ClearCustomAddress(ctx, in.ProfileID); err != nil {
		t.Fatal(err)
	}
	if old, err := repo.QueueVerification(ctx, next, cipher); err != nil || old.Current {
		t.Fatal("clear replay", err)
	}
	if err := repo.RequestPendingAddress(ctx, 7, in.ProfileID, in.Address, "legacy-hash", time.Now().Add(time.Hour)); !errors.Is(err, ErrEmailLegacyWriter) {
		t.Fatal("adopted bridge", err)
	}
	// A fresh profile's pending update failure must roll back both outbox and prefs.
	fresh := emailOutboxIntent()
	fresh.ProfileID = "rollback"
	if _, err := pool.Exec(ctx, `ALTER TABLE notification_email_prefs ADD CONSTRAINT reject_pending_fixture CHECK(profile_id<>'rollback' OR pending_email='')`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueVerification(ctx, fresh, cipher); err == nil {
		t.Fatal("synthetic failure accepted")
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_email_verifications WHERE id=$1`, fresh.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("orphan outbox", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_email_prefs WHERE profile_id='rollback'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("partial pending state", err)
	}
	foreign := emailOutboxIntent()
	foreign.ProfileID = "unused"
	foreign.Address = "other@example.test"
	if _, err := repo.QueueVerification(ctx, foreign, cipher); !errors.Is(err, ErrEmailAddressInUse) {
		t.Fatal("foreign login address", err)
	}
	if err := repo.RequestPendingAddress(ctx, 7, "legacy", in.Address, "legacy-hash", time.Now().Add(time.Hour)); err != nil {
		t.Fatal("unadopted bridge changed", err)
	}
}
func TestEmailOutboxConcurrentAdmissionAndBridgeBarrier(t *testing.T) {
	repo, pool, schema := emailOutboxFixture(t)
	ctx := t.Context()
	in := emailOutboxIntent()
	cipher := testPushCipher(t)
	results := make(chan error, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() { _, err := repo.QueueVerification(ctx, in, cipher); results <- err })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_email_verifications`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate admission", count, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = lockEmailVerificationProfile(ctx, tx, 7, in.ProfileID); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- repo.RequestPendingAddress(runCtx, 7, in.ProfileID, in.Address, "legacy-hash", time.Now().Add(time.Hour))
	}()
	for {
		var blocked bool
		if err = pool.QueryRow(runCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND cardinality(pg_blocking_pids(pid))>0)`, schema).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("bridge bypassed profile lock: %v", e)
		case <-runCtx.Done():
			t.Fatal("bridge not blocked")
		default:
			runtime.Gosched()
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-done:
		if !errors.Is(e, ErrEmailLegacyWriter) {
			t.Fatal("bridge after commit", e)
		}
	case <-runCtx.Done():
		t.Fatal("bridge did not finish")
	}
}
