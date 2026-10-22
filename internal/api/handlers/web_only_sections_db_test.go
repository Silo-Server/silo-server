package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

// TestWebOnlyHeroRowsDB pins a web-only hero row end to end on Home and on a
// library page: the admin save keeps web_only on a featured row only, a read
// that does not declare the web client family never gets the row (its
// single-row read is not found), the web does, and the row goes back to
// every client once it stops being the hero.
func TestWebOnlyHeroRowsDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	profile := f.ids[0] + "-web-only"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: profile}); err != nil {
		t.Fatal(err)
	}
	repo := sections.NewRepository(f.pool)
	h := NewSectionHandler(repo, nil)
	h.StoreProvider = provider
	h.FolderRepo = catalog.NewFolderRepository(f.pool)
	viewerCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: f.account})
	viewerCtx = apimw.SetProfileID(viewerCtx, profile)
	viewerCtx = access.SetScope(viewerCtx, access.Scope{UserID: f.account, ProfileID: profile, AllowedLibraryIDs: []int{f.library}, LibrariesRestricted: true})
	app, web := SectionViewer{}, SectionViewer{Web: true}

	for _, scope := range []string{"home", "library"} {
		t.Run(scope, func(t *testing.T) {
			var libraryID *int
			libraryKey := ""
			if scope == "library" {
				libraryID, libraryKey = &f.library, strconv.Itoa(f.library)
			}
			create := func(title string, featured bool) AdminSection {
				t.Helper()
				created, err := h.CreateAdminSection(ctx, AdminSectionCreate{Scope: scope, LibraryID: libraryID, SectionType: string(sections.SectionRecentlyAdded),
					Title: title, Featured: featured, WebOnly: true, ItemLimit: 10, Config: json.RawMessage(`{}`), Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = repo.Delete(context.Background(), created.ID) })
				return created
			}
			hero := create("Web hero", true)
			plain := create("Plain row", false)
			if !hero.WebOnly || plain.WebOnly {
				t.Fatalf("saved web_only: hero %v, plain row %v; want only the hero", hero.WebOnly, plain.WebOnly)
			}
			layout := func(v SectionViewer) map[string]bool {
				t.Helper()
				var view SectionLayoutView
				var err error
				if scope == "home" {
					view, err = h.HomeLayout(viewerCtx, v)
				} else {
					view, err = h.LibraryLayout(viewerCtx, f.library, v)
				}
				if err != nil {
					t.Fatal(err)
				}
				ids := map[string]bool{}
				for _, s := range view.Sections {
					ids[s.ID] = true
				}
				return ids
			}
			if got := layout(app); got[hero.ID] || !got[plain.ID] {
				t.Fatalf("app layout: hero %v, plain row %v; want the plain row alone", got[hero.ID], got[plain.ID])
			}
			if got := layout(web); !got[hero.ID] || !got[plain.ID] {
				t.Fatalf("web layout: hero %v, plain row %v; want both", got[hero.ID], got[plain.ID])
			}
			var itemsErr error
			if scope == "home" {
				_, itemsErr = h.HomeSectionItems(viewerCtx, hero.ID, app)
			} else {
				_, itemsErr = h.LibrarySectionItems(viewerCtx, f.library, hero.ID, app)
			}
			if apiErr, ok := errors.AsType[*APIError](itemsErr); !ok || apiErr.Status != http.StatusNotFound {
				t.Fatalf("app reading the web-only row = %v, want not found", itemsErr)
			}

			// A profile that turned the hero banner off gets a regular row,
			// which every client shows.
			saveOverrides := func(overrides []userstore.SectionOverride) {
				t.Helper()
				if err := store.SaveSectionOverrides(ctx, profile, scope, libraryKey, overrides); err != nil {
					t.Fatal(err)
				}
			}
			saveOverrides([]userstore.SectionOverride{{ID: hero.ID + "-off", SectionID: hero.ID, Featured: new(false)}})
			if !layout(app)[hero.ID] {
				t.Fatal("a profile's regular row stayed off the apps")
			}
			saveOverrides(nil)

			// Turning the hero off clears web_only for good: turning it back on
			// sends the row to every client until web_only is set again.
			for _, step := range []struct {
				update      AdminSectionUpdate
				wantWebOnly bool
			}{
				{AdminSectionUpdate{Featured: new(false)}, false},
				{AdminSectionUpdate{Featured: new(true)}, false},
				{AdminSectionUpdate{WebOnly: new(true)}, true},
			} {
				updated, err := h.UpdateAdminSection(ctx, hero.ID, step.update)
				if err != nil {
					t.Fatal(err)
				}
				if updated.WebOnly != step.wantWebOnly || layout(app)[hero.ID] == step.wantWebOnly {
					t.Fatalf("after %+v: web_only %v, in the app layout %v", step.update, updated.WebOnly, layout(app)[hero.ID])
				}
			}

			// A featured collection taking the hero spot clears web_only too.
			// Home is shared with other tests, so only the library page runs it.
			if scope == "library" {
				if err := repo.ClearFeaturedForSurface(ctx, scope, libraryID, ""); err != nil {
					t.Fatal(err)
				}
				stored, err := repo.GetByID(ctx, hero.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Featured || stored.WebOnly || !layout(app)[hero.ID] {
					t.Fatalf("after another hero took the page: featured %v, web_only %v", stored.Featured, stored.WebOnly)
				}
			}
		})
	}
}
