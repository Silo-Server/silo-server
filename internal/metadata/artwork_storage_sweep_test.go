package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func TestParseArtworkObjectKeyRebuildsOriginalVariant(t *testing.T) {
	t.Parallel()
	got, ok := parseArtworkObjectKey(blobstore.ObjectInfo{
		Key: "local/movies/31190/19f56348/poster/w780.abc123.webp",
	})
	if !ok {
		t.Fatal("expected a well-formed ladder key to parse")
	}
	want := "local/movies/31190/19f56348/poster/original.abc123.webp"
	if got.original != want {
		t.Fatalf("original path = %q, want %q", got.original, want)
	}
	if got.key != "local/movies/31190/19f56348/poster/w780.abc123.webp" {
		t.Fatalf("key was rewritten: %q", got.key)
	}
}

func TestParseArtworkObjectKeyLeavesOriginalUnchanged(t *testing.T) {
	t.Parallel()
	// The original variant must map to itself, or every currently-referenced
	// object would look unreferenced and be deleted.
	key := "tmdb/people/1352462/profile/original.deadbeef.webp"
	got, ok := parseArtworkObjectKey(blobstore.ObjectInfo{Key: key})
	if !ok {
		t.Fatal("expected the original variant to parse")
	}
	if got.original != key {
		t.Fatalf("original path = %q, want %q", got.original, key)
	}
}

func TestParseArtworkObjectKeyRejectsUnrecognizedShapes(t *testing.T) {
	t.Parallel()
	// Anything that does not decompose is skipped rather than guessed at:
	// an unmodelled key is the one case where deleting could destroy
	// something this code does not understand.
	for _, key := range []string{
		"local/movies/31190/poster/original.webp", // no hash segment
		"local/movies/31190/poster/a.b.c.d.webp",  // too many segments
		"orphan-at-root.webp",                     // no directory
		"local/movies/31190/poster/.abc123.webp",  // empty variant
		"local/movies/31190/poster/w300..webp",    // empty hash
		"local/movies/31190/poster/w300.abc123.",  // empty extension
		"local/movies/31190/poster/",              // directory marker
	} {
		if _, ok := parseArtworkObjectKey(blobstore.ObjectInfo{Key: key}); ok {
			t.Errorf("key %q parsed but should have been rejected", key)
		}
	}
}

func TestParseArtworkObjectKeyCarriesModifiedTime(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	got, ok := parseArtworkObjectKey(blobstore.ObjectInfo{
		Key:     "local/movies/1/poster/w300.abc.webp",
		ModTime: when,
	})
	if !ok {
		t.Fatal("expected key to parse")
	}
	if got.modified.IsZero() || !got.modified.Equal(when) {
		t.Fatalf("modified = %v, want %v", got.modified, when)
	}
}

func TestNewArtworkStorageSweeperRequiresDependencies(t *testing.T) {
	t.Parallel()
	if s := NewArtworkStorageSweeper(nil, nil); s != nil {
		t.Fatal("expected nil sweeper without a pool or storage client")
	}
}

// The anomaly guard is the difference between this task reclaiming space and
// this task deleting the artwork library, so its thresholds are asserted
// directly rather than left implicit.
func TestArtworkSweepAnomalyGuardThresholds(t *testing.T) {
	t.Parallel()
	if artworkSweepAnomalyRatio <= 0 || artworkSweepAnomalyRatio >= 1 {
		t.Fatalf("anomaly ratio %v must be a fraction between 0 and 1", artworkSweepAnomalyRatio)
	}
	if artworkSweepAnomalyFloor < 1 {
		t.Fatal("anomaly floor must exempt at least the smallest pages")
	}
	if artworkSweepMinAge <= 0 {
		t.Fatal("age floor must be positive or freshly cached artwork can be deleted")
	}

	// A page where nearly everything is unreferenced is far more likely a
	// broken reference check than a genuinely empty catalog.
	parsed, doomed := 1000, 900
	if !(parsed >= artworkSweepAnomalyFloor && float64(doomed) > artworkSweepAnomalyRatio*float64(parsed)) {
		t.Fatal("a 90%-unreferenced full page must trip the anomaly guard")
	}

	// Normal cleanup must not trip it: the measured orphan rate on a real
	// deployment was 27%, and the worst single category was 52%.
	parsed, doomed = 1000, 520
	if parsed >= artworkSweepAnomalyFloor && float64(doomed) > artworkSweepAnomalyRatio*float64(parsed) {
		t.Fatal("a 52%-unreferenced page is normal cleanup and must not trip the guard")
	}

	// A short final page may legitimately be entirely unreferenced.
	parsed, doomed = 10, 10
	if parsed >= artworkSweepAnomalyFloor && float64(doomed) > artworkSweepAnomalyRatio*float64(parsed) {
		t.Fatal("a short trailing page must be exempt from the anomaly guard")
	}
}

// fakeArtworkStorage records what the sweep asked it to delete so the
// destructive path can be asserted rather than inferred.
type fakeArtworkStorage struct {
	pages   [][]blobstore.ObjectInfo
	tokens  []string
	deleted []string
	calls   int
}

func (f *fakeArtworkStorage) Delete(_ context.Context, keys []string) (int, error) {
	f.deleted = append(f.deleted, keys...)
	return len(keys), nil
}

func (f *fakeArtworkStorage) List(_ context.Context, _, _ string, _ int) ([]blobstore.ObjectInfo, string, error) {
	if f.calls >= len(f.pages) {
		return nil, "", nil
	}
	page, token := f.pages[f.calls], f.tokens[f.calls]
	f.calls++
	return page, token, nil
}

func ageingObjects(prefix string, n int, age time.Duration) []blobstore.ObjectInfo {
	when := time.Now().Add(-age)
	out := make([]blobstore.ObjectInfo, 0, n)
	for i := 0; i < n; i++ {
		stamp := when
		out = append(out, blobstore.ObjectInfo{
			Key:     fmt.Sprintf("%s/item%d/poster/w300.hash%d.webp", prefix, i, i),
			ModTime: stamp,
		})
	}
	return out
}

// sweepWithoutDatabase exercises SweepPrefix with the reference lookup stubbed
// out, so the guards can be tested without a live catalog. referenced is the
// set of original-variant paths to treat as still in use.
func sweepWithoutDatabase(t *testing.T, storage *fakeArtworkStorage, referenced map[string]struct{}, maxPages int) (ArtworkStorageSweepStats, error) {
	t.Helper()
	sweeper := &ArtworkStorageSweeper{store: storage, now: time.Now}
	sweeper.lookup = func(_ context.Context, paths []string) (map[string]struct{}, error) {
		found := make(map[string]struct{})
		for _, p := range paths {
			if _, ok := referenced[p]; ok {
				found[p] = struct{}{}
			}
		}
		return found, nil
	}
	return sweeper.SweepPrefix(context.Background(), "local/", "", maxPages)
}

// A local backend keeps subtitles, diagnostic bundles, job artifacts, and
// avatars in the same root as artwork. An empty prefix would walk all of them,
// and parseArtworkObjectKey would read a name like "report.tar.gz" as a
// revisioned variant and delete it.
func TestSweepRefusesAnEmptyPrefix(t *testing.T) {
	t.Parallel()
	storage := &fakeArtworkStorage{
		pages:  [][]blobstore.ObjectInfo{ageingObjects("local", 2, 72*time.Hour)},
		tokens: []string{""},
	}
	sweeper := &ArtworkStorageSweeper{store: storage, now: time.Now}
	for _, prefix := range []string{"", "   "} {
		if _, err := sweeper.SweepPrefix(context.Background(), prefix, "", 1); err == nil {
			t.Fatalf("prefix %q: unbounded sweep accepted", prefix)
		}
	}
	if len(storage.deleted) != 0 {
		t.Fatalf("deleted %v on a refused sweep", storage.deleted)
	}
}

func TestSweepDeletesOnlyUnreferencedObjects(t *testing.T) {
	t.Parallel()
	objects := ageingObjects("local", 4, 72*time.Hour)
	referenced := map[string]struct{}{
		"local/item0/poster/original.hash0.webp": {},
		"local/item1/poster/original.hash1.webp": {},
		"local/item2/poster/original.hash2.webp": {},
	}
	storage := &fakeArtworkStorage{pages: [][]blobstore.ObjectInfo{objects}, tokens: []string{""}}

	stats, err := sweepWithoutDatabase(t, storage, referenced, 1)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if stats.Referenced != 3 {
		t.Fatalf("referenced = %d, want 3", stats.Referenced)
	}
	if len(storage.deleted) != 1 || storage.deleted[0] != "local/item3/poster/w300.hash3.webp" {
		t.Fatalf("deleted %v, want only the unreferenced object", storage.deleted)
	}
	if !stats.PrefixDone {
		t.Fatal("an empty continuation token must mark the prefix finished")
	}
}

func TestSweepSkipsObjectsUnderTheAgeFloor(t *testing.T) {
	t.Parallel()
	// Nothing is referenced, so only the age floor can save these.
	storage := &fakeArtworkStorage{
		pages:  [][]blobstore.ObjectInfo{ageingObjects("local", 5, time.Hour)},
		tokens: []string{""},
	}
	stats, err := sweepWithoutDatabase(t, storage, map[string]struct{}{}, 1)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if stats.TooNew != 5 {
		t.Fatalf("too_new = %d, want 5", stats.TooNew)
	}
	if len(storage.deleted) != 0 {
		t.Fatalf("deleted %v; freshly written objects must never be deleted", storage.deleted)
	}
}

func TestSweepFailsClosedOnMissingTimestamp(t *testing.T) {
	t.Parallel()
	// Storage that reports no modification time gives no way to tell a
	// just-written object from an old one, so the sweep must not delete it.
	storage := &fakeArtworkStorage{
		pages:  [][]blobstore.ObjectInfo{{{Key: "local/item0/poster/w300.hash0.webp"}}},
		tokens: []string{""},
	}
	stats, err := sweepWithoutDatabase(t, storage, map[string]struct{}{}, 1)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if stats.TooNew != 1 {
		t.Fatalf("too_new = %d, want 1: an unknown age must fail closed", stats.TooNew)
	}
	if len(storage.deleted) != 0 {
		t.Fatalf("deleted %v; an object of unknown age must never be deleted", storage.deleted)
	}
}

func TestSweepStopsInsteadOfDeletingAnAnomalousPage(t *testing.T) {
	t.Parallel()
	// A full page where nothing resolves is the signature of a broken
	// reference check, not an empty catalog.
	storage := &fakeArtworkStorage{
		pages:  [][]blobstore.ObjectInfo{ageingObjects("local", 200, 72*time.Hour)},
		tokens: []string{"next"},
	}
	stats, err := sweepWithoutDatabase(t, storage, map[string]struct{}{}, 1)
	if err == nil {
		t.Fatal("expected the anomaly guard to stop the sweep")
	}
	if !stats.StoppedOnAnomaly {
		t.Fatal("stats must record that the sweep stopped on an anomaly")
	}
	if len(storage.deleted) != 0 {
		t.Fatalf("deleted %v; the anomaly guard must prevent every deletion on that page", storage.deleted)
	}
}

func TestSweepResumesAcrossPagesAndStopsAtMaxPages(t *testing.T) {
	t.Parallel()
	referenced := map[string]struct{}{}
	for i := 0; i < 100; i++ {
		referenced[fmt.Sprintf("local/item%d/poster/original.hash%d.webp", i, i)] = struct{}{}
	}
	storage := &fakeArtworkStorage{
		pages: [][]blobstore.ObjectInfo{
			ageingObjects("local", 100, 72*time.Hour),
			ageingObjects("local", 100, 72*time.Hour),
			ageingObjects("local", 100, 72*time.Hour),
		},
		tokens: []string{"t1", "t2", ""},
	}
	stats, err := sweepWithoutDatabase(t, storage, referenced, 2)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if stats.Pages != 2 {
		t.Fatalf("pages = %d, want 2: maxPages must bound the run", stats.Pages)
	}
	if stats.NextToken != "t2" {
		t.Fatalf("next token = %q, want %q so the next run resumes here", stats.NextToken, "t2")
	}
	if stats.PrefixDone {
		t.Fatal("a bounded run that stopped early must not claim the prefix is finished")
	}
}

func TestSweepWithoutAPoolSkipsClusterLocking(t *testing.T) {
	t.Parallel()
	// The lock needs a pool. A sweeper without one (as in these tests) must
	// still run rather than dereference nil — pglock's nil-pool behavior is
	// not safe to rely on.
	storage := &fakeArtworkStorage{
		pages:  [][]blobstore.ObjectInfo{ageingObjects("local", 2, 72*time.Hour)},
		tokens: []string{""},
	}
	stats, err := sweepWithoutDatabase(t, storage, map[string]struct{}{
		"local/item0/poster/original.hash0.webp": {},
		"local/item1/poster/original.hash1.webp": {},
	}, 1)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if stats.Skipped {
		t.Fatal("a sweeper with no pool must not report itself skipped")
	}
	if stats.Referenced != 2 {
		t.Fatalf("referenced = %d, want 2", stats.Referenced)
	}
}
