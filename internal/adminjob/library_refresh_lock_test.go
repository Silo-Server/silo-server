package adminjob

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLibraryRefreshLockKeyIsPerLibrary(t *testing.T) {
	t.Parallel()

	first, second := libraryRefreshLockKey(1), libraryRefreshLockKey(2)
	if first == second {
		t.Fatalf("libraries 1 and 2 share lock key %#x", first)
	}
	for _, key := range []int64{first, second} {
		if key>>32 != libraryRefreshAdvisoryLockNamespace {
			t.Fatalf("lock key %#x is outside the library refresh namespace", key)
		}
	}
}

func TestLibraryRefreshExcludesAConcurrentRefreshOfTheSameLibrary(t *testing.T) {
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

	// A library ID no fixture uses, so a shared test database cannot collide.
	const libraryID = 0x7FFF_F001
	lister := &libraryRefreshTestItemLister{}
	executor := &LibraryRefreshExecutor{
		itemLister: lister,
		folderRepo: &itemRefreshTestFolderRepo{folder: &models.MediaFolder{ID: libraryID, Enabled: true}},
		refresher:  &itemRefreshTestRefresher{},
	}
	executor.SetLibraryLockPool(pool)
	req := LibraryRefreshRequest{LibraryID: libraryID, Mode: LibraryRefreshModeFull}

	held, acquired, err := pglock.TryAcquire(ctx, pool, libraryRefreshLockKey(libraryID))
	if err != nil || !acquired {
		t.Fatalf("hold library lock: acquired=%v err=%v", acquired, err)
	}
	if _, err := executor.Execute(ctx, req, nil); !errors.Is(err, ErrLibraryRefreshInProgress) {
		t.Fatalf("Execute() while another refresh holds the library = %v, want ErrLibraryRefreshInProgress", err)
	}
	if err := held.Release(ctx); err != nil {
		t.Fatalf("release library lock: %v", err)
	}

	if _, err := executor.Execute(ctx, req, nil); err != nil {
		t.Fatalf("Execute() after the other refresh finished = %v", err)
	}
	again, acquired, err := pglock.TryAcquire(ctx, pool, libraryRefreshLockKey(libraryID))
	if err != nil || !acquired {
		t.Fatalf("library lock still held after Execute returned: acquired=%v err=%v", acquired, err)
	}
	if err := again.Release(ctx); err != nil {
		t.Fatalf("release library lock: %v", err)
	}
}
