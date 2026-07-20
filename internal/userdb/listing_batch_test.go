package userdb

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newListingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func TestListProfilesAttachesAllowedLibrariesInOrder(t *testing.T) {
	db := newListingTestDB(t)
	for _, profile := range []Profile{
		{ID: "p1", Name: "One", AllowedLibraryIDs: []int{5, 2}, CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "p2", Name: "Two", AllowedLibraryIDs: []int{3}, CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "p3", Name: "Three", CreatedAt: "2026-01-03T00:00:00Z"},
	} {
		if err := CreateProfile(db, profile); err != nil {
			t.Fatalf("CreateProfile(%s): %v", profile.ID, err)
		}
	}

	profiles, err := ListProfiles(db)
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(profiles) != 3 {
		t.Fatalf("ListProfiles returned %d profiles, want 3", len(profiles))
	}
	if got, want := profiles[0].AllowedLibraryIDs, []int{2, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("p1 AllowedLibraryIDs = %v, want %v", got, want)
	}
	if got, want := profiles[1].AllowedLibraryIDs, []int{3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("p2 AllowedLibraryIDs = %v, want %v", got, want)
	}
	if profiles[2].AllowedLibraryIDs != nil {
		t.Fatalf("p3 AllowedLibraryIDs = %v, want nil", profiles[2].AllowedLibraryIDs)
	}
}

func TestListCollectionsAttachesAllowedProfilesInOrder(t *testing.T) {
	db := newListingTestDB(t)
	collection, err := CreateCollection(db, userstore.CreateCollectionInput{
		CreatorProfileID:  "p1",
		Name:              "Shared",
		IsShared:          true,
		AllowedProfileIDs: []string{"p3", "p2"},
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	collections, err := ListCollections(db, "p2")
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(collections) != 1 || collections[0].ID != collection.ID {
		t.Fatalf("ListCollections = %+v, want collection %s", collections, collection.ID)
	}
	if got, want := collections[0].AllowedProfileIDs, []string{"p1", "p2", "p3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AllowedProfileIDs = %v, want %v", got, want)
	}
}

func TestBatchAttachDoesNotScaleBindParametersWithRows(t *testing.T) {
	db := newListingTestDB(t)
	const aboveSQLiteVariableLimit = 32767

	profiles := make([]Profile, aboveSQLiteVariableLimit)
	if err := attachAllowedLibraries(db, profiles); err != nil {
		t.Fatalf("attachAllowedLibraries: %v", err)
	}

	collections := make([]Collection, aboveSQLiteVariableLimit)
	if err := attachCollectionProfiles(db, "viewer", collections); err != nil {
		t.Fatalf("attachCollectionProfiles: %v", err)
	}
}
