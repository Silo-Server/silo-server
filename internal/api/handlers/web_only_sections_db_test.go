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

// webOnlyRowsPage is one page (Home or a library page) of the web-only hero
// row test, read as one profile.
type webOnlyRowsPage struct {
	h          *SectionHandler
	repo       *sections.Repository
	store      userstore.UserStore
	account    int
	profile    string
	scope      string
	library    int
	libraryID  *int
	libraryKey string
}

// viewer is the profile's request context, which may see the test library.
func (p webOnlyRowsPage) viewer(t *testing.T) context.Context {
	ctx := apimw.SetClaims(t.Context(), &auth.Claims{UserID: p.account})
	ctx = apimw.SetProfileID(ctx, p.profile)
	return access.SetScope(ctx, access.Scope{UserID: p.account, ProfileID: p.profile, AllowedLibraryIDs: []int{p.library}, LibrariesRestricted: true})
}

func (p webOnlyRowsPage) create(t *testing.T, title string, featured bool) AdminSection {
	t.Helper()
	created, err := p.h.CreateAdminSection(t.Context(), AdminSectionCreate{Scope: p.scope, LibraryID: p.libraryID, SectionType: string(sections.SectionRecentlyAdded),
		Title: title, Featured: featured, WebOnly: true, ItemLimit: 10, Config: json.RawMessage(`{}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.repo.Delete(context.Background(), created.ID) })
	return created
}

// layout answers the IDs of the page's layout for a viewer.
func (p webOnlyRowsPage) layout(t *testing.T, v SectionViewer) map[string]bool {
	t.Helper()
	var view SectionLayoutView
	var err error
	if p.scope == "home" {
		view, err = p.h.HomeLayout(p.viewer(t), v)
	} else {
		view, err = p.h.LibraryLayout(p.viewer(t), p.library, v)
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

func (p webOnlyRowsPage) items(t *testing.T, id string, v SectionViewer) error {
	if p.scope == "home" {
		_, err := p.h.HomeSectionItems(p.viewer(t), id, v)
		return err
	}
	_, err := p.h.LibrarySectionItems(p.viewer(t), p.library, id, v)
	return err
}

func (p webOnlyRowsPage) saveOverrides(t *testing.T, overrides []userstore.SectionOverride) {
	t.Helper()
	if err := p.store.SaveSectionOverrides(t.Context(), p.profile, p.scope, p.libraryKey, overrides); err != nil {
		t.Fatal(err)
	}
}

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
	home := webOnlyRowsPage{h: h, repo: repo, store: store, account: f.account, profile: profile, scope: "home", library: f.library}
	library := home
	library.scope, library.libraryID, library.libraryKey = "library", &f.library, strconv.Itoa(f.library)
	for _, page := range []webOnlyRowsPage{home, library} {
		t.Run(page.scope, func(t *testing.T) { testWebOnlyHeroRows(t, page) })
	}
}

func testWebOnlyHeroRows(t *testing.T, page webOnlyRowsPage) {
	app, web := SectionViewer{}, SectionViewer{Web: true}
	hero := page.create(t, "Web hero", true)
	plain := page.create(t, "Plain row", false)
	if !hero.WebOnly || plain.WebOnly {
		t.Fatalf("saved web_only: hero %v, plain row %v; want only the hero", hero.WebOnly, plain.WebOnly)
	}
	if got := page.layout(t, app); got[hero.ID] || !got[plain.ID] {
		t.Fatalf("app layout: hero %v, plain row %v; want the plain row alone", got[hero.ID], got[plain.ID])
	}
	if got := page.layout(t, web); !got[hero.ID] || !got[plain.ID] {
		t.Fatalf("web layout: hero %v, plain row %v; want both", got[hero.ID], got[plain.ID])
	}
	itemsErr := page.items(t, hero.ID, app)
	if apiErr, ok := errors.AsType[*APIError](itemsErr); !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("app reading the web-only row = %v, want not found", itemsErr)
	}

	// A profile that turned the hero banner off gets a regular row, which
	// every client shows.
	page.saveOverrides(t, []userstore.SectionOverride{{ID: hero.ID + "-off", SectionID: hero.ID, Featured: new(false)}})
	if !page.layout(t, app)[hero.ID] {
		t.Fatal("a profile's regular row stayed off the apps")
	}
	page.saveOverrides(t, nil)

	// Turning the hero off clears web_only for good: turning it back on sends
	// the row to every client until web_only is set again.
	for _, step := range []struct {
		update      AdminSectionUpdate
		wantWebOnly bool
	}{
		{AdminSectionUpdate{Featured: new(false)}, false},
		{AdminSectionUpdate{Featured: new(true)}, false},
		{AdminSectionUpdate{WebOnly: new(true)}, true},
	} {
		updated, err := page.h.UpdateAdminSection(t.Context(), hero.ID, step.update)
		if err != nil {
			t.Fatal(err)
		}
		if updated.WebOnly != step.wantWebOnly || page.layout(t, app)[hero.ID] == step.wantWebOnly {
			t.Fatalf("after %+v: web_only %v, in the app layout %v", step.update, updated.WebOnly, page.layout(t, app)[hero.ID])
		}
	}

	// A featured collection taking the hero spot clears web_only too. Home
	// is shared with other tests, so only the library page runs it.
	if page.scope != "library" {
		return
	}
	if err := page.repo.ClearFeaturedForSurface(t.Context(), page.scope, page.libraryID, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := page.repo.GetByID(t.Context(), hero.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Featured || stored.WebOnly || !page.layout(t, app)[hero.ID] {
		t.Fatalf("after another hero took the page: featured %v, web_only %v", stored.Featured, stored.WebOnly)
	}
}
