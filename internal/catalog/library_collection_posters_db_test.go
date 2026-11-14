package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
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

// TestServerSmartCollectionCollagesDB covers the collage of a smart server
// collection, whose members are its query's matches rather than stored items
// (#903): it is built from the first matches each viewer can access, in the
// query's order, ready right after the collection is saved, and list reads
// serve it without running the query. Set SILO_TEST_DATABASE_URL to a
// migrated database to run it.
func TestServerSmartCollectionCollagesDB(t *testing.T) {
	ctx := context.Background()
	f := seedServerSmartCollage(t)
	gen := &fakeCollageGenerator{}
	svc := NewLibraryCollectionService(f.repo, nil, nil, nil)
	svc.CollageGen = gen
	posterOf := func(access AccessFilter) (CollectionPoster, bool) {
		poster, ok := svc.CollectionPosters(ctx, []*models.LibraryCollection{f.collection}, access)[f.collection.ID]
		return poster, ok
	}
	unrestricted := AccessFilter{}
	restricted := AccessFilter{AllowedLibraryIDs: []int{f.shownLibrary}}
	everyMatch := []string{f.poster("a"), f.poster("b"), f.poster("c"), f.poster("d")}
	accessibleMatches := CollectionCollageKey([]string{f.poster("b"), f.poster("c"), f.poster("d"), f.poster("e")})

	t.Run("saving the collection builds the unrestricted collage", func(t *testing.T) {
		svc.MaybeGenerateCollage(ctx, f.collection.ID)
		// The build runs in the background; wait for its record without
		// reading the collection's posters, which would ask for a build too.
		pollUntil(t, "saving the collection recorded its collage", func() bool {
			recorded, err := f.repo.getSmartCollages(ctx, []string{f.collection.ID}, serverSmartCollageAccessKey(unrestricted))
			if err != nil {
				t.Fatalf("load recorded collage: %v", err)
			}
			_, ok := recorded[f.collection.ID]
			return ok
		})
		if got, _ := posterOf(unrestricted); got.CollageKey != CollectionCollageKey(everyMatch) {
			t.Fatalf("poster = %+v, want the collage of the first four matches", got)
		}
		if sources := gen.sources(CollectionCollageKey(everyMatch)); !slices.Equal(sources, everyMatch) {
			t.Fatalf("collage composed from %v, want %v", sources, everyMatch)
		}
	})

	t.Run("a list read serves the collage without running the query", func(t *testing.T) {
		before := f.tracer.statements.Load()
		got, _ := posterOf(unrestricted)
		if n := f.tracer.statements.Load() - before; n != 1 {
			t.Fatalf("list read sent %d statements, want 1", n)
		}
		if got.CollageKey != CollectionCollageKey(everyMatch) {
			t.Fatalf("poster = %+v, want the collage of the first four matches", got)
		}
	})

	t.Run("a restricted viewer sees only matches it can access", func(t *testing.T) {
		pollUntil(t, "the restricted viewer's collage shows only accessible matches", func() bool {
			got, ok := posterOf(restricted)
			if ok && got.CollageKey == CollectionCollageKey(everyMatch) {
				t.Fatal("restricted viewer was served the collage showing a title it can't access")
			}
			return ok && got.CollageKey == accessibleMatches
		})
	})

	t.Run("a collage the artwork reconcile cleared is rebuilt on the next read", func(t *testing.T) {
		// The reconcile resets a collage whose object is missing by clearing
		// its path (metadata.artworkSweepTables), keeping the row.
		if _, err := f.repo.pool.Exec(ctx, `
			UPDATE library_collection_poster_variants SET poster_path = '', poster_thumbhash = ''
			WHERE collection_id = $1 AND variant_key = $2
		`, f.collection.ID, CollectionCollageKey(everyMatch)); err != nil {
			t.Fatalf("clear collage: %v", err)
		}
		pollUntil(t, "the cleared collage was rebuilt", func() bool {
			got, ok := posterOf(unrestricted)
			return ok && got.CollageKey == CollectionCollageKey(everyMatch)
		})
	})

	t.Run("an uploaded poster wins over the collage", func(t *testing.T) {
		uploaded := *f.collection
		uploaded.PosterURL = "collection-images/uploaded/original.webp"
		got := svc.CollectionPosters(ctx, []*models.LibraryCollection{&uploaded}, unrestricted)[f.collection.ID]
		if got.Path != uploaded.PosterURL || got.CollageKey != "" {
			t.Fatalf("poster = %+v, want the uploaded poster", got)
		}
	})
}

// serverSmartCollageFixture is a smart server collection matching titles a
// to e by genre, in title order. Titles b to e are in shownLibrary, the
// library it shows in; a is in another library.
type serverSmartCollageFixture struct {
	repo         *LibraryCollectionRepository
	tracer       *statementCountTracer
	collection   *models.LibraryCollection
	shownLibrary int
	poster       func(name string) string
}

func seedServerSmartCollage(t *testing.T) serverSmartCollageFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	f := serverSmartCollageFixture{tracer: &statementCountTracer{}}
	config.ConnConfig.Tracer = f.tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := fmt.Sprintf("server-smart-collage-%d", time.Now().UnixNano())
	f.shownLibrary = seedCollagePosterLibrary(t, pool, prefix+"-shown")
	other := seedCollagePosterLibrary(t, pool, prefix+"-other")
	genre := prefix + "-genre"
	f.poster = func(name string) string { return fmt.Sprintf("test/%s-%s/poster/original.webp", prefix, name) }
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		library := f.shownLibrary
		if name == "a" {
			library = other
		}
		contentID := prefix + "-" + name
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, genres, poster_path)
			VALUES ($1, 'movie', $2, $2, ARRAY[$3], $4)
		`, contentID, name, genre, f.poster(name)); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		})
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, library); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}

	f.repo = NewLibraryCollectionRepository(pool)
	query := fmt.Sprintf(`{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":%q}]}],"sort":{"field":"title","order":"asc"}}`, genre)
	f.collection, err = f.repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryIDs:      []int{f.shownLibrary},
		Slug:            prefix,
		Title:           "Smart collage",
		CollectionType:  "smart",
		QueryDefinition: json.RawMessage(query),
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() {
		_ = f.repo.Delete(context.Background(), f.collection.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "collection-images/"+f.collection.ID+"/%")
	})
	return f
}

// pollUntil waits up to ten seconds for done, which observes the result of
// work running in the background.
func pollUntil(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(20 * time.Millisecond) //nolint:forbidigo // the background collage build signals nothing but its stored row
	}
}
