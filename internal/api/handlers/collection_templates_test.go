package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
)

// TestV1TemplateListKeepsEveryTemplate pins /api/v1 to the full built-in
// catalog. The /api/v2 admin list leaves out templates no import route accepts;
// the frozen v1 body still carries TMDB Discover and franchise templates.
func TestV1TemplateListKeepsEveryTemplate(t *testing.T) {
	full := templates.CatalogDefault()
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(full); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(want.Bytes(), []byte(`"source":"tmdb_discover"`)) || !bytes.Contains(want.Bytes(), []byte(`"source":"tmdb_collection"`)) {
		t.Fatal("built-in catalog must contain Discover and franchise templates")
	}

	rec := httptest.NewRecorder()
	NewCollectionTemplateHandler(nil).HandleListTemplates(rec, httptest.NewRequest(http.MethodGet, "/admin/collections/templates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), want.Bytes()) {
		t.Fatalf("v1 template list changed:\n got %.300s\nwant %.300s", rec.Body.String(), want.String())
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
