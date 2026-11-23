package database

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestPostgresContentRatingAgeRederive seeds rows the way the frozen backfill
// left them and checks that only ratings Normalize now resolves change.
func TestPostgresContentRatingAgeRederive(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	prefix := fmt.Sprintf("content-rating-age-rederive-%d-", time.Now().UnixNano())
	rows := []struct {
		rating string
		stored int
		want   *int
	}{
		{"SE-7", 99, intPtr(7)},
		{"DE-16", 99, intPtr(16)},
		{"US-NR", 99, nil},
		{"NZ-M", 99, intPtr(99)},
		{"SE-Btl", 99, intPtr(99)},
		// Already resolved: the repair touches only the unrecognized age.
		{"PG-13", 13, intPtr(13)},
	}
	for i, row := range rows {
		if _, err := pool.Exec(ctx, `
INSERT INTO media_items (content_id, type, title, content_rating, content_rating_age)
VALUES ($1, 'movie', $1, $2, $3)`, fmt.Sprintf("%s%d", prefix, i), row.rating, row.stored); err != nil {
			t.Fatalf("seed %q: %v", row.rating, err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%") })

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := rederiveContentRatingAges(ctx, sqlDB); err != nil {
		t.Fatalf("rederiveContentRatingAges: %v", err)
	}

	for i, row := range rows {
		var got *int
		if err := pool.QueryRow(ctx, `SELECT content_rating_age FROM media_items WHERE content_id = $1`,
			fmt.Sprintf("%s%d", prefix, i)).Scan(&got); err != nil {
			t.Fatalf("reading %q: %v", row.rating, err)
		}
		if (got == nil) != (row.want == nil) || (got != nil && *got != *row.want) {
			t.Errorf("%q: content_rating_age = %v, want %v", row.rating, fmtAge(got), fmtAge(row.want))
		}
	}
}

func intPtr(v int) *int { return &v }

func fmtAge(age *int) string {
	if age == nil {
		return "NULL"
	}
	return strconv.Itoa(*age)
}
