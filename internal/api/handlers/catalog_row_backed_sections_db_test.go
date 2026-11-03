package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// A row the catalog cannot express as a query (here New to Library) browses
// past its item limit, in row order, on both Home and its library page.
func TestCatalogRowBackedSectionBrowseDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	// Give the fixture's items distinct, ordered creation times: newest last.
	for i, id := range f.ids {
		f.exec(t, `UPDATE media_items SET created_at = NOW() - make_interval(mins => $2) WHERE content_id = $1`, id, len(f.ids)-i)
	}
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, sections.NewFetcher(f.pool))
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	resolver := catalog.NewCatalogResolver(catalog.NewBrowseRepository(f.pool), catalog.NewItemRepository(f.pool)).WithSectionResolver(h)
	viewer := catalog.AccessFilter{UserID: f.account, AllowedLibraryIDs: []int{f.library}}
	c := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
	c = access.SetScope(c, access.Scope{UserID: f.account, AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})

	// The visible library holds ids[1:]; newest first.
	want := []string{f.ids[4], f.ids[3], f.ids[2], f.ids[1]}
	for _, scope := range []string{"home", "library"} {
		t.Run(scope, func(t *testing.T) {
			var libraryID *int
			if scope == "library" {
				libraryID = &f.library
			}
			row, err := repo.Create(ctx, &sections.PageSection{Scope: scope, LibraryID: libraryID, SectionType: sections.SectionNewToLibrary,
				Title: "New to library", ItemLimit: 2, Enabled: true, Config: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = repo.Delete(context.Background(), row.ID) })

			req := catalog.CatalogRequest{Source: catalog.CatalogSourceSection, Scope: scope, LibraryID: f.library,
				SectionID: row.ID, CursorPaging: true, Limit: 3}
			var got []string
			for range 3 {
				page, err := resolver.Resolve(c, req, viewer)
				if err != nil {
					t.Fatalf("browse %s row: %v", scope, err)
				}
				if page.Total != len(want) || !page.TotalExact {
					t.Fatalf("total = %d exact=%v, want %d exact", page.Total, page.TotalExact, len(want))
				}
				for _, item := range page.Items {
					got = append(got, item.ContentID)
				}
				if !page.HasMore {
					break
				}
				req.Offset += len(page.Items)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("paged items = %v, want %v", got, want)
			}

			req.Offset, req.SectionID = 0, row.ID+"-missing"
			if _, err := resolver.Resolve(c, req, viewer); !errors.Is(err, catalog.ErrCatalogSourceNotFound) {
				t.Fatalf("browse unknown row = %v, want source not found", err)
			}
		})
	}
}

func TestMergeSectionBrowseItemsLeadsWithTheRow(t *testing.T) {
	items := func(ids ...string) []*models.MediaItem {
		out := make([]*models.MediaItem, len(ids))
		for i, id := range ids {
			out[i] = &models.MediaItem{ContentID: id}
		}
		return out
	}
	ids := func(list []*models.MediaItem) []string {
		out := make([]string, len(list))
		for i, item := range list {
			out[i] = item.ContentID
		}
		return out
	}
	// A daily pick at the row's limit is not a prefix of the wider fetch.
	shown := items("c", "a")
	more := append(items("a", "b", "d", "c", "e"), nil)
	if got, want := ids(mergeSectionBrowseItems(shown, more, 10)), []string{"c", "a", "b", "d", "e"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %v, want %v", got, want)
	}
	if got, want := ids(mergeSectionBrowseItems(shown, more, 3)), []string{"c", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("capped merge = %v, want %v", got, want)
	}
}
