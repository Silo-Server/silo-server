package notifications

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

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
