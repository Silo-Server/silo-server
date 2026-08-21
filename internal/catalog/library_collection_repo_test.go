package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLibraryCollectionColumnsKeepPosterSuppressionInScanOrder(t *testing.T) {
	want := "lc.poster_thumbhash, lc.backdrop_thumbhash, lc.poster_auto_generated, lc.poster_suppressed, lc.poster_from_template"
	if !strings.Contains(libraryCollectionColumns, want) {
		t.Fatalf("library collection scan columns do not contain %q in order", want)
	}
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
