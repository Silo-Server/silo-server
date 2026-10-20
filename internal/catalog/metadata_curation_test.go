package catalog

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestCurationScopeMembershipCommittedWhileWaitingPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const item = "curation-lock-fixture"
	var allowed, added int
	for _, folder := range []*int{&allowed, &added} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('movies','curation lock fixture') RETURNING id`).Scan(folder); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, item)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{allowed, added})
	}()
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Fixture')`, item); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item, allowed); err != nil {
		t.Fatal(err)
	}
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	// The FK holds KEY SHARE on the parent until this transaction commits.
	if _, err := writer.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item, added); err != nil {
		t.Fatal(err)
	}
	guard, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Rollback(context.Background()) }()
	var pid int
	if err := guard.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	authCtx := access.WithMetadataCurationAuthorizer(ctx, func(_ context.Context, libraries []int) error {
		if len(libraries) != 1 || libraries[0] != allowed {
			return access.ErrMetadataCurationScope
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := LockMetadataCurationItems(authCtx, guard, item)
		done <- err
	}()
	waitCurationLock(t, ctx, pool, pid)
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, access.ErrMetadataCurationScope) {
		t.Fatalf("fresh membership was not refused: %v", err)
	}
	if err := guard.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM media_item_libraries WHERE content_id=$1 AND media_folder_id=$2`, item, added); err != nil {
		t.Fatal(err)
	}
	// Conversely, a later membership insert waits for the guard's parent lock.
	guard, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockMetadataCurationItems(ctx, guard, item); err != nil {
		t.Fatal(err)
	}
	writer, err = pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := writer.Exec(ctx, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, item, added)
		done <- err
	}()
	waitCurationLock(t, ctx, pool, pid)
	if err := guard.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := writer.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitCurationLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pid int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
