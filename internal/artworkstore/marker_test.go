package artworkstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReadMarkerBeforeInitialization(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.ReadMarker(context.Background()); !errors.Is(err, ErrNoMarker) {
		t.Fatalf("ReadMarker = %v, want ErrNoMarker", err)
	}
}

// TestEnsureMarkerCreatesOnceAndIsStable is the basis of store-generation
// pinning: the first call mints the id the database binds to, and every later
// call on the same physical copy must return that same id without reporting a
// creation.
func TestEnsureMarkerCreatesOnceAndIsStable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	marker, created, err := store.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}
	if !created {
		t.Fatal("first EnsureMarker reported created=false")
	}
	if marker.Version != markerFormatVersion {
		t.Errorf("Version = %d, want %d", marker.Version, markerFormatVersion)
	}
	if !validMarkerID(marker.ID) {
		t.Errorf("ID = %q, want %d lowercase hex characters", marker.ID, markerIDBytes*2)
	}
	if marker.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	again, created, err := store.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("second EnsureMarker: %v", err)
	}
	if created {
		t.Error("second EnsureMarker reported created=true")
	}
	if again.ID != marker.ID {
		t.Errorf("ID changed across calls: %q then %q", marker.ID, again.ID)
	}

	read, err := store.ReadMarker(ctx)
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if read.ID != marker.ID {
		t.Errorf("ReadMarker ID = %q, want %q", read.ID, marker.ID)
	}

	// A reopened store (a restart) must see the same marker.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := NewFilesystemStore(store.Root())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, created, err := reopened.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("EnsureMarker after restart: %v", err)
	}
	if created || restarted.ID != marker.ID {
		t.Fatalf("after restart: created=%v id=%q, want created=false id=%q", created, restarted.ID, marker.ID)
	}
}

// TestEnsureMarkerIsSingleWinnerUnderRace models several API nodes starting
// against one fresh shared mount: exactly one marker may be created, and every
// node must agree on it.
func TestEnsureMarkerIsSingleWinnerUnderRace(t *testing.T) {
	store := newTestStore(t)

	const callers = 8
	start := make(chan struct{})
	type result struct {
		marker  Marker
		created bool
		err     error
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			marker, created, err := store.EnsureMarker(context.Background())
			results <- result{marker: marker, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	creations := 0
	id := ""
	for res := range results {
		if res.err != nil {
			t.Fatalf("EnsureMarker: %v", res.err)
		}
		if res.created {
			creations++
		}
		if id == "" {
			id = res.marker.ID
		} else if res.marker.ID != id {
			t.Fatalf("markers disagree: %q and %q", id, res.marker.ID)
		}
	}
	if creations != 1 {
		t.Fatalf("created reported %d times, want exactly 1", creations)
	}
	assertNoTempFiles(t, store)
}

// TestWriteMarkerExclusiveIsCreateOnly pins the link-unsupported fallback: on
// filesystems without hard links the marker must still be published with a
// create-only primitive, so a racing node can never replace a marker another
// node already published (a rename would, leaving the loser pinned to a
// generation no store carries).
func TestWriteMarkerExclusiveIsCreateOnly(t *testing.T) {
	store := newTestStore(t)
	root, release, err := store.openRoot()
	if err != nil {
		t.Fatalf("openRoot: %v", err)
	}
	defer release()

	first := []byte(`{"version":1,"id":"11111111111111111111111111111111","created_at":"2026-01-01T00:00:00Z"}` + "\n")
	if err := writeMarkerExclusive(root, first); err != nil {
		t.Fatalf("first writeMarkerExclusive: %v", err)
	}
	second := []byte(`{"version":1,"id":"22222222222222222222222222222222","created_at":"2026-01-01T00:00:00Z"}` + "\n")
	if err := writeMarkerExclusive(root, second); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second writeMarkerExclusive = %v, want ErrExist", err)
	}

	marker, err := store.ReadMarker(context.Background())
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if marker.ID != "11111111111111111111111111111111" {
		t.Fatalf("marker ID = %q, want the first writer's marker to survive", marker.ID)
	}
}

// TestEnsureMarkerDetectsADifferentDisk is the multi-node safety check: a node
// pointed at a different root sees a different marker, which is what turns an
// unsupported topology into a loud startup failure instead of silent
// divergence.
func TestEnsureMarkerDetectsADifferentDisk(t *testing.T) {
	first := newTestStore(t)
	second := newTestStore(t)

	firstMarker, _, err := first.EnsureMarker(context.Background())
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}
	secondMarker, created, err := second.EnsureMarker(context.Background())
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}
	if !created {
		t.Fatal("a fresh root reported an existing marker")
	}
	if secondMarker.ID == firstMarker.ID {
		t.Fatal("two independent roots minted the same marker id")
	}
}

// TestMarkerSurvivesACopiedTree pins portability: copying the tree copies the
// marker, so a storage-only import re-binds an existing physical copy rather
// than looking like a brand-new store.
func TestMarkerSurvivesACopiedTree(t *testing.T) {
	source := newTestStore(t)
	ctx := context.Background()
	mustWrite(t, source, testKey, []byte("poster-bytes"))
	marker, _, err := source.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}

	destinationRoot := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(destinationRoot, os.DirFS(source.Root())); err != nil {
		t.Fatalf("copying store: %v", err)
	}
	destination, err := NewFilesystemStore(destinationRoot)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = destination.Close() })

	copied, created, err := destination.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("EnsureMarker on the copy: %v", err)
	}
	if created {
		t.Error("the copied tree minted a new marker")
	}
	if copied.ID != marker.ID {
		t.Errorf("copied marker id = %q, want %q", copied.ID, marker.ID)
	}
	// The logical key resolves in the copy without any rewrite.
	if _, err := destination.Stat(ctx, testKey); err != nil {
		t.Errorf("Stat in the copied store: %v", err)
	}
}

func TestReadMarkerRejectsCorruptDocuments(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"not json", "definitely not json"},
		{"unsupported version", `{"version":99,"id":"0123456789abcdef0123456789abcdef","created_at":"2026-01-01T00:00:00Z"}`},
		{"missing id", `{"version":1,"created_at":"2026-01-01T00:00:00Z"}`},
		{"malformed id", `{"version":1,"id":"NOT-HEX","created_at":"2026-01-01T00:00:00Z"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			path := filepath.Join(store.Root(), markerFileName)
			if err := os.WriteFile(path, []byte(tc.content), storeFilePerm); err != nil {
				t.Fatalf("seeding marker: %v", err)
			}
			if _, err := store.ReadMarker(context.Background()); err == nil {
				t.Fatal("ReadMarker on a corrupt marker = nil, want an error")
			} else if errors.Is(err, ErrNoMarker) {
				t.Fatalf("ReadMarker = %v, want a corruption error rather than ErrNoMarker", err)
			}
			// A corrupt marker must not be silently replaced: that would
			// re-bind the store generation and hide the real problem.
			if _, _, err := store.EnsureMarker(context.Background()); err == nil {
				t.Fatal("EnsureMarker overwrote a corrupt marker")
			}
		})
	}
}

// TestMarkerIsUnreachableThroughObjectKeys keeps the marker outside the object
// namespace: no signed delivery URL or GC batch can read, overwrite, or delete
// it.
func TestMarkerIsUnreachableThroughObjectKeys(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	marker, _, err := store.EnsureMarker(ctx)
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}

	if err := ValidateKey(markerFileName); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("ValidateKey(%q) = %v, want ErrInvalidKey", markerFileName, err)
	}
	if _, err := store.Open(ctx, markerFileName); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Open(marker) = %v, want ErrInvalidKey", err)
	}
	if _, err := store.DeleteObjects(ctx, []string{markerFileName}); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("DeleteObjects(marker) = %v, want ErrInvalidKey", err)
	}
	if err := store.WriteImmutable(ctx, markerFileName, []byte("x"), ObjectMetadata{}); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("WriteImmutable(marker) = %v, want ErrInvalidKey", err)
	}
	after, err := store.ReadMarker(ctx)
	if err != nil {
		t.Fatalf("ReadMarker: %v", err)
	}
	if after.ID != marker.ID {
		t.Fatalf("marker id changed to %q, want %q", after.ID, marker.ID)
	}
}

func TestEnsureMarkerRespectsContextCancellation(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.EnsureMarker(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureMarker = %v, want context.Canceled", err)
	}
	if _, err := store.ReadMarker(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadMarker = %v, want context.Canceled", err)
	}
}

func TestMarkerCreatedAtIsUTC(t *testing.T) {
	store := newTestStore(t)
	marker, _, err := store.EnsureMarker(context.Background())
	if err != nil {
		t.Fatalf("EnsureMarker: %v", err)
	}
	if _, offset := marker.CreatedAt.Zone(); offset != 0 {
		t.Errorf("CreatedAt zone offset = %d, want UTC", offset)
	}
	if marker.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("CreatedAt = %s, want a past timestamp", marker.CreatedAt)
	}
}
