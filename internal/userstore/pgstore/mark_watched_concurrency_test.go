package pgstore

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestMarkWatchedBatchConcurrentRetriesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("mark-retry-%d", time.Now().UnixNano())
	config.ConnConfig.RuntimeParams["application_name"] = name
	config.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, name).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) }()
	store := newStore(pool, userID)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,completed) VALUES($1,'p1','movie-retry',false)`, userID); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM user_watch_progress WHERE user_id=$1 AND profile_id='p1' AND media_item_id='movie-retry' FOR UPDATE`, userID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		written []userstore.WatchHistoryEntry
		err     error
	}
	done := make(chan outcome, 2)
	for range 2 {
		go func() {
			entries, err := store.MarkWatchedBatch(ctx, "p1", []userstore.MarkWatchedTarget{{MediaItemID: "movie-retry", DurationSeconds: 120}}, []userstore.WatchHistoryEntry{{ProfileID: "p1", MediaItemID: "movie-retry", Completed: true}})
			done <- outcome{entries, err}
		}()
	}
	// Both requests must be waiting on the existing progress row before the
	// lock is released. This makes the conflict recheck deterministic.
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, name).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("both retries did not reach the locked row")
		}
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	emitted := 0
	for range 2 {
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			emitted += len(result.written)
		case <-timeout.C:
			t.Fatal("retries did not complete")
		}
	}
	if emitted != 1 {
		t.Fatalf("provider entries emitted = %d, want 1", emitted)
	}
	history, err := store.ListHistory(ctx, "p1", 10, 0)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %+v %v", history, err)
	}
}
