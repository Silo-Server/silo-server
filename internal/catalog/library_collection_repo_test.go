package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryCollectionColumnsKeepPosterSuppressionInScanOrder(t *testing.T) {
	want := "lc.poster_thumbhash, lc.backdrop_thumbhash, lc.poster_auto_generated, lc.poster_suppressed, lc.poster_from_template"
	if !strings.Contains(libraryCollectionColumns, want) {
		t.Fatalf("library collection scan columns do not contain %q in order", want)
	}
}

func TestAcquirePosterMutationLockTimesOutAndReleasesResources(t *testing.T) {
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

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire lock-holder connection: %v", err)
	}
	t.Cleanup(holder.Release)
	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock-holder transaction: %v", err)
	}
	t.Cleanup(func() { _ = holderTx.Rollback(ctx) })

	collectionID := fmt.Sprintf("poster-lock-timeout-%d", time.Now().UnixNano())
	if _, err := holderTx.Exec(ctx, libraryCollectionPosterAdvisoryLockSQL, collectionID); err != nil {
		t.Fatalf("hold poster advisory lock: %v", err)
	}

	oldTimeout := libraryCollectionPosterLockTimeout
	libraryCollectionPosterLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { libraryCollectionPosterLockTimeout = oldTimeout })

	repo := NewLibraryCollectionRepository(pool)
	started := time.Now()
	release, err := repo.AcquirePosterMutationLock(ctx, collectionID)
	if release != nil {
		release()
		t.Fatal("contended poster lock unexpectedly returned a release function")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("lock error = %v, want PostgreSQL lock timeout (55P03)", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("poster lock timeout took %s, want a bounded failure", elapsed)
	}

	if err := holderTx.Rollback(ctx); err != nil {
		t.Fatalf("release held poster lock: %v", err)
	}
	release, err = repo.AcquirePosterMutationLock(ctx, collectionID)
	if err != nil {
		t.Fatalf("acquire poster lock after timeout cleanup: %v", err)
	}
	release()
}

func TestLibraryCollectionPosterSuppressionRoundTrip(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	var libraryID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("poster-suppression-%d", suffix),
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, libraryID)
	})

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryID:        libraryID,
		LibraryIDs:       []int{libraryID},
		Slug:             fmt.Sprintf("poster-suppression-%d", suffix),
		Title:            "Poster suppression mapping",
		CollectionType:   "manual",
		Visibility:       "visible",
		PosterSuppressed: true,
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if !collection.PosterSuppressed {
		t.Fatal("created collection lost poster suppression")
	}

	loaded, err := repo.GetByID(ctx, collection.ID)
	if err != nil {
		t.Fatalf("reload collection: %v", err)
	}
	if !loaded.PosterSuppressed {
		t.Fatal("scanned collection lost poster suppression")
	}

	updated, err := repo.UpdateGeneratedPosterIfAllowed(ctx, collection.ID, "collection-images/generated.webp", "thumbhash")
	if err != nil {
		t.Fatalf("conditionally update suppressed poster: %v", err)
	}
	if updated {
		t.Fatal("suppressed collection accepted an automatic poster")
	}

	notSuppressed := false
	if err := repo.Update(ctx, UpdateLibraryCollectionInput{
		ID:               collection.ID,
		PosterSuppressed: &notSuppressed,
	}); err != nil {
		t.Fatalf("clear poster suppression: %v", err)
	}
	updated, err = repo.UpdateGeneratedPosterIfAllowed(ctx, collection.ID, "collection-images/generated.webp", "thumbhash")
	if err != nil {
		t.Fatalf("conditionally update unsuppressed poster: %v", err)
	}
	if !updated {
		t.Fatal("unsuppressed empty collection rejected an automatic poster")
	}
}

func TestLibraryCollectionPosterMutationLocksSerializePerCollection(t *testing.T) {
	var locks libraryCollectionPosterMutationLocks
	unlockFirst := locks.lock("collection-1")
	defer func() {
		if unlockFirst != nil {
			unlockFirst()
		}
	}()

	sameStarted := make(chan struct{})
	sameAcquired := make(chan struct{})
	go func() {
		close(sameStarted)
		unlock := locks.lock("collection-1")
		close(sameAcquired)
		unlock()
	}()
	<-sameStarted
	select {
	case <-sameAcquired:
		t.Fatal("same collection acquired lock concurrently")
	case <-time.After(50 * time.Millisecond):
	}

	otherAcquired := make(chan struct{})
	go func() {
		unlock := locks.lock("collection-2")
		close(otherAcquired)
		unlock()
	}()
	select {
	case <-otherAcquired:
	case <-time.After(time.Second):
		t.Fatal("different collection was unnecessarily blocked")
	}

	unlockFirst()
	unlockFirst = nil
	select {
	case <-sameAcquired:
	case <-time.After(time.Second):
		t.Fatal("same collection did not acquire lock after release")
	}
}

func TestLibraryCollectionLifecycleLockIDs(t *testing.T) {
	tests := []struct {
		name  string
		oldID int
		newID int
		want  []int
	}{
		{name: "same", oldID: 4, newID: 4, want: []int{4}},
		{name: "ascending", oldID: 4, newID: 9, want: []int{4, 9}},
		{name: "stable sorted", oldID: 9, newID: 4, want: []int{4, 9}},
		{name: "removed", oldID: 4, newID: 0, want: []int{4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := libraryCollectionLifecycleLockIDs(tt.oldID, tt.newID)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("lock IDs = %v, want %v", got, tt.want)
			}
		})
	}
}

// PostgreSQL describes $1 by its first cast. In pgx's default statement-cache
// mode an int argument must then encode as that type: bigint works, text does
// not ("cannot find encode plan"), which broke every create and scope update.
func TestLibraryCollectionLifecycleLockBindsIntegerParameter(t *testing.T) {
	if !strings.Contains(libraryCollectionLifecycleLockSQL, "$1::bigint::text") {
		t.Fatalf("lifecycle lock SQL must bind $1 as bigint before text: %s", libraryCollectionLifecycleLockSQL)
	}
	typeMap := pgtype.NewMap()
	if _, err := typeMap.Encode(pgtype.Int8OID, pgtype.BinaryFormatCode, 42, nil); err != nil {
		t.Fatalf("encode int lifecycle key as bigint: %v", err)
	}
	if _, err := typeMap.Encode(pgtype.TextOID, pgtype.TextFormatCode, 42, nil); err == nil {
		t.Fatal("pgx unexpectedly encodes an int as text; the bigint bind is no longer the only safe form")
	}
}

func TestLibraryCollectionLifecycleLockAcceptsIntegerKeys(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	// pgxpool.New keeps pgx's default statement-cache exec mode, which encodes
	// arguments against the parameter types PostgreSQL describes.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	// Create, Update and folder deletion all pass Go ints here.
	if _, err := tx.Exec(ctx, libraryCollectionLifecycleLockSQL, 42); err != nil {
		t.Fatalf("lifecycle lock with int key: %v", err)
	}

	var heldKey, wantKey int64
	if err := tx.QueryRow(ctx, `SELECT hashtextextended('library_collection_lifecycle:42', 0)`).Scan(&wantKey); err != nil {
		t.Fatalf("compute expected lock key: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT (classid::bigint << 32) | objid::bigint
		FROM pg_locks
		WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND objsubid = 1`).Scan(&heldKey); err != nil {
		t.Fatalf("read held advisory lock: %v", err)
	}
	if heldKey != wantKey {
		t.Fatalf("held lifecycle lock key = %d, want %d for library 42", heldKey, wantKey)
	}
}
