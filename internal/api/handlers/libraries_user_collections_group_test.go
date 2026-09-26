package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestCreateLibrarySeedsUserCollectionsGroupAndSurfacesPersonalCollectionDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := time.Now().UnixNano()
	libraryHandler := NewLibraryHandler(catalog.NewFolderRepository(pool), nil, nil, pool, nil)
	libraryHandler.ScanQueue = &scanControlQueueFixture{}
	created, err := libraryHandler.CreateLibrary(t.Context(), LibraryCreateRequest{
		Paths: []string{fmt.Sprintf("/test/user-collections-%d", suffix)},
		Type:  "movies",
		Name:  fmt.Sprintf("User collections %d", suffix),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, created.ID)
	})

	groups := catalog.NewLibraryCollectionGroupRepository(pool)
	seeded, err := groups.ListByLibrary(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 1 {
		t.Fatalf("collection groups = %d, want exactly 1", len(seeded))
	}
	wantID := catalog.CanonicalUserCollectionsGroupID(created.ID)
	group := seeded[0]
	if group.ID != wantID ||
		group.Name != catalog.CanonicalUserCollectionsGroupTitle ||
		group.Slug != catalog.CanonicalUserCollectionsGroupLabel ||
		group.Kind != models.GroupKindUserCollections ||
		group.DefaultSortMode != models.GroupSortManual ||
		group.SortOrder != catalog.CanonicalUserCollectionsGroupSortOrder {
		t.Fatalf("canonical user collections group = %+v", group)
	}
	var label, title string
	if err := pool.QueryRow(t.Context(), `
		SELECT label, title
		FROM library_collection_groups
		WHERE id = $1`, wantID).Scan(&label, &title); err != nil {
		t.Fatal(err)
	}
	if label != catalog.CanonicalUserCollectionsGroupLabel || title != catalog.CanonicalUserCollectionsGroupTitle {
		t.Fatalf("legacy group identity = (%q, %q)", label, title)
	}

	var userID int
	username := fmt.Sprintf("user-collections-%d", suffix)
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, username).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("user-collections-profile-%d", suffix)
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'Owner')`, profileID, userID); err != nil {
		t.Fatal(err)
	}
	store, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreateCollection(t.Context(), userstore.CreateCollectionInput{
		CreatorProfileID:           profileID,
		Name:                       "Visible personal collection",
		CollectionType:             "manual",
		QueryDefinition:            fmt.Sprintf(`{"library_ids":[%d]}`, created.ID),
		IncludeInServerCollections: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	collections := NewLibraryCollectionHandler(catalog.NewLibraryCollectionRepository(pool), nil, catalog.NewItemRepository(pool), nil)
	collections.FolderRepo = catalog.NewFolderRepository(pool)
	collections.GroupRepo = groups
	collections.UserCollectionPool = pool
	tab, err := collections.LibraryCollectionsTab(t.Context(), created.ID, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Groups) != 1 || tab.Groups[0].ID != wantID || tab.Groups[0].Kind != models.GroupKindUserCollections {
		t.Fatalf("user collections groups = %+v", tab.Groups)
	}
	if len(tab.Groups[0].Collections) != 1 || tab.Groups[0].Collections[0].ID != personal.ID {
		t.Fatalf("personal collections = %+v", tab.Groups[0].Collections)
	}
}
