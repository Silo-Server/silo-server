package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCollectionSourceOrderSurvivesPreferenceChangeDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("sort-sentinel-%d", time.Now().UnixNano())
	var library, userID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	profile := prefix + "-profile"
	exec(`INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'one')`, profile, userID)
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
	}()
	ids := []string{prefix + "-a", prefix + "-b", prefix + "-c", prefix + "-d"}
	for i, id := range ids {
		exec(`INSERT INTO media_items(content_id,type,title,status,genres) VALUES($1,'movie',$2,'released','{}')`, id, fmt.Sprintf("Title %d", 4-i))
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library)
	}
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: profile, Name: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	libraryRepo := NewLibraryCollectionRepository(pool)
	shared, err := libraryRepo.Create(ctx, CreateLibraryCollectionInput{LibraryID: library, Slug: prefix, Title: "Shared"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = libraryRepo.Delete(context.Background(), shared.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, shared.ID)
	}()
	smart, err := libraryRepo.Create(ctx, CreateLibraryCollectionInput{LibraryID: library, Slug: prefix + "-smart", Title: "Smart", CollectionType: "smart", QueryDefinition: []byte(`{"sort":{"field":"title","order":"desc"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = libraryRepo.Delete(context.Background(), smart.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM library_collection_revisions WHERE collection_id=$1`, smart.ID)
	}()
	var members []LibraryCollectionItemInput
	for i, id := range ids {
		if err := store.AddCollectionItem(ctx, personal.ID, id, i); err != nil {
			t.Fatal(err)
		}
		members = append(members, LibraryCollectionItemInput{MediaItemID: id})
	}
	if err := libraryRepo.ReplaceItems(ctx, shared.ID, members); err != nil {
		t.Fatal(err)
	}
	resolver := NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
	access := AccessFilter{UserID: userID, ProfileID: profile, AllowedLibraryIDs: []int{library}}
	for _, tc := range []struct {
		source   CatalogSource
		kind, id string
	}{{CatalogSourceLibraryCollection, userstore.CollectionKindLibrary, shared.ID}, {CatalogSourceUserCollection, userstore.CollectionKindUser, personal.ID}, {CatalogSourceLibraryCollection, userstore.CollectionKindLibrary, smart.ID}} {
		t.Run(tc.kind, func(t *testing.T) {
			req := CatalogRequest{Source: tc.source, CollectionID: tc.id, CursorPaging: true, UseSourceOrder: true, Limit: 2}
			first, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if !first.EffectiveSortResolved || first.EffectiveSort != (QuerySort{}) || first.Next == nil || first.CursorScope == nil || first.Items[0].ContentID != ids[0] {
				t.Fatalf("first source order: %+v", first)
			}
			if err := store.SetCollectionSortPreference(ctx, userstore.CollectionSortPreference{ProfileID: profile, CollectionKind: tc.kind, CollectionID: tc.id, SortField: "title", SortOrder: querySortAsc}); err != nil {
				t.Fatal(err)
			}
			req.After = first.Next
			req.ResolvedSort = new(first.EffectiveSort)
			next, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Items) != 2 || next.Items[0].ContentID != ids[2] || next.Items[1].ContentID != ids[3] || next.HasMore || next.CursorScope.Collection.Revision != first.CursorScope.Collection.Revision {
				t.Fatalf("preference changed continuation: %+v", next)
			}
			sectionID := prefix + "-section-" + tc.id
			configField := "library_collection_id"
			if tc.source == CatalogSourceUserCollection {
				configField = "user_collection_id"
			}
			exec(`INSERT INTO page_sections(id,scope,library_id,section_type,title,config) VALUES($1,'library',$2,'collection','Collection',jsonb_build_object($3::text,$4::text))`, sectionID, library, configField, tc.id)
			defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM page_sections WHERE id=$1`, sectionID) }()
			delegated := req
			delegated.Source = CatalogSourceSection
			delegated.Scope = "library"
			delegated.LibraryID = library
			delegated.SectionID = sectionID
			sectionPage, err := resolver.Resolve(ctx, delegated, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(sectionPage.Items) != 2 || sectionPage.Items[0].ContentID != ids[2] || sectionPage.Items[1].ContentID != ids[3] {
				t.Fatalf("section dropped frozen source order: %+v", sectionPage)
			}
			req.After = first.CursorScope
			req.Seek = new(2)
			jumped, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if len(jumped.Items) != 2 || jumped.Items[0].ContentID != ids[2] || jumped.Items[1].ContentID != ids[3] {
				t.Fatalf("preference changed window seek: %+v", jumped)
			}
			req.After = nil
			req.Seek = nil
			req.ResolvedSort = nil
			fresh, err := resolver.Resolve(ctx, req, access)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Items[0].ContentID != ids[3] || fresh.EffectiveSort.Field != "title" {
				t.Fatalf("fresh request did not read new preference: %+v", fresh)
			}
		})
	}
	for _, source := range []CatalogSource{CatalogSourceFavorites, CatalogSourceWatchlist} {
		page, err := resolver.Resolve(ctx, CatalogRequest{Source: source, CursorPaging: true, UseSourceOrder: true, Limit: 2}, access)
		if err != nil {
			t.Fatal(err)
		}
		if !page.EffectiveSortResolved || page.EffectiveSort != (QuerySort{}) {
			t.Fatalf("personal list source-order resolution unmarked: %+v", page)
		}
	}
}
