package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

func (f *fakeAdminCollections) AdminCollectionTemplateCatalog(context.Context) templates.Catalog {
	f.catalogReads++
	if f.catalog != nil {
		return *f.catalog
	}
	return templates.CatalogDefault()
}

const adminTemplatesPath = "/api/v2/admin/collections/templates"

func getAdminTemplateCatalog(t *testing.T, f *fakeAdminCollections) templates.Catalog {
	t.Helper()
	rec := do(t, adminCollectionsTestHandler(t, f), http.MethodGet, adminTemplatesPath, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body templates.Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// TestAdminTemplateCatalogOffersOnlyCreatableTemplates checks that the admin
// template list holds only mdblist, tmdb and tmdb_list templates. TMDB
// Discover and franchise templates reach admins through Starter packs, so a
// single card for one of them could never be created.
func TestAdminTemplateCatalogOffersOnlyCreatableTemplates(t *testing.T) {
	full := templates.CatalogDefault()
	want := map[string]int{}
	for _, group := range full.Categories {
		for _, tmpl := range group.Templates {
			switch tmpl.Source {
			case templates.SourceMDBList, templates.SourceTMDB, templates.SourceTMDBList:
				want[tmpl.ID]++
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("built-in catalog has no creatable templates")
	}

	got := getAdminTemplateCatalog(t, newFakeAdminCollections())
	kept := map[string]int{}
	for _, group := range got.Categories {
		if len(group.Templates) == 0 {
			t.Errorf("category %q listed with no templates", group.Category)
		}
		for _, tmpl := range group.Templates {
			kept[tmpl.ID]++
			if tmpl.Source == templates.SourceTMDBDiscover || tmpl.Source == templates.SourceTMDBCollection {
				t.Errorf("template %s has source %s, which only a Starter pack can apply", tmpl.ID, tmpl.Source)
			}
		}
	}
	if len(kept) != len(want) {
		t.Errorf("kept %d templates, want %d", len(kept), len(want))
	}
	for id := range want {
		if kept[id] != 1 {
			t.Errorf("creatable template %s listed %d times, want once", id, kept[id])
		}
	}
}

// TestAdminTemplateCatalogDropsEmptiedCategories checks that a category whose
// templates are all bundle-only is left out rather than listed empty, and that
// the remaining categories keep their order.
func TestAdminTemplateCatalogDropsEmptiedCategories(t *testing.T) {
	discover := templates.Template{ID: "discover_only", Title: "Discover only", Category: templates.CategoryTopRated, Source: templates.SourceTMDBDiscover, MediaKind: templates.MediaMovie, TMDBDiscover: &templates.TMDBDiscoverSpec{MediaType: "movie", SortBy: "vote_average.desc"}}
	franchise := templates.Template{ID: "franchise", Title: "Franchise", Category: templates.CategoryEditorial, Source: templates.SourceTMDBCollection, MediaKind: templates.MediaMovie, TMDBCollection: &templates.TMDBCollectionSpec{CollectionID: 10}}
	chart := templates.Template{ID: "chart", Title: "Chart", Category: templates.CategoryTrending, Source: templates.SourceTMDB, MediaKind: templates.MediaMovie, TMDB: &templates.TMDBSpec{Preset: "trending", MediaType: "movie", TimeWindow: "week"}}
	list := templates.Template{ID: "list", Title: "List", Category: templates.CategoryCustom, Source: templates.SourceTMDBList, MediaKind: templates.MediaMixed, TMDBList: &templates.TMDBListSpec{}}
	f := newFakeAdminCollections()
	f.catalog = &templates.Catalog{Categories: []templates.CategoryGroup{
		{Category: templates.CategoryTrending, Label: "Trending", Templates: []templates.Template{chart}},
		{Category: templates.CategoryTopRated, Label: "Top Rated", Templates: []templates.Template{discover}},
		{Category: templates.CategoryEditorial, Label: "Editorial", Templates: []templates.Template{franchise}},
		{Category: templates.CategoryCustom, Label: "Custom", Templates: []templates.Template{list}},
	}}

	got := getAdminTemplateCatalog(t, f)
	if len(got.Categories) != 2 {
		t.Fatalf("categories %+v, want trending then custom", got.Categories)
	}
	for i, want := range []string{"chart", "list"} {
		group := got.Categories[i]
		if len(group.Templates) != 1 || group.Templates[0].ID != want {
			t.Errorf("category %d = %s %+v, want only %s", i, group.Category, group.Templates, want)
		}
	}
}

// TestBuiltinBundleOnlyTemplatesCarryTheirSource pins what the admin catalog
// filter relies on: every built-in TMDB Discover or franchise template is
// labeled with that source, so filtering by source catches all of them.
func TestBuiltinBundleOnlyTemplatesCarryTheirSource(t *testing.T) {
	for _, tmpl := range templates.List() {
		if tmpl.TMDBDiscover != nil && tmpl.Source != templates.SourceTMDBDiscover {
			t.Errorf("%s has a tmdb_discover spec but source %s", tmpl.ID, tmpl.Source)
		}
		if tmpl.TMDBCollection != nil && tmpl.Source != templates.SourceTMDBCollection {
			t.Errorf("%s has a tmdb_collection spec but source %s", tmpl.ID, tmpl.Source)
		}
	}
}

// TestAdminTemplateCatalogRequiresActingAdmin restates #193 S5 for the
// template list: a regular account and a non-primary profile on an admin
// account are refused before the catalog is read.
func TestAdminTemplateCatalogRequiresActingAdmin(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, adminTemplatesPath, "", headers), TypePermissionDenied)
	}
	if f.catalogReads != 0 {
		t.Fatal("refused request read the template catalog")
	}
}
