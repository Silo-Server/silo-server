package userstore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type cleanupUserLister struct {
	users []*models.User
}

func (l cleanupUserLister) List(context.Context) ([]*models.User, error) { return l.users, nil }

type cleanupStoreProvider struct {
	stores map[int]userstore.UserStore
}

func (p cleanupStoreProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	return p.stores[userID], nil
}

func (cleanupStoreProvider) Close() error { return nil }

func TestCollectionSortPreferenceCleanerDeletesAcrossUsers(t *testing.T) {
	ctx := context.Background()
	provider := cleanupStoreProvider{stores: make(map[int]userstore.UserStore)}
	for _, userID := range []int{1, 2} {
		db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), userID)
		if err != nil {
			t.Fatalf("open user %d database: %v", userID, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		store := userdb.NewSQLiteUserStore(db.DB)
		profileID := "profile-" + string(rune('0'+userID))
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
			t.Fatalf("create profile: %v", err)
		}
		if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{
			ProfileID: profileID, CollectionKind: userstore.CollectionKindLibrary,
			CollectionID: "library-collection", SortField: "title", SortOrder: "asc",
		}); err != nil {
			t.Fatalf("set preference: %v", err)
		}
		provider.stores[userID] = store
	}

	cleaner := userstore.NewCollectionSortPreferenceCleaner(cleanupUserLister{users: []*models.User{{ID: 1}, {ID: 2}}}, provider)
	cleaner.DeleteForCollection(ctx, userstore.CollectionKindLibrary, "library-collection")

	for userID, store := range provider.stores {
		profileID := "profile-" + string(rune('0'+userID))
		pref, err := store.GetCollectionSortPreference(ctx, profileID, userstore.CollectionKindLibrary, "library-collection")
		if err != nil || pref != nil {
			t.Fatalf("user %d preference = %+v, err = %v; want nil", userID, pref, err)
		}
	}
}
