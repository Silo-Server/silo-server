package catalog

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeCollageGenerator records the collages it is asked to compose instead of
// fetching and storing images.
type fakeCollageGenerator struct {
	mu       sync.Mutex
	composed map[string][]string
	fail     error
}

func (g *fakeCollageGenerator) CollectionCollagePath(collectionID, key string) string {
	return fmt.Sprintf("collection-images/%s/collage/original.%s.webp", collectionID, key)
}

func (g *fakeCollageGenerator) ComposeCollectionCollage(_ context.Context, collectionID, key string, sources []string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail != nil {
		return "", "", g.fail
	}
	if g.composed == nil {
		g.composed = map[string][]string{}
	}
	g.composed[key] = append([]string(nil), sources...)
	return g.CollectionCollagePath(collectionID, key), "th-" + key, nil
}

func (g *fakeCollageGenerator) sources(key string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.composed[key]
}

func TestCollectionCollageReadsKeepActiveVariantsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	libraryID := seedCollagePosterLibrary(t, pool, fmt.Sprintf("collage-usage-%d", suffix))
	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryIDs:     []int{libraryID},
		Slug:           fmt.Sprintf("collage-usage-%d", suffix),
		Title:          "Collage usage",
		CollectionType: "manual",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })

	active := CollectionCollage{
		CollectionCollageRef: CollectionCollageRef{CollectionID: collection.ID, Key: "1111111111111111"},
		Path:                 fmt.Sprintf("collection-images/%s/collage/original.1111111111111111.webp", collection.ID),
		Thumbhash:            "active-thumbhash",
	}
	unused := CollectionCollage{
		CollectionCollageRef: CollectionCollageRef{CollectionID: collection.ID, Key: "2222222222222222"},
		Path:                 fmt.Sprintf("collection-images/%s/collage/original.2222222222222222.webp", collection.ID),
	}
	for _, c := range []CollectionCollage{active, unused} {
		if err := repo.SaveCollectionCollage(ctx, c); err != nil {
			t.Fatalf("save collage: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, c.Path)
		})
	}
	if _, err := pool.Exec(ctx, `
		UPDATE library_collection_poster_variants SET last_used_at = NOW() - interval '8 days'
		WHERE collection_id = $1
	`, collection.ID); err != nil {
		t.Fatalf("age collages: %v", err)
	}

	svc := NewLibraryCollectionService(repo, nil, nil, nil)
	svc.CollageGen = &fakeCollageGenerator{}
	// Jellyfin image-tag requests use this lookup without re-listing the collection.
	readAt := time.Now()
	poster, found, err := svc.CollectionCollage(ctx, collection.ID, active.Key)
	if err != nil || !found {
		t.Fatalf("read active collage: found=%v err=%v", found, err)
	}
	if poster.Path != active.Path || poster.Thumbhash != active.Thumbhash || poster.CollageKey != active.Key {
		t.Fatalf("poster = %+v, want the active collage", poster)
	}
	stored, err := repo.GetCollectionCollages(ctx, []CollectionCollageRef{active.CollectionCollageRef})
	if err != nil {
		t.Fatalf("load active collage: %v", err)
	}
	touchedAt := stored[active.CollectionCollageRef].LastUsedAt
	if touchedAt.Before(readAt) {
		t.Fatalf("tagged read left last_used_at at %s, before the read at %s", touchedAt, readAt)
	}

	if _, found, err := svc.CollectionCollage(ctx, collection.ID, active.Key); err != nil || !found {
		t.Fatalf("read active collage again: found=%v err=%v", found, err)
	}
	stored, err = repo.GetCollectionCollages(ctx, []CollectionCollageRef{active.CollectionCollageRef})
	if err != nil {
		t.Fatalf("reload active collage: %v", err)
	}
	if got := stored[active.CollectionCollageRef].LastUsedAt; !got.Equal(touchedAt) {
		t.Fatalf("a second read within the touch interval changed last_used_at from %s to %s", touchedAt, got)
	}

	sources := []string{"new/poster/original.webp"}
	newRef := CollectionCollageRef{CollectionID: collection.ID, Key: CollectionCollageKey(sources)}
	if err := svc.buildCollage(ctx, newRef, sources); err != nil {
		t.Fatalf("build another collage: %v", err)
	}
	stored, err = repo.GetCollectionCollages(ctx, []CollectionCollageRef{active.CollectionCollageRef, unused.CollectionCollageRef})
	if err != nil {
		t.Fatalf("load collages after retirement: %v", err)
	}
	if _, ok := stored[active.CollectionCollageRef]; !ok {
		t.Fatal("a collage just served by its image tag was retired")
	}
	if _, ok := stored[unused.CollectionCollageRef]; ok {
		t.Fatal("an unused collage survived retirement")
	}
	assertCollageQueuedForCollector(t, pool, unused.Path)
}

// assertCollageQueuedForCollector checks that the collage at path waits in
// the artwork collector's queue with every object stored for it, after the
// grace period.
func assertCollageQueuedForCollector(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	var keys []string
	var graced bool
	if err := pool.QueryRow(context.Background(), `
		SELECT object_keys, not_before > NOW() + interval '23 hours'
		FROM artwork_revision_gc_candidates WHERE original_path = $1
	`, path).Scan(&keys, &graced); err != nil {
		t.Fatalf("collage %s is not queued for the collector: %v", path, err)
	}
	want := []string{path}
	for _, width := range CollectionPosterWidths {
		want = append(want, strings.Replace(path, "/original.", fmt.Sprintf("/w%d.", width), 1))
	}
	if !slices.Equal(keys, want) || !graced {
		t.Fatalf("queued keys = %v graced=%v, want %v after the grace period", keys, graced, want)
	}
}

func seedCollagePosterLibrary(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id
	`, name).Scan(&id); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, id)
	})
	return id
}
