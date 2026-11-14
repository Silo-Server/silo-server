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

// A server collection's row sweep converges on the collection's final title
// when a later rename lands between the sweep reading the title and writing
// the account: the later rename's own sweep has already passed that row,
// which still carried the first old name.
func TestCollectionRowRenamerFollowsALaterRename(t *testing.T) {
	ctx := context.Background()
	db, err := userdb.NewUserDB(filepath.Join(t.TempDir(), "user.db"), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := userdb.NewSQLiteUserStore(db.DB)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "p", Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSectionOverrides(ctx, "p", "home", "", []userstore.SectionOverride{{
		ID: "row", IsUserAdded: true, UserSectionType: "collection", UserTitle: "A",
		UserConfig: `{"library_collection_id":"c1"}`,
	}}); err != nil {
		t.Fatal(err)
	}

	// The A→B sweep reads "B"; the B→C rename commits right after.
	title := "B"
	currentTitle := func(context.Context) (string, error) {
		read := title
		title = "C"
		return read, nil
	}
	renamer := userstore.NewCollectionRowRenamer(cleanupUserLister{users: []*models.User{{ID: 1}}}, cleanupStoreProvider{stores: map[int]userstore.UserStore{1: store}})
	renamer.RenameForCollection(ctx, "c1", "A", currentTitle)

	overrides, err := store.ListSectionOverrides(ctx, "p", "home", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(overrides) != 1 || overrides[0].UserTitle != "C" {
		t.Fatalf("rows = %+v, want one row titled C", overrides)
	}
}
