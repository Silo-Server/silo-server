package usercollections

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// pgOwner limits every collection owner to a PG rating ceiling.
type pgOwner struct{}

func (pgOwner) OwnerFilter(context.Context, int, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, nil
}

// syncCollageGenerator records the collages it composes instead of storing them.
type syncCollageGenerator struct {
	mu       sync.Mutex
	composed map[string][]string
}

func (g *syncCollageGenerator) CollectionCollagePath(collectionID, key string) string {
	return fmt.Sprintf("user-collection-images/%s/collage/original.%s.webp", collectionID, key)
}

func (g *syncCollageGenerator) ComposeCollectionCollage(_ context.Context, collectionID, key string, sources []string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.composed == nil {
		g.composed = map[string][]string{}
	}
	g.composed[key] = slices.Clone(sources)
	return g.CollectionCollagePath(collectionID, key), "th", nil
}

// TestSyncBuildsTheOwnersCollageDB checks that a finished sync builds the
// collage its owner sees, from the synced titles the owner can access.
func TestSyncBuildsTheOwnersCollageDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	prefix := fmt.Sprintf("sync-collage-%d", suffix)
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	var collectionID string
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id=$1`, account)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "user-collection-images/"+collectionID+"/%")
	})
	// The list, in order: an R title, then two PG titles with posters.
	tmdbBase := int(suffix % 1_000_000_000 * 10)
	var entries staticTMDBList
	poster := func(i int) string { return fmt.Sprintf("test/%s-%d/poster/original.webp", prefix, i) }
	for i, age := range []int{17, 8, 8} {
		tmdb := tmdbBase + i
		entries = append(entries, catalog.TMDBCollectionEntry{ID: tmdb, MediaType: "movie"})
		if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,tmdb_id,content_rating_age,poster_path) VALUES($1,'movie',$1,$2,$3,$4)`,
			fmt.Sprintf("%s-%d", prefix, i), strconv.Itoa(tmdb), age, poster(i)); err != nil {
			t.Fatal(err)
		}
	}

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "owner", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: "owner", Name: "Imported", CollectionType: "tmdb", QueryDefinition: "{}",
		SourceConfig: `{"mode":"tmdb_list","url":"https://www.themoviedb.org/list/310"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	collectionID = collection.ID

	gen := &syncCollageGenerator{}
	svc := NewService(provider, catalog.NewItemRepository(pool), catalog.NewLibraryItemRepository(pool), pgOwner{}, nil, slog.New(slog.DiscardHandler))
	svc.TMDBLists = entries
	svc.Collages = catalog.NewPersonalCollectionCollages(pool, gen)
	svc.Collages.RefreshDelay = 0
	if _, err := svc.SyncCollection(ctx, account, collection.ID); err != nil {
		t.Fatal(err)
	}

	want := []string{poster(1), poster(2)}
	key := catalog.CollectionCollageKey(want)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var stored bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_personal_collection_poster_variants WHERE user_id=$1 AND collection_id=$2 AND variant_key=$3)`,
			account, collection.ID, key).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sync built no collage from %v", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	gen.mu.Lock()
	defer gen.mu.Unlock()
	if got := gen.composed[key]; !slices.Equal(got, want) {
		t.Fatalf("collage composed from %v, want %v", got, want)
	}
}
