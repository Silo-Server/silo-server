package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestCatalogProfileSectionsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	owner, other := f.ids[0]+"-owner", f.ids[0]+"-other"
	for _, profile := range []string{owner, other} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile}); err != nil {
			t.Fatal(err)
		}
	}
	collection, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{
		CreatorProfileID: owner, Name: "Personal smart collection", CollectionType: "smart",
		QueryDefinition: fmt.Sprintf(`{"library_ids":[%d],"media_scope":"movie","sort":{"field":"title","order":"asc"},"limit":100}`, f.library),
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, nil)
	h.StoreProvider = provider
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(f.pool), catalog.NewItemRepository(f.pool)).WithUserStoreProvider(provider).WithSectionResolver(h)
	viewer := catalog.AccessFilter{UserID: f.account, ProfileID: owner, AllowedLibraryIDs: []int{f.library}}
	viewerContext := func(profile string) context.Context {
		c := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
		c = apimw.SetProfileID(c, profile)
		return access.SetScope(c, access.Scope{UserID: f.account, ProfileID: profile, AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})
	}
	for _, scope := range []string{"home", "library"} {
		t.Run(scope, func(t *testing.T) {
			libraryKey := ""
			var libraryID *int
			if scope == "library" {
				libraryKey, libraryID = strconv.Itoa(f.library), &f.library
			}
			rowID := f.ids[0] + "-" + scope
			overrides := []userstore.SectionOverride{{
				ID: rowID, IsUserAdded: true, UserSectionType: "collection", UserTitle: "My picks",
				UserConfig: fmt.Sprintf(`{"user_collection_id":%q}`, collection.ID),
			}}
			save := func() {
				t.Helper()
				if err := store.SaveSectionOverrides(ctx, owner, scope, libraryKey, overrides); err != nil {
					t.Fatal(err)
				}
			}
			save()
			req := catalog.CatalogRequest{Source: catalog.CatalogSourceSection, Scope: scope, LibraryID: f.library, SectionID: rowID, CursorPaging: true, Limit: 2}
			var got []string
			for range 3 {
				page, err := resolver.Resolve(viewerContext(owner), req, viewer)
				if err != nil {
					t.Fatalf("browse personal %s row: %v", scope, err)
				}
				for _, item := range page.Items {
					got = append(got, item.ContentID)
				}
				if !page.HasMore {
					break
				}
				req.After = page.Next
			}
			if !reflect.DeepEqual(got, f.ids[1:]) {
				t.Fatalf("paged items = %v, want %v", got, f.ids[1:])
			}
			req.After = nil
			assertMissing := func(c context.Context, a catalog.AccessFilter) {
				t.Helper()
				if _, err := resolver.Resolve(c, req, a); !errors.Is(err, catalog.ErrCatalogSourceNotFound) {
					t.Fatalf("browse invisible row = %v, want source not found", err)
				}
			}
			otherViewer := viewer
			otherViewer.ProfileID = other
			assertMissing(viewerContext(other), otherViewer)
			if scope == "library" {
				req.LibraryID = f.hidden
				assertMissing(viewerContext(owner), viewer)
				req.LibraryID = f.library
				f.exec(t, `UPDATE media_folders SET enabled=false WHERE id=$1`, f.library)
				assertMissing(viewerContext(owner), viewer)
				f.exec(t, `UPDATE media_folders SET enabled=true WHERE id=$1`, f.library)
			}
			for _, removed := range []bool{false, true} {
				overrides[0].Hidden, overrides[0].Removed = !removed, removed
				save()
				assertMissing(viewerContext(owner), viewer)
			}
			overrides[0].Hidden, overrides[0].Removed = false, false
			save()

			// A profile's config override must win over the stored row, and a
			// hidden stored row must not return through a raw-table fallback.
			admin, err := repo.Create(ctx, &sections.PageSection{Scope: scope, LibraryID: libraryID, SectionType: sections.SectionCollection,
				Title: "Server picks", ItemLimit: 20, Enabled: true, Config: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repo.Delete(context.Background(), admin.ID) })
			overrides = append(overrides, userstore.SectionOverride{ID: rowID + "-override", SectionID: admin.ID,
				Config: fmt.Sprintf(`{"user_collection_id":%q}`, collection.ID)})
			save()
			req.SectionID = admin.ID
			page, err := resolver.Resolve(viewerContext(owner), req, viewer)
			if err != nil || len(page.Items) != 2 || page.Items[0].ContentID != f.ids[1] {
				t.Fatalf("browse customized row = %+v, %v", page, err)
			}

			lookupErr := errors.New("profile settings unavailable")
			for _, failProvider := range []bool{false, true} {
				h.StoreProvider = &failingSectionProvider{UserStoreProvider: provider, err: lookupErr, failProvider: failProvider}
				if _, err := resolver.Resolve(viewerContext(owner), req, viewer); !errors.Is(err, lookupErr) {
					t.Fatalf("failed profile lookup = %v, want %v", err, lookupErr)
				}
				var layoutErr error
				if scope == "home" {
					_, layoutErr = h.HomeLayout(viewerContext(owner))
				} else {
					_, layoutErr = h.LibraryLayout(viewerContext(owner), f.library)
				}
				if apiErr, ok := errors.AsType[*APIError](layoutErr); !ok || apiErr.Status != http.StatusInternalServerError {
					t.Fatalf("layout after failed profile lookup = %v, want internal error", layoutErr)
				}
			}
			h.StoreProvider = provider
			for _, removed := range []bool{false, true} {
				overrides[1].Hidden, overrides[1].Removed = !removed, removed
				save()
				assertMissing(viewerContext(owner), viewer)
			}
		})
	}
}

type failingSectionProvider struct {
	userstore.UserStoreProvider
	err          error
	failProvider bool
}

func (p *failingSectionProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	if p.failProvider {
		return nil, p.err
	}
	store, err := p.UserStoreProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &failingSectionStore{UserStore: store, err: p.err}, nil
}

type failingSectionStore struct {
	userstore.UserStore
	err error
}

func (s *failingSectionStore) ListSectionOverrides(context.Context, string, string, string) ([]userstore.SectionOverride, error) {
	return nil, s.err
}
