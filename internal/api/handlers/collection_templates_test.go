package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

func TestCollectionTemplateHandlerReturnsBuiltinCatalog(t *testing.T) {
	h := NewCollectionTemplateHandler(nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/collections/templates", nil)
	rec := httptest.NewRecorder()
	h.HandleListTemplates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body templates.Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Categories) == 0 {
		t.Fatal("expected at least one category in response")
	}
	totalTemplates := 0
	for _, group := range body.Categories {
		totalTemplates += len(group.Templates)
	}
	if totalTemplates < 5 {
		t.Errorf("expected several templates, got %d", totalTemplates)
	}
}

// Bundle apply dedupes collections by slugified title per library
// (applyTemplateBundle's slug-match adoption), so two builtin templates with
// the same title slug can never coexist: whichever applies second is silently
// skipped as already_exists.
func TestBuiltinTemplateTitleSlugsAreUnique(t *testing.T) {
	bySlug := make(map[string]string)
	for _, tmpl := range templates.List() {
		slug := slugifyCollectionName(tmpl.Title)
		if other, exists := bySlug[slug]; exists {
			t.Errorf("templates %q and %q share title slug %q; bundle apply would silently skip one", other, tmpl.ID, slug)
			continue
		}
		bySlug[slug] = tmpl.ID
	}
}

func TestCollectionTemplateHandlerHonoursInjectedRegistry(t *testing.T) {
	registry := templates.NewRegistry()
	registry.Register(templates.Template{
		ID:        "test_only",
		Title:     "Test only",
		Category:  templates.CategoryTrending,
		Source:    templates.SourceTMDB,
		MediaKind: templates.MediaMovie,
		TMDB:      &templates.TMDBSpec{Preset: "popular", MediaType: "movie"},
	})

	h := NewCollectionTemplateHandler(registry)
	rec := httptest.NewRecorder()
	h.HandleListTemplates(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body templates.Catalog
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Categories) != 1 || len(body.Categories[0].Templates) != 1 {
		t.Fatalf("unexpected catalog shape: %+v", body)
	}
	if body.Categories[0].Templates[0].ID != "test_only" {
		t.Errorf("got %q, want test_only", body.Categories[0].Templates[0].ID)
	}
}

func TestLibraryCollectionHandlerListsTemplateBundles(t *testing.T) {
	registry := templates.NewRegistry()
	registry.Register(templates.Template{
		ID:        "test_template",
		Title:     "Test template",
		Category:  templates.CategoryTrending,
		Source:    templates.SourceTMDB,
		MediaKind: templates.MediaMovie,
		TMDB:      &templates.TMDBSpec{Preset: "popular", MediaType: "movie"},
	})
	registry.RegisterBundle(templates.Bundle{
		ID:          "test_bundle",
		Title:       "Test bundle",
		Description: "Test description",
		TemplateIDs: []string{"test_template"},
	})
	h := &LibraryCollectionHandler{TemplateRegistry: registry}

	rec := httptest.NewRecorder()
	h.HandleListTemplateBundles(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body templates.BundleCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Bundles) != 1 || body.Bundles[0].ID != "test_bundle" {
		t.Fatalf("unexpected bundle catalog: %+v", body)
	}
}
