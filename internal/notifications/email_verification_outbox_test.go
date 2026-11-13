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
	preAccountKeyEmailTables(t, pool)
	up, _ := emailAccountKeyMigration(t)
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	return NewEmailPrefsRepository(pool), pool, schema
}

// preAccountKeyEmailTables builds the email notification tables as they were
// before 20261108230041 keyed them by account, plus stand-ins holding only the
// columns that migration reads from the tables that say which accounts use a
// profile id. The pool's search path must start with a fresh schema.
func preAccountKeyEmailTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, prefs, ok := strings.Cut(emailMigration(t, "20260612001031_profile_email_notifications.sql"), "-- +goose Up")
	if ok {
		_, prefs, ok = strings.Cut(prefs, "DROP TABLE notification_email_prefs;")
	}
	if ok {
		prefs, _, ok = strings.Cut(prefs, "-- The per-profile sweep")
	}
	if !ok {
		t.Fatal("unexpected email prefs migration layout")
	}
	ddl := []string{
		prefs,
		strings.Split(strings.Split(emailMigration(t, "20260906191317_notification_email_verification_outbox.sql"), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0],
		strings.Split(strings.Split(emailMigration(t, "20260907045012_notification_email_verification_dispatch.sql"), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0],
		`CREATE TABLE user_profiles (user_id integer, id text);
		CREATE TABLE profile_series_interest (user_id integer, profile_id text);
		CREATE TABLE notification_deliveries (user_id integer, profile_id text);
		CREATE TABLE notification_preferences (user_id integer, profile_id text);`,
	}
	for _, sql := range ddl {
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
}

// emailAccountKeyMigration returns 20261108230041's Up and Down SQL,
// unqualified so they apply to the test schema.
func emailAccountKeyMigration(t *testing.T) (string, string) {
	t.Helper()
	up, down, ok := strings.Cut(emailMigration(t, "20261108230041_email_prefs_account_key.sql"), "-- +goose Down")
	if !ok {
		t.Fatal("missing Down section")
	}
	return up, down
}

// emailMigration reads a migration with its public. qualifiers removed.
func emailMigration(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../migrations/sql/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "public.", "")
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
