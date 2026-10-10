package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collections/templates"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func (f *fakeAdminCollections) ListAdminCollectionTemplates(context.Context) ([]templates.BundleWithTemplates, error) {
	f.bundleReads++
	if f.bundles != nil {
		return f.bundles, nil
	}
	return templates.Default.BundlesWithTemplates(), nil
}

const templateBundlesPath = "/api/v2/admin/collections/template-bundles"

// TestAdminTemplateBundlesListTheirTemplates checks that every built-in bundle
// lists its templates in template_ids order, Discover and Franchise templates
// included, and that only the franchise placeholder needs setup.
func TestAdminTemplateBundlesListTheirTemplates(t *testing.T) {
	h := adminCollectionsTestHandler(t, newFakeAdminCollections())
	rec := do(t, h, http.MethodGet, templateBundlesPath, "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		Bundles []struct {
			ID          string   `json:"id"`
			TemplateIDs []string `json:"template_ids"`
			Templates   []struct {
				ID         string  `json:"id"`
				Title      string  `json:"title"`
				Source     string  `json:"source"`
				MediaKind  string  `json:"media_kind"`
				Featured   *bool   `json:"featured"`
				PosterPath *string `json:"poster_path"`
				NeedsSetup *bool   `json:"needs_setup"`
			} `json:"templates"`
		} `json:"bundles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Bundles) != len(templates.ListBundles()) {
		t.Fatalf("got %d bundles, want %d", len(body.Bundles), len(templates.ListBundles()))
	}
	sources := map[string]bool{}
	for _, b := range body.Bundles {
		ids := make([]string, 0, len(b.Templates))
		for _, s := range b.Templates {
			ids = append(ids, s.ID)
			sources[s.Source] = true
			tmpl, ok := templates.Get(s.ID)
			if !ok {
				t.Fatalf("%s: unknown template %q", b.ID, s.ID)
			}
			if s.Title != tmpl.Title || s.Source != string(tmpl.Source) || s.MediaKind != string(tmpl.MediaKind) {
				t.Errorf("%s/%s: summary %+v does not match the template", b.ID, s.ID, s)
			}
			if s.Featured == nil || *s.Featured != tmpl.Featured {
				t.Errorf("%s/%s: featured %v, want %v", b.ID, s.ID, s.Featured, tmpl.Featured)
			}
			gotPoster := ""
			if s.PosterPath != nil {
				gotPoster = *s.PosterPath
			}
			if gotPoster != tmpl.PosterPath {
				t.Errorf("%s/%s: poster_path %q, want %q", b.ID, s.ID, gotPoster, tmpl.PosterPath)
			}
			wantSetup := s.ID == "tmdb_franchise_placeholder"
			if s.NeedsSetup == nil || *s.NeedsSetup != wantSetup {
				t.Errorf("%s/%s: needs_setup %v, want %v", b.ID, s.ID, s.NeedsSetup, wantSetup)
			}
		}
		if !slices.Equal(ids, b.TemplateIDs) {
			t.Errorf("%s: templates %v, want template_ids %v", b.ID, ids, b.TemplateIDs)
		}
	}
	for _, source := range []templates.Source{templates.SourceTMDBDiscover, templates.SourceTMDBCollection} {
		if !sources[string(source)] {
			t.Errorf("no bundle summary has source %s", source)
		}
	}
}

// TestAdminTemplateBundlesRequireActingAdmin restates #193 S5 for the bundle
// list: a regular account and a non-primary profile on an admin account are
// refused before the catalog is read.
func TestAdminTemplateBundlesRequireActingAdmin(t *testing.T) {
	f := newFakeAdminCollections()
	h := adminCollectionsTestHandler(t, f)
	for _, headers := range []map[string]string{bearer(memberToken), with(bearer(adminToken), "X-Profile-Id", "p-owner")} {
		requireProblem(t, do(t, h, http.MethodGet, templateBundlesPath, "", headers), TypePermissionDenied)
	}
	if f.bundleReads != 0 {
		t.Fatal("refused request read the bundle catalog")
	}
}

type featuredAdminCollections struct{ *fakeAdminCollections }

func (featuredAdminCollections) AdminCollectionFeatures(context.Context) userstore.CollectionFeatures {
	return userstore.CollectionFeatures{}
}

// TestAdminCollectionCapabilitiesAdvertiseTemplateSummaries checks that a client
// can learn from the capability document, not the server version, that bundles
// carry their template summaries.
func TestAdminCollectionCapabilitiesAdvertiseTemplateSummaries(t *testing.T) {
	deps, _ := libraryDeps(t)
	deps.AdminCollections = featuredAdminCollections{newFakeAdminCollections()}
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/admin/collections/capabilities", "", bearer(adminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var body struct {
		TemplateSummaries *bool `json:"template_summaries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TemplateSummaries == nil || !*body.TemplateSummaries {
		t.Fatalf("template_summaries not advertised: %s", rec.Body)
	}
}
