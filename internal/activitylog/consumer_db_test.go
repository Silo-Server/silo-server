package activitylog

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/logstream"
)

func TestRecordLastActivityKeepsNewestDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	var user int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,role) VALUES($1,$2,'x','user') RETURNING id`,
		"last-active-"+suffix, "last-active-"+suffix+"@example.test").Scan(&user); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, user) }()
	deleted := -1
	lastActive := func() time.Time {
		t.Helper()
		var ts time.Time
		if err := pool.QueryRow(ctx, `SELECT last_active_at FROM user_last_activity WHERE user_id=$1`, user).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// The batch's newest entry wins; anonymous entries and accounts deleted
	// since the request was logged are skipped without failing the batch.
	if err := recordLastActivity(ctx, pool, []LogEntry{
		{Timestamp: base.Add(time.Minute), UserID: &user},
		{Timestamp: base.Add(time.Hour)},
		{Timestamp: base, UserID: &user},
		{Timestamp: base, UserID: &deleted},
	}); err != nil {
		t.Fatal(err)
	}
	if got := lastActive(); !got.Equal(base.Add(time.Minute)) {
		t.Fatalf("last_active_at = %v, want %v", got, base.Add(time.Minute))
	}
	if err := pool.QueryRow(ctx, `SELECT 1 FROM user_last_activity WHERE user_id=$1`, deleted).Scan(new(int)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted account row: %v", err)
	}

	// A batch flushed late by another node never moves it back.
	if err := recordLastActivity(ctx, pool, []LogEntry{{Timestamp: base, UserID: &user}}); err != nil {
		t.Fatal(err)
	}
	if got := lastActive(); !got.Equal(base.Add(time.Minute)) {
		t.Fatalf("stale batch moved last_active_at to %v", got)
	}

	// An audited mutation stored its own row; the consumer records only the
	// account's activity and does not store a second row.
	consumer := NewConsumer(pool, logstream.NewHub("test", nil))
	if err := consumer.insertBatch(ctx, []LogEntry{{Timestamp: base.Add(2 * time.Minute), UserID: &user, RequestID: "last-active-" + suffix, Method: "PUT", Persisted: true}}); err != nil {
		t.Fatal(err)
	}
	if got := lastActive(); !got.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("last_active_at = %v, want %v", got, base.Add(2*time.Minute))
	}
	var stored int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_log WHERE request_id=$1`, "last-active-"+suffix).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("persisted entry stored %d rows: %v", stored, err)
	}
}
