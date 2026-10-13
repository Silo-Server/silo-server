package sections

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

type stubCollectionOwners map[string]catalog.AccessFilter

func (s stubCollectionOwners) OwnerFilter(_ context.Context, _ int, ownerProfileID string) (catalog.AccessFilter, error) {
	filter, ok := s[ownerProfileID]
	if !ok {
		return catalog.AccessFilter{}, fmt.Errorf("unknown owner %q", ownerProfileID)
	}
	return filter, nil
}

// A home row configured with a personal collection follows the collection
// visibility rule (#1615): another profile's shared collection shows only
// what both its owner and the viewer can access, and another profile's
// private collection shows nothing.
func TestFetchUserCollectionProfileVisibilityPostgres(t *testing.T) {
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

	prefix := fmt.Sprintf("home-row-visibility-%d", time.Now().UnixNano())
	libraries := make([]int, 2)
	for i := range libraries {
		if err := pool.QueryRow(ctx,
			`INSERT INTO media_folders(type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
			fmt.Sprintf("%s-%d", prefix, i),
		).Scan(&libraries[i]); err != nil {
			t.Fatalf("seed library: %v", err)
		}
	}
	ownerOnly, shared := prefix+"-owner-library", prefix+"-other-library"
	for i, contentID := range []string{ownerOnly, shared} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_items(content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'released', '{}')`,
			contentID,
		); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_item_libraries(content_id, media_folder_id) VALUES ($1, $2)`,
			contentID, libraries[i],
		); err != nil {
			t.Fatalf("seed item library: %v", err)
		}
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, prefix,
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	const owner, viewer = "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
	for _, profile := range []string{owner, viewer} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile, IsPrimary: profile == owner}); err != nil {
			t.Fatalf("CreateProfile: %v", err)
		}
	}
	createWithItems := func(name string, isShared bool) string {
		t.Helper()
		collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
			CreatorProfileID: owner, Name: name, IsShared: isShared,
		})
		if err != nil {
			t.Fatalf("CreateCollection(%s): %v", name, err)
		}
		for i, contentID := range []string{ownerOnly, shared} {
			if err := store.AddCollectionItem(ctx, collection.ID, contentID, i); err != nil {
				t.Fatalf("AddCollectionItem: %v", err)
			}
		}
		return collection.ID
	}
	sharedID := createWithItems("Shared", true)
	privateID := createWithItems("Private", false)

	f := NewFetcher(pool)
	f.StoreProvider = provider
	// The owner can open only the first library; the viewer can open both.
	f.CollectionOwners = stubCollectionOwners{owner: {AllowedLibraryIDs: []int{libraries[0]}}}
	viewerFilter := catalog.AccessFilter{UserID: userID, ProfileID: viewer, AllowedLibraryIDs: libraries}
	row := ResolvedSection{ID: "home-row", SectionType: SectionCollection, ItemLimit: 20}

	tests := []struct {
		name         string
		collectionID string
		profileID    string
		filter       catalog.AccessFilter
		want         []string
	}{
		{
			name:         "owner reads own collection with own access",
			collectionID: privateID,
			profileID:    owner,
			filter:       catalog.AccessFilter{UserID: userID, ProfileID: owner, AllowedLibraryIDs: libraries},
			want:         []string{ownerOnly, shared},
		},
		{
			name:         "shared collection limited to owner and viewer access",
			collectionID: sharedID,
			profileID:    viewer,
			filter:       viewerFilter,
			want:         []string{ownerOnly},
		},
		{
			name:         "another profile's private collection is empty",
			collectionID: privateID,
			profileID:    viewer,
			filter:       viewerFilter,
			want:         []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, total, err := f.fetchUserCollection(ctx, row, nil, nil, userID, tt.profileID, tt.filter, tt.collectionID)
			if err != nil {
				t.Fatalf("fetchUserCollection: %v", err)
			}
			if got := sectionMediaItemIDs(items); !reflect.DeepEqual(got, tt.want) || total != len(tt.want) {
				t.Fatalf("items = %v (total %d), want %v", got, total, tt.want)
			}
		})
	}
}
