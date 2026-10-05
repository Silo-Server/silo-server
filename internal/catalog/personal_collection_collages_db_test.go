package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// fixedOwnerAccess answers one owner filter for every personal collection.
type fixedOwnerAccess struct{ filter AccessFilter }

func (o fixedOwnerAccess) OwnerFilter(context.Context, int, string) (AccessFilter, error) {
	return o.filter, nil
}

// TestPersonalCollectionCollagesFollowTheViewerDB covers personal collection
// collages on the database: each viewer's collage is built only from titles
// it can see in the collection (for another profile's shared collection, only
// those the owner can see too), a read never composes but builds a missing
// collage in the background, Refresh builds ahead of the next read, and a
// deleted collection hands its collages to the artwork collector. Set
// SILO_TEST_DATABASE_URL to a migrated database to run it.
func TestPersonalCollectionCollagesFollowTheViewerDB(t *testing.T) {
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
	prefix := fmt.Sprintf("personal-collage-%d", suffix)
	libraryA := seedCollagePosterLibrary(t, pool, prefix+"-a")
	libraryB := seedCollagePosterLibrary(t, pool, prefix+"-b")
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	owner, kid := prefix+"-owner", prefix+"-kid"
	if _, err := pool.Exec(ctx, `INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $3, 'owner'), ($2, $3, 'kid')`, owner, kid, userID); err != nil {
		t.Fatalf("seed profiles: %v", err)
	}

	// Titles sort by name in the same order as their positions, so the smart
	// collection below lists them in the manual collection's order.
	type title struct {
		name    string
		age     int
		library int
		poster  bool
	}
	titles := []title{
		{"a-r", 17, libraryA, true},
		{"b-pg1", 8, libraryA, true},
		{"c-no-poster", 8, libraryA, false},
		{"d-g", 0, libraryB, true},
		{"e-pg2", 8, libraryA, true},
		{"f-pg3", 8, libraryA, true},
	}
	genre := prefix + "-genre"
	poster := func(name string) string { return fmt.Sprintf("test/%s-%s/poster/original.webp", prefix, name) }
	contentIDs := make([]string, 0, len(titles))
	for _, ti := range titles {
		contentID := fmt.Sprintf("%s-%s", prefix, ti.name)
		path := ""
		if ti.poster {
			path = poster(ti.name)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_items (content_id, type, title, sort_title, genres, poster_path, content_rating_age)
			VALUES ($1, 'movie', $2, $2, ARRAY[$3], $4, $5)
		`, contentID, ti.name, genre, path, ti.age); err != nil {
			t.Fatalf("seed %s: %v", ti.name, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
		})
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, contentID, ti.library); err != nil {
			t.Fatalf("link %s: %v", ti.name, err)
		}
		contentIDs = append(contentIDs, contentID)
	}

	store, err := pgstore.NewPostgresProvider(pool).ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	manual, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: owner, Name: "Manual", CollectionType: "manual", IsShared: true})
	if err != nil {
		t.Fatalf("create manual collection: %v", err)
	}
	for i, id := range contentIDs {
		if err := store.AddCollectionItem(ctx, manual.ID, id, i); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	smartQuery := fmt.Sprintf(`{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":%q}]}],"sort":{"field":"title","order":"asc"}}`, genre)
	smart, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: owner, Name: "Smart", CollectionType: "smart", QueryDefinition: smartQuery, IsShared: true})
	if err != nil {
		t.Fatalf("create smart collection: %v", err)
	}
	// Deleting the account deletes its collections and queues their
	// collages for the artwork collector; drop those entries too.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id = $1`, userID)
		for _, id := range []string{manual.ID, smart.ID} {
			_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "collection-images/"+id+"/%")
		}
	})
	manualDef := PersonalCollectionDefinition{ID: manual.ID, CollectionType: "manual"}
	smartDef := PersonalCollectionDefinition{ID: smart.ID, CollectionType: "smart", QueryDefinition: smartQuery}

	unrestricted := AccessFilter{UserID: userID, ProfileID: owner}
	pg := AccessFilter{UserID: userID, ProfileID: kid, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
	// The kid reads the owner's shared collection, and the owner may see only
	// library A: the kid's filter is both limits together.
	kidReadsShared, err := PersonalCollectionFilter(ctx, fixedOwnerAccess{AccessFilter{AllowedLibraryIDs: []int{libraryA}}}, pg, userID, kid, owner)
	if err != nil {
		t.Fatalf("owner filter: %v", err)
	}
	noLibraries := AccessFilter{UserID: userID, ProfileID: kid, AllowedLibraryIDs: []int{}}

	gen := &fakeCollageGenerator{}
	collages := NewPersonalCollectionCollages(pool, gen)
	collages.RefreshDelay = 0

	t.Run("sources are the first titles the viewer can see that have a poster", func(t *testing.T) {
		for name, tc := range map[string]struct {
			access AccessFilter
			want   []string
		}{
			"owner":                           {unrestricted, []string{poster("a-r"), poster("b-pg1"), poster("d-g"), poster("e-pg2")}},
			"PG viewer":                       {pg, []string{poster("b-pg1"), poster("d-g"), poster("e-pg2"), poster("f-pg3")}},
			"PG viewer of a library A owner":  {kidReadsShared, []string{poster("b-pg1"), poster("e-pg2"), poster("f-pg3")}},
			"viewer without libraries at all": {noLibraries, nil},
		} {
			sources, err := collages.ListSources(ctx, userID, []PersonalCollectionDefinition{manualDef, smartDef}, tc.access)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, id := range []string{manual.ID, smart.ID} {
				if got := sources[id]; !slices.Equal(got, tc.want) {
					t.Fatalf("%s: sources of %s = %v, want %v", name, id, got, tc.want)
				}
			}
		}
	})

	kidSources, err := collages.ListSources(ctx, userID, []PersonalCollectionDefinition{manualDef}, kidReadsShared)
	if err != nil {
		t.Fatalf("kid sources: %v", err)
	}
	kidRef := CollectionCollageRef{CollectionID: manual.ID, Key: CollectionCollageKey(kidSources[manual.ID])}
	collageStore := personalCollageStore{pool: pool, userID: userID}

	t.Run("a read serves no poster and builds the viewer's collage in the background", func(t *testing.T) {
		if got, ok := collages.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef}, kidReadsShared)[manual.ID]; ok {
			t.Fatalf("first read served %+v before any collage was built", got)
		}
		waitForStoredCollage(t, collageStore, kidRef)
		got := collages.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef}, kidReadsShared)[manual.ID]
		if got.CollageKey != kidRef.Key || got.Path != fmt.Sprintf("collection-images/%s/collage/original.%s.webp", manual.ID, kidRef.Key) || got.Thumbhash != "th-"+kidRef.Key {
			t.Fatalf("kid poster = %+v, want collage %s", got, kidRef.Key)
		}
		built := gen.sources(kidRef.Key)
		if slices.Contains(built, poster("a-r")) || slices.Contains(built, poster("d-g")) {
			t.Fatalf("kid collage built from %v, which holds a title the kid or the owner can't see", built)
		}
	})

	t.Run("the owner's collage is its own", func(t *testing.T) {
		ownerSources, _ := collages.ListSources(ctx, userID, []PersonalCollectionDefinition{manualDef}, unrestricted)
		ownerRef := CollectionCollageRef{CollectionID: manual.ID, Key: CollectionCollageKey(ownerSources[manual.ID])}
		if err := collages.Prepare(ctx, userID, manualDef, unrestricted); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		got := collages.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef}, unrestricted)[manual.ID]
		if got.CollageKey != ownerRef.Key || got.CollageKey == kidRef.Key {
			t.Fatalf("owner poster = %+v, want collage %s", got, ownerRef.Key)
		}
	})

	t.Run("a viewer who can see no title with a poster gets no collage", func(t *testing.T) {
		if got := collages.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef, smartDef}, noLibraries); len(got) != 0 {
			t.Fatalf("posters = %+v, want none", got)
		}
	})

	t.Run("Refresh builds the collage after a change, ahead of the next read", func(t *testing.T) {
		// Moving the last title to the front changes the owner's first four.
		if err := store.ReorderCollectionItems(ctx, manual.ID, append([]string{contentIDs[5]}, contentIDs[:5]...)); err != nil {
			t.Fatalf("reorder: %v", err)
		}
		sources, _ := collages.ListSources(ctx, userID, []PersonalCollectionDefinition{manualDef}, unrestricted)
		want := []string{poster("f-pg3"), poster("a-r"), poster("b-pg1"), poster("d-g")}
		if !slices.Equal(sources[manual.ID], want) {
			t.Fatalf("sources after reorder = %v, want %v", sources[manual.ID], want)
		}
		collages.Refresh(userID, manualDef, unrestricted)
		waitForStoredCollage(t, collageStore, CollectionCollageRef{CollectionID: manual.ID, Key: CollectionCollageKey(want)})
	})

	t.Run("a smart collection's collage follows its query", func(t *testing.T) {
		if err := collages.Prepare(ctx, userID, smartDef, pg); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		got := collages.Posters(ctx, userID, []PersonalCollectionDefinition{smartDef}, pg)[smart.ID]
		want := []string{poster("b-pg1"), poster("d-g"), poster("e-pg2"), poster("f-pg3")}
		if got.CollageKey != CollectionCollageKey(want) || !slices.Equal(gen.sources(got.CollageKey), want) {
			t.Fatalf("smart poster = %+v built from %v, want %v", got, gen.sources(got.CollageKey), want)
		}
	})

	t.Run("without a generator nothing is served or built", func(t *testing.T) {
		off := NewPersonalCollectionCollages(pool, nil)
		if got := off.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef}, kidReadsShared); len(got) != 0 {
			t.Fatalf("posters = %+v, want none", got)
		}
		if err := off.Prepare(ctx, userID, manualDef, unrestricted); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		var nilCollages *PersonalCollectionCollages
		nilCollages.Refresh(userID, manualDef, unrestricted)
		if got := nilCollages.Posters(ctx, userID, []PersonalCollectionDefinition{manualDef}, unrestricted); len(got) != 0 {
			t.Fatalf("nil posters = %+v, want none", got)
		}
	})

	t.Run("a collage for a deleted collection is not saved", func(t *testing.T) {
		err := collageStore.SaveCollectionCollage(ctx, CollectionCollage{
			CollectionCollageRef: CollectionCollageRef{CollectionID: prefix + "-missing", Key: "k"},
			Path:                 "user-collection-images/missing/collage/original.k.webp",
		})
		if !errors.Is(err, ErrPersonalCollectionNotFound) {
			t.Fatalf("err = %v, want ErrPersonalCollectionNotFound", err)
		}
	})

	t.Run("deleting a collection hands its collages to the collector", func(t *testing.T) {
		kidPath := fmt.Sprintf("collection-images/%s/collage/original.%s.webp", manual.ID, kidRef.Key)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "collection-images/"+manual.ID+"/%")
		})
		if err := store.DeleteCollection(ctx, manual.ID); err != nil {
			t.Fatalf("delete collection: %v", err)
		}
		var left int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_personal_collection_poster_variants WHERE collection_id = $1`, manual.ID).Scan(&left); err != nil {
			t.Fatalf("count collages: %v", err)
		}
		if left != 0 {
			t.Fatalf("%d collages outlived their collection", left)
		}
		assertCollageQueuedForCollector(t, pool, kidPath)
	})
}

// waitForStoredCollage waits for a background build to store ref.
func waitForStoredCollage(t *testing.T, store collageStore, ref CollectionCollageRef) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		stored, err := store.GetCollectionCollages(context.Background(), []CollectionCollageRef{ref})
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
