package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// recordingCollageGenerator stores nothing; it records the collages it is
// asked to compose, and holds a compose while gate is set.
type recordingCollageGenerator struct {
	mu       sync.Mutex
	composed map[string][]string
	gate     chan struct{}
}

func (g *recordingCollageGenerator) CollectionCollagePath(collectionID, key string) string {
	return fmt.Sprintf("user-collection-images/%s/collage/original.%s.webp", collectionID, key)
}

func (g *recordingCollageGenerator) ComposeCollectionCollage(ctx context.Context, collectionID, key string, sources []string) (string, string, error) {
	g.mu.Lock()
	gate := g.gate
	g.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.composed == nil {
		g.composed = map[string][]string{}
	}
	g.composed[key] = slices.Clone(sources)
	return g.CollectionCollagePath(collectionID, key), "th-" + key, nil
}

func (g *recordingCollageGenerator) built(key string) ([]string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	sources, ok := g.composed[key]
	return sources, ok
}

// waitForCollage waits until the generator has composed key.
func (g *recordingCollageGenerator) waitFor(t *testing.T, key string) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if sources, ok := g.built(key); ok {
			return sources
		}
		if time.Now().After(deadline) {
			t.Fatalf("collage %s was never composed", key)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// collagePosterResolver signs a stored key as itself.
type collagePosterResolver struct{}

func (collagePosterResolver) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		out[key] = catalog.ResolvedImageURL{URL: "https://cdn.test/" + key}
	}
	return out
}

// libraryOwnerAccess limits every owner to one library.
type libraryOwnerAccess struct{ library int }

func (o libraryOwnerAccess) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{AllowedLibraryIDs: []int{o.library}}, nil
}

// TestPersonalCollectionCollagesDB covers the personal collection collage
// read and build paths through the handlers: a change builds the owner's
// collage in the background, /api/v2 lists show each profile its own collage
// (a shared collection's viewer only titles both it and the owner can see)
// without waiting for a build, and the frozen /api/v1 list shows none.
func TestPersonalCollectionCollagesDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "viewer"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// f.ids[0] lives in f.hidden, the rest in f.library. Ages: G, G, R, PG,
	// PG; f.ids[1] has no poster.
	poster := func(i int) string { return fmt.Sprintf("test/%s/poster/original.webp", f.ids[i]) }
	for i, age := range []int{0, 0, 17, 8, 8} {
		path := poster(i)
		if i == 1 {
			path = ""
		}
		f.exec(t, `UPDATE media_items SET content_rating_age=$2, poster_path=$3, title=$4, sort_title=$4, genres=ARRAY[$5::text] WHERE content_id=$1`, f.ids[i], age, path, fmt.Sprintf("t%d", i), f.ids[0]+"-genre")
	}

	// Deleting the collections queues their collages for the artwork
	// collector; drop those entries too.
	var collectionIDs []string
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM user_personal_collections WHERE user_id=$1`, f.account)
		for _, id := range collectionIDs {
			_, _ = f.pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "user-collection-images/"+id+"/%")
		}
	})

	gen := &recordingCollageGenerator{}
	h := NewCollectionHandler(provider)
	h.Executor = &catalog.QueryExecutor{Pool: f.pool}
	h.ArtworkResolver = collagePosterResolver{}
	h.CollectionOwners = libraryOwnerAccess{library: f.library}
	h.Collages = catalog.NewPersonalCollectionCollages(f.pool, gen)
	h.Collages.RefreshDelay = 0

	as := func(profile string, scope access.Scope) context.Context {
		c := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account, ProfileID: profile})
		return access.SetScope(c, scope)
	}
	ownerCtx := as("owner", access.Scope{})
	viewerCtx := as("viewer", access.Scope{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}})

	created, err := h.CreatePersonalCollection(ownerCtx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Shared", CollectionType: "manual", IsShared: true}})
	if err != nil {
		t.Fatal(err)
	}
	collectionIDs = append(collectionIDs, created.ID)
	if created.PosterURL != "" || created.PosterIsCollage {
		t.Fatalf("a new empty collection shows poster %q (collage %v)", created.PosterURL, created.PosterIsCollage)
	}
	for i, id := range f.ids {
		if err := h.AddPersonalCollectionItem(ownerCtx, f.account, "owner", created.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	def := []catalog.PersonalCollectionDefinition{{ID: created.ID, CollectionType: "manual"}}
	ownerSources, err := h.Collages.ListSources(ctx, f.account, def, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	ownerKey := catalog.CollectionCollageKey(ownerSources[created.ID])
	if want := []string{poster(0), poster(2), poster(3), poster(4)}; !slices.Equal(ownerSources[created.ID], want) {
		t.Fatalf("owner sources = %v, want %v", ownerSources[created.ID], want)
	}

	t.Run("adding titles builds the owner's collage", func(t *testing.T) {
		gen.waitFor(t, ownerKey)
	})

	findView := func(t *testing.T, ctx context.Context, profile string) PersonalCollectionView {
		t.Helper()
		var list PersonalCollectionListView
		deadline := time.Now().Add(10 * time.Second)
		for {
			// The build is composed before its row is saved; read until
			// the row lands or the deadline passes.
			list, err = h.ListPersonalCollections(ctx, f.account, profile)
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Collections) != 1 || list.Collections[0].ID != created.ID {
				t.Fatalf("list = %+v", list.Collections)
			}
			if list.Collections[0].PosterIsCollage || time.Now().After(deadline) {
				return list.Collections[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	t.Run("an /api/v2 list shows the owner its collage", func(t *testing.T) {
		got := findView(t, WithNativeAPIV2(ownerCtx), "owner")
		want := "https://cdn.test/" + cardThumbnailPath(gen.CollectionCollagePath(created.ID, ownerKey))
		if !got.PosterIsCollage || got.PosterURL != want || got.PosterThumbhash != "th-"+ownerKey {
			t.Fatalf("owner view poster = %q (collage %v, thumbhash %q), want %q", got.PosterURL, got.PosterIsCollage, got.PosterThumbhash, want)
		}
	})

	// getCollection and updateCollection answer the editor state behind a
	// strong ETag, which carries no presigned poster of any kind.
	t.Run("the editor state carries no poster", func(t *testing.T) {
		got, err := h.PersonalCollectionEditor(WithNativeAPIV2(ownerCtx), f.account, "owner", created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Collection.PosterURL != "" || got.Collection.PosterIsCollage || got.Collection.PosterThumbhash != "" {
			t.Fatalf("editor poster = %q (collage %v, thumbhash %q), want none", got.Collection.PosterURL, got.Collection.PosterIsCollage, got.Collection.PosterThumbhash)
		}
	})

	t.Run("an /api/v2 library tab marks the owner's collage", func(t *testing.T) {
		lh := &LibraryCollectionHandler{Executor: h.Executor, CollectionOwners: h.CollectionOwners, PersonalCollages: h.Collages}
		got := lh.withVisibleItemCounts(WithNativeAPIV2(ownerCtx), f.account, "owner", []usercollections.ServerVisibleCollection{{ID: created.ID, CreatorProfileID: "owner", CollectionType: "manual"}})
		if len(got) != 1 || !got[0].PosterIsCollage || got[0].PosterPath != gen.CollectionCollagePath(created.ID, ownerKey) {
			t.Fatalf("library tab entries = %+v, want the owner's collage %s", got, ownerKey)
		}
	})

	t.Run("the /api/v1 list shows no collage", func(t *testing.T) {
		list, err := h.ListPersonalCollections(ownerCtx, f.account, "owner")
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Collections[0]; got.PosterURL != "" || got.PosterIsCollage {
			t.Fatalf("v1 poster = %q (collage %v), want none", got.PosterURL, got.PosterIsCollage)
		}
	})

	t.Run("a viewer's list answers before its collage is built, then shows its own", func(t *testing.T) {
		// The owner sees only f.library and the viewer only up to PG, so
		// the viewer's collage leaves out f.ids[0] and f.ids[2].
		viewerFilter := catalog.IntersectAccess(catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, catalog.AccessFilter{AllowedLibraryIDs: []int{f.library}})
		viewerSources, err := h.Collages.ListSources(ctx, f.account, def, viewerFilter)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{poster(3), poster(4)}; !slices.Equal(viewerSources[created.ID], want) {
			t.Fatalf("viewer sources = %v, want %v", viewerSources[created.ID], want)
		}
		viewerKey := catalog.CollectionCollageKey(viewerSources[created.ID])

		gate := make(chan struct{})
		gen.mu.Lock()
		gen.gate = gate
		gen.mu.Unlock()
		v2 := WithNativeAPIV2(viewerCtx)
		list, err := h.ListPersonalCollections(v2, f.account, "viewer")
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Collections[0]; got.PosterURL != "" || got.PosterIsCollage {
			t.Fatalf("viewer's first read showed %q (collage %v) while its collage was still being built", got.PosterURL, got.PosterIsCollage)
		}
		gen.mu.Lock()
		gen.gate = nil
		gen.mu.Unlock()
		close(gate)

		if built := gen.waitFor(t, viewerKey); slices.Contains(built, poster(0)) || slices.Contains(built, poster(2)) {
			t.Fatalf("viewer collage built from %v", built)
		}
		got := findView(t, v2, "viewer")
		if !got.PosterIsCollage || got.PosterThumbhash != "th-"+viewerKey {
			t.Fatalf("viewer view = %+v, want collage %s", got, viewerKey)
		}
	})

	t.Run("editing a smart collection's rules builds its new collage", func(t *testing.T) {
		query := func(sort string) json.RawMessage {
			return json.RawMessage(fmt.Sprintf(`{"match":"all","groups":[{"match":"all","rules":[{"field":"genre","op":"is","value":%q}]}],"sort":{"field":"title","order":%q}}`, f.ids[0]+"-genre", sort))
		}
		smart, err := h.CreatePersonalCollection(ownerCtx, PersonalCollectionCreateCommand{UserID: f.account, ProfileID: "owner", Request: PersonalCollectionCreateRequest{Name: "Smart", CollectionType: "smart", QueryDefinition: query("asc")}})
		if err != nil {
			t.Fatal(err)
		}
		collectionIDs = append(collectionIDs, smart.ID)
		ascending := catalog.CollectionCollageKey([]string{poster(0), poster(2), poster(3), poster(4)})
		gen.waitFor(t, ascending)

		if _, err := h.UpdatePersonalCollection(ownerCtx, PersonalCollectionUpdateCommand{UserID: f.account, ProfileID: "owner", CollectionID: smart.ID, Request: PersonalCollectionUpdateRequest{QueryDefinition: query("desc")}}); err != nil {
			t.Fatal(err)
		}
		gen.waitFor(t, catalog.CollectionCollageKey([]string{poster(4), poster(3), poster(2), poster(0)}))
	})

	t.Run("without artwork storage the account reports no artwork", func(t *testing.T) {
		features, err := h.PersonalCollectionFeatures(ctx, f.account)
		if err != nil {
			t.Fatal(err)
		}
		if features.Artwork {
			t.Fatal("artwork reported without artwork storage")
		}
	})
}
