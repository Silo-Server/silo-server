package pgstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresHistoryWitness(t *testing.T) {
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

	storetest.RunHistoryWitness(t, func(t *testing.T) userstore.UserStore {
		var userID int
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
			fmt.Sprintf("conf-historywitness-%d", time.Now().UnixNano()),
		).Scan(&userID); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id = $1`, userID)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		})
		return newStore(pool, userID)
	})
}
