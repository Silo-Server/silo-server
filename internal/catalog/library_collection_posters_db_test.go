package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeCollageGenerator records the collages it is asked to compose instead of
// fetching and storing images.
type fakeCollageGenerator struct {
	mu       sync.Mutex
	composed map[string][]string
}

func (g *fakeCollageGenerator) ComposeCollectionCollage(_ context.Context, collectionID, key string, sources []string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.composed == nil {
		g.composed = map[string][]string{}
	}
	g.composed[key] = append([]string(nil), sources...)
	return fmt.Sprintf("collection-images/%s/collage/original.%s.webp", collectionID, key), "th-" + key, nil
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

// TestCollectionCollagesFollowTheViewerDB covers #1618 end to end on the
// database: each viewer's collage is built only from members that viewer can
// access, the first read builds it in the background, and building one
// collage deletes the collection's unused ones. Set SILO_TEST_DATABASE_URL to
// a migrated database to run it.
func TestCollectionCollagesFollowTheViewerDB(t *testing.T) {
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
	libraryA := seedCollagePosterLibrary(t, pool, fmt.Sprintf("collage-a-%d", suffix))
	libraryB := seedCollagePosterLibrary(t, pool, fmt.Sprintf("collage-b-%d", suffix))

	type member struct {
		name    string
		age     int
		library int
		poster  bool
	}
	members := []member{
		{"r", 17, libraryA, true},
		{"pg1", 8, libraryA, true},
		{"no-poster", 8, libraryA, false},
		{"g", 0, libraryB, true},
		{"pg2", 8, libraryA, true},
		{"pg3", 8, libraryA, true},
	}
	poster := func(name string) string { return fmt.Sprintf("test/%s-%d/poster/original.webp", name, suffix) }
	inputs := make([]LibraryCollectionItemInput, 0, len(members))
	for _, m := range members {
		contentID := fmt.Sprintf("collage-%s-%d", m.name, suffix)
		path := ""
		if m.poster {
			path = poster(m.name)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, genres, poster_path, content_rating_age)
			VALUES ($1, 'movie', $1, $1, '{}'::text[], $2, $3)
		`, contentID, path, m.age); err != nil {
			t.Fatalf("seed %s: %v", m.name, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		})
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, m.library); err != nil {
			t.Fatalf("link %s: %v", m.name, err)
		}
		inputs = append(inputs, LibraryCollectionItemInput{MediaItemID: contentID})
	}

	repo := NewLibraryCollectionRepository(pool)
	collection, err := repo.Create(ctx, CreateLibraryCollectionInput{
		LibraryIDs:     []int{libraryA, libraryB},
		Slug:           fmt.Sprintf("collage-%d", suffix),
		Title:          "Collage",
		CollectionType: "manual",
	})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), collection.ID) })
	if err := repo.ReplaceItems(ctx, collection.ID, inputs); err != nil {
		t.Fatalf("replace items: %v", err)
	}

	unrestricted := AccessFilter{}
	pg := AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	libraryAOnly := AccessFilter{AllowedLibraryIDs: []int{libraryA}}
	noLibraries := AccessFilter{AllowedLibraryIDs: []int{}}

	t.Run("sources are the first accessible members with a poster", func(t *testing.T) {
		for name, tc := range map[string]struct {
			access AccessFilter
			want   []string
		}{
			"unrestricted":   {unrestricted, []string{poster("r"), poster("pg1"), poster("g"), poster("pg2")}},
			"PG":             {pg, []string{poster("pg1"), poster("g"), poster("pg2"), poster("pg3")}},
			"library A only": {libraryAOnly, []string{poster("r"), poster("pg1"), poster("pg2"), poster("pg3")}},
			"no libraries":   {noLibraries, nil},
		} {
			sources, err := repo.ListCollectionCollageSources(ctx, []string{collection.ID}, tc.access)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got := sources[collection.ID]; !slices.Equal(got, tc.want) {
				t.Fatalf("%s: sources = %v, want %v", name, got, tc.want)
			}
		}
		// A collection listed twice, as a Jellyfin Ids= request can, keeps
		// the same sources and therefore the same key.
		sources, err := repo.ListCollectionCollageSources(ctx, []string{collection.ID, collection.ID}, pg)
		if err != nil {
			t.Fatalf("repeated ID: %v", err)
		}
		if want := []string{poster("pg1"), poster("g"), poster("pg2"), poster("pg3")}; !slices.Equal(sources[collection.ID], want) {
			t.Fatalf("repeated ID: sources = %v, want %v", sources[collection.ID], want)
		}
	})

	gen := &fakeCollageGenerator{}
	svc := NewLibraryCollectionService(repo, nil, nil, nil)
	svc.CollageGen = gen
	stored, err := repo.GetByID(ctx, collection.ID)
	if err != nil {
		t.Fatalf("load collection: %v", err)
	}

	pgSources, _ := repo.ListCollectionCollageSources(ctx, []string{collection.ID}, pg)
	pgRef := CollectionCollageRef{CollectionID: collection.ID, Key: CollectionCollageKey(pgSources[collection.ID])}

	t.Run("a restricted viewer's first read builds its own collage", func(t *testing.T) {
		if got, ok := svc.CollectionPosters(ctx, []*models.LibraryCollection{stored}, pg)[collection.ID]; ok {
			t.Fatalf("first read served %+v before any collage was built", got)
		}
		waitForCollectionCollage(t, repo, pgRef)
		got := svc.CollectionPosters(ctx, []*models.LibraryCollection{stored}, pg)[collection.ID]
		if got.CollageKey != pgRef.Key || got.Path == "" || got.Thumbhash != "th-"+pgRef.Key {
			t.Fatalf("PG poster = %+v, want collage %s", got, pgRef.Key)
		}
		if built := gen.sources(pgRef.Key); slices.Contains(built, poster("r")) || !slices.Equal(built, pgSources[collection.ID]) {
			t.Fatalf("PG collage built from %v, want %v", built, pgSources[collection.ID])
		}
	})

	t.Run("a sync builds the unrestricted collage up front", func(t *testing.T) {
		svc.MaybeGenerateCollage(ctx, collection.ID)
		got := svc.CollectionPosters(ctx, []*models.LibraryCollection{stored}, unrestricted)[collection.ID]
		if got.CollageKey == "" || got.CollageKey == pgRef.Key {
			t.Fatalf("unrestricted poster = %+v, want its own collage", got)
		}
		if !slices.Contains(gen.sources(got.CollageKey), poster("r")) {
			t.Fatalf("unrestricted collage built from %v, want the R member first", gen.sources(got.CollageKey))
		}
	})

	t.Run("building a collage retires unused ones to the artwork collector", func(t *testing.T) {
		staleRef := CollectionCollageRef{CollectionID: collection.ID, Key: "0000000000000000"}
		stalePath := fmt.Sprintf("collection-images/%s/collage/original.%s.webp", collection.ID, staleRef.Key)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, stalePath)
		})
		if err := repo.SaveCollectionCollage(ctx, CollectionCollage{CollectionCollageRef: staleRef, Path: stalePath}); err != nil {
			t.Fatalf("save stale collage: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE library_collection_poster_variants SET last_used_at = NOW() - interval '8 days'
			WHERE collection_id = $1 AND variant_key = $2
		`, staleRef.CollectionID, staleRef.Key); err != nil {
			t.Fatalf("age stale collage: %v", err)
		}
		if err := svc.PrepareCollectionCollage(ctx, collection.ID, libraryAOnly); err != nil {
			t.Fatalf("prepare collage: %v", err)
		}
		left, err := repo.GetCollectionCollages(ctx, []CollectionCollageRef{staleRef, pgRef})
		if err != nil {
			t.Fatalf("load collages: %v", err)
		}
		if _, ok := left[staleRef]; ok {
			t.Fatal("unused collage survived a build")
		}
		if _, ok := left[pgRef]; !ok {
			t.Fatal("a collage in use was retired")
		}
		assertCollageQueuedForCollector(t, pool, stalePath)
	})

	t.Run("a collage whose collection is gone is queued for the collector", func(t *testing.T) {
		orphan := fmt.Sprintf("collection-images/missing-%d/collage/original.k.webp", suffix)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path = $1`, orphan)
		})
		if err := repo.QueueCollectionCollageObjects(ctx, []string{orphan}); err != nil {
			t.Fatalf("queue: %v", err)
		}
		assertCollageQueuedForCollector(t, pool, orphan)
	})

	t.Run("an uploaded poster is shown to everyone", func(t *testing.T) {
		uploaded := *stored
		uploaded.PosterURL = fmt.Sprintf("collection-images/%s/poster/original.abc.webp", collection.ID)
		for _, viewer := range []AccessFilter{unrestricted, pg} {
			got := svc.CollectionPosters(ctx, []*models.LibraryCollection{&uploaded}, viewer)[collection.ID]
			if got.Path != uploaded.PosterURL || got.CollageKey != "" {
				t.Fatalf("poster = %+v, want the uploaded poster", got)
			}
		}
	})

	t.Run("a collage for a deleted collection is not saved", func(t *testing.T) {
		err := repo.SaveCollectionCollage(ctx, CollectionCollage{
			CollectionCollageRef: CollectionCollageRef{CollectionID: fmt.Sprintf("missing-%d", suffix), Key: "k"},
			Path:                 "collection-images/missing/collage/original.k.webp",
		})
		if !errors.Is(err, ErrLibraryCollectionNotFound) {
			t.Fatalf("err = %v, want ErrLibraryCollectionNotFound", err)
		}
	})
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
	if !slices.Equal(keys, CollectionPosterObjectKeys(path)) || !graced {
		t.Fatalf("queued keys = %v graced=%v, want %v after the grace period", keys, graced, CollectionPosterObjectKeys(path))
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

// waitForCollectionCollage waits for a background build to store ref.
func waitForCollectionCollage(t *testing.T, repo *LibraryCollectionRepository, ref CollectionCollageRef) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, err := repo.GetCollectionCollages(context.Background(), []CollectionCollageRef{ref})
		if err != nil {
			t.Fatalf("load collage: %v", err)
		}
		if _, ok := stored[ref]; ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("collage %s was never built", ref.Key)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
